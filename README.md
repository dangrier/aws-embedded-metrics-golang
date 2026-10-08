# aws-embedded-metrics-golang

[![Go Reference](https://pkg.go.dev/badge/github.com/dangrier/aws-embedded-metrics-golang/emf.svg)](https://pkg.go.dev/github.com/dangrier/aws-embedded-metrics-golang/emf)
[![ci](https://github.com/dangrier/aws-embedded-metrics-golang/actions/workflows/ci.yml/badge.svg)](https://github.com/dangrier/aws-embedded-metrics-golang/actions/workflows/ci.yml)
[![security](https://github.com/dangrier/aws-embedded-metrics-golang/actions/workflows/security.yml/badge.svg)](https://github.com/dangrier/aws-embedded-metrics-golang/actions/workflows/security.yml)

A Go library for the AWS CloudWatch [Embedded Metric Format](https://docs.aws.amazon.com/AmazonCloudWatch/latest/monitoring/CloudWatch_Embedded_Metric_Format_Specification.html) (EMF).

EMF lets you publish CloudWatch metrics by writing JSON log lines. CloudWatch Logs reads them and
creates the metrics for you, so there are no CloudWatch API calls, credentials or clients in your
code, and nothing to mock in tests.

- Simple, chainable API for metrics, dimensions, properties and namespaces
- Output always follows the EMF spec, and is tested against AWS's JSON Schema and fuzzed
- Bad input is skipped and reported, instead of silently breaking the whole log line
- Safe to use from several goroutines at once
- Lambda dimensions and properties added for you
- The library itself only uses the standard library

Forked from [prozz/aws-embedded-metrics-golang](https://github.com/prozz/aws-embedded-metrics-golang).

## Installation

Requires Go 1.27 or newer.

```shell
go get github.com/dangrier/aws-embedded-metrics-golang
```

## Usage

```go
import "github.com/dangrier/aws-embedded-metrics-golang/emf"

emf.New().
	Namespace("shop").
	Dimension("Service", "checkout").
	MetricAs("OrderTotal", 120, emf.Count).
	Log()
```

This writes one line to stdout:

```json
{"OrderTotal":120,"Service":"checkout","_aws":{"Timestamp":1791500000000,"CloudWatchMetrics":[{"Namespace":"shop","Dimensions":[["Service"]],"Metrics":[{"Name":"OrderTotal","Unit":"Count"}]}]}}
```

In Lambda, stdout already goes to CloudWatch Logs. Elsewhere, use the
[CloudWatch agent](https://docs.aws.amazon.com/AmazonCloudWatch/latest/monitoring/CloudWatch_Embedded_Metric_Format_Generation_CloudWatch_Agent.html)
or another log shipper.

There are runnable examples in the
[package docs](https://pkg.go.dev/github.com/dangrier/aws-embedded-metrics-golang/emf#pkg-examples).

### Metrics

```go
logger := emf.New()

logger.Metric("Requests", 1)                            // int
logger.MetricFloat("Ratio", 0.75)                       // float64
logger.MetricAs("Errors", 2, emf.Count)                 // int with a unit
logger.MetricFloatAs("Latency", 12.5, emf.Milliseconds) // float64 with a unit
logger.Metrics(map[string]int{"Hits": 9, "Misses": 1})  // several at once
logger.MetricsFloatAs(map[string]float64{"P50": 10, "P99": 48}, emf.Milliseconds)
```

Every [CloudWatch unit](https://pkg.go.dev/github.com/dangrier/aws-embedded-metrics-golang/emf#MetricUnit)
has a constant, like `emf.Seconds`, `emf.Bytes`, `emf.Percent` and `emf.CountSecond`.

### Dimensions

```go
// Each call adds its own dimension set, so each metric is published once per set.
logger.Dimension("Service", "checkout").Dimension("Region", "ap-southeast-2")

// One set with both dimensions, so each metric is published once for the combination.
logger.DimensionSet(
	emf.NewDimension("Service", "checkout"),
	emf.NewDimension("Region", "ap-southeast-2"),
)
```

### Properties

Properties are logged with the metrics, but don't become metrics or dimensions. They're handy for
searching in CloudWatch Logs Insights.

```go
logger.Property("requestId", requestID)
```

### Namespaces and contexts

Metrics go in the `aws-embedded-metrics` namespace unless you set one. A context adds metrics with
their own namespace and dimensions to the same log line.

```go
logger := emf.New().Namespace("shop").Metric("Orders", 3)
logger.NewContext().
	Namespace("payments").
	Dimension("Provider", "card").
	MetricAs("Declined", 1, emf.Count)
logger.Log()
```

### Logging with defer

```go
func processOrder(id string) error {
	metrics := emf.New().Namespace("api").Dimension("Operation", "ProcessOrder")
	defer metrics.Log()

	start := time.Now()
	// ... process the order ...
	metrics.MetricFloatAs("Latency", float64(time.Since(start).Milliseconds()), emf.Milliseconds)
	return nil
}
```

## Options

```go
emf.New(
	emf.WithWriter(os.Stderr),                     // write somewhere other than stdout
	emf.WithTimestamp(time.Now().Add(-time.Hour)), // record metrics for another time
	emf.WithoutDimensions(),                       // leave out the Lambda dimensions and properties
	emf.WithLogGroup("my-logs"),                   // log group for the CloudWatch agent
	emf.WithErrorHandler(func(err error) {         // hear about skipped input and write errors
		log.Println(err)
	}),
)
```

### Lambda defaults

When `AWS_LAMBDA_FUNCTION_NAME` is set, each logger adds:

- a dimension set of `ServiceName` (the function name) and `ServiceType` (`AWS::Lambda::Function`)
- `executionEnvironment`, `memorySize`, `functionVersion` and `logStreamId` properties

Use `WithoutDimensions()` to leave these out. If X-Ray sampled the request, the trace ID is also
added as a `traceId` property.

## Spec compliance and errors

Output always follows the [EMF specification](https://docs.aws.amazon.com/AmazonCloudWatch/latest/monitoring/CloudWatch_Embedded_Metric_Format_Specification.html).
Input that would break it is skipped, and the rest is still logged. This includes NaN or infinite
values, invalid names, unknown units, and a name used for both a metric and a dimension.

Two limits are about when or how much you log, so the library reports them but still writes the
line:

- **Timestamp:** CloudWatch only publishes metrics with a timestamp from 14 days in the past to
  2 hours in the future. It keeps a line outside that window as a log event, but won't turn it into
  metrics.
- **Size:** CloudWatch Logs rejects log lines over 1 MB, usually because of very large properties.

Use `WithErrorHandler` to find out about any of these. The errors wrap `emf.ErrInvalid`:

```go
emf.New(emf.WithErrorHandler(func(err error) {
	if errors.Is(err, emf.ErrInvalid) {
		log.Println("metrics:", err)
	}
}))
```

### More than 100 metrics

A single call to `Log()` may write more than one line. The spec allows at most 100 metrics per log
event, and CloudWatch drops every metric in an event that has more. So if you log more than 100
metrics at once (across all contexts), they are split across several lines of up to 100 metrics
each. Every line keeps the same timestamp, properties and dimensions, and each metric keeps its own
namespace and dimensions. If your code reads the output (in tests, for example), expect one or more
lines per `Log()`.

## Concurrency

A `Logger` and its contexts are safe to use from several goroutines at once. Lines from concurrent
`Log()` calls are never mixed together. The error handler may be called from several goroutines
too, and it can safely use the logger.

## Development

Common tasks use [just](https://github.com/casey/just):

| Command              | What it does                                              |
|----------------------|-----------------------------------------------------------|
| `just check`         | Build, vet, lint and test, the same as CI                 |
| `just test-race`     | Tests with the race detector and coverage                 |
| `just fuzz [time]`   | Fuzz the output against the EMF spec (default 1 minute)   |
| `just bench`         | Run the benchmarks                                        |
| `just bench-compare` | Compare benchmarks with `main`, and fail on a regression  |
| `just scan`          | Scan for known vulnerabilities with Grype and govulncheck |
| `just fmt`           | Format the code                                           |

## Contributing

Pull requests are welcome. For major changes, please open an issue first to discuss what you would
like to change. Please make sure to update tests.

## License

[MIT](LICENSE)
