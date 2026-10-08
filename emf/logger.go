package emf

import (
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"maps"
	"os"
	"strings"
	"time"
)

// Logger for metrics with default Context.
type Logger struct {
	out               io.Writer
	timestamp         int64
	defaultContext    Context
	contexts          []*Context
	values            map[string]any
	withoutDimensions bool
	logGroupName      string
	onError           func(error)
	registry          *registry
}

// Context gives ability to add another MetricDirective section for Logger.
type Context struct {
	metricDirective MetricDirective
	values          map[string]any
	registry        *registry
}

// LoggerOption defines a function that can be used to customize a logger.
type LoggerOption func(l *Logger)

// WithWriter customizes the writer used by a logger.
func WithWriter(w io.Writer) LoggerOption {
	return func(l *Logger) {
		l.out = w
	}
}

// WithTimestamp customizes the timestamp used by a logger. CloudWatch only
// publishes metrics with a timestamp from 14 days in the past to 2 hours in
// the future, checked when Log is called.
func WithTimestamp(t time.Time) LoggerOption {
	return func(l *Logger) {
		l.timestamp = t.UnixMilli()
	}
}

// WithoutDimensions ignores default AWS Lambda related properties and dimensions.
func WithoutDimensions() LoggerOption {
	return func(l *Logger) {
		l.withoutDimensions = true
	}
}

// WithLogGroup sets the log group when ingesting metrics into Cloudwatch Logging Agent.
func WithLogGroup(logGroup string) LoggerOption {
	return func(l *Logger) {
		l.logGroupName = logGroup
	}
}

// WithErrorHandler sets a function that is called when the logger skips
// input that would break the EMF spec, or fails to write. Skipped input
// errors wrap ErrInvalid. Without a handler, these errors are ignored.
func WithErrorHandler(fn func(error)) LoggerOption {
	return func(l *Logger) {
		l.onError = fn
	}
}

// New creates logger with reasonable defaults for Lambda functions:
// - Prints to os.Stdout.
// - Context based on Lambda environment variables.
// - Timestamp set to the time when New was called.
// Specify LoggerOptions to customize the logger.
func New(opts ...LoggerOption) *Logger {
	l := Logger{
		out:       os.Stdout,
		timestamp: time.Now().UnixMilli(),
	}

	// apply any options
	for _, opt := range opts {
		opt(&l)
	}

	values := make(map[string]any)

	if !l.withoutDimensions {
		// set default properties for lambda function
		fnName := os.Getenv("AWS_LAMBDA_FUNCTION_NAME")
		if fnName != "" {
			values["executionEnvironment"] = os.Getenv("AWS_EXECUTION_ENV")
			values["memorySize"] = os.Getenv("AWS_LAMBDA_FUNCTION_MEMORY_SIZE")
			values["functionVersion"] = os.Getenv("AWS_LAMBDA_FUNCTION_VERSION")
			values["logStreamId"] = os.Getenv("AWS_LAMBDA_LOG_STREAM_NAME")
		}
	}

	// only collect traces which have been sampled
	amznTraceID := os.Getenv("_X_AMZN_TRACE_ID")
	if strings.Contains(amznTraceID, "Sampled=1") {
		values["traceId"] = amznTraceID
	}

	l.values = values
	l.registry = newRegistry(l.onError)
	l.defaultContext = newContext(values, l.withoutDimensions, l.registry)

	return &l
}

// Dimension helps builds DimensionSet.
type Dimension struct {
	Key, Value string
}

// NewDimension creates Dimension from key/value pair.
func NewDimension(key, value string) Dimension {
	return Dimension{
		Key:   key,
		Value: value,
	}
}

// Namespace sets namespace on default context.
func (l *Logger) Namespace(namespace string) *Logger {
	l.defaultContext.Namespace(namespace)
	return l
}

