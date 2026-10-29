package emf

import (
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"maps"
	"os"
	"slices"
	"strings"
	"sync"
	"time"
)

// Logger for metrics with default Context. A Logger and its Contexts are
// safe to use from several goroutines at once.
type Logger struct {
	out               io.Writer
	timestamp         int64
	defaultContext    Context
	contexts          []*Context
	properties        map[string]any
	withoutDimensions bool
	logGroupName      string
	onError           func(error)
	registry          *registry
	writeMu           sync.Mutex
}

// Context gives ability to add another MetricDirective section for Logger.
type Context struct {
	metricDirective MetricDirective
	dimensions      []dimensionValue
	registry        *registry
}

// dimensionValue is a context's value for a dimension key. The value is
// stored as an any so it isn't converted again for each log event.
type dimensionValue struct {
	key   string
	value any
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
// The handler may use the logger. If the logger is used from several
// goroutines, the handler may be called from several goroutines at once.
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

	for _, opt := range opts {
		opt(&l)
	}

	if !l.withoutDimensions {
		// set default properties for lambda function
		fnName := os.Getenv("AWS_LAMBDA_FUNCTION_NAME")
		if fnName != "" {
			l.setProperty("executionEnvironment", os.Getenv("AWS_EXECUTION_ENV"))
			l.setProperty("memorySize", os.Getenv("AWS_LAMBDA_FUNCTION_MEMORY_SIZE"))
			l.setProperty("functionVersion", os.Getenv("AWS_LAMBDA_FUNCTION_VERSION"))
			l.setProperty("logStreamId", os.Getenv("AWS_LAMBDA_LOG_STREAM_NAME"))
		}
	}

	// only collect traces which have been sampled
	amznTraceID := os.Getenv("_X_AMZN_TRACE_ID")
	if strings.Contains(amznTraceID, "Sampled=1") {
		l.setProperty("traceId", amznTraceID)
	}

	l.registry = newRegistry(l.onError)
	l.registry.logger = &l
	l.defaultContext = newContext(l.withoutDimensions, l.registry)

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
	l.registry.do(func() { l.defaultContext.namespace(namespace) })
	return l
}

// Property sets property. A property can't use the name of a metric or
// dimension.
func (l *Logger) Property(key, value string) *Logger {
	l.registry.do(func() {
		if l.registry.checkProperty(key) {
			l.setProperty(key, value)
		}
	})
	return l
}

// setProperty sets a property whose key is already checked. It must be
// called while holding the registry lock, or from New.
func (l *Logger) setProperty(key, value string) {
	if l.properties == nil {
		l.properties = make(map[string]any)
	}
	l.properties[key] = value
}

// Dimension adds single dimension on default context. See DimensionSet for
// what happens when the key is already set.
func (l *Logger) Dimension(key, value string) *Logger {
	return l.DimensionSet(NewDimension(key, value))
}

// DimensionSet adds multiple dimensions on default context. If any dimension
// is invalid, the whole set is skipped. A key given more than once is added
// once, and the last value wins. If the context already has a set with the
// same keys, in any order, the set isn't added again, but the values are
// updated.
func (l *Logger) DimensionSet(dimensions ...Dimension) *Logger {
	l.registry.do(func() { l.defaultContext.dimensionSet(dimensions) })
	return l
}

// Metric puts int metric on default context.
func (l *Logger) Metric(name string, value int) *Logger {
	return l.MetricAs(name, value, None)
}

// Metrics puts all of the int metrics on default context.
func (l *Logger) Metrics(m map[string]int) *Logger {
	return l.MetricsAs(m, None)
}

// MetricFloat puts float metric on default context.
func (l *Logger) MetricFloat(name string, value float64) *Logger {
	return l.MetricFloatAs(name, value, None)
}

// MetricsFloat puts all of the float metrics on default context.
func (l *Logger) MetricsFloat(m map[string]float64) *Logger {
	return l.MetricsFloatAs(m, None)
}

// MetricAs puts int metric with MetricUnit on default context.
func (l *Logger) MetricAs(name string, value int, unit MetricUnit) *Logger {
	l.registry.do(func() { l.defaultContext.put(name, value, unit, standardResolution) })
	return l
}

// MetricsAs puts all of the int metrics with MetricUnit on default context.
func (l *Logger) MetricsAs(m map[string]int, unit MetricUnit) *Logger {
	l.registry.do(func() {
		for name, value := range m {
			l.defaultContext.put(name, value, unit, standardResolution)
		}
	})
	return l
}

// MetricFloatAs puts float metric with MetricUnit on default context.
func (l *Logger) MetricFloatAs(name string, value float64, unit MetricUnit) *Logger {
	l.registry.do(func() { l.defaultContext.put(name, value, unit, standardResolution) })
	return l
}

