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
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// Logger for metrics with default Context. A Logger and its Contexts are
// safe to use from several goroutines at once.
type Logger struct {
	out               io.Writer
	timestamp         int64
	defaultContext    Context
	contexts          []*Context
	defaultProperties map[string]any // the Lambda properties and traceId, set by New
	properties        map[string]any // set with Property (a string) or PropertyValue (a json.RawMessage)
	withoutDimensions bool
	mergeDefaults     bool
	defaults          *defaultDimensions // nil outside Lambda, or with WithoutDimensions
	logGroupName      string
	onError           func(error)
	registry          *registry
	writeMu           sync.Mutex
}

// Context gives ability to add another MetricDirective section for Logger.
type Context struct {
	metricDirective MetricDirective    // its Dimensions are only the sets added to the context
	dimensions      []dimensionValue   // only the values added to the context
	defaults        *defaultDimensions // the logger's, or nil once the context drops them
	// written are the sets to write when the context has sets of its own
	// and defaults: its sets combined with the default set. They are kept
	// as each set is added, so Log doesn't build them again.
	written  []DimensionSet
	registry *registry
}

// defaultDimensions are the Lambda default dimensions, shared by the
// contexts of a logger that haven't dropped them. They are kept apart from the dimensions added to a
// context, and only combined with them when a line is built. A value added
// to a context with a default key replaces the default value in that
// context. They are never changed after they are made, so loggers can share
// them too.
type defaultDimensions struct {
	fnName string
	set    DimensionSet
	values []dimensionValue
	merge  bool           // set by WithMergedDefaultDimensions
	sets   []DimensionSet // {set}, written for a context with no sets of its own
}

// defaultDimensionsCache holds the last defaults made, without and with
// merging, so a new logger for each Lambda invocation doesn't make them
// again.
var defaultDimensionsCache [2]atomic.Pointer[defaultDimensions]