// Property sets property. A property can't use the name of a metric or
// dimension.
func (l *Logger) Property(key, value string) *Logger {
	if l.registry.checkProperty(key) {
		l.values[key] = value
	}
	return l
}

// Dimension adds single dimension on default context.
func (l *Logger) Dimension(key, value string) *Logger {
	l.defaultContext.Dimension(key, value)
	return l
}

// DimensionSet adds multiple dimensions on default context.
func (l *Logger) DimensionSet(dimensions ...Dimension) *Logger {
	l.defaultContext.DimensionSet(dimensions...)
	return l
}

// Metric puts int metric on default context.
func (l *Logger) Metric(name string, value int) *Logger {
	l.defaultContext.put(name, value, None)
	return l
}

// Metrics puts all of the int metrics on default context.
func (l *Logger) Metrics(m map[string]int) *Logger {
	return l.MetricsAs(m, None)
}

// MetricFloat puts float metric on default context.
func (l *Logger) MetricFloat(name string, value float64) *Logger {
	l.defaultContext.put(name, value, None)
	return l
}

// MetricsFloat puts all of the float metrics on default context.
func (l *Logger) MetricsFloat(m map[string]float64) *Logger {
	return l.MetricsFloatAs(m, None)
}

// MetricAs puts int metric with MetricUnit on default context.
func (l *Logger) MetricAs(name string, value int, unit MetricUnit) *Logger {
	l.defaultContext.put(name, value, unit)
	return l
}

// MetricsAs puts all of the int metrics with MetricUnit on default context.
func (l *Logger) MetricsAs(m map[string]int, unit MetricUnit) *Logger {
	for name, value := range m {
		l.defaultContext.put(name, value, unit)
	}
	return l
}

// MetricFloatAs puts float metric with MetricUnit on default context.
func (l *Logger) MetricFloatAs(name string, value float64, unit MetricUnit) *Logger {
	l.defaultContext.put(name, value, unit)
	return l
}

// MetricsFloatAs puts all of the float metrics with MetricUnit on default context.
func (l *Logger) MetricsFloatAs(m map[string]float64, unit MetricUnit) *Logger {
	for name, value := range m {
		l.defaultContext.put(name, value, unit)
	}
	return l
}

// Log prints all Contexts and metric values to chosen output in Embedded Metric Format.
// The spec allows at most 100 metrics per log event, so more than that are
// split across several lines. A timestamp outside the window CloudWatch
// accepts (14 days in the past to 2 hours in the future) is reported to the
// error handler, but the lines are still written.
func (l *Logger) Log() {
	var metrics []MetricDirective
	if len(l.defaultContext.metricDirective.Metrics) > 0 {
		metrics = append(metrics, l.defaultContext.metricDirective)
	}
	for _, v := range l.contexts {
		if len(v.metricDirective.Metrics) > 0 {
			metrics = append(metrics, v.metricDirective)
		}
	}

	if len(metrics) == 0 {
		return
	}

	l.registry.checkTimestamp(time.UnixMilli(l.timestamp), time.Now())
	for event := range splitDirectives(metrics, maxMetricsPerEvent) {
		l.write(event)
	}
}

// splitDirectives yields log events with at most limit metrics each. A
// directive with too many metrics is split into several directives with the
// same namespace and dimensions.
func splitDirectives(directives []MetricDirective, limit int) iter.Seq[[]MetricDirective] {
	return func(yield func([]MetricDirective) bool) {
		var event []MetricDirective
		count := 0
		for _, d := range directives {
			for remaining := d.Metrics; len(remaining) > 0; {
				n := min(limit-count, len(remaining))
				part := d
				part.Metrics, remaining = remaining[:n], remaining[n:]
				event = append(event, part)
				count += n
				if count == limit {
					if !yield(event) {
						return
					}
					event, count = nil, 0
				}
			}
		}
		if len(event) > 0 {
			yield(event)
		}
	}
}