// MetricsFloatAs puts all of the float metrics with MetricUnit on default context.
func (l *Logger) MetricsFloatAs(m map[string]float64, unit MetricUnit) *Logger {
	l.registry.do(func() {
		for name, value := range m {
			l.defaultContext.put(name, value, unit, standardResolution)
		}
	})
	return l
}

// Log prints all Contexts and metric values to chosen output in Embedded Metric Format.
// Contexts share a line unless they disagree on a dimension value (the same
// key with different values), in which case they are written on separate
// lines so each context's metrics keep their own value. The spec allows at
// most 100 metrics per log event, and 100 values per metric, so more than
// that are split across several lines. A timestamp outside the window CloudWatch
// accepts (14 days in the past to 2 hours in the future), or a line over
// CloudWatch's 1 MB limit, is reported to the error handler, but the lines
// are still written.
//
// A Logger isn't meant to be reused. Log writes every value added since New,
// so calling it again writes the earlier values again, and the timestamp stays
// at the time New was called (or the one set by WithTimestamp). Create a new
// Logger for each unit of work, such as each Lambda invocation.
func (l *Logger) Log() {
	// Take a snapshot under the lock, then encode outside it so concurrent
	// Log calls don't wait on each other. Writing has its own lock, so lines
	// are never mixed together.
	var events []map[string]any
	l.registry.do(func() { events = l.snapshot() })

	var errs []error
	lines := make([][]byte, 0, len(events))
	for _, values := range events {
		buf, err := json.Marshal(values)
		if err != nil {
			errs = append(errs, fmt.Errorf("emf: encoding metrics: %w", err))
			continue
		}
		if len(buf) > maxEventBytes {
			errs = append(errs, fmt.Errorf("%w: log line is %d bytes, over CloudWatch's 1 MB limit, so CloudWatch will reject it",
				ErrInvalid, len(buf)))
		}
		lines = append(lines, append(buf, '\n'))
	}

	l.writeMu.Lock()
	for _, line := range lines {
		if _, err := l.out.Write(line); err != nil {
			errs = append(errs, fmt.Errorf("emf: writing metrics: %w", err))
		}
	}
	l.writeMu.Unlock()

	l.registry.handle(errs)
}

// snapshot returns the root object of each log event to write. It must be
// called while holding the registry lock. Metric and dimension slices are
// only ever appended to, so the snapshot stays valid after the lock is
// released.
func (l *Logger) snapshot() []map[string]any {
	directives := l.directives()
	if len(directives) == 0 {
		return nil
	}

	l.registry.checkTimestamp(time.UnixMilli(l.timestamp), time.Now())

	// A metric with more than 100 values is logged over several rounds of
	// events, with up to 100 of its values in each round.
	rounds := 1
	for name := range l.registry.more {
		rounds = max(rounds, (l.registry.valueCount(name)+maxValuesPerMetric-1)/maxValuesPerMetric)
	}

	// The dimension values every line carries. With only the default
	// context, its values are copied straight into each line instead, and
	// there are no other contexts to disagree with.
	var dims map[string]any
	disagree := false
	if len(l.contexts) > 0 {
		dims, disagree = l.lineDimensions()
	}

	// Usually the contexts agree on every dimension value, so they share
	// lines. Contexts that disagree are written on separate lines.
	if !disagree {
		return l.groupEvents(nil, contextGroup{directives: directives}, dims, rounds)
	}
	var events []map[string]any
	for _, g := range l.groupContexts() {
		events = l.groupEvents(events, g, dims, rounds)
	}
	return events
}

// lineDimensions returns the dimension values of every context, for keys
// that a line's own contexts don't set, and whether any two contexts with
// metrics disagree on a value. Later contexts with metrics replace earlier
// ones. A context without metrics only adds keys no context with metrics
// sets, so it can't change the value a context with metrics is written with.
func (l *Logger) lineDimensions() (dims map[string]any, disagree bool) {
	size := 0
	for c := range l.allContexts() {
		size = max(size, len(c.dimensions))
	}
	dims = make(map[string]any, size)
	var without []*Context
	for c := range l.allContexts() {
		if len(c.metricDirective.Metrics) == 0 {
			without = append(without, c)
			continue
		}
		for _, d := range c.dimensions {
			if v, ok := dims[d.key]; ok && v != d.value {
				disagree = true
			}
			dims[d.key] = d.value
		}
	}
	// Later contexts without metrics win among themselves, so add them last
	// first, keeping keys that are already set.
	for _, c := range slices.Backward(without) {
		for _, d := range c.dimensions {
			if _, ok := dims[d.key]; !ok {
				dims[d.key] = d.value
			}
		}
	}
	return dims, disagree
}

