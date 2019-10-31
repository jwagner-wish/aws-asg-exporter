package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/autoscaling"
	"github.com/aws/aws-sdk-go/service/ec2"
	flags "github.com/jessevdk/go-flags"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/sirupsen/logrus"
	"github.com/wish/aws-asg-exporter/pkg/k8sNode"
)

const (
	contentTypeHeader     = "Content-Type"
	contentEncodingHeader = "Content-Encoding"
	acceptEncodingHeader  = "Accept-Encoding"
)

type ops struct {
	LogLevel string `long:"log-level" env:"LOG_LEVEL" description:"Log level" default:"info"`
	BindAddr string `long:"bind-address" short:"p" env:"BIND_ADDRESS" default:":9655" description:"address for binding metrics listener"`

	TTL     time.Duration `long:"ttl" env:"TTL" default:"30s" description:"TTL for local cache"`
	Filter  string        `long:"filter" env:"FILTER" description:"comma separated map (e.g. k1=v1,k2=v2)"`
	NameTag string        `long:"name-tag" env:"NAME_TAG" description:"override name using given tag"`
}

var (
	opts           *ops
	globalCache    *cache
	mu             *sync.Mutex
	nodeController *k8sNode.Controller
)

type instance struct {
	ID            string
	Name          string
	LaunchTime    time.Time
	JoinedK8sTime time.Time
}

type asg struct {
	Name            string
	DesiredCapacity int64
	MaxSize         int64
	MinSize         int64
	Tags            map[string]string
	InstanceIds     []string
	InstanceStatus  map[string]int
}

type cache struct {
	Date      time.Time
	Instances map[string]instance
	Metrics   []*dto.MetricFamily
}

func main() {
	opts = &ops{}
	globalCache = &cache{Date: time.Unix(0, 0)}
	mu = &sync.Mutex{}
	parser := flags.NewParser(opts, flags.Default)
	if _, err := parser.Parse(); err != nil {
		// If the error was from the parser, then we can simply return
		// as Parse() prints the error already
		if _, ok := err.(*flags.Error); ok {
			os.Exit(1)
		}
		logrus.Fatalf("Error parsing flags: %v", err)
	}

	// Use log level
	level, err := logrus.ParseLevel(opts.LogLevel)
	if err != nil {
		logrus.Fatalf("Unknown log level %s: %v", opts.LogLevel, err)
	}
	logrus.SetLevel(level)

	// Set the log format to have a reasonable timestamp
	formatter := &logrus.TextFormatter{
		FullTimestamp: true,
	}
	logrus.SetFormatter(formatter)

	newC, err := k8sNode.NewController()
	if err != nil {
		logrus.Fatalf("Could not create node watcher: %v", err)
	}
	nodeController = newC
	stopCh := make(chan struct{})
	defer close(stopCh)
	nodeController.Run(stopCh)

	http.HandleFunc("/", handler)
	http.HandleFunc("/healthcheck", handler)
	http.HandleFunc("/metrics", metricsHandler)
	logrus.Infof("Started listening on %v", opts.BindAddr)
	logrus.Fatal(http.ListenAndServe(opts.BindAddr, nil))
}

func convertGroup(g *autoscaling.Group) (*asg, error) {
	a := &asg{
		Name:            *g.AutoScalingGroupName,
		DesiredCapacity: *g.DesiredCapacity,
		MaxSize:         *g.MaxSize,
		MinSize:         *g.MinSize,
		InstanceIds:     make([]string, 0),
		Tags:            make(map[string]string),
		InstanceStatus:  make(map[string]int),
	}
	for _, tag := range g.Tags {
		a.Tags[*tag.Key] = *tag.Value
	}
	for _, inst := range g.Instances {
		v, ok := a.InstanceStatus[*inst.HealthStatus]
		if !ok {
			a.InstanceStatus[*inst.HealthStatus] = 1
		} else {
			a.InstanceStatus[*inst.HealthStatus] = v + 1
		}
		a.InstanceIds = append(a.InstanceIds, *inst.InstanceId)
	}
	return a, nil
}