// newDefaultDimensions returns the defaults for a Lambda function. They are
// read only, and may be shared with other loggers.
func newDefaultDimensions(fnName string, merge bool) *defaultDimensions {
	cache := &defaultDimensionsCache[0]
	if merge {
		cache = &defaultDimensionsCache[1]
	}
	if d := cache.Load(); d != nil && d.fnName == fnName {
		return d
	}
	set := DimensionSet{"ServiceName", "ServiceType"}
	d := &defaultDimensions{
		fnName: fnName,
		set:    set,
		values: []dimensionValue{
			{"ServiceType", "AWS::Lambda::Function"},
			{"ServiceName", fnName},
		},
		merge: merge,
		sets:  []DimensionSet{set},
	}
	cache.Store(d)
	return d
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

// WithMergedDefaultDimensions adds the Lambda default dimensions,
// ServiceName and ServiceType, to each of your dimension sets, as the
// official AWS libraries do, instead of writing them as a separate set. A
// context without dimension sets of its own still gets the default set,
// unless it dropped the defaults with ResetDimensions(false) or
// SetDimensions(false, ...).
// Each metric is then published once per set of yours, instead of once more
// for the default set, so it costs less. The 30-key limit of a dimension set
// counts the default keys it doesn't already have, so a set of yours can have
// at most 28 other keys.
//
// It does nothing outside Lambda (when AWS_LAMBDA_FUNCTION_NAME isn't set),
// or with WithoutDimensions.
func WithMergedDefaultDimensions() LoggerOption {
	return func(l *Logger) {
		l.mergeDefaults = true
	}
}

// WithLogGroup sets the log group when ingesting metrics into Cloudwatch Logging Agent.
func WithLogGroup(logGroup string) LoggerOption {
	return func(l *Logger) {
		l.logGroupName = logGroup
	}
}

// WithErrorHandler sets a function that is called when the logger skips
// input that would break the EMF spec or can't be encoded as JSON, when a
// dimension or metric replaces a property with the same key, or when it
// fails to write. Errors about input wrap ErrInvalid. Without a handler,
// these errors are ignored.
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
// Specify LoggerOptions to customize the logger. The Lambda environment
// variables are read once, by New, and contexts made later with NewContext
// use the same values.
func New(opts ...LoggerOption) *Logger {
	l := Logger{
		out:       os.Stdout,
		timestamp: time.Now().UnixMilli(),
	}

	for _, opt := range opts {
		opt(&l)
	}

	if !l.withoutDimensions {
		// set default properties and dimensions for lambda function
		fnName := os.Getenv("AWS_LAMBDA_FUNCTION_NAME")
		if fnName != "" {
			l.defaults = newDefaultDimensions(fnName, l.mergeDefaults)
			l.setDefaultProperty("executionEnvironment", os.Getenv("AWS_EXECUTION_ENV"))
			l.setDefaultProperty("memorySize", os.Getenv("AWS_LAMBDA_FUNCTION_MEMORY_SIZE"))
			l.setDefaultProperty("functionVersion", os.Getenv("AWS_LAMBDA_FUNCTION_VERSION"))
			l.setDefaultProperty("logStreamId", os.Getenv("AWS_LAMBDA_LOG_STREAM_NAME"))
		}
	}

	// only collect traces which have been sampled
	amznTraceID := os.Getenv("_X_AMZN_TRACE_ID")
	if strings.Contains(amznTraceID, "Sampled=1") {
		l.setDefaultProperty("traceId", amznTraceID)
	}

	l.registry = newRegistry(l.onError)
	l.registry.logger = &l
	l.defaultContext = newContext(l.defaults, l.registry)

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
// dimension, so it is skipped and reported. A key that isn't valid UTF-8 is
// skipped and reported too, since encoding/json writes each invalid byte as
// U+FFFD, so two different keys could be written as the same JSON key.
// A dimension or metric added later with the same key is kept and replaces
// the property, and that is reported too.
// Setting a key again replaces the value, whether it was set with Property
// or PropertyValue.
func (l *Logger) Property(key, value string) *Logger {
	l.registry.do(func() { l.setProperty(key, value) })
	return l
}

// PropertyValue sets a property to any value that can be encoded as JSON,
// such as a number, a bool, a slice, a map or a struct. Numbers stay numbers
// in the log line, so CloudWatch Logs Insights can compare them, as in
// filter statusCode >= 500. A nil value is written as JSON null.
//
// The value is encoded with encoding/json when PropertyValue is called, so
// its rules apply: for example, a []byte is written as a base64 string, and
// a MarshalJSON method with a pointer receiver is only used when value is a
// pointer. Changing a map or slice afterwards doesn't change what is logged.
// A value that can't be encoded, such as a channel, a func, NaN, or a
// json.Marshaler that fails or panics, is skipped and reported with an error
// that wraps ErrInvalid and the error from encoding/json, or the panic value
// if it is an error.
//
// A value whose JSON has an object with the same key more than once, at any
// depth, is skipped and reported too, since a reader may keep either value.
// A map key that isn't valid UTF-8 can cause this, since each invalid byte
// is written as U+FFFD, so map[string]int{"\xff": 1, "\xfe": 2} has the key
// "\uFFFD" twice. So can a json.Marshaler or a json.RawMessage.
//
// encoding/json copies the JSON from a json.Marshaler or json.RawMessage
// as it is, so it may hold bytes that aren't valid UTF-8, which readers
// decode in different ways. Such a value is skipped and reported too.
//
// The key is checked the same way as for Property, and setting a key again
// replaces the value, whether it was set with Property or PropertyValue. If
// both the key and the value are invalid, both are reported. A
// json.Marshaler may use the logger. If it sets the same key, the value
// passed to this call wins, since it is stored after it is encoded.
func (l *Logger) PropertyValue(key string, value any) *Logger {
	// Encode outside the lock, so a json.Marshaler may use the logger.
	encoded, err := encodeProperty(value)
	validUTF8 := err != nil || utf8.Valid(encoded)
	var dup string
	var hasDup bool
	if err == nil && validUTF8 {
		dup, hasDup = duplicateKey(encoded)
	}
	l.registry.do(func() {
		keyOK := l.registry.checkProperty(key)
		switch {
		case err != nil:
			l.registry.report(fmt.Errorf("%w: skipped property %q: value can't be encoded as JSON: %w", ErrInvalid, key, err))
		case !validUTF8:
			l.registry.invalid("skipped property %q: value's JSON is not valid UTF-8", key)
		case hasDup:
			l.registry.invalid("skipped property %q: value has an object with the key %q more than once", key, dup)
		case keyOK:
			l.storeProperty(key, json.RawMessage(encoded))
		}
	})
	return l
}

// encodeProperty encodes a property value as JSON. A panic in a
// json.Marshaler is returned as an error, so a bad value can't crash the
// program that logs it.
func encodeProperty(value any) (encoded []byte, err error) {
	defer func() {
		if r := recover(); r != nil {
			if e, ok := r.(error); ok {
				err = fmt.Errorf("panic while encoding: %w", e)
			} else {
				err = fmt.Errorf("panic while encoding: %v", r)
			}
		}
	}()
	return json.Marshal(value)
}

// setProperty sets a property set with Property, if the key is allowed. It
// must be called while holding the registry lock.
func (l *Logger) setProperty(key string, value any) {
	if l.registry.checkProperty(key) {
		l.storeProperty(key, value)
	}
}

// storeProperty stores a property whose key is already checked. It must be
// called while holding the registry lock.
func (l *Logger) storeProperty(key string, value any) {
	if l.properties == nil {
		l.properties = make(map[string]any)
	}
	l.properties[key] = value
}

// setDefaultProperty sets one of the properties New adds. A dimension or
// metric may replace it without being reported.
func (l *Logger) setDefaultProperty(key, value string) {
	if l.defaultProperties == nil {
		l.defaultProperties = make(map[string]any)
	}
	l.defaultProperties[key] = value
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
// updated. A key that is already a property replaces it, and that is
// reported. With WithMergedDefaultDimensions in Lambda, a set may have at
// most 30 keys including ServiceName and ServiceType, unless the context
// dropped the defaults.
func (l *Logger) DimensionSet(dimensions ...Dimension) *Logger {
	l.registry.do(func() { l.defaultContext.dimensionSet(dimensions) })
	return l
}

// ResetDimensions removes the dimension sets and values added to the
// default context. With useDefault false, the default context also stops
// writing the Lambda default dimensions, ServiceName and ServiceType, while
// the Lambda properties are still written. With useDefault true, it writes
// them again. Other contexts aren't changed. See Context.ResetDimensions for
// what happens to the removed keys.
func (l *Logger) ResetDimensions(useDefault bool) *Logger {
	l.registry.do(func() { l.defaultContext.resetDimensions(useDefault) })
	return l
}

// SetDimensions replaces the dimension sets and values of the default
// context with sets, each added as with DimensionSet. With useDefault false,
// the default context stops writing the Lambda default dimensions, as the
// official AWS libraries' setDimensions does. With useDefault true, they are
// kept, or written again if they were dropped. With no sets, it is the same
// as ResetDimensions(useDefault). See Context.SetDimensions for details.
func (l *Logger) SetDimensions(useDefault bool, sets ...[]Dimension) *Logger {
	l.registry.do(func() { l.defaultContext.setDimensions(useDefault, sets) })
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
// only ever appended to, or replaced by new ones when dimensions are reset,
// so the snapshot stays valid after the lock is released.
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
		size = max(size, c.dimensionCount())
	}
	dims = make(map[string]any, size)
	var without []*Context
	for c := range l.allContexts() {
		if len(c.metricDirective.Metrics) == 0 {
			without = append(without, c)
			continue
		}
		for d := range c.dimensionValues() {
			if v, ok := dims[d.key]; ok && v != d.value {
				disagree = true
			}
			dims[d.key] = d.value
		}
	}
	// Later contexts without metrics win among themselves, so add them last
	// first, keeping keys that are already set.
	for _, c := range slices.Backward(without) {
		for d := range c.dimensionValues() {
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
			directives = append(directives, c.directive())
		}
	}
	return directives
}

// addDimensions adds c's dimension values to values, and reports whether
// they agree with the values already there. It stops at the first value
// that doesn't agree.
func addDimensions(values map[string]any, c *Context) bool {
	for d := range c.dimensionValues() {
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
	for d := range c.dimensionValues() {
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
		groups[i].directives = append(groups[i].directives, c.directive())
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
	size := len(l.defaultProperties) + len(l.properties) + len(dims) + 1
	if dims == nil {
		size += l.defaultContext.dimensionCount()
	}
	for _, d := range directives {
		size += len(d.Metrics)
	}
	values := make(map[string]any, size)
	maps.Copy(values, l.defaultProperties)
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
		c = newContext(l.defaults, l.registry)
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
// updated. A key that is already a property replaces it, and that is
// reported. With WithMergedDefaultDimensions in Lambda, a set may have at
// most 30 keys including ServiceName and ServiceType, unless the context
// dropped the defaults.
func (c *Context) DimensionSet(dimensions ...Dimension) *Context {
	c.registry.do(func() { c.dimensionSet(dimensions) })
	return c
}

// ResetDimensions removes the dimension sets and values added to the
// context. With useDefault false, the context also stops writing the Lambda
// default dimensions, ServiceName and ServiceType, while the Lambda
// properties are still written. With useDefault true, it writes them again.
// The logger's other contexts, and contexts made later with NewContext, still
// write them. Outside Lambda, or with WithoutDimensions, useDefault makes no
// difference.
//
// The removed keys are no longer dimensions of the context, so a property
// they replaced is written again, and the context no longer disagrees with
// other contexts on their values, so it can share their lines again. A
// metric or property may then use a removed key, unless another context
// still has a dimension with that key. When the logger has the Lambda
// defaults (in Lambda, without WithoutDimensions), ServiceName and
// ServiceType stay reserved for them, even in a context that dropped them,
// so the defaults can be written again. A property that was skipped because
// its key was a dimension isn't restored: set it again.
//
// Dimensions added afterwards follow the usual rules. A dimension that
// replaces a property is reported again, since the property was no longer
// replaced in between.
func (c *Context) ResetDimensions(useDefault bool) *Context {
	c.registry.do(func() { c.resetDimensions(useDefault) })
	return c
}

// SetDimensions replaces the dimension sets and values of the context with
// sets, in one step, so a concurrent Log writes either the old dimensions or
// the new ones. With useDefault false, the context stops writing the Lambda
// default dimensions, as the official AWS libraries' setDimensions does. With
// useDefault true, they are kept, or written again if they were dropped, and
// WithMergedDefaultDimensions merges them into each set.
//
// Each set is added as with DimensionSet: an invalid set is skipped and
// reported while the others are still added, a set with the same keys as an
// earlier one isn't added again, and the 30-key limit counts the merged
// default keys. A key that replaces a property is reported, unless the
// context already had it as a dimension before the call. With no sets, it is
// the same as ResetDimensions(useDefault). See ResetDimensions for what
// happens to keys that aren't set again.
func (c *Context) SetDimensions(useDefault bool, sets ...[]Dimension) *Context {
	c.registry.do(func() { c.setDimensions(useDefault, sets) })
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
	c.addDimensionSet(dimensions, nil)
}

// resetDimensions removes the dimension sets and values added to c, and
// sets whether c writes the logger's default dimensions. The slices are
// replaced, not truncated, so a snapshot taken before stays valid.
func (c *Context) resetDimensions(useDefault bool) {
	c.metricDirective.Dimensions = nil
	c.dimensions = nil
	c.written = nil
	c.defaults = nil
	if useDefault {
		c.defaults = c.registry.logger.defaults
	}
}

// resetOwnDimensions removes the dimension sets and values added to c, and
// keeps c's choice about the default dimensions: a context that dropped them
// with ResetDimensions(false) or SetDimensions(false, ...) doesn't get them
// back. Flush calls it on each context while holding the registry lock, so
// the next unit of work starts without the last one's dimensions.
func (c *Context) resetOwnDimensions() {
	c.resetDimensions(c.defaults != nil)
}

// setDimensions replaces c's dimension sets and values with sets.
func (c *Context) setDimensions(useDefault bool, sets [][]Dimension) {
	before := c.dimensions
	c.resetDimensions(useDefault)
	for _, dimensions := range sets {
		c.addDimensionSet(dimensions, before)
	}
}

// addDimensionSet adds a set of dimensions to c. A key that replaces a
// property is reported, unless it is in before, the values c had before
// SetDimensions reset them, since then the property was already replaced.
func (c *Context) addDimensionSet(dimensions []Dimension, before []dimensionValue) {
	set := dimensionKeys(dimensions)
	if !c.registry.checkDimensionSet(dimensions, set, c.mergedDefaults()) {
		return
	}
	// The dimension is kept, but the property is no longer logged.
	for _, key := range set {
		if c.registry.replacesProperty(key) && dimensionIndex(before, key) < 0 {
			c.registry.invalid("dimension %q replaces the property with the same key", key)
		}
	}
	for _, d := range dimensions {
		c.setDimension(d.Key, d.Value)
	}
	// A repeated set would publish each metric again for the same dimensions.
	// A set with the same keys as the default set is still added, and left
	// out when the line is built if the defaults are written as their own
	// set.
	for _, existing := range c.metricDirective.Dimensions {
		if sameKeys(existing, set) {
			return
		}
	}
	c.metricDirective.Dimensions = append(c.metricDirective.Dimensions, set)
	c.addWritten(set)
}

// addWritten combines a set newly added to c with the defaults, and adds
// the result to the sets to write, unless it is already there. Without
// merging, the default set comes first, and a set with the default keys
// isn't added again. With merging, the default keys are added to the set.
// Like the sets added to c, written is only appended to, or replaced by a
// new slice when dimensions are reset, so a snapshot of it stays valid.
func (c *Context) addWritten(set DimensionSet) {
	defaults := c.defaults
	if defaults == nil {
		return
	}
	if c.written == nil {
		// Most contexts have only a few sets, so this is usually the only
		// allocation.
		c.written = make([]DimensionSet, 0, 4)
	}
	if !defaults.merge {
		if len(c.written) == 0 {
			c.written = append(c.written, defaults.set)
		}
		if !sameKeys(set, defaults.set) {
			c.written = append(c.written, set)
		}
		return
	}
	merged := mergeKeys(defaults.set, set)
	if !slices.ContainsFunc(c.written, func(s DimensionSet) bool { return sameKeys(s, merged) }) {
		c.written = append(c.written, merged)
	}
}

// mergedDefaults returns the default set if it is merged into each of c's
// sets, or nil.
func (c *Context) mergedDefaults() DimensionSet {
	if c.defaults == nil || !c.defaults.merge {
		return nil
	}
	return c.defaults.set
}

// directive returns c's directive with the dimension sets to write: the
// sets added to c, combined with the default set.
func (c *Context) directive() MetricDirective {
	d := c.metricDirective
	d.Dimensions = c.dimensionSets()
	return d
}

// dimensionSets returns the dimension sets to write for c. Without
// defaults, they are c's own sets. Otherwise, with no sets of c's own, they
// are the default set. If the defaults are merged, the default keys are added
// to each of c's sets, and sets that end up with the same keys are written
// once. If not, the default set comes first, followed by c's sets other than
// one with the default keys. Those are kept in c.written as each set is
// added.
func (c *Context) dimensionSets() []DimensionSet {
	switch {
	case c.defaults == nil:
		return c.metricDirective.Dimensions
	case len(c.metricDirective.Dimensions) == 0:
		return c.defaults.sets
	}
	return c.written
}

// mergeKeys returns the keys of defaults, followed by the keys of set that
// aren't in defaults. Both must have each key once.
func mergeKeys(defaults, set DimensionSet) DimensionSet {
	merged := make(DimensionSet, 0, len(defaults)+len(set))
	merged = append(merged, defaults...)
	for _, key := range set {
		if !slices.Contains(defaults, key) {
			merged = append(merged, key)
		}
	}
	return merged
}

// dimensionValues yields c's dimension values: those added to c, then the
// default values for keys that weren't added to c.
func (c *Context) dimensionValues() iter.Seq[dimensionValue] {
	return func(yield func(dimensionValue) bool) {
		for _, d := range c.dimensions {
			if !yield(d) {
				return
			}
		}
		if c.defaults == nil {
			return
		}
		for _, d := range c.defaults.values {
			if c.dimensionIndex(d.key) < 0 && !yield(d) {
				return
			}
		}
	}
}

// dimensionCount returns at least the number of c's dimension values, for
// sizing maps.
func (c *Context) dimensionCount() int {
	if c.defaults == nil {
		return len(c.dimensions)
	}
	return len(c.dimensions) + len(c.defaults.values)
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

// dimensionIndex returns the index of key in c.dimensions, or -1. It
// doesn't look at the defaults.
func (c *Context) dimensionIndex(key string) int {
	return dimensionIndex(c.dimensions, key)
}

// dimensionIndex returns the index of key in dimensions, or -1.
func dimensionIndex(dimensions []dimensionValue, key string) int {
	for i, d := range dimensions {
		if d.key == key {
			return i
		}
	}
	return -1
}

// copyDimensions copies c's dimension values, including defaults, into
// values.
func (c *Context) copyDimensions(values map[string]any) {
	for d := range c.dimensionValues() {
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

func newContext(defaults *defaultDimensions, reg *registry) Context {
	return Context{
		metricDirective: MetricDirective{Namespace: "aws-embedded-metrics"},
		defaults:        defaults,
		registry:        reg,
	}
}
