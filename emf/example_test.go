package emf_test

import (
	"errors"
	"fmt"
	"io"
	"math"
	"os"
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

// PutValues logs several values for one metric in a single line, such as
// the latency of each request in a batch. CloudWatch keeps every value, so
// statistics like percentiles stay accurate.
func ExampleLogger_PutValues() {
	emf.New(exampleTime).
		Namespace("api").
		PutValues("Latency", []float64{12.5, 8.1, 30.2}, emf.Unit(emf.Milliseconds)).
		Log()
	// Output:
	// {"Latency":[12.5,8.1,30.2],"_aws":{"Timestamp":1791500000000,"CloudWatchMetrics":[{"Namespace":"api","Dimensions":[[]],"Metrics":[{"Name":"Latency","Unit":"Milliseconds"}]}]}}
}

// Logging the same metric name again adds to its values, instead of
// replacing them.
func ExampleLogger_Put() {
	logger := emf.New(exampleTime).Namespace("api")
	for _, latency := range []float64{12.5, 8.1} {
		logger.Put("Latency", latency, emf.Unit(emf.Milliseconds))
	}
	logger.Log()
	// Output:
	// {"Latency":[12.5,8.1],"_aws":{"Timestamp":1791500000000,"CloudWatchMetrics":[{"Namespace":"api","Dimensions":[[]],"Metrics":[{"Name":"Latency","Unit":"Milliseconds"}]}]}}
}

// High-resolution metrics are stored per second instead of per minute.
func ExampleHighResolution() {
	emf.New(exampleTime).
		Namespace("api").
		Put("QueueDepth", 42, emf.Unit(emf.Count), emf.HighResolution()).
		Log()
	// Output:
	// {"QueueDepth":42,"_aws":{"Timestamp":1791500000000,"CloudWatchMetrics":[{"Namespace":"api","Dimensions":[[]],"Metrics":[{"Name":"QueueDepth","Unit":"Count","StorageResolution":1}]}]}}
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

// PropertyValue logs numbers, bools, lists and objects as JSON, so CloudWatch
// Logs Insights can compare them, as in filter statusCode >= 500.
func ExampleLogger_PropertyValue() {
	emf.New(exampleTime).
		PropertyValue("statusCode", 503).
		PropertyValue("cached", false).
		PropertyValue("tags", []string{"beta", "eu"}).
		PropertyValue("client", struct {
			Name    string `json:"name"`
			Version int    `json:"version"`
		}{"ios", 42}).
		Metric("Requests", 1).
		Log()
	// Output:
	// {"Requests":1,"_aws":{"Timestamp":1791500000000,"CloudWatchMetrics":[{"Namespace":"aws-embedded-metrics","Dimensions":[[]],"Metrics":[{"Name":"Requests","Unit":"None"}]}]},"cached":false,"client":{"name":"ios","version":42},"statusCode":503,"tags":["beta","eu"]}
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

// In Lambda, the default dimensions ServiceName and ServiceType are merged
// into each of your dimension sets, so each metric is published once, for
// the combination, instead of once for the default set and once for yours.
func ExampleWithMergedDefaultDimensions() {
	defer setExampleLambdaEnv()()

	emf.New(exampleTime, emf.WithMergedDefaultDimensions()).
		Namespace("shop").
		Dimension("Service", "checkout").
		MetricAs("Orders", 1, emf.Count).
		Log()
	// Output:
	// {"Orders":1,"Service":"checkout","ServiceName":"checkout-fn","ServiceType":"AWS::Lambda::Function","_aws":{"Timestamp":1791500000000,"CloudWatchMetrics":[{"Namespace":"shop","Dimensions":[["ServiceName","ServiceType","Service"]],"Metrics":[{"Name":"Orders","Unit":"Count"}]}]},"executionEnvironment":"AWS_Lambda_provided.al2023","functionVersion":"$LATEST","logStreamId":"2026/11/03/[$LATEST]abc123","memorySize":"128"}
}

// setExampleLambdaEnv sets the environment Lambda sets for you, to show the
// output in Lambda, and returns a function that restores it.
func setExampleLambdaEnv() (restore func()) {
	lambdaEnv := map[string]string{
		"AWS_LAMBDA_FUNCTION_NAME":        "checkout-fn",
		"AWS_EXECUTION_ENV":               "AWS_Lambda_provided.al2023",
		"AWS_LAMBDA_FUNCTION_MEMORY_SIZE": "128",
		"AWS_LAMBDA_FUNCTION_VERSION":     "$LATEST",
		"AWS_LAMBDA_LOG_STREAM_NAME":      "2026/11/03/[$LATEST]abc123",
	}
	var restores []func()
	for k, v := range lambdaEnv {
		old, wasSet := os.LookupEnv(k)
		_ = os.Setenv(k, v)
		restores = append(restores, func() {
			if wasSet {
				_ = os.Setenv(k, old)
			} else {
				_ = os.Unsetenv(k)
			}
		})
	}
	return func() {
		for _, restore := range restores {
			restore()
		}
	}
}

// SetDimensions replaces the dimension sets. With useDefault false, the
// Lambda default dimensions ServiceName and ServiceType are dropped, as the
// official AWS libraries' setDimensions does, while the Lambda properties are
// still logged.
func ExampleLogger_SetDimensions() {
	defer setExampleLambdaEnv()()

	const useDefault = false
	emf.New(exampleTime).
		Namespace("shop").
		SetDimensions(useDefault,
			[]emf.Dimension{emf.NewDimension("Service", "checkout")},
			[]emf.Dimension{emf.NewDimension("Service", "checkout"), emf.NewDimension("Region", "ap-southeast-2")},
		).
		MetricAs("Orders", 1, emf.Count).
		Log()
	// Output:
	// {"Orders":1,"Region":"ap-southeast-2","Service":"checkout","_aws":{"Timestamp":1791500000000,"CloudWatchMetrics":[{"Namespace":"shop","Dimensions":[["Service"],["Service","Region"]],"Metrics":[{"Name":"Orders","Unit":"Count"}]}]},"executionEnvironment":"AWS_Lambda_provided.al2023","functionVersion":"$LATEST","logStreamId":"2026/11/03/[$LATEST]abc123","memorySize":"128"}
}

// ResetDimensions removes the dimensions added to a context. Here a context
// also drops the Lambda default dimensions, so its metric is published
// without dimensions, while the logger's other metric keeps them.
func ExampleContext_ResetDimensions() {
	defer setExampleLambdaEnv()()

	logger := emf.New(exampleTime).Namespace("shop").MetricAs("Orders", 1, emf.Count)
	logger.NewContext().
		Namespace("billing").
		Dimension("Plan", "pro").
		ResetDimensions(false).
		MetricAs("Invoices", 1, emf.Count)
	logger.Log()
	// Output:
	// {"Invoices":1,"Orders":1,"ServiceName":"checkout-fn","ServiceType":"AWS::Lambda::Function","_aws":{"Timestamp":1791500000000,"CloudWatchMetrics":[{"Namespace":"shop","Dimensions":[["ServiceName","ServiceType"]],"Metrics":[{"Name":"Orders","Unit":"Count"}]},{"Namespace":"billing","Dimensions":[[]],"Metrics":[{"Name":"Invoices","Unit":"Count"}]}]},"executionEnvironment":"AWS_Lambda_provided.al2023","functionVersion":"$LATEST","logStreamId":"2026/11/03/[$LATEST]abc123","memorySize":"128"}
}

// Flush writes the metrics, then clears the values and properties and
// removes the contexts, so one logger can be reused for each unit of work,
// such as each Lambda invocation. The namespace and dimensions of the logger
// are kept, and contexts are made again for each unit of work. Each flush
// writes the time it is called, unless a timestamp was set, as it is here so
// the output stays the same.
func ExampleLogger_Flush() {
	logger := emf.New(exampleTime).Namespace("api").Dimension("Operation", "GetItem")

	handle := func(requestID, customer string) {
		defer logger.Flush()
		logger.Property("requestId", requestID).MetricAs("Requests", 1, emf.Count)
		logger.NewContext().Namespace("billing").Dimension("Customer", customer).MetricAs("Charges", 1, emf.Count)
	}
	handle("request-1", "acme")
	handle("request-2", "globex")
	// Output:
	// {"Charges":1,"Customer":"acme","Operation":"GetItem","Requests":1,"_aws":{"Timestamp":1791500000000,"CloudWatchMetrics":[{"Namespace":"api","Dimensions":[["Operation"]],"Metrics":[{"Name":"Requests","Unit":"Count"}]},{"Namespace":"billing","Dimensions":[["Customer"]],"Metrics":[{"Name":"Charges","Unit":"Count"}]}]},"requestId":"request-1"}
	// {"Charges":1,"Customer":"globex","Operation":"GetItem","Requests":1,"_aws":{"Timestamp":1791500000000,"CloudWatchMetrics":[{"Namespace":"api","Dimensions":[["Operation"]],"Metrics":[{"Name":"Requests","Unit":"Count"}]},{"Namespace":"billing","Dimensions":[["Customer"]],"Metrics":[{"Name":"Charges","Unit":"Count"}]}]},"requestId":"request-2"}
}

// SetTimestamp records metrics for another time, such as when an event
// happened rather than when it was processed. The timestamp is kept across
// flushes, until it is set again.
func ExampleLogger_SetTimestamp() {
	logger := emf.New().Namespace("queue")
	for _, sent := range []time.Time{time.UnixMilli(1791500000000), time.UnixMilli(1791500060000)} {
		logger.SetTimestamp(sent).MetricAs("Messages", 1, emf.Count).Flush()
	}
	// Output:
	// {"Messages":1,"_aws":{"Timestamp":1791500000000,"CloudWatchMetrics":[{"Namespace":"queue","Dimensions":[[]],"Metrics":[{"Name":"Messages","Unit":"Count"}]}]}}
	// {"Messages":1,"_aws":{"Timestamp":1791500060000,"CloudWatchMetrics":[{"Namespace":"queue","Dimensions":[[]],"Metrics":[{"Name":"Messages","Unit":"Count"}]}]}}
}

// With WithResetDimensionsOnFlush, each Flush also removes the dimensions
// you added, so each unit of work adds its own.
func ExampleWithResetDimensionsOnFlush() {
	logger := emf.New(exampleTime, emf.WithResetDimensionsOnFlush()).Namespace("api")

	logger.Dimension("Customer", "acme").MetricAs("Requests", 1, emf.Count).Flush()
	logger.MetricAs("Requests", 1, emf.Count).Flush()
	// Output:
	// {"Customer":"acme","Requests":1,"_aws":{"Timestamp":1791500000000,"CloudWatchMetrics":[{"Namespace":"api","Dimensions":[["Customer"]],"Metrics":[{"Name":"Requests","Unit":"Count"}]}]}}
	// {"Requests":1,"_aws":{"Timestamp":1791500000000,"CloudWatchMetrics":[{"Namespace":"api","Dimensions":[[]],"Metrics":[{"Name":"Requests","Unit":"Count"}]}]}}
}