func convertASGsToMetrics(t time.Time, asgs []*asg, instances map[string]instance) ([]*dto.MetricFamily, error) {
	out := []*dto.MetricFamily{}
	generateGaugeFamily := func(name, help string) *dto.MetricFamily {
		g := dto.MetricType_GAUGE
		return &dto.MetricFamily{
			Name:   aws.String(name),
			Help:   aws.String(help),
			Type:   &g,
			Metric: []*dto.Metric{},
		}
	}

	minFamily := generateGaugeFamily("aws_asg_min_size", "ASG minimum size")
	maxFamily := generateGaugeFamily("aws_asg_max_size", "ASG maximum size")
	desiredFamily := generateGaugeFamily("aws_asg_desired_capacity", "ASG desired capacity")
	instancesFamily := generateGaugeFamily("aws_asg_instances", "ASG number of instances")
	instanceStatus := generateGaugeFamily("aws_asg_instance_status", "ASG number of instances by status")
	oldestUnbootstrappedFamily := generateGaugeFamily("aws_asg_oldest_unbootstrapped_instance_age_seconds", "The age in seconds of the oldest instance in the ASG not yet in the cluster")
	numUnbootstrappedFamily := generateGaugeFamily("aws_asg_num_unbootstrapped_instances", "The number of instances in the ASG that have not joined the cluster")

	for _, asg := range asgs {
		generateMetric := func(v float64) *dto.Metric {
			lp := &dto.LabelPair{Name: aws.String("name"), Value: aws.String(asg.Name)}
			return &dto.Metric{
				Label: []*dto.LabelPair{lp},
				Gauge: &dto.Gauge{Value: &v},
			}
		}

		oldestUnbootstrappedSeconds := 0.0
		numUnbootstrapped := 0
		for _, i := range asg.InstanceIds {
			if inst, ok := instances[i]; ok {
				if inst.JoinedK8sTime.IsZero() {
					numUnbootstrapped++
					secondsSinceStart := float64((t.Sub(inst.LaunchTime)) / time.Second)
					if secondsSinceStart > oldestUnbootstrappedSeconds {
						oldestUnbootstrappedSeconds = secondsSinceStart
					}
				}
			}
		}

		minFamily.Metric = append(minFamily.Metric, generateMetric(float64(asg.MinSize)))
		maxFamily.Metric = append(maxFamily.Metric, generateMetric(float64(asg.MaxSize)))
		desiredFamily.Metric = append(desiredFamily.Metric, generateMetric(float64(asg.DesiredCapacity)))
		instancesFamily.Metric = append(instancesFamily.Metric, generateMetric(float64(len(asg.InstanceIds))))
		oldestUnbootstrappedFamily.Metric = append(oldestUnbootstrappedFamily.Metric, generateMetric(float64(oldestUnbootstrappedSeconds)))
		numUnbootstrappedFamily.Metric = append(numUnbootstrappedFamily.Metric, generateMetric(float64(numUnbootstrapped)))

		for k, v := range asg.InstanceStatus {
			m := generateMetric(float64(v))
			lp := &dto.LabelPair{Name: aws.String("health_status"), Value: aws.String(k)}
			m.Label = append(m.Label, lp)
			instanceStatus.Metric = append(instanceStatus.Metric, m)
		}
	}

	out = append(out, minFamily)
	out = append(out, maxFamily)
	out = append(out, desiredFamily)
	out = append(out, instancesFamily)
	out = append(out, instanceStatus)
	out = append(out, oldestUnbootstrappedFamily)
	out = append(out, numUnbootstrappedFamily)
	return out, nil
}

