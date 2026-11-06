package emf

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// ErrInvalid is wrapped by every error passed to the WithErrorHandler
// handler about input: input the logger skipped because it would break the
// EMF spec or can't be encoded as JSON, and input it kept but reported, such
// as a dimension or metric that replaces a property.
var ErrInvalid = errors.New("emf: invalid input")

// Limits from the EMF specification.
const (
	maxNameLen           = 255
	maxDimensionKeyLen   = 250
	maxDimensionValueLen = 1024
	maxDimensionSetSize  = 30
	maxMetricsPerEvent   = 100
	maxValuesPerMetric   = 100
	maxTimestampAge      = 14 * 24 * time.Hour
	maxTimestampAhead    = 2 * time.Hour
	maxEventBytes        = 1 << 20 // 1 MB, CloudWatch Logs' event size limit
	metadataKey          = "_aws"
)

// maxMetricMagnitude is 2^360, the largest magnitude a metric value may have.
var maxMetricMagnitude = math.Ldexp(1, 360)

// registry tracks the root member names used by a Logger and all of its
// Contexts, so metrics and dimensions don't overwrite each other. Its lock
// guards all state shared by the Logger and its Contexts.
type registry struct {
	mu      sync.Mutex
	logger  *Logger // owner, to find metric definitions in its contexts
	onError func(error)
	pending []error
	metrics map[string]any   // the first value of each metric
	more    map[string][]any // later values of repeated metrics, made when first needed
}

