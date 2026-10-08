# aws-embedded-metrics-golang

[![ci](https://github.com/dangrier/aws-embedded-metrics-golang/actions/workflows/ci.yml/badge.svg)](https://github.com/dangrier/aws-embedded-metrics-golang/actions/workflows/ci.yml)
[![security](https://github.com/dangrier/aws-embedded-metrics-golang/actions/workflows/security.yml/badge.svg)](https://github.com/dangrier/aws-embedded-metrics-golang/actions/workflows/security.yml)

Go implementation of AWS CloudWatch [Embedded Metric Format](https://docs.aws.amazon.com/AmazonCloudWatch/latest/monitoring/CloudWatch_Embedded_Metric_Format_Specification.html)

Forked from [prozz/aws-embedded-metrics-golang](https://github.com/prozz/aws-embedded-metrics-golang).

It's aim is to simplify reporting metrics to CloudWatch:

- using EMF avoids additional HTTP API calls to CloudWatch as metrics are logged in JSON format to stdout
- no need for additional dependencies in your services (or mocks in tests) to report metrics from inside your code
- built in support for default dimensions and properties for Lambda functions

Supports namespaces, setting dimensions and properties as well as different contexts (at least partially).

## Installation

Requires Go 1.24 or newer.

```shell
go get github.com/dangrier/aws-embedded-metrics-golang
```

## Usage

```
emf.New().Namespace("mtg").Metric("totalWins", 1500).Log()

emf.New().Dimension("colour", "red").
    MetricAs("gameLength", 2, emf.Seconds).Log()

emf.New().DimensionSet(
        emf.NewDimension("format", "edh"),
        emf.NewDimension("commander", "Muldrotha")).
    MetricAs("wins", 1499, emf.Count).Log()
```

You may also use the lib together with `defer`.

```
m := emf.New() // sets up whatever you fancy here
defer m.Log()

// any reporting metrics calls
```

Customizing the logger:
```
emf.New(
    emf.WithWriter(os.Stderr), // Log to stderr.
    emf.WithTimestamp(time.Now().Add(-time.Hour)), // Record past metrics.
    emf.WithoutDimensions(), // Do not include useful Lambda related dimensions.
    emf.WithLogGroup("my-logs"), // Add specific log group.
    emf.WithErrorHandler(func(err error) { log.Println(err) }), // Hear about skipped input.
)
```

Output always follows the [EMF specification](https://docs.aws.amazon.com/AmazonCloudWatch/latest/monitoring/CloudWatch_Embedded_Metric_Format_Specification.html).
Input that would break it is skipped, and the rest is still logged. This includes NaN or infinite
values, invalid names, unknown units, and a name used for both a metric and a dimension.
Use `WithErrorHandler` to find out what was skipped. Those errors wrap `emf.ErrInvalid`.

A single call to `Log()` may write more than one line. The spec allows at most 100 metrics
per log event, and CloudWatch drops every metric in an event that has more. So if you log
more than 100 metrics at once (across all contexts), they are split across several lines of
up to 100 metrics each. Every line keeps the same timestamp, properties and dimensions, and
each metric keeps its own namespace and dimensions. If your code reads the output (in tests,
for example), expect one or more lines per `Log()`.

Functions for reporting metrics:

```
func Metric(name string, value int)
func Metrics(m map[string]int)
func MetricAs(name string, value int, unit MetricUnit)
func MetricsAs(m map[string]int, unit MetricUnit)

func MetricFloat(name string, value float64)
func MetricsFloat(m map[string]float64)
func MetricFloatAs(name string, value float64, unit MetricUnit)
func MetricsFloatAs(m map[string]float64, unit MetricUnit)
```

Functions for setting up dimensions:

```
func Dimension(key, value string)
func DimensionSet(dimensions ...Dimension) // use `func NewDimension` for creating one
```

## Contributing
Pull requests are welcome. For major changes, please open an issue first to discuss what you would like to change.
Please make sure to update tests.

## License
[MIT](https://choosealicense.com/licenses/mit/)