// groupEvents appends the root objects of a group's log events to events.
func (l *Logger) groupEvents(events []map[string]any, g contextGroup, dims map[string]any, rounds int) []map[string]any {
	for round := range rounds {
		directives := g.directives
		if round > 0 {
			directives = l.directivesInRound(g.directives, round)
		}
		for event := range splitDirectives(directives, maxMetricsPerEvent) {
			events = append(events, l.eventValues(event, g, dims, round))
		}
	}
	return events
}

// directives returns the directive of each context with metrics.
func (l *Logger) directives() []MetricDirective {
	var directives []MetricDirective
	for c := range l.allContexts() {
		if len(c.metricDirective.Metrics) > 0 {
			directives = append(directives, c.metricDirective)
		}
	}
	return directives
}

// addDimensions adds c's dimension values to values, and reports whether
// they agree with the values already there. It stops at the first value
// that doesn't agree.
func addDimensions(values map[string]any, c *Context) bool {
	for _, d := range c.dimensions {
		if v, ok := values[d.key]; ok && v != d.value {
			return false
		}
		values[d.key] = d.value
	}
	return true
}

// contextGroup is a set of contexts with metrics whose dimension values
// agree, so their directives can share log lines.
type contextGroup struct {
	contexts   []*Context // nil when every context with metrics agrees
	directives []MetricDirective
	values     map[string]any // dimension values of the contexts, while grouping
}

// agrees reports whether c's dimension values agree with every context in
// the group.
func (g contextGroup) agrees(c *Context) bool {
	for _, d := range c.dimensions {
		if v, ok := g.values[d.key]; ok && v != d.value {
			return false
		}
	}
	return true
}

// groupContexts returns the contexts with metrics in groups whose contexts
// don't disagree on any dimension value. Each context joins the first group
// it agrees with, so groups and the contexts in them keep the order of
// allContexts.
func (l *Logger) groupContexts() []contextGroup {
	var groups []contextGroup
	for c := range l.allContexts() {
		if len(c.metricDirective.Metrics) == 0 {
			continue
		}
		i := slices.IndexFunc(groups, func(g contextGroup) bool { return g.agrees(c) })
		if i < 0 {
			i = len(groups)
			groups = append(groups, contextGroup{values: make(map[string]any)})
		}
		addDimensions(groups[i].values, c)
		groups[i].contexts = append(groups[i].contexts, c)
		groups[i].directives = append(groups[i].directives, c.metricDirective)
	}
	return groups
}

// allContexts yields the default context, then the contexts in the order
// they were created.
func (l *Logger) allContexts() iter.Seq[*Context] {
	return func(yield func(*Context) bool) {
		if !yield(&l.defaultContext) {
			return
		}
		for _, c := range l.contexts {
			if !yield(c) {
				return
			}
		}
	}
}

