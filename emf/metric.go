package emf

import "slices"

// MetricOption sets how Put and PutValues log a metric.
type MetricOption func(*metricOptions)

type metricOptions struct {
	unit       MetricUnit
	resolution int
}

// Unit sets the unit of a metric. Without it, the unit is None.
func Unit(unit MetricUnit) MetricOption {
	return func(o *metricOptions) {
		o.unit = unit
	}
}

// HighResolution stores a metric at 1 second resolution instead of the
// standard 60 seconds, so it can be graphed and alarmed on per second.
// High-resolution metrics cost more to store and alarm on.
func HighResolution() MetricOption {
	return func(o *metricOptions) {
		o.resolution = highResolution
	}
}

// Resolutions for MetricDefinition.StorageResolution. Standard is left out
// of the output, since CloudWatch uses it by default.
const (
	standardResolution = 0
	highResolution     = 1
)

func newMetricOptions(opts []MetricOption) metricOptions {
	o := metricOptions{unit: None}
	for _, opt := range opts {
		opt(&o)
	}
	return o
}

// Put puts a metric on the default context. Logging the same name again
// adds to its values, which are logged as a list.
func (l *Logger) Put(name string, value float64, opts ...MetricOption) *Logger {
	o := newMetricOptions(opts)
	l.registry.do(func() { l.defaultContext.put(name, value, o.unit, o.resolution) })
	return l
}

// PutValues puts a metric with several values on the default context, such
// as the latency of each request in a batch. More than 100 values are split
// across several log lines.
func (l *Logger) PutValues(name string, values []float64, opts ...MetricOption) *Logger {
	o := newMetricOptions(opts)
	l.registry.do(func() { l.defaultContext.putValues(name, values, o) })
	return l
}

// Put puts a metric on the given context. Logging the same name again adds
// to its values, which are logged as a list.
func (c *Context) Put(name string, value float64, opts ...MetricOption) *Context {
	o := newMetricOptions(opts)
	c.registry.do(func() { c.put(name, value, o.unit, o.resolution) })
	return c
}

// PutValues puts a metric with several values on the given context. More
// than 100 values are split across several log lines.
func (c *Context) PutValues(name string, values []float64, opts ...MetricOption) *Context {
	o := newMetricOptions(opts)
	c.registry.do(func() { c.putValues(name, values, o) })
	return c
}

// The methods below must be called while holding the registry lock.

func (c *Context) putValues(name string, values []float64, o metricOptions) {
	if len(values) == 0 {
		c.registry.invalid("skipped metric %q: no values", name)
		return
	}
	if !c.registry.checkMetricName(name, o.unit) {
		return
	}
	for _, v := range values {
		c.add(name, v, o.unit, o.resolution)
	}
}

func (c *Context) put(name string, value any, unit MetricUnit, resolution int) {
	if c.registry.checkMetricName(name, unit) {
		c.add(name, value, unit, resolution)
	}
}

// add adds a value to a metric whose name and unit are already checked.
func (c *Context) add(name string, value any, unit MetricUnit, resolution int) {
	r := c.registry
	if !r.checkMetricValue(name, value) {
		return
	}

	def := MetricDefinition{Name: name, Unit: unit, StorageResolution: resolution}
	if !r.isMetric(name) {
		c.metricDirective.Metrics = append(c.metricDirective.Metrics, def)
		r.metrics[name] = value
		return
	}

	// The name is already a metric. Every context shares its values, so its
	// unit and resolution must match wherever it was first defined.
	i := slices.IndexFunc(c.metricDirective.Metrics, func(m MetricDefinition) bool { return m.Name == name })
	existing := def
	if i >= 0 {
		existing = c.metricDirective.Metrics[i]
	} else if d, ok := r.definition(name); ok {
		existing = d
	}
	if existing != def {
		r.invalid("skipped metric %q: unit %q and %s don't match its earlier unit %q and %s",
			name, unit, resolutionName(resolution), existing.Unit, resolutionName(existing.StorageResolution))
		return
	}
	if i < 0 {
		// First use of the name in this context. It shares the values.
		c.metricDirective.Metrics = append(c.metricDirective.Metrics, def)
	}
	if r.more == nil {
		r.more = make(map[string][]any)
	}
	r.more[name] = append(r.more[name], value)
}

func resolutionName(resolution int) string {
	if resolution == highResolution {
		return "high resolution"
	}
	return "standard resolution"
}
