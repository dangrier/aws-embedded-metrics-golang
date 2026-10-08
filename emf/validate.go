package emf

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"sync"
	"time"
)

// ErrInvalid is wrapped by every error passed to the WithErrorHandler
// handler for input the logger skipped because it would break the EMF spec.
var ErrInvalid = errors.New("emf: invalid input")

// Limits from the EMF specification.
const (
	maxNameLen           = 255
	maxDimensionKeyLen   = 250
	maxDimensionValueLen = 1024
	maxDimensionSetSize  = 30
	maxMetricsPerEvent   = 100
	maxTimestampAge      = 14 * 24 * time.Hour
	maxTimestampAhead    = 2 * time.Hour
	metadataKey          = "_aws"
)

// maxMetricMagnitude is 2^360, the largest magnitude a metric value may have.
var maxMetricMagnitude = math.Ldexp(1, 360)

// registry tracks the root member names used by a Logger and all of its
// Contexts, so metrics and dimensions don't overwrite each other. Its lock
// guards all state shared by the Logger and its Contexts.
type registry struct {
	mu         sync.Mutex
	onError    func(error)
	pending    []error
	dimensions map[string]bool
	metrics    map[string]bool
}

func newRegistry(onError func(error)) *registry {
	return &registry{
		onError:    onError,
		dimensions: make(map[string]bool),
		metrics:    make(map[string]bool),
	}
}

// do runs fn while holding the lock, then passes any errors fn reported to
// the error handler. The handler runs after the lock is released, so it can
// use the logger.
func (r *registry) do(fn func()) {
	r.mu.Lock()
	fn()
	errs := r.pending
	r.pending = nil
	r.mu.Unlock()

	r.handle(errs)
}

// handle passes errs to the error handler. It must be called without
// holding the lock.
func (r *registry) handle(errs []error) {
	if r.onError == nil {
		return
	}
	for _, err := range errs {
		r.onError(err)
	}
}

// report queues err for the error handler. It must be called inside do.
func (r *registry) report(err error) {
	r.pending = append(r.pending, err)
}

func (r *registry) invalid(format string, a ...any) {
	r.report(fmt.Errorf("%w: "+format, append([]any{ErrInvalid}, a...)...))
}

func (r *registry) checkNamespace(namespace string) bool {
	if !validString(namespace, maxNameLen) {
		r.invalid("skipped namespace %q: must be 1 to %d printable ASCII characters, not only whitespace", namespace, maxNameLen)
		return false
	}
	return true
}

func (r *registry) checkDimensionSet(dimensions []Dimension) bool {
	if len(dimensions) > maxDimensionSetSize {
		r.invalid("skipped dimension set: has %d dimensions, more than %d", len(dimensions), maxDimensionSetSize)
		return false
	}
	for _, d := range dimensions {
		switch {
		case !validString(d.Key, maxDimensionKeyLen) || strings.HasPrefix(d.Key, ":"):
			r.invalid("skipped dimension set: key %q must be 1 to %d printable ASCII characters, not only whitespace, and not start with ':'", d.Key, maxDimensionKeyLen)
		case d.Key == metadataKey:
			r.invalid("skipped dimension set: key %q is reserved", d.Key)
		case r.metrics[d.Key]:
			r.invalid("skipped dimension set: key %q is already a metric", d.Key)
		case !validString(d.Value, maxDimensionValueLen):
			r.invalid("skipped dimension set: value %q for key %q must be 1 to %d printable ASCII characters, not only whitespace", d.Value, d.Key, maxDimensionValueLen)
		default:
			continue
		}
		return false
	}
	return true
}

func (r *registry) checkMetric(name string, value any, unit MetricUnit) bool {
	switch {
	case !validString(name, maxNameLen):
		r.invalid("skipped metric %q: name must be 1 to %d printable ASCII characters, not only whitespace", name, maxNameLen)
	case name == metadataKey:
		r.invalid("skipped metric %q: name is reserved", name)
	case r.dimensions[name]:
		r.invalid("skipped metric %q: name is already a dimension", name)
	case !validUnit(unit):
		r.invalid("skipped metric %q: unknown unit %q", name, unit)
	case !validValue(value):
		r.invalid("skipped metric %q: value %v must be a finite number between -2^360 and 2^360", name, value)
	default:
		return true
	}
	return false
}

// checkTimestamp reports a timestamp CloudWatch won't publish metrics for.
// The log line is still written, since CloudWatch keeps it as a log event.
func (r *registry) checkTimestamp(timestamp, now time.Time) {
	if timestamp.Before(now.Add(-maxTimestampAge)) || timestamp.After(now.Add(maxTimestampAhead)) {
		r.invalid("timestamp %s is more than 14 days in the past or 2 hours in the future, so CloudWatch will not publish these metrics",
			timestamp.UTC().Format(time.RFC3339))
	}
}

func (r *registry) checkProperty(key string) bool {
	switch {
	case key == metadataKey:
		r.invalid("skipped property %q: key is reserved", key)
	case r.metrics[key]:
		r.invalid("skipped property %q: key is already a metric", key)
	case r.dimensions[key]:
		r.invalid("skipped property %q: key is already a dimension", key)
	default:
		return true
	}
	return false
}

// validString reports whether s has 1 to maxLen printable ASCII characters
// and is not only whitespace.
func validString(s string, maxLen int) bool {
	if len(s) < 1 || len(s) > maxLen || strings.TrimSpace(s) == "" {
		return false
	}
	return !strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 || r > 0x7e })
}

func validValue(value any) bool {
	f, ok := value.(float64)
	if !ok {
		return true // ints are always within range
	}
	return !math.IsNaN(f) && math.Abs(f) <= maxMetricMagnitude
}

func validUnit(unit MetricUnit) bool {
	return unit == "" || slices.Contains(units, unit)
}