func newRegistry(onError func(error)) *registry {
	return &registry{
		onError: onError,
		metrics: make(map[string]any),
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

// checkDimensionSet checks dimensions, with keys from dimensionKeys. The
// size limit counts each key once. If defaults isn't nil, it is merged into
// the set when it is written, so the limit also counts the keys of defaults
// that the set doesn't have.
func (r *registry) checkDimensionSet(dimensions []Dimension, keys, defaults DimensionSet) bool {
	if len(keys) > maxDimensionSetSize {
		r.invalid("skipped dimension set: has more than %d keys", maxDimensionSetSize)
		return false
	}
	if defaults != nil && len(defaults)+countMissing(keys, defaults) > maxDimensionSetSize {
		r.invalid("skipped dimension set: has more than %d keys with the Lambda default dimensions merged in", maxDimensionSetSize)
		return false
	}
	for _, d := range dimensions {
		switch {
		case !validString(d.Key, maxDimensionKeyLen) || strings.HasPrefix(d.Key, ":"):
			r.invalid("skipped dimension set: key %q must be 1 to %d printable ASCII characters, not only whitespace, and not start with ':'", d.Key, maxDimensionKeyLen)
		case d.Key == metadataKey:
			r.invalid("skipped dimension set: key %q is reserved", d.Key)
		case r.isMetric(d.Key):
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

// countMissing returns the number of keys that aren't in set.
func countMissing(keys, set DimensionSet) int {
	n := 0
	for _, key := range keys {
		if !slices.Contains(set, key) {
			n++
		}
	}
	return n
}

// definition returns the first definition of a metric name in any of the
// logger's contexts.
func (r *registry) definition(name string) (MetricDefinition, bool) {
	contexts := append([]*Context{&r.logger.defaultContext}, r.logger.contexts...)
	for _, c := range contexts {
		for _, m := range c.metricDirective.Metrics {
			if m.Name == name {
				return m, true
			}
		}
	}
	return MetricDefinition{}, false
}

// isDimension reports whether any of the logger's contexts has a dimension
// with the given key, or the key is one of the logger's Lambda default
// dimensions. The default keys stay reserved even in contexts that dropped
// them with ResetDimensions(false) or SetDimensions(false, ...), so writing
// the defaults again can't clash with a metric or property.
func (r *registry) isDimension(key string) bool {
	if d := r.logger.defaults; d != nil && slices.Contains(d.set, key) {
		return true
	}
	for c := range r.logger.allContexts() {
		if c.dimensionIndex(key) >= 0 {
			return true
		}
	}
	return false
}

// replacesProperty reports whether a new dimension or metric with the given
// key replaces a property set with Property or PropertyValue. Only the first
// dimension or metric with the key does, since after that the property is
// already replaced. The properties New adds may be replaced without a
// report.
func (r *registry) replacesProperty(key string) bool {
	_, ok := r.logger.properties[key]
	return ok && !r.isDimension(key) && !r.isMetric(key)
}

func (r *registry) isMetric(name string) bool {
	_, ok := r.metrics[name]
	return ok
}

func (r *registry) valueCount(name string) int {
	return 1 + len(r.more[name])
}

// valuesInRound returns a metric's values for one round of log events, at
// most limit per round. A metric with one value is logged as a number, and
// otherwise as a list.
func (r *registry) valuesInRound(name string, round, limit int) any {
	more := r.more[name]
	if len(more) == 0 {
		return r.metrics[name]
	}
	start := round * limit
	end := min(start+limit, 1+len(more))
	part := make([]any, 0, end-start)
	for i := start; i < end; i++ {
		if i == 0 {
			part = append(part, r.metrics[name])
		} else {
			part = append(part, more[i-1])
		}
	}
	return part
}

func (r *registry) checkMetricName(name string, unit MetricUnit) bool {
	switch {
	case !validString(name, maxNameLen):
		r.invalid("skipped metric %q: name must be 1 to %d printable ASCII characters, not only whitespace", name, maxNameLen)
	case name == metadataKey:
		r.invalid("skipped metric %q: name is reserved", name)
	case r.isDimension(name):
		r.invalid("skipped metric %q: name is already a dimension", name)
	case !validUnit(unit):
		r.invalid("skipped metric %q: unknown unit %q", name, unit)
	default:
		return true
	}
	return false
}

func (r *registry) checkMetricValue(name string, value any) bool {
	if !validValue(value) {
		r.invalid("skipped metric %q: value %v must be a finite number between -2^360 and 2^360", name, value)
		return false
	}
	return true
}

// checkTimestamp reports a timestamp CloudWatch won't publish metrics for.
// The log line is still written, since CloudWatch keeps it as a log event.
func (r *registry) checkTimestamp(timestamp, now time.Time) {
	if timestamp.Before(now.Add(-maxTimestampAge)) || timestamp.After(now.Add(maxTimestampAhead)) {
		r.invalid("timestamp %s is more than 14 days in the past or 2 hours in the future, so CloudWatch will not publish these metrics",
			timestamp.UTC().Format(time.RFC3339))
	}
}

// checkProperty checks a property key. A key that isn't valid UTF-8 is
// rejected, since encoding/json writes each invalid byte as U+FFFD, so two
// different keys could be written as the same JSON key.
func (r *registry) checkProperty(key string) bool {
	switch {
	case !utf8.ValidString(key):
		r.invalid("skipped property %q: key must be valid UTF-8", key)
	case key == metadataKey:
		r.invalid("skipped property %q: key is reserved", key)
	case r.isMetric(key):
		r.invalid("skipped property %q: key is already a metric", key)
	case r.isDimension(key):
		r.invalid("skipped property %q: key is already a dimension", key)
	default:
		return true
	}
	return false
}

// duplicateKey returns a key that an object in encoded has more than once,
// at any depth. Keys are compared as a JSON reader sees them, after escapes
// are decoded and invalid UTF-8 is replaced with U+FFFD. encoded must be
// valid JSON with no whitespace outside strings, as json.Marshal writes it.
func duplicateKey(encoded []byte) (string, bool) {
	// Only an object can have a duplicate key, so most values are done here.
	if bytes.IndexByte(encoded, '{') < 0 {
		return "", false
	}
	var keysBuf [16][]byte
	var startsBuf [8]int
	keys := keysBuf[:0]     // the keys of the open objects, innermost last
	starts := startsBuf[:0] // where each open object's keys start in keys
	for i := 0; i < len(encoded); i++ {
		switch encoded[i] {
		case '{':
			starts = append(starts, len(keys))
		case '}':
			if len(starts) == 0 {
				return "", false
			}
			start := starts[len(starts)-1]
			if key, ok := repeatedKey(keys[start:]); ok {
				return key, true
			}
			keys, starts = keys[:start], starts[:len(starts)-1]
		case '"':
			end := stringEnd(encoded, i)
			if end < 0 {
				return "", false
			}
			// With no whitespace, a string is a key if a colon follows it.
			if end+1 < len(encoded) && encoded[end+1] == ':' {
				keys = append(keys, decodeKey(encoded[i:end+1]))
			}
			i = end
		}
	}
	return "", false
}

// stringEnd returns the index of the quote that ends the JSON string that
// starts at encoded[start], or -1 if it doesn't end.
func stringEnd(encoded []byte, start int) int {
	for i := start + 1; i < len(encoded); i++ {
		switch encoded[i] {
		case '\\':
			i++ // skip the escaped character
		case '"':
			return i
		}
	}
	return -1
}

// decodeKey returns a quoted JSON key as a reader sees it. Most keys have no
// escapes and are valid UTF-8, so they are used as they are.
func decodeKey(quoted []byte) []byte {
	raw := quoted[1 : len(quoted)-1]
	if bytes.IndexByte(raw, '\\') < 0 && utf8.Valid(raw) {
		return raw
	}
	var key string
	_ = json.Unmarshal(quoted, &key) // can't fail for a valid JSON string
	return []byte(key)
}

// repeatedKey returns a key that keys has more than once. It sorts keys.
func repeatedKey(keys [][]byte) (string, bool) {
	if len(keys) < 2 {
		return "", false
	}
	slices.SortFunc(keys, bytes.Compare)
	for i := 1; i < len(keys); i++ {
		if bytes.Equal(keys[i-1], keys[i]) {
			return string(keys[i]), true
		}
	}
	return "", false
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
	// Only floats need checking, because an int can't be NaN or reach 2^360.
	f, isFloat := value.(float64)
	return !isFloat || (!math.IsNaN(f) && math.Abs(f) <= maxMetricMagnitude)
}

func validUnit(unit MetricUnit) bool {
	return unit == "" || slices.Contains(units, unit)
}
