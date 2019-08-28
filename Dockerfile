FROM golang:1.12
COPY . /go/src/github.com/wish/aws-asg-exporter/
ENV GO111MODULE=on
WORKDIR /go/src/github.com/wish/aws-asg-exporter
RUN CGO_ENABLED=0 GOOS=linux go build -o exporter -a -installsuffix cgo .



FROM alpine:3.7
RUN apk --no-cache add ca-certificates
WORKDIR /root/
COPY --from=0 /go/src/github.com/wish/aws-asg-exporter/exporter /root/exporter
CMD /root/exporter