func getData(ctx context.Context, filter map[string]string, nametag string) ([]*dto.MetricFamily, error) {
	t := time.Now()

	logrus.Debugf("getData called")

	mu.Lock()
	if !t.After(globalCache.Date.Add(opts.TTL)) {
		// Cache hit
		logrus.Debugf("getData cache hit")
		mu.Unlock()
		return globalCache.Metrics, nil
	}
	mu.Unlock()
	logrus.Debugf("getData cache miss")

	svc := autoscaling.New(session.New())
	input := &autoscaling.DescribeAutoScalingGroupsInput{}
	asgs := []*asg{}
	if err := svc.DescribeAutoScalingGroupsPagesWithContext(ctx, input,
		func(page *autoscaling.DescribeAutoScalingGroupsOutput, lastPage bool) bool {
		loop:
			for _, group := range page.AutoScalingGroups {
				a, err := convertGroup(group)
				if err != nil {
					logrus.Warnf("Could not generate ASG: %v", err)
					return false
				}
				for fk, fv := range filter {
					tagv, ok := a.Tags[fk]
					if !ok {
						continue loop
					}
					if tagv != fv {
						continue loop
					}
				}
				if nametag != "" {
					v, ok := a.Tags[nametag]
					if ok {
						a.Name = v
					}
				}
				asgs = append(asgs, a)
			}
			return true
		}); err != nil {
		logrus.Warnf("DescribeAutoScalingGroups failed: %v", err)
		return nil, err
	}

	instanceIds := []string{}
	for _, asg := range asgs {
		instanceIds = append(instanceIds, asg.InstanceIds...)
	}
	instances, err := getInstances(ctx, instanceIds)
	if err != nil {
		logrus.Warnf("Failed to fetch instance details: %v", err)
		return nil, err
	}

	asg, err := convertASGsToMetrics(t, asgs, instances)
	if err != nil {
		logrus.Warnf("convertASGsToMetrics failed: %v", err)
		return nil, err
	}
	mu.Lock()
	globalCache = &cache{
		Date:      t,
		Metrics:   asg,
		Instances: instances,
	}
	mu.Unlock()
	return asg, err
}

func getInstances(ctx context.Context, instanceIds []string) (map[string]instance, error) {
	instances := map[string]instance{}

	mu.Lock()
	unknownInstances := []*string{}
	for _, ID := range instanceIds {
		i := ID
		instance, ok := globalCache.Instances[i]
		if !ok {
			unknownInstances = append(unknownInstances, &i)
		} else {
			instances[i] = instance
		}
	}
	mu.Unlock()

	svc := ec2.New(session.New())
	input := &ec2.DescribeInstancesInput{
		InstanceIds: unknownInstances,
	}
	if len(unknownInstances) > 0 {
		err := svc.DescribeInstancesPagesWithContext(ctx, input, func(page *ec2.DescribeInstancesOutput, lastPage bool) bool {
			mu.Lock()
			defer mu.Unlock()
			for _, reservation := range page.Reservations {
				for _, i := range reservation.Instances {
					if i.LaunchTime != nil && i.InstanceId != nil && i.PrivateDnsName != nil {
						instances[*i.InstanceId] = instance{
							ID:         *i.InstanceId,
							Name:       *i.PrivateDnsName,
							LaunchTime: *i.LaunchTime,
						}
					}
				}
			}
			return true
		})
		if err != nil {
			return instances, err
		}
	}

	for i := range instances {
		if instances[i].JoinedK8sTime.IsZero() {
			n, err := nodeController.NodeByName(instances[i].Name)
			if err != nil {
				return instances, err
			}
			if n != nil {
				inst := instances[i]
				inst.JoinedK8sTime = n.CreationTimestamp.Time
				instances[i] = inst
			}
		}
	}

	return instances, nil
}

func metricsHandler(rsp http.ResponseWriter, req *http.Request) {
	filter := map[string]string{}
	for _, item := range strings.Split(opts.Filter, ",") {
		if !strings.Contains(item, "=") {
			continue
		} else {
			spl := strings.Split(item, "=")
			filter[spl[0]] = spl[1]
		}
	}

	out, err := getData(req.Context(), filter, opts.NameTag)
	if err != nil {
		logrus.Warnf("Could not get ASG data: %v", err)
	}
	contentType := expfmt.Negotiate(req.Header)
	header := rsp.Header()
	header.Set(contentTypeHeader, string(contentType))

	w := io.Writer(rsp)
	enc := expfmt.NewEncoder(w, contentType)

	var lastErr error
	for _, mf := range out {
		if err := enc.Encode(mf); err != nil {
			lastErr = err
			httpError(rsp, err)
			return
		}
	}

	if lastErr != nil {
		httpError(rsp, lastErr)
	}
}

func httpError(rsp http.ResponseWriter, err error) {
	rsp.Header().Del(contentEncodingHeader)
	http.Error(
		rsp,
		"An error has occurred while serving metrics:\n\n"+err.Error(),
		http.StatusInternalServerError,
	)
}

func handler(w http.ResponseWriter, r *http.Request) {
	fmt.Fprintf(w, "OK\n")
}
