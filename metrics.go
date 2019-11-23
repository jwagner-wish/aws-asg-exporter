package main

import (
	"time"

	"github.com/aws/aws-sdk-go/aws"
	dto "github.com/prometheus/client_model/go"
)

func convertASGsToMetrics(asgs AWSMetrics) []*dto.MetricFamily {
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
	instances := generateGaugeFamily("aws_asg_instances", "ASG number of instances")
	instanceStatus := generateGaugeFamily("aws_asg_instance_status", "ASG number of instances by status")

	for _, asg := range asgs.Groups {
		generateMetric := func(v float64) *dto.Metric {
			lp := &dto.LabelPair{Name: aws.String("name"), Value: aws.String(asg.Name)}
			return &dto.Metric{
				Label: []*dto.LabelPair{lp},
				Gauge: &dto.Gauge{Value: &v},
			}
		}

		minFamily.Metric = append(minFamily.Metric, generateMetric(float64(asg.MinSize)))
		maxFamily.Metric = append(maxFamily.Metric, generateMetric(float64(asg.MaxSize)))
		desiredFamily.Metric = append(desiredFamily.Metric, generateMetric(float64(asg.DesiredCapacity)))
		instances.Metric = append(instances.Metric, generateMetric(float64(asg.Instances)))

		for k, v := range asg.InstanceStatus {
			m := generateMetric(float64(v))
			lp := &dto.LabelPair{Name: aws.String("health_status"), Value: aws.String(k)}
			m.Label = append(m.Label, lp)
			instanceStatus.Metric = append(instanceStatus.Metric, m)
		}
	}

	dataFetchTime := generateGaugeFamily("aws_asg_last_fetch_time_seconds", "How long it last took to fetch ASG data")
	fetchSeconds := float64(asgs.FetchTime) / float64(time.Second)
	dataFetchTime.Metric = []*dto.Metric{
		&dto.Metric{
			Gauge: &dto.Gauge{Value: &fetchSeconds},
		},
	}

	out = append(out, minFamily)
	out = append(out, maxFamily)
	out = append(out, desiredFamily)
	out = append(out, instances)
	out = append(out, instanceStatus)
	out = append(out, dataFetchTime)
	return out
}

func errorCountMetrics(counts AWSErrors) *dto.MetricFamily {
	generateGaugeFamily := func(name, help string) *dto.MetricFamily {
		c := dto.MetricType_COUNTER
		return &dto.MetricFamily{
			Name:   aws.String(name),
			Help:   aws.String(help),
			Type:   &c,
			Metric: []*dto.Metric{},
		}
	}

	errFamily := generateGaugeFamily("aws_asg_request_error_count", "Total number of errors in fetching data")

	for msg, count := range counts {
		generateMetric := func(v float64) *dto.Metric {
			lp := &dto.LabelPair{Name: aws.String("error"), Value: aws.String(msg)}
			return &dto.Metric{
				Label:   []*dto.LabelPair{lp},
				Counter: &dto.Counter{Value: &v},
			}
		}

		errFamily.Metric = append(errFamily.Metric, generateMetric(float64(count)))
	}

	if len(errFamily.Metric) == 0 {
		return nil
	}

	return errFamily
}
