module github.com/wish/aws-asg-exporter

go 1.12

require (
	github.com/aws/aws-sdk-go v1.23.10
	github.com/jessevdk/go-flags v1.4.0
	github.com/matttproud/golang_protobuf_extensions v1.0.1 // indirect
	github.com/prometheus/client_model v0.0.0-20180712105110-5c3871d89910
	github.com/prometheus/common v0.0.0-20181126121408-4724e9255275
	github.com/sirupsen/logrus v1.4.2
	k8s.io/api v0.0.0-20190620084959-7cf5895f2711
	k8s.io/apimachinery v0.0.0-20190612205821-1799e75a0719
	k8s.io/client-go v0.0.0-20190620085101-78d2af792bab
)
