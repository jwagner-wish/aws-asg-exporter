package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	flags "github.com/jessevdk/go-flags"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/sirupsen/logrus"
)

const (
	contentTypeHeader     = "Content-Type"
	contentEncodingHeader = "Content-Encoding"
	acceptEncodingHeader  = "Accept-Encoding"
)

type ops struct {
	AWSOps
	LogLevel string        `long:"log-level" env:"LOG_LEVEL" description:"Log level" default:"info"`
	BindAddr string        `long:"bind-address" short:"p" env:"BIND_ADDRESS" default:":9655" description:"address for binding metrics listener"`
	TTL      time.Duration `long:"ttl" env:"TTL" default:"30s" description:"TTL for local cache"`
}

var (
	opts           *ops
	clientInstance *AWSClient
)

func main() {
	opts = &ops{}
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

	// Initialize the awsClient and start polling
	clientInstance = NewClient(&opts.AWSOps)
	go clientInstance.Run(opts.TTL - 5*time.Second)

	http.HandleFunc("/", handler)
	http.HandleFunc("/healthcheck", handler)
	http.HandleFunc("/metrics", metricsHandler)
	logrus.Infof("Started listening on %v", opts.BindAddr)
	logrus.Fatal(http.ListenAndServe(opts.BindAddr, nil))
}

func metricsHandler(rsp http.ResponseWriter, req *http.Request) {
	awsGroups, errors := clientInstance.GetMetrics()

	metrics := []*dto.MetricFamily{}
	if time.Now().Sub(awsGroups.LastFetched) > opts.TTL {
		logrus.Warnf("Last AWS metrics are older than TTL. Not exporting them.")
	} else {
		metrics = append(metrics, convertASGsToMetrics(awsGroups)...)
	}

	errFamily := errorCountMetrics(errors)
	if errFamily != nil {
		metrics = append(metrics, errFamily)
	}

	contentType := expfmt.Negotiate(req.Header)
	header := rsp.Header()
	header.Set(contentTypeHeader, string(contentType))

	w := io.Writer(rsp)
	enc := expfmt.NewEncoder(w, contentType)

	var lastErr error
	for _, mf := range metrics {
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
