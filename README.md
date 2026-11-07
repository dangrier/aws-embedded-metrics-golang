# aws-embedded-metrics-golang

[![Go Reference](https://pkg.go.dev/badge/github.com/dangrier/aws-embedded-metrics-golang/emf.svg)](https://pkg.go.dev/github.com/dangrier/aws-embedded-metrics-golang/emf)
[![ci](https://github.com/dangrier/aws-embedded-metrics-golang/actions/workflows/ci.yml/badge.svg)](https://github.com/dangrier/aws-embedded-metrics-golang/actions/workflows/ci.yml)
[![security](https://github.com/dangrier/aws-embedded-metrics-golang/actions/workflows/security.yml/badge.svg)](https://github.com/dangrier/aws-embedded-metrics-golang/actions/workflows/security.yml)

A Go library for the AWS CloudWatch [Embedded Metric Format](https://docs.aws.amazon.com/AmazonCloudWatch/latest/monitoring/CloudWatch_Embedded_Metric_Format_Specification.html) (EMF).

EMF lets you publish CloudWatch metrics by writing JSON log lines. CloudWatch Logs reads them and
creates the metrics for you, so there are no CloudWatch API calls, credentials or clients in your
code, and nothing to mock in tests.

- Simple, chainable API for metrics, dimensions, properties and namespaces
- Lists of values and high-resolution (1 second) metrics
- Output always follows the EMF spec, and is tested against AWS's JSON Schema and fuzzed
- Bad input is skipped and reported, instead of silently breaking the whole log line
- Safe to use from several goroutines at once
- Reuse one logger for each unit of work with `Flush`
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

`Put` and `PutValues` take options for the unit and resolution:

```go
logger.Put("Latency", 12.5, emf.Unit(emf.Milliseconds))
logger.Put("QueueDepth", 42, emf.Unit(emf.Count), emf.HighResolution())
logger.PutValues("Latency", []float64{12.5, 8.1, 30.2}, emf.Unit(emf.Milliseconds))
```

Every [CloudWatch unit](https://pkg.go.dev/github.com/dangrier/aws-embedded-metrics-golang/emf#MetricUnit)
has a constant, like `emf.Seconds`, `emf.Bytes`, `emf.Percent` and `emf.CountSecond`.

### Lists of values

A metric can have a list of values, such as the latency of each request in a batch. CloudWatch keeps
every value, so statistics like percentiles stay accurate. Use `PutValues`, or log the same name
more than once and the values are collected:

```go
logger.Put("Latency", 12.5).Put("Latency", 8.1) // logs "Latency":[12.5,8.1]
```

The same name in two contexts is published in both namespaces, sharing all of its values, even
when the contexts are written on separate lines. So a repeated name must use the same unit and
resolution everywhere, or the new value is skipped and reported. The spec allows at most 100 values
per metric, so longer lists are split across several lines.

### High resolution

`emf.HighResolution()` stores a metric per second instead of per minute, so you can graph and alarm
on it at 1 second intervals. High-resolution metrics cost more.

### Dimensions

```go
// Each call adds its own dimension set, so each metric is published once per set.
logger.Dimension("Service", "checkout").Dimension("Region", "ap-southeast-2")

// A set with the same keys as one already added, in any order, isn't added again, but its
// values are updated. This leaves one ["Service"] set, with Service now "cart".
logger.Dimension("Service", "cart")

// One set with both dimensions, so each metric is published once for the combination.
logger.DimensionSet(
	emf.NewDimension("Service", "checkout"),
	emf.NewDimension("Region", "ap-southeast-2"),
)
```

A key repeated within one set is written once, with the last value. Repeats are only removed within
a context, since each context is a separate directive with its own namespace.

To replace or remove dimensions, use `SetDimensions` or `ResetDimensions`, on the logger or on a
context. `useDefault` says whether the context keeps the [Lambda default dimensions](#lambda-defaults),
as in the official AWS libraries:

```go
// Replace the dimension sets. With useDefault false, ServiceName and ServiceType are no longer
// written as dimensions, but the Lambda properties still are.
logger.SetDimensions(false,
	[]emf.Dimension{emf.NewDimension("Service", "checkout")},
	[]emf.Dimension{emf.NewDimension("Service", "checkout"), emf.NewDimension("Region", "ap-southeast-2")},
)

// Remove the dimensions you added, and keep (or bring back) the Lambda defaults.
logger.ResetDimensions(true)
```

Each set passed to `SetDimensions` is checked and added as with `DimensionSet`, and an invalid set is
skipped and reported while the others are kept. With no sets, `SetDimensions(useDefault)` is the same
as `ResetDimensions(useDefault)`. Both change only that context, in one step, so a concurrent `Log`
writes either the old dimensions or the new ones.

Once a dimension is removed, a metric or property may use its name, unless another context still has
that dimension. A property the dimension had replaced is written again. `ServiceName` and
`ServiceType` stay reserved while the logger has the Lambda defaults (in Lambda, without
`WithoutDimensions()`), even after a context drops them, so the defaults can be written again. Contexts that were written on separate lines because they disagreed on a value
share a line again once that value is removed.

### Properties

Properties are logged with the metrics, but don't become metrics or dimensions. They're handy for
searching in CloudWatch Logs Insights.

```go
logger.Property("requestId", requestID)
```

`Property` takes a string. For numbers, booleans, lists, maps and structs, use `PropertyValue`,
which writes the value as JSON:

```go
logger.
	PropertyValue("statusCode", 503).
	PropertyValue("cached", false).
	PropertyValue("tags", []string{"beta", "eu"})
```

Numbers matter because Logs Insights can only compare a field as a number if it was logged as one.
With `"statusCode":503`, this query finds the server errors, but with `"statusCode":"503"` it
doesn't:

```
fields @timestamp, requestId, statusCode
| filter statusCode >= 500
```

The value is encoded with `encoding/json` when `PropertyValue` is called, so its rules apply: struct
`json` tags are used, a `[]byte` is written as a base64 string, and a `MarshalJSON` method with a
pointer receiver is only used when you pass a pointer. Changing a map or slice afterwards doesn't
change what is logged. `nil` is written as `null`. A value that can't be encoded, such as a channel,
a func, `NaN`, or a `json.Marshaler` that fails or panics, is skipped and reported to the error
handler with an error that wraps `emf.ErrInvalid`. Setting a key again replaces its value, whether
it was set with `Property` or `PropertyValue`.

Each key in a log line must be unique, because when a JSON object has the same key twice, which
value a reader keeps is undefined. `encoding/json` writes each byte of a key that isn't valid UTF-8
as U+FFFD, so two different keys can be written as the same one. To avoid that:

- A property key that isn't valid UTF-8, such as `"\xff"`, is skipped and reported. This applies to
  `Property` and `PropertyValue`. Keys in other languages, like `"café"` or `"日本"`, are fine.
- A `PropertyValue` value whose JSON has an object with the same key more than once, at any depth,
  is skipped and reported. This can come from a map key that isn't valid UTF-8, as in
  `map[string]int{"\xff": 1, "\xfe": 2}`, or from a `json.Marshaler` or `json.RawMessage` that
  repeats a key.
- A `PropertyValue` value from a `json.Marshaler` or `json.RawMessage` whose JSON isn't valid UTF-8
  is skipped and reported. `encoding/json` copies that JSON as it is, and readers decode invalid
  bytes in different ways, so keys that differ in Go can be one key elsewhere. Go strings are fine:
  `encoding/json` replaces their invalid bytes with U+FFFD.

A property shares the log line with dimensions and metrics, so each key can only hold one of them.
This applies to properties set with `Property` and `PropertyValue` alike. A property with the key of
a metric or dimension is skipped and reported. A dimension or metric added later with a property's
key is kept, and replaces the property, which is reported too. A dimension replaces it on every
line, and a metric on the lines the metric is written on. The Lambda properties the library adds
(see [Lambda defaults](#lambda-defaults)) can be replaced without a report.

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

Each context keeps its own dimension values. Contexts that disagree on a dimension value (the same
key with different values) are written on separate lines, so each context's metrics are published
under its own value. Contexts that agree still share a line. Properties go on every line.

```go
logger := emf.New()
logger.NewContext().Namespace("payments").Dimension("Provider", "card").Metric("Declined", 1)
logger.NewContext().Namespace("payments").Dimension("Provider", "paypal").Metric("Refunds", 2)
logger.Log() // two lines: one with "Provider":"card", one with "Provider":"paypal"
```

`Flush()` removes the contexts after writing them. See [Reusing a logger with Flush](#reusing-a-logger-with-flush).

### Logging with defer

Create a new logger for each unit of work, such as a request or a batch, and call `Log()` once at
the end, or reuse one logger and call [`Flush()`](#reusing-a-logger-with-flush) at the end of each:

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

`Log()` doesn't clear anything. Each `Log()` writes every value added since `New()`, so calling it
again writes the earlier values again. A metric logged once before each of two `Log()` calls comes
out as `"Requests":1`, then `"Requests":[1,1]`. Values keep building up for as long as the logger
lives, and every line keeps the timestamp set when `New()` was called (or by `WithTimestamp` or
`SetTimestamp`).

So in Lambda, with `Log()`, create the logger inside the handler, not at package level or in `init`.
A logger shared across invocations double counts, keeps growing, and stamps every metric with the
cold-start time. To share one logger, use `Flush()` instead.

### Reusing a logger with Flush

`Flush()` writes the same lines as `Log()`, then clears the metric values and properties, and
removes the contexts, so the logger is ready for the next unit of work. This is how the official AWS libraries' `flush()` works.
In Lambda, create the logger once, and flush it at the end of each invocation:

```go
var metrics = emf.New().Namespace("api").Dimension("Operation", "GetItem")

func handler(ctx context.Context, req Request) (Response, error) {
	defer metrics.Flush()

	metrics.Property("requestId", req.ID).MetricAs("Requests", 1, emf.Count)
	// ...
	return Response{}, nil
}
```

Each invocation writes `"Requests":1`, never `[1,1]`. After a flush:

- **Cleared:** metric values, and properties set with `Property` or `PropertyValue`. A metric name
  can be used again with another unit, or as a dimension or property, and a dimension or metric
  isn't reported as replacing a property that was cleared.
- **Removed:** the contexts made with `NewContext`, with their dimensions, so one invocation's
  dimension values, such as a customer ID, are never written on the next one's lines. Make the
  contexts for each unit of work after the last flush. A context kept after the flush that removed
  it does nothing, and each use is reported to the error handler.
- **Kept:** the namespace and dimensions of the logger's default context, its choice about the
  Lambda default dimensions, and the Lambda properties, which are written on every line. Lambda
  sets the X-Ray trace for each invocation, so each flush reads `traceId` again.
- **Timestamp:** each flush writes the time it is called, so a logger kept for hours doesn't write
  an old time. A timestamp set with `WithTimestamp` or `SetTimestamp` is written instead, by every
  flush, until it is set again. `Log()` still writes the time of `New()`.

`Flush()` with no metrics writes nothing, as `Log()` does, but still clears the properties and
removes the contexts. Values added from other goroutines while a flush runs are written by that
flush or the next one, never both.

On a reused logger, call only `Flush()`. `Log()` clears nothing, so a value logged by `Log()` is
written again by the next `Flush()`, and `Log()` still writes the time of `New()`.

The pattern above assumes one invocation at a time in each process, which is how Lambda runs
functions by default. If one process handles several requests at once, they share the logger: one
request's `Flush()` writes the others' metrics and clears their properties. Then create a logger for
each request instead.

To also drop the dimensions you added to the logger itself after each flush, so each unit of work
adds its own, use `WithResetDimensionsOnFlush()`. Each flush then works like `ResetDimensions` on
the default context, including dimensions set with `SetDimensions`, and keeps its choice about the
Lambda default dimensions: if it dropped them with `SetDimensions(false, ...)` or
`ResetDimensions(false)`, it doesn't get them back.

```go
metrics := emf.New(emf.WithResetDimensionsOnFlush()).Namespace("api")
metrics.Dimension("Customer", "acme").Metric("Requests", 1).Flush() // "Dimensions":[["Customer"]]
metrics.Metric("Requests", 1).Flush()                              // "Dimensions":[[]]
```

The official libraries differ on this default: Node.js keeps your dimensions after a flush, as this
library does, and Python drops them.

### Timestamps

A logger writes the time `New()` was called, or with `Flush()`, the time of each flush. To record
metrics for another time, such as when an event happened, set the timestamp with `WithTimestamp` or
`SetTimestamp`:

```go
metrics.SetTimestamp(event.Time).Metric("Messages", 1).Flush()
```

A timestamp set either way applies to every line written afterwards, by `Log()` or `Flush()`, until
it is set again. There is no way back to the time of each flush, and the zero `time.Time` isn't
special: it is written as that time, and reported as outside CloudWatch's window. It is checked against [CloudWatch's window](#spec-compliance-and-errors) when a
line is written, not when it is set.

## Options

```go
emf.New(
	emf.WithWriter(os.Stderr),                     // write somewhere other than stdout
	emf.WithTimestamp(time.Now().Add(-time.Hour)), // record metrics for another time
	emf.WithResetDimensionsOnFlush(),              // drop your dimensions after each Flush
	emf.WithoutDimensions(),                       // leave out the Lambda dimensions and properties
	emf.WithMergedDefaultDimensions(),             // add the Lambda dimensions to each of your sets
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

Use `WithoutDimensions()` to leave these out. To keep the properties but drop the dimension set for
one context, use `SetDimensions(false, ...)` with the sets that context should have, or
`ResetDimensions(false)`, which also removes the dimensions you added to it. If X-Ray sampled the
request, the trace ID is also added as a `traceId` property. The environment is read once, by `New`,
and contexts made later with `NewContext` use the same values, except that each `Flush()` reads the
trace ID again, since Lambda sets it for each invocation.

By default, `ServiceName` and `ServiceType` are written as a set of their own, alongside yours. So
once you add a dimension, each metric is published twice: once per function, and once for your
dimension across all functions.

```json
"Dimensions":[["ServiceName","ServiceType"],["Service"]]
```

`WithMergedDefaultDimensions()` adds them to each of your sets instead, as the official AWS
libraries do:

```go
emf.New(emf.WithMergedDefaultDimensions()).Dimension("Service", "checkout").Metric("Orders", 1).Log()
```

```json
"Dimensions":[["ServiceName","ServiceType","Service"]]
```

Each metric is no longer published for the default set on its own, which usually saves one custom
metric per name. A context with no sets of its own still gets `["ServiceName","ServiceType"]`. If
merging makes two sets the same, such as a set you added with `ServiceName` in it, the set is
written once. The 30-key limit counts the merged set, so a set can have at most 28 keys besides
`ServiceName` and `ServiceType`. A set with more is skipped and reported. The option does nothing
outside Lambda, or with `WithoutDimensions()`.

Turning it on for existing code moves each Lambda metric to the combined series. The old series stop
getting data, so update any dashboards and alarms that use them.

## Spec compliance and errors

Output always follows the [EMF specification](https://docs.aws.amazon.com/AmazonCloudWatch/latest/monitoring/CloudWatch_Embedded_Metric_Format_Specification.html).
Input that would break it is skipped, and the rest is still logged. This includes NaN or infinite
values, invalid names, unknown units, a name used for both a metric and a dimension, a property
value that can't be encoded as JSON, and a property key or value that would repeat a JSON key.

Some input is reported, but still written:

- **Timestamp:** CloudWatch only publishes metrics with a timestamp from 14 days in the past to
  2 hours in the future. It keeps a line outside that window as a log event, but won't turn it into
  metrics. A timestamp set with `WithTimestamp` or `SetTimestamp` is checked each time a line is
  written, by `Log()` or `Flush()`.
- **Size:** CloudWatch Logs rejects log lines over 1 MB, usually because of very large properties.
- **Replaced property:** a dimension or metric with the same key as a property replaces it. See
  [Properties](#properties).

Use `WithErrorHandler` to find out about any of these. The errors wrap `emf.ErrInvalid`:

```go
emf.New(emf.WithErrorHandler(func(err error) {
	if errors.Is(err, emf.ErrInvalid) {
		log.Println("metrics:", err)
	}
}))
```

### More than 100 metrics or values

A single call to `Log()` or `Flush()` may write more than one line. The spec allows at most 100 metrics per log
event, and 100 values per metric, and CloudWatch drops every metric in an event that has more. So if
you log more than 100 metrics at once (across all contexts that share a line), or a metric with more
than 100 values, they are split across several lines. Contexts that disagree on a dimension value
are also written on separate lines, and each of those is split the same way. Every line keeps the
same timestamp and properties, and each metric keeps its own namespace, dimensions and dimension
values. If your code reads the output (in tests, for example), expect one or more lines per `Log()`
or `Flush()`.

## Concurrency

A `Logger` and its contexts are safe to use from several goroutines at once. Lines from concurrent
`Log()` and `Flush()` calls are never mixed together. The error handler may be called from several goroutines
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