// write prints one log event. It holds every property and dimension, but
// only the metric values its directives refer to.
func (l *Logger) write(directives []MetricDirective) {
	values := maps.Clone(l.values)
	maps.DeleteFunc(values, func(key string, _ any) bool {
		return l.registry.metrics[key]
	})
	for _, d := range directives {
		for _, m := range d.Metrics {
			values[m.Name] = l.values[m.Name]
		}
	}
	values[metadataKey] = Metadata{
		Timestamp:    l.timestamp,
		Metrics:      directives,
		LogGroupName: l.logGroupName,
	}

	buf, err := json.Marshal(values)
	if err != nil {
		l.registry.report(fmt.Errorf("emf: encoding metrics: %w", err))
		return
	}
	if _, err := fmt.Fprintln(l.out, string(buf)); err != nil {
		l.registry.report(fmt.Errorf("emf: writing metrics: %w", err))
	}
}

// NewContext creates new context for given logger.
func (l *Logger) NewContext() *Context {
	c := newContext(l.values, l.withoutDimensions, l.registry)
	l.contexts = append(l.contexts, &c)
	return &c
}

// Namespace sets namespace on given context.
func (c *Context) Namespace(namespace string) *Context {
	if c.registry.checkNamespace(namespace) {
		c.metricDirective.Namespace = namespace
	}
	return c
}

// Dimension adds single dimension on given context.
func (c *Context) Dimension(key, value string) *Context {
	return c.DimensionSet(NewDimension(key, value))
}

// DimensionSet adds multiple dimensions on given context. If any dimension
// is invalid, the whole set is skipped.
func (c *Context) DimensionSet(dimensions ...Dimension) *Context {
	if !c.registry.checkDimensionSet(dimensions) {
		return c
	}
	set := make(DimensionSet, 0, len(dimensions))
	for _, d := range dimensions {
		set = append(set, d.Key)
		c.values[d.Key] = d.Value
		c.registry.dimensions[d.Key] = true
	}
	c.metricDirective.Dimensions = append(c.metricDirective.Dimensions, set)
	return c
}

// Metric puts int metric on given context.
func (c *Context) Metric(name string, value int) *Context {
	return c.put(name, value, None)
}

// MetricFloat puts float metric on given context.
func (c *Context) MetricFloat(name string, value float64) *Context {
	return c.put(name, value, None)
}

// MetricAs puts int metric with MetricUnit on given context.
func (c *Context) MetricAs(name string, value int, unit MetricUnit) *Context {
	return c.put(name, value, unit)
}

// MetricFloatAs puts float metric with MetricUnit on given context.
func (c *Context) MetricFloatAs(name string, value float64, unit MetricUnit) *Context {
	return c.put(name, value, unit)
}

func newContext(values map[string]any, withoutDimensions bool, reg *registry) Context {
	var defaultDimensions []DimensionSet
	if !withoutDimensions {
		// set default dimensions for lambda function
		fnName := os.Getenv("AWS_LAMBDA_FUNCTION_NAME")
		if fnName != "" {
			defaultDimensions = []DimensionSet{{"ServiceName", "ServiceType"}}
			values["ServiceType"] = "AWS::Lambda::Function"
			values["ServiceName"] = fnName
			reg.dimensions["ServiceType"] = true
			reg.dimensions["ServiceName"] = true
		}
	}

	return Context{
		metricDirective: MetricDirective{
			Namespace:  "aws-embedded-metrics",
			Dimensions: defaultDimensions,
		},
		values:   values,
		registry: reg,
	}
}

func (c *Context) put(name string, value any, unit MetricUnit) *Context {
	if !c.registry.checkMetric(name, value, unit) {
		return c
	}
	c.registry.metrics[name] = true
	c.metricDirective.Metrics = append(c.metricDirective.Metrics, MetricDefinition{
		Name: name,
		Unit: unit,
	})
	c.values[name] = value
	return c
}
