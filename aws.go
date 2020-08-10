package main

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go/aws/client"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/autoscaling"
	"github.com/sirupsen/logrus"
)

// AWSClient polls the AWS api for autoscaling groups
type AWSClient struct {
	svc     *autoscaling.AutoScaling
	filter  map[string]string
	nameTag string
	lock    *sync.RWMutex
	metrics AWSMetrics
	errors  AWSErrors
}

// AWSOps represents the options of an AWSClient
type AWSOps struct {
	Filter  string `long:"filter" env:"FILTER" description:"comma separated map (e.g. k1=v1,k2=v2)"`
	NameTag string `long:"name-tag" env:"NAME_TAG" description:"override name using given tag"`
}

// AWSMetrics represents the results of a metrics pull
type AWSMetrics struct {
	LastFetched time.Time
	FetchTime   time.Duration
	Groups      []*ASG
}

// AWSErrors represents errors and their number of occurences
type AWSErrors map[string]int

// ASG represents a single autoscaling group
type ASG struct {
	Name            string
	DesiredCapacity int64
	MaxSize         int64
	MinSize         int64
	Tags            map[string]string
	Instances       int
	InstanceStatus  map[string]int
}

// NewClient creates an AWS metrics client
func NewClient(opts *AWSOps) *AWSClient {
	filter := map[string]string{}
	for _, item := range strings.Split(opts.Filter, ",") {
		if !strings.Contains(item, "=") {
			continue
		} else {
			spl := strings.Split(item, "=")
			filter[spl[0]] = spl[1]
		}
	}

	svc := autoscaling.New(session.Must(session.NewSession()))
	svc.Client.Retryer = client.DefaultRetryer{
		NumMaxRetries:    9,
		MinRetryDelay:    1 * time.Second,
		MinThrottleDelay: 1 * time.Second,
	}

	c := AWSClient{
		svc:     svc,
		filter:  filter,
		nameTag: opts.NameTag,
		lock:    &sync.RWMutex{},
		errors:  make(map[string]int),
	}

	logrus.Infof("Performing initial API sync")
	c.updateMetrics()
	logrus.Infof("Finished initial sync")
	return &c
}

// Run starts polling metrics
func (m *AWSClient) Run(pollPeriod time.Duration) {
	for {
		duration := m.updateMetrics()
		time.Sleep(pollPeriod - duration)
	}
}

// GetMetrics fetches the latest metrics and the error totals
func (m *AWSClient) GetMetrics() (AWSMetrics, AWSErrors) {
	m.lock.RLock()
	defer m.lock.RUnlock()
	return m.metrics, m.errors
}

func (m *AWSClient) updateMetrics() time.Duration {
	startTime := time.Now()
	data, err := m.fetchGroups()

	m.lock.Lock()
	endTime := time.Now()
	logrus.Debugf("Got data in %v seconds", float64(endTime.Sub(startTime))/float64(time.Second))

	if err != nil {
		logrus.Errorf("Error fetching data: %v", err)
		if _, ok := m.errors[err.Error()]; !ok {
			m.errors[err.Error()] = 1
		} else {
			m.errors[err.Error()]++
		}
	} else {
		m.metrics = AWSMetrics{
			LastFetched: endTime,
			FetchTime:   endTime.Sub(startTime),
			Groups:      data,
		}
	}
	m.lock.Unlock()

	return endTime.Sub(startTime)
}

func (m *AWSClient) fetchGroups() ([]*ASG, error) {
	logrus.Debugf("fetchGroups called")

	input := &autoscaling.DescribeAutoScalingGroupsInput{}
	asgs := []*ASG{}
	if err := m.svc.DescribeAutoScalingGroupsPagesWithContext(context.Background(), input,
		func(page *autoscaling.DescribeAutoScalingGroupsOutput, lastPage bool) bool {
			logrus.Debugf("got page")
		loop:
			for _, group := range page.AutoScalingGroups {
				a := convertGroup(group)
				for fk, fv := range m.filter {
					tagv, ok := a.Tags[fk]
					if !ok {
						continue loop
					}
					if tagv != fv {
						continue loop
					}
				}
				if m.nameTag != "" {
					v, ok := a.Tags[m.nameTag]
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
	return asgs, nil
}

func convertGroup(g *autoscaling.Group) *ASG {
	a := &ASG{
		Name:            *g.AutoScalingGroupName,
		DesiredCapacity: *g.DesiredCapacity,
		MaxSize:         *g.MaxSize,
		MinSize:         *g.MinSize,
		Instances:       len(g.Instances),
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
	}
	return a
}
