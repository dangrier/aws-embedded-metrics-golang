package emf_test

import (
	"errors"
	"fmt"
	"io"
	"math"
	"time"

	"github.com/dangrier/aws-embedded-metrics-golang/emf"
)

// The examples use a fixed timestamp so their output stays the same. In
// your code, leave WithTimestamp out and the time of New is used.
var exampleTime = emf.WithTimestamp(time.UnixMilli(1791500000000))

// Log a metric with a namespace and a dimension. The output goes to stdout,
// where Lambda (or the CloudWatch agent) sends it to CloudWatch Logs, and
// CloudWatch turns it into a metric.
func Example() {
	emf.New(exampleTime).
		Namespace("shop").
		Dimension("Service", "checkout").
		MetricAs("OrderTotal", 120, emf.Count).
		Log()
	// Output:
	// {"OrderTotal":120,"Service":"checkout","_aws":{"Timestamp":1791500000000,"CloudWatchMetrics":[{"Namespace":"shop","Dimensions":[["Service"]],"Metrics":[{"Name":"OrderTotal","Unit":"Count"}]}]}}
}

// Use defer to log metrics when a function returns, however it returns.
func ExampleLogger_Log() {
	processOrder := func(id string) error {
		metrics := emf.New(exampleTime).Namespace("api").Dimension("Operation", "ProcessOrder")
		defer metrics.Log()

		// ... process the order, timing it with time.Since ...
		latency := 12.5
		metrics.Metric("Orders", 1).MetricFloatAs("Latency", latency, emf.Milliseconds)
		return nil
	}

	_ = processOrder("order-1")
	// Output:
	// {"Latency":12.5,"Operation":"ProcessOrder","Orders":1,"_aws":{"Timestamp":1791500000000,"CloudWatchMetrics":[{"Namespace":"api","Dimensions":[["Operation"]],"Metrics":[{"Name":"Orders","Unit":"None"},{"Name":"Latency","Unit":"Milliseconds"}]}]}}
}

// A dimension set publishes each metric once for that combination of
// dimensions. Adding two separate dimensions instead publishes each metric
// twice, once per dimension.
func ExampleLogger_DimensionSet() {
	emf.New(exampleTime).
		Namespace("shop").
		DimensionSet(
			emf.NewDimension("Service", "checkout"),
			emf.NewDimension("Region", "ap-southeast-2"),
		).
		MetricFloatAs("Latency", 42.5, emf.Milliseconds).
		Log()
	// Output:
	// {"Latency":42.5,"Region":"ap-southeast-2","Service":"checkout","_aws":{"Timestamp":1791500000000,"CloudWatchMetrics":[{"Namespace":"shop","Dimensions":[["Service","Region"]],"Metrics":[{"Name":"Latency","Unit":"Milliseconds"}]}]}}
}

// Contexts log metrics with their own namespace and dimensions in the same
// line.
func ExampleLogger_NewContext() {
	logger := emf.New(exampleTime).Namespace("shop").Metric("Orders", 3)
	logger.NewContext().
		Namespace("payments").
		Dimension("Provider", "card").
		MetricAs("Declined", 1, emf.Count)
	logger.Log()
	// Output:
	// {"Declined":1,"Orders":3,"Provider":"card","_aws":{"Timestamp":1791500000000,"CloudWatchMetrics":[{"Namespace":"shop","Dimensions":[[]],"Metrics":[{"Name":"Orders","Unit":"None"}]},{"Namespace":"payments","Dimensions":[["Provider"]],"Metrics":[{"Name":"Declined","Unit":"Count"}]}]}}
}

// Properties are logged alongside the metrics but don't become metrics or
// dimensions, which makes them handy for searching in CloudWatch Logs
// Insights.
func ExampleLogger_Property() {
	emf.New(exampleTime).
		Property("requestId", "989ffbf8-9ace-4817-a57c-e4dd734019ee").
		Metric("Requests", 1).
		Log()
	// Output:
	// {"Requests":1,"_aws":{"Timestamp":1791500000000,"CloudWatchMetrics":[{"Namespace":"aws-embedded-metrics","Dimensions":[[]],"Metrics":[{"Name":"Requests","Unit":"None"}]}]},"requestId":"989ffbf8-9ace-4817-a57c-e4dd734019ee"}
}

// The error handler hears about input the logger skips because it would
// break the EMF spec. The rest is still logged.
func ExampleWithErrorHandler() {
	logger := emf.New(
		emf.WithWriter(io.Discard), // only show the errors here
		emf.WithErrorHandler(func(err error) {
			if errors.Is(err, emf.ErrInvalid) {
				fmt.Println(err)
			}
		}),
	)

	logger.
		MetricFloat("Ratio", math.NaN()). // skipped and reported
		Metric("Requests", 1).            // still logged
		Log()
	// Output:
	// emf: invalid input: skipped metric "Ratio": value NaN must be a finite number between -2^360 and 2^360
}