// directivesInRound returns the directives with only the metrics that still
// have values to log in the given round.
func (l *Logger) directivesInRound(directives []MetricDirective, round int) []MetricDirective {
	var out []MetricDirective
	for _, d := range directives {
		var metrics []MetricDefinition
		for _, m := range d.Metrics {
			if l.registry.valueCount(m.Name) > round*maxValuesPerMetric {
				metrics = append(metrics, m)
			}
		}
		if len(metrics) > 0 {
			d.Metrics = metrics
			out = append(out, d)
		}
	}
	return out
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

// eventValues returns the root object of one log event for a group. It holds
// every property and dimension, and the values for the round of the metrics
// its directives refer to. A dimension value replaces a property with the
// same key, and the group's own dimension values replace those from dims.
// If dims is nil, the default context is the only context.
func (l *Logger) eventValues(directives []MetricDirective, g contextGroup, dims map[string]any, round int) map[string]any {
	size := len(l.properties) + len(dims) + 1
	if dims == nil {
		size += len(l.defaultContext.dimensions)
	}
	for _, d := range directives {
		size += len(d.Metrics)
	}
	values := make(map[string]any, size)
	maps.Copy(values, l.properties)
	if dims == nil {
		l.defaultContext.copyDimensions(values)
	} else {
		maps.Copy(values, dims)
	}
	for _, c := range g.contexts {
		c.copyDimensions(values)
	}
	for _, d := range directives {
		for _, m := range d.Metrics {
			values[m.Name] = l.registry.valuesInRound(m.Name, round, maxValuesPerMetric)
		}
	}
	values[metadataKey] = Metadata{
		Timestamp:    l.timestamp,
		Metrics:      directives,
		LogGroupName: l.logGroupName,
	}
	return values
}

// NewContext creates new context for given logger. A context has its own
// namespace, dimension sets and dimension values, and shares the logger's
// properties. Contexts are written on the same line, unless they set the
// same dimension key to different values. Then they are written on separate
// lines, so each context's metrics keep their own value.
func (l *Logger) NewContext() *Context {
	var c Context
	l.registry.do(func() {
		c = newContext(l.withoutDimensions, l.registry)
		l.contexts = append(l.contexts, &c)
	})
	return &c
}

// Namespace sets namespace on given context.
func (c *Context) Namespace(namespace string) *Context {
	c.registry.do(func() { c.namespace(namespace) })
	return c
}

// Dimension adds single dimension on given context. See DimensionSet for
// what happens when the key is already set.
func (c *Context) Dimension(key, value string) *Context {
	return c.DimensionSet(NewDimension(key, value))
}

// DimensionSet adds multiple dimensions on given context. If any dimension
// is invalid, the whole set is skipped. A key given more than once is added
// once, and the last value wins. If the context already has a set with the
// same keys, in any order, the set isn't added again, but the values are
// updated.
func (c *Context) DimensionSet(dimensions ...Dimension) *Context {
	c.registry.do(func() { c.dimensionSet(dimensions) })
	return c
}

// Metric puts int metric on given context.
func (c *Context) Metric(name string, value int) *Context {
	return c.MetricAs(name, value, None)
}

// MetricFloat puts float metric on given context.
func (c *Context) MetricFloat(name string, value float64) *Context {
	return c.MetricFloatAs(name, value, None)
}

// MetricAs puts int metric with MetricUnit on given context.
func (c *Context) MetricAs(name string, value int, unit MetricUnit) *Context {
	c.registry.do(func() { c.put(name, value, unit, standardResolution) })
	return c
}

// MetricFloatAs puts float metric with MetricUnit on given context.
func (c *Context) MetricFloatAs(name string, value float64, unit MetricUnit) *Context {
	c.registry.do(func() { c.put(name, value, unit, standardResolution) })
	return c
}

// The methods below must be called while holding the registry lock.

func (c *Context) namespace(namespace string) {
	if c.registry.checkNamespace(namespace) {
		c.metricDirective.Namespace = namespace
	}
}

func (c *Context) dimensionSet(dimensions []Dimension) {
	set := dimensionKeys(dimensions)
	if !c.registry.checkDimensionSet(dimensions, set) {
		return
	}
	for _, d := range dimensions {
		c.setDimension(d.Key, d.Value)
	}
	// A repeated set would publish each metric again for the same dimensions.
	for _, existing := range c.metricDirective.Dimensions {
		if sameKeys(existing, set) {
			return
		}
	}
	c.metricDirective.Dimensions = append(c.metricDirective.Dimensions, set)
}

// setDimension sets the value of a dimension whose key is already checked.
// Setting a key again keeps the last value.
func (c *Context) setDimension(key, value string) {
	if i := c.dimensionIndex(key); i >= 0 {
		c.dimensions[i].value = value
		return
	}
	if c.dimensions == nil {
		// Most contexts have only a few dimensions.
		c.dimensions = make([]dimensionValue, 0, 4)
	}
	c.dimensions = append(c.dimensions, dimensionValue{key, value})
}

// dimensionIndex returns the index of key in c.dimensions, or -1.
func (c *Context) dimensionIndex(key string) int {
	for i, d := range c.dimensions {
		if d.key == key {
			return i
		}
	}
	return -1
}

// copyDimensions copies c's dimension values into values.
func (c *Context) copyDimensions(values map[string]any) {
	for _, d := range c.dimensions {
		values[d.key] = d.value
	}
}

// dimensionKeys returns the keys of dimensions in order, each once. It stops
// once there are more keys than a dimension set may have, since the set
// will be skipped anyway.
func dimensionKeys(dimensions []Dimension) DimensionSet {
	set := make(DimensionSet, 0, min(len(dimensions), maxDimensionSetSize+1))
	for _, d := range dimensions {
		if slices.Contains(set, d.Key) {
			continue
		}
		set = append(set, d.Key)
		if len(set) > maxDimensionSetSize {
			break
		}
	}
	return set
}

// sameKeys reports whether a and b have the same keys in any order. Both
// must have each key once.
func sameKeys(a, b DimensionSet) bool {
	if len(a) != len(b) {
		return false
	}
	for _, key := range a {
		if !slices.Contains(b, key) {
			return false
		}
	}
	return true
}

func newContext(withoutDimensions bool, reg *registry) Context {
	c := Context{
		metricDirective: MetricDirective{Namespace: "aws-embedded-metrics"},
		registry:        reg,
	}
	if !withoutDimensions {
		// set default dimensions for lambda function
		fnName := os.Getenv("AWS_LAMBDA_FUNCTION_NAME")
		if fnName != "" {
			c.metricDirective.Dimensions = []DimensionSet{{"ServiceName", "ServiceType"}}
			c.setDimension("ServiceType", "AWS::Lambda::Function")
			c.setDimension("ServiceName", fnName)
		}
	}
	return c
}
