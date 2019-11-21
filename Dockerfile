FROM golang:1.13
ENV GO111MODULE=on
WORKDIR /go/src/github.com/wish/aws-asg-exporter

# Cache dependencies
COPY go.mod .
COPY go.sum .
RUN go mod download

COPY . /go/src/github.com/wish/aws-asg-exporter/
RUN CGO_ENABLED=0 GOOS=linux go build -o exporter -a -installsuffix cgo .

FROM alpine:3.7
RUN apk --no-cache add ca-certificates
WORKDIR /root/
COPY --from=0 /go/src/github.com/wish/aws-asg-exporter/exporter /root/exporter
CMD /root/exporter
