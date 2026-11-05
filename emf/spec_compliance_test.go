package emf_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"math/rand/v2"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dangrier/aws-embedded-metrics-golang/emf"
)

var allUnits = []emf.MetricUnit{
	emf.None, emf.Seconds, emf.Microseconds, emf.Milliseconds,
	emf.Bytes, emf.Kilobytes, emf.Megabytes, emf.Gigabytes, emf.Terabytes,
	emf.Bits, emf.Kilobits, emf.Megabits, emf.Gigabits, emf.Terabits,
	emf.Percent, emf.Count,
	emf.BytesSecond, emf.KilobytesSecond, emf.MegabytesSecond, emf.GigabytesSecond, emf.TerabytesSecond,
	emf.BitsSecond, emf.KilobitsSecond, emf.MegabitsSecond, emf.GigabitsSecond, emf.TerabitsSecond,
	emf.CountSecond,
}

// TestSpecValidatorAcceptsAWSExample makes sure the validator passes the
// example document from the AWS spec.
func TestSpecValidatorAcceptsAWSExample(t *testing.T) {
	example, err := os.ReadFile(specExampleFile)
	if err != nil {
		t.Fatal(err)
	}
	exampleTime := time.UnixMilli(1574109732004)

	for _, v := range specViolations(example, exampleTime) {
		t.Error(v)
	}
}

// TestSpecValidatorRejectsInvalid makes sure the validator catches
// documents that break the spec, so a passing compliance test means
// something.
func TestSpecValidatorRejectsInvalid(t *testing.T) {
	now := time.UnixMilli(1574109732004)
	aws := func(directive string) string {
		return `{"_aws":{"Timestamp":1574109732004,"CloudWatchMetrics":[` + directive + `]},"d":"v","m":1}`
	}
	tcs := map[string]string{
		"null dimensions":          aws(`{"Namespace":"ns","Dimensions":null,"Metrics":[{"Name":"m"}]}`),
		"no dimension sets":        aws(`{"Namespace":"ns","Dimensions":[],"Metrics":[{"Name":"m"}]}`),
		"blank namespace":          aws(`{"Namespace":"  ","Dimensions":[[]],"Metrics":[{"Name":"m"}]}`),
		"unknown unit":             aws(`{"Namespace":"ns","Dimensions":[[]],"Metrics":[{"Name":"m","Unit":"Parsecs"}]}`),
		"dimension starts with :":  aws(`{"Namespace":"ns","Dimensions":[[":d"]],"Metrics":[{"Name":"m"}]}`),
		"missing metric target":    aws(`{"Namespace":"ns","Dimensions":[[]],"Metrics":[{"Name":"missing"}]}`),
		"missing dimension target": aws(`{"Namespace":"ns","Dimensions":[["missing"]],"Metrics":[{"Name":"m"}]}`),
		"metric target is string":  aws(`{"Namespace":"ns","Dimensions":[[]],"Metrics":[{"Name":"d"}]}`),
		"dimension target number":  aws(`{"Namespace":"ns","Dimensions":[["m"]],"Metrics":[{"Name":"m"}]}`),
		"extra directive member":   aws(`{"Namespace":"ns","Dimensions":[[]],"Metrics":[{"Name":"m"}],"Extra":1}`),
		"bad storage resolution":   aws(`{"Namespace":"ns","Dimensions":[[]],"Metrics":[{"Name":"m","StorageResolution":5}]}`),
		"repeated dimension key":   aws(`{"Namespace":"ns","Dimensions":[["d","d"]],"Metrics":[{"Name":"m"}]}`),
		"repeated dimension set":   `{"_aws":{"Timestamp":1574109732004,"CloudWatchMetrics":[{"Namespace":"ns","Dimensions":[["d","e"],["e","d"]],"Metrics":[{"Name":"m"}]}]},"d":"v","e":"v","m":1}`,
		"repeated empty set":       aws(`{"Namespace":"ns","Dimensions":[[],[]],"Metrics":[{"Name":"m"}]}`),
		"missing _aws":             `{"m":1}`,
		"trailing data":            aws(`{"Namespace":"ns","Dimensions":[[]],"Metrics":[{"Name":"m"}]}`) + `{}`,
		"empty line":               "\n",
		"metric too big": `{"_aws":{"Timestamp":1574109732004,"CloudWatchMetrics":[` +
			`{"Namespace":"ns","Dimensions":[[]],"Metrics":[{"Name":"m"}]}]},"m":1e200}`,
		"timestamp too old": `{"_aws":{"Timestamp":1,"CloudWatchMetrics":[` +
			`{"Namespace":"ns","Dimensions":[[]],"Metrics":[{"Name":"m"}]}]},"m":1}`,
		"31 dimensions": thirtyOneDimensions(),
		"101 metric definitions": aws(`{"Namespace":"ns","Dimensions":[[]],"Metrics":[` +
			strings.TrimSuffix(strings.Repeat(`{"Name":"m"},`, 101), ",") + `]}`),
	}

	for name, doc := range tcs {
		t.Run(name, func(t *testing.T) {
			if v := specViolations([]byte(doc), now); len(v) == 0 {
				t.Errorf("expected violations, got none for %s", doc)
			}
		})
	}
}

// thirtyOneDimensions returns a document whose only violation is a dimension
// set with 31 different keys.
func thirtyOneDimensions() string {
	keys := make([]string, 31)
	values := make([]string, 31)
	for i := range keys {
		keys[i] = fmt.Sprintf(`"d%d"`, i)
		values[i] = fmt.Sprintf(`"d%d":"v"`, i)
	}
	return `{"_aws":{"Timestamp":1574109732004,"CloudWatchMetrics":[{"Namespace":"ns","Dimensions":[[` +
		strings.Join(keys, ",") + `]],"Metrics":[{"Name":"m"}]}]},` + strings.Join(values, ",") + `,"m":1}`
}

// TestSpecCompliance checks the output of every TestEmf case against the
// AWS spec.
func TestSpecCompliance(t *testing.T) {
	for _, tc := range emfTestCases {
		t.Run(tc.name, func(t *testing.T) {
			for line := range bytes.Lines(tc.run(t)) {
				assertCompliant(t, line)
			}
		})
	}

	t.Run("empty dimension set", func(t *testing.T) {
		var buf bytes.Buffer
		emf.New(emf.WithWriter(&buf), emf.WithoutDimensions()).
			DimensionSet().
			Metric("m", 1).
			Log()
		assertCompliant(t, buf.Bytes())
	})

	t.Run("30 keys with repeats", func(t *testing.T) {
		var buf bytes.Buffer
		var errs []error
		var dims []emf.Dimension
		for i := range 30 {
			dims = append(dims, emf.NewDimension(fmt.Sprintf("d%d", i), "v"), emf.NewDimension(fmt.Sprintf("d%d", i), "w"))
		}
		emf.New(emf.WithWriter(&buf), emf.WithoutDimensions(), emf.WithErrorHandler(func(err error) {
			errs = append(errs, err)
		})).
			DimensionSet(dims...).
			Metric("m", 1).
			Log()
		if len(errs) > 0 {
			t.Errorf("expected no errors, got %v", errs)
		}
		assertCompliant(t, buf.Bytes())
		if !bytes.Contains(buf.Bytes(), []byte(`"d29":"w"`)) {
			t.Errorf("dimension set missing from output: %s", buf.Bytes())
		}
	})

	for _, unit := range allUnits {
		t.Run("unit "+string(unit), func(t *testing.T) {
			var buf bytes.Buffer
			emf.New(emf.WithWriter(&buf), emf.WithoutDimensions()).
				MetricAs("m", 1, unit).
				Log()
			assertCompliant(t, buf.Bytes())
		})
	}
}

// FuzzSpecCompliance feeds random input through the API and checks the
// output is always spec compliant. Anything invalid must be skipped and
// reported to the error handler. Run it with: just fuzz
func FuzzSpecCompliance(f *testing.F) {
	f.Add("ns", "dimKey", "dimValue", "other", "intMetric", 1, "floatMetric", 1.5, "None", "prop", uint8(0), uint8(0))
	f.Add("aws-embedded-metrics", "Service", "api", "other", "latency", 250, "size", 1024.0, "Milliseconds", "requestId", uint8(0), uint8(0))
	f.Add("ns", "same", "value", "other", "same", 1, "other", 2.0, "Count", "p", uint8(0), uint8(0))
	f.Add("ns", "d", "v", "other", "m", 1, "f", math.NaN(), "None", "p", uint8(0), uint8(0))
	f.Add("ns", "d", "v", "other", "m", 1, "f", math.Inf(-1), "None", "p", uint8(0), uint8(0))
	f.Add("ns", "d", "v", "other", "m", 1, "f", 1e300, "None", "p", uint8(0), uint8(0))
	f.Add("ns", "d", "v", "other", "_aws", 1, "f", 1.0, "None", "_aws", uint8(0), uint8(0))
	f.Add(" ", ":d", "", "", "", 1, "\x00", 1.0, "Parsecs", "m", uint8(0), uint8(0))
	f.Add("ns", "d", "v", "other", "m", 1, "f", 1.0, "None", "p", uint8(98), uint8(0))
	f.Add("ns", "d", "v", "other", "m", 1, "f", 1.0, "None", "p", uint8(255), uint8(0))
	f.Add("ns", "d", "v", "v", "m", 1, "f", 1.0, "None", "p", uint8(150), uint8(0))
	f.Add("ns", "d", "v", "w", "m", 1, "f", 1.0, "None", "p", uint8(150), uint8(0))
	f.Add("ns", "d", "v", "other", "m", 1, "f", 1.0, "None", "p", uint8(1), uint8(0))
	f.Add("ns", "d", "v", "d", "m", 1, "f", math.NaN(), "None", "d", uint8(5), uint8(0))
	f.Add("ns", "d", "v", "w", "m", 1, "f", 1.0, "None", "p", uint8(150), uint8(1))
	f.Add("ns", "d", "v", "w", "m", 1, "f", 1.0, "None", "p", uint8(150), uint8(2))
	f.Add("ns", "ServiceName", "v", "w", "m", 1, "f", 1.0, "None", "ServiceType", uint8(3), uint8(2))
	f.Add("ns", "d", "v", "w", "ServiceName", 1, "ServiceType", 1.0, "None", "p", uint8(0), uint8(2))
	f.Add("ns", "d", "v", "w", "m", 1, "f", 1.0, "None", "p", uint8(5), uint8(4))
	f.Add("ns", "d", "v", "w", "d", 1, "f", 1.0, "None", "p", uint8(5), uint8(8))
	f.Add("ns", "d", "v", "w", "m", 1, "f", 1.0, "None", "d", uint8(5), uint8(11))
	f.Add("ns", "ServiceName", "v", "w", "m", 1, "f", 1.0, "None", "p", uint8(150), uint8(14))
	f.Add("ns", "d", "v", "w", "m", 1, "f", 1.0, "None", "p", uint8(150), uint8(12))
	f.Add("ns", "d", "v", "v", "m", 1, "f", 1.0, "None", "p", uint8(3), uint8(13))

	// The Lambda default dimensions are used unless mode%3 is 0, and merged
	// into each dimension set if it is 2. mode/3%5 picks how dimensions are
	// reset or replaced before the line is written.
	f.Setenv("AWS_LAMBDA_FUNCTION_NAME", "fn")

	f.Fuzz(func(t *testing.T, namespace, dimKey, dimValue, otherValue, intName string, intValue int,
		floatName string, floatValue float64, unit, propKey string, extraMetrics, mode uint8) {
		var buf bytes.Buffer
		var errs []error
		loggerOpts := []emf.LoggerOption{emf.WithWriter(&buf), emf.WithErrorHandler(func(err error) {
			errs = append(errs, err)
		})}
		switch mode % 3 {
		case 0:
			loggerOpts = append(loggerOpts, emf.WithoutDimensions())
		case 2:
			loggerOpts = append(loggerOpts, emf.WithMergedDefaultDimensions())
		}
		logger := emf.New(loggerOpts...)
		// A property no other input may replace, unless the fuzzer picks
		// its key, which is checked against every line below.
		checked := fuzzPropertyValue(extraMetrics+3, intValue, floatValue, dimValue)
		logger.PropertyValue(fuzzCheckedKey, checked)

		// The property is set first and last, so dimensions and metrics with
		// its key replace it, and it can't replace them. Property and
		// PropertyValue replace each other, and the other value's key may
		// clash with a dimension or metric too.
		logger.
			Property(propKey, "first").
			PropertyValue(otherValue, fuzzPropertyValue(extraMetrics, intValue, floatValue, dimValue)).
			PropertyValue(propKey, fuzzPropertyValue(extraMetrics+1, intValue, floatValue, dimValue)).
			Namespace(namespace).
			Dimension(dimKey, dimValue).
			DimensionSet(emf.NewDimension(dimKey, dimValue), emf.NewDimension(dimKey, dimValue)).
			MetricAs(intName, intValue, emf.MetricUnit(unit)).
			MetricFloatAs(floatName, floatValue, emf.MetricUnit(unit)).
			Property(propKey, "value").
			PropertyValue(propKey, fuzzPropertyValue(extraMetrics+2, intValue, floatValue, dimValue))
		ctx := logger.NewContext().Dimension(dimKey, dimValue)
		for i := range int(extraMetrics) {
			ctx.Metric(fmt.Sprintf("extra%d", i), i)
		}

		// Repeat names, lists of values and high resolution, including
		// lists long enough to be split.
		var opts []emf.MetricOption
		if extraMetrics%2 == 1 {
			opts = append(opts, emf.HighResolution())
		}
		logger.Put(intName, floatValue, opts...)
		values := make([]float64, int(extraMetrics)%150)
		for i := range values {
			values[i] = floatValue * float64(i)
		}
		ctx.PutValues(floatName, values, append(opts, emf.Unit(emf.MetricUnit(unit)))...)

		// A context that may disagree with the others on the dimension
		// value, so its metrics are written on separate lines. It shares a
		// metric name with the others.
		other := logger.NewContext().Namespace(namespace).Dimension(dimKey, otherValue)
		for i := range int(extraMetrics) / 2 {
			other.Metric(fmt.Sprintf("other%d", i), i)
		}
		other.PutValues(floatName, values, append(opts, emf.Unit(emf.MetricUnit(unit)))...)

		// Reset or replace dimensions, then use the removed names again. Only
		// keys from the input are used, so the checked property can't clash.
		useDefault := mode%2 == 0
		switch mode / 3 % 5 {
		case 1:
			logger.ResetDimensions(useDefault).Dimension(propKey, otherValue)
		case 2:
			// A removed dimension name may become a metric or property.
			logger.ResetDimensions(useDefault).Metric(dimKey, intValue).Property(dimKey, "p")
			ctx.ResetDimensions(!useDefault)
			other.ResetDimensions(useDefault)
			logger.PropertyValue(dimKey, fuzzPropertyValue(extraMetrics, intValue, floatValue, dimValue))
		case 3:
			logger.SetDimensions(useDefault,
				[]emf.Dimension{emf.NewDimension(dimKey, otherValue)},
				[]emf.Dimension{emf.NewDimension(propKey, dimValue), emf.NewDimension(dimKey, dimValue)},
				[]emf.Dimension{emf.NewDimension(dimKey, otherValue)})
			other.SetDimensions(!useDefault)
		case 4:
			ctx.SetDimensions(useDefault, []emf.Dimension{emf.NewDimension(dimKey, otherValue)}).Metric(intName, intValue)
			other.SetDimensions(useDefault, nil, []emf.Dimension{emf.NewDimension(floatName, dimValue)})
		}
		logger.Log()

		if buf.Len() == 0 {
			// Both metrics were skipped, so they must have been reported.
			if len(errs) < 2 {
				t.Fatalf("no output, but only %d errors reported: %v", len(errs), errs)
			}
			return
		}
		for line := range bytes.Lines(buf.Bytes()) {
			assertCompliant(t, line)
		}
		for _, err := range errs {
			if !errors.Is(err, emf.ErrInvalid) {
				t.Errorf("error %v does not wrap ErrInvalid", err)
			}
		}
		if !slices.Contains([]string{dimKey, otherValue, intName, floatName, propKey}, fuzzCheckedKey) {
			assertPropertyValue(t, buf.Bytes(), errs, fuzzCheckedKey, checked)
		}
	})
}

// fuzzCheckedKey is the key of the property FuzzSpecCompliance checks is
// written on every line.
const fuzzCheckedKey = "fuzzChecked"

// assertPropertyValue checks a property set with PropertyValue and not
// replaced is written on every line as the value encoded as JSON, or, if
// the value can't be encoded, isn't written and is reported.
func assertPropertyValue(t *testing.T, out []byte, errs []error, key string, value any) {
	t.Helper()
	want, encodeErr := json.Marshal(value)
	for line := range bytes.Lines(out) {
		var members map[string]json.RawMessage
		if err := json.Unmarshal(line, &members); err != nil {
			t.Fatal(err)
		}
		got, ok := members[key]
		switch {
		case encodeErr == nil && !ok:
			t.Errorf("expected %q in %s", key, line)
		case encodeErr == nil && !bytes.Equal(got, want):
			t.Errorf("expected %q to be %s, got %s in %s", key, want, got, line)
		case encodeErr != nil && ok:
			t.Errorf("expected %q to be skipped, since %v, got %s", key, encodeErr, got)
		}
	}
	if encodeErr != nil && !slices.ContainsFunc(errs, func(err error) bool {
		return strings.Contains(err.Error(), fmt.Sprintf("skipped property %q", key))
	}) {
		t.Errorf("expected property %q to be reported, got %v", key, errs)
	}
}

// fuzzPropertyValue returns a property value of a type picked by kind,
// made from the fuzzer's input. A float may be NaN or infinite, and a
// json.Marshaler or json.RawMessage may not be valid JSON, which
// PropertyValue skips and reports.
func fuzzPropertyValue(kind uint8, i int, f float64, s string) any {
	switch kind % 9 {
	case 0:
		return i
	case 1:
		return f
	case 2:
		return kind%4 == 2
	case 3:
		return nil
	case 4:
		return []any{s, i, f}
	case 5:
		return map[string]any{s: map[string]any{"n": i, "f": f}}
	case 6:
		return fuzzMarshaler(s) // may not be valid JSON
	case 7:
		return json.RawMessage(s) // may not be valid JSON
	default:
		return s + "\xff<&>" // invalid UTF-8, and characters JSON escapes
	}
}

// fuzzMarshaler is a json.Marshaler that returns its string as is.
type fuzzMarshaler string

func (m fuzzMarshaler) MarshalJSON() ([]byte, error) { return []byte(m), nil }

// TestErrorHandler checks each kind of invalid input is skipped and
// reported, while valid metrics on the same logger are still written.
func TestErrorHandler(t *testing.T) {
	tcs := map[string]func(*emf.Logger){
		"NaN value":             func(l *emf.Logger) { l.MetricFloat("bad", math.NaN()) },
		"infinite value":        func(l *emf.Logger) { l.MetricFloat("bad", math.Inf(1)) },
		"value too big":         func(l *emf.Logger) { l.MetricFloat("bad", 1e300) },
		"empty metric name":     func(l *emf.Logger) { l.Metric("", 1) },
		"blank metric name":     func(l *emf.Logger) { l.Metric("   ", 1) },
		"non-ASCII metric name": func(l *emf.Logger) { l.Metric("caf\u00e9", 1) },
		"metric name too long":  func(l *emf.Logger) { l.Metric(strings.Repeat("m", 256), 1) },
		"reserved metric name":  func(l *emf.Logger) { l.Metric("_aws", 1) },
		"unknown unit":          func(l *emf.Logger) { l.MetricAs("bad", 1, emf.MetricUnit("Parsecs")) },
		"metric named as dimension": func(l *emf.Logger) {
			l.Dimension("dup", "v").Metric("dup", 1)
		},
		"dimension named as metric": func(l *emf.Logger) {
			l.Metric("dup", 1).Dimension("dup", "v")
		},
		"dimension key starts with colon": func(l *emf.Logger) { l.Dimension(":d", "v") },
		"empty dimension value":           func(l *emf.Logger) { l.Dimension("d", "") },
		"31 dimensions": func(l *emf.Logger) {
			var dims []emf.Dimension
			for i := range 31 {
				dims = append(dims, emf.NewDimension(fmt.Sprintf("d%d", i), "v"))
			}
			l.DimensionSet(dims...)
		},
		"blank namespace":         func(l *emf.Logger) { l.Namespace(" ") },
		"property named a metric": func(l *emf.Logger) { l.Metric("dup", 1).Property("dup", "v") },
		"reserved property":       func(l *emf.Logger) { l.Property("_aws", "v") },
		"property value named a metric": func(l *emf.Logger) {
			l.Metric("dup", 1).PropertyValue("dup", 2)
		},
		"property value named a dimension": func(l *emf.Logger) {
			l.Dimension("dup", "v").PropertyValue("dup", 2)
		},
		"property value named a context dimension": func(l *emf.Logger) {
			l.NewContext().Dimension("dup", "v")
			l.PropertyValue("dup", 2)
		},
		"reserved property value": func(l *emf.Logger) { l.PropertyValue("_aws", map[string]int{"Timestamp": 1}) },
		"context NaN value":       func(l *emf.Logger) { l.NewContext().MetricFloat("bad", math.NaN()).Metric("ctx", 1) },
	}

	for name, given := range tcs {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			var errs []error
			logger := emf.New(emf.WithWriter(&buf), emf.WithoutDimensions(), emf.WithErrorHandler(func(err error) {
				errs = append(errs, err)
			}))
			given(logger)
			logger.Metric("good", 1).Log()

			if len(errs) != 1 {
				t.Fatalf("expected 1 error, got %d: %v", len(errs), errs)
			}
			if !errors.Is(errs[0], emf.ErrInvalid) {
				t.Errorf("error %v does not wrap ErrInvalid", errs[0])
			}
			assertCompliant(t, buf.Bytes())
			if !bytes.Contains(buf.Bytes(), []byte(`"good":1`)) {
				t.Errorf("valid metric missing from output: %s", buf.Bytes())
			}
		})
	}

	t.Run("write error", func(t *testing.T) {
		var errs []error
		emf.New(emf.WithWriter(failingWriter{}), emf.WithErrorHandler(func(err error) {
			errs = append(errs, err)
		})).Metric("m", 1).Log()

		if len(errs) != 1 || errors.Is(errs[0], emf.ErrInvalid) {
			t.Errorf("expected 1 write error, got %v", errs)
		}
	})

	t.Run("no handler", func(t *testing.T) {
		var buf bytes.Buffer
		emf.New(emf.WithWriter(&buf), emf.WithoutDimensions()).
			MetricFloat("bad", math.NaN()).
			Metric("good", 1).
			Log()
		assertCompliant(t, buf.Bytes())
	})
}

// TestPropertyReplaced checks a dimension or metric with the same key as a
// property is kept, and reported once, unless the property is one of the
// Lambda properties New adds.
func TestPropertyReplaced(t *testing.T) {
	lambda := map[string]string{
		"AWS_LAMBDA_FUNCTION_NAME":        "some-func-name",
		"AWS_EXECUTION_ENV":               "golang",
		"AWS_LAMBDA_FUNCTION_MEMORY_SIZE": "128",
		"AWS_LAMBDA_FUNCTION_VERSION":     "1",
		"AWS_LAMBDA_LOG_STREAM_NAME":      "log/stream",
		"_X_AMZN_TRACE_ID":                "Root=1;Sampled=1",
	}
	tcs := []struct {
		name     string
		env      map[string]string
		given    func(*emf.Logger)
		reported int
		want     []string // in the output
	}{
		{
			name:     "dimension",
			given:    func(l *emf.Logger) { l.Property("dup", "p").Dimension("dup", "v") },
			reported: 1,
			want:     []string{`"dup":"v"`},
		},
		{
			name: "dimension set",
			given: func(l *emf.Logger) {
				l.Property("dup", "p").DimensionSet(emf.NewDimension("a", "b"), emf.NewDimension("dup", "v"))
			},
			reported: 1,
			want:     []string{`"dup":"v"`, `"a":"b"`},
		},
		{
			name:     "metric",
			given:    func(l *emf.Logger) { l.Property("dup", "p").Metric("dup", 7) },
			reported: 1,
			want:     []string{`"dup":7`},
		},
		{
			name: "context dimension",
			given: func(l *emf.Logger) {
				l.Property("dup", "p")
				l.NewContext().Dimension("dup", "v").Metric("ctx", 1)
			},
			reported: 1,
			want:     []string{`"dup":"v"`},
		},
		{
			name: "dimension repeated, in two contexts",
			given: func(l *emf.Logger) {
				l.Property("dup", "p").Dimension("dup", "v").Dimension("dup", "w")
				l.NewContext().Dimension("dup", "x").Metric("ctx", 1)
			},
			reported: 1,
			want:     []string{`"dup":"w"`, `"dup":"x"`},
		},
		{
			name: "metric repeated, in two contexts",
			given: func(l *emf.Logger) {
				l.Property("dup", "p").Put("dup", 1).Put("dup", 2)
				l.NewContext().Put("dup", 3)
			},
			reported: 1,
			want:     []string{`"dup":[1,2,3]`},
		},
		{
			name:     "dimension replaces PropertyValue",
			given:    func(l *emf.Logger) { l.PropertyValue("dup", 503).Dimension("dup", "v") },
			reported: 1,
			want:     []string{`"dup":"v"`},
		},
		{
			name: "context dimension replaces PropertyValue",
			given: func(l *emf.Logger) {
				l.PropertyValue("dup", []int{1, 2})
				l.NewContext().Dimension("dup", "v").Metric("ctx", 1)
			},
			reported: 1,
			want:     []string{`"dup":"v"`},
		},
		{
			name:     "metric replaces PropertyValue",
			given:    func(l *emf.Logger) { l.PropertyValue("dup", true).Metric("dup", 7) },
			reported: 1,
			want:     []string{`"dup":7`},
		},
		{
			name:     "dimension replaces PropertyValue that replaced Property",
			given:    func(l *emf.Logger) { l.Property("dup", "p").PropertyValue("dup", 1).Dimension("dup", "v") },
			reported: 1,
			want:     []string{`"dup":"v"`},
		},
		{
			name:     "skipped PropertyValue keeps the earlier value",
			given:    func(l *emf.Logger) { l.PropertyValue("dup", 1).PropertyValue("dup", math.NaN()) },
			reported: 1, // only that the value can't be encoded
			want:     []string{`"dup":1`},
		},
		{
			name: "skipped dimension set",
			given: func(l *emf.Logger) {
				l.Property("dup", "p").DimensionSet(emf.NewDimension("dup", "v"), emf.NewDimension(":bad", "v"))
			},
			reported: 1, // only that the set was skipped
			want:     []string{`"dup":"p"`},
		},
		{
			name:     "skipped metric",
			given:    func(l *emf.Logger) { l.Property("dup", "p").MetricFloat("dup", math.NaN()) },
			reported: 1, // only that the metric was skipped
			want:     []string{`"dup":"p"`},
		},
		{
			name: "Lambda properties",
			env:  lambda,
			given: func(l *emf.Logger) {
				l.Dimension("functionVersion", "v").
					DimensionSet(emf.NewDimension("executionEnvironment", "e"), emf.NewDimension("logStreamId", "s")).
					Dimension("traceId", "t").
					Metric("memorySize", 256)
			},
			want: []string{`"functionVersion":"v"`, `"executionEnvironment":"e"`, `"logStreamId":"s"`, `"traceId":"t"`, `"memorySize":256`},
		},
		{
			name: "Lambda property set with Property",
			env:  lambda,
			given: func(l *emf.Logger) {
				l.Property("functionVersion", "mine").Dimension("functionVersion", "v")
			},
			reported: 1,
			want:     []string{`"functionVersion":"v"`},
		},
	}

	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			var buf bytes.Buffer
			var errs []error
			opts := []emf.LoggerOption{emf.WithWriter(&buf), emf.WithErrorHandler(func(err error) {
				errs = append(errs, err)
			})}
			if tc.env == nil {
				opts = append(opts, emf.WithoutDimensions())
			}
			logger := emf.New(opts...)
			tc.given(logger)
			logger.Metric("good", 1).Log()

			if len(errs) != tc.reported {
				t.Errorf("expected %d errors, got %d: %v", tc.reported, len(errs), errs)
			}
			for _, err := range errs {
				if !errors.Is(err, emf.ErrInvalid) {
					t.Errorf("error %v does not wrap ErrInvalid", err)
				}
			}
			for line := range bytes.Lines(buf.Bytes()) {
				assertCompliant(t, line)
			}
			for _, want := range tc.want {
				if !bytes.Contains(buf.Bytes(), []byte(want)) {
					t.Errorf("expected %s in output: %s", want, buf.Bytes())
				}
			}
		})
	}
}

// TestTimestampWindow checks timestamps CloudWatch won't publish are
// reported, and that the line is still written.
func TestTimestampWindow(t *testing.T) {
	day := 24 * time.Hour
	tcs := []struct {
		name     string
		offset   time.Duration
		reported bool
	}{
		{"now", 0, false},
		{"13 days ago", -13 * day, false},
		{"1 hour ahead", time.Hour, false},
		{"15 days ago", -15 * day, true},
		{"3 hours ahead", 3 * time.Hour, true},
	}

	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			var errs []error
			emf.New(
				emf.WithWriter(&buf),
				emf.WithoutDimensions(),
				emf.WithTimestamp(time.Now().Add(tc.offset)),
				emf.WithErrorHandler(func(err error) { errs = append(errs, err) }),
			).Metric("m", 1).Log()

			if buf.Len() == 0 {
				t.Error("expected the line to be written")
			}
			if !tc.reported {
				if len(errs) != 0 {
					t.Errorf("expected no errors, got %v", errs)
				}
				assertCompliant(t, buf.Bytes())
				return
			}
			if len(errs) != 1 || !errors.Is(errs[0], emf.ErrInvalid) {
				t.Errorf("expected 1 ErrInvalid error, got %v", errs)
			}
		})
	}

	t.Run("reported once when split", func(t *testing.T) {
		var errs []error
		logger := emf.New(
			emf.WithWriter(io.Discard),
			emf.WithTimestamp(time.Now().Add(-15*day)),
			emf.WithErrorHandler(func(err error) { errs = append(errs, err) }),
		)
		for i := range 250 {
			logger.Metric(fmt.Sprintf("m%d", i), i)
		}
		logger.Log()

		if len(errs) != 1 {
			t.Errorf("expected 1 error, got %d", len(errs))
		}
	})

	t.Run("no metrics, no error", func(t *testing.T) {
		var errs []error
		emf.New(
			emf.WithTimestamp(time.Now().Add(-15*day)),
			emf.WithErrorHandler(func(err error) { errs = append(errs, err) }),
		).Log()

		if len(errs) != 0 {
			t.Errorf("expected no errors, got %v", errs)
		}
	})
}

// TestEventSizeLimit checks lines over CloudWatch's 1 MB limit are
// reported, once per line, and still written.
func TestEventSizeLimit(t *testing.T) {
	tcs := []struct {
		name          string
		propertyBytes int
		metrics       int
		reported      int
	}{
		{"small", 100, 1, 0},
		{"just under 1 MB", 1<<20 - 1024, 1, 0},
		{"2 MB", 2 << 20, 1, 1},
		{"2 MB split across 2 lines", 2 << 20, 150, 2},
	}

	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			var errs []error
			logger := emf.New(emf.WithWriter(&buf), emf.WithoutDimensions(), emf.WithErrorHandler(func(err error) {
				errs = append(errs, err)
			})).Property("big", strings.Repeat("x", tc.propertyBytes))
			for i := range tc.metrics {
				logger.Metric(fmt.Sprintf("m%d", i), i)
			}
			logger.Log()

			if buf.Len() < tc.propertyBytes {
				t.Errorf("expected the lines to be written, got %d bytes", buf.Len())
			}
			if len(errs) != tc.reported {
				t.Fatalf("expected %d errors, got %d: %v", tc.reported, len(errs), errs)
			}
			for _, err := range errs {
				if !errors.Is(err, emf.ErrInvalid) {
					t.Errorf("error %v does not wrap ErrInvalid", err)
				}
			}
			if tc.reported == 0 {
				for line := range bytes.Lines(buf.Bytes()) {
					assertCompliant(t, line)
				}
			}
		})
	}

	t.Run("PropertyValue over 1 MB", func(t *testing.T) {
		const n = 1 << 20
		timestamp := emf.WithTimestamp(time.Now())
		var small, buf bytes.Buffer
		emf.New(emf.WithWriter(&small), emf.WithoutDimensions(), timestamp).Metric("m", 1).Log()

		var errs []error
		emf.New(emf.WithWriter(&buf), emf.WithoutDimensions(), timestamp, emf.WithErrorHandler(func(err error) {
			errs = append(errs, err)
		})).
			PropertyValue("big", make([]int, n)).
			Metric("m", 1).
			Log()

		// The same line, plus "big":[0,0,...,0] and a comma: n zeros and
		// n-1 commas in the list.
		want := small.Len() + len(`"big":[]`) + 2*n - 1 + len(",")
		if buf.Len() != want {
			t.Errorf("expected a line of %d bytes, got %d", want, buf.Len())
		}
		if len(errs) != 1 || !errors.Is(errs[0], emf.ErrInvalid) {
			t.Fatalf("expected 1 ErrInvalid error, got %v", errs)
		}
		// The report gives the size of the line, without its newline.
		if size := fmt.Sprintf("%d bytes", want-1); !strings.Contains(errs[0].Error(), size) {
			t.Errorf("expected %q in %v", size, errs[0])
		}
	})
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }

// TestSplitsLargeLogs checks more than 100 metrics are split into several
// compliant log lines, with every metric logged exactly once.
func TestSplitsLargeLogs(t *testing.T) {
	tcs := []struct {
		name          string
		defaultCount  int
		contextCount  int
		expectedLines int
	}{
		{"exactly 100", 100, 0, 1},
		{"101 in one directive", 101, 0, 2},
		{"250 in one directive", 250, 0, 3},
		{"split across contexts", 60, 60, 2},
		{"context only", 0, 201, 3},
	}

	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			logger := emf.New(emf.WithWriter(&buf), emf.WithoutDimensions()).
				Namespace("default-ns").
				Dimension("d", "v").
				Property("p", "x")
			for i := range tc.defaultCount {
				logger.Metric(fmt.Sprintf("default%d", i), i)
			}
			ctx := logger.NewContext().Namespace("context-ns").Dimension("c", "w")
			for i := range tc.contextCount {
				ctx.Metric(fmt.Sprintf("context%d", i), i)
			}
			logger.Log()

			type metric struct {
				namespace string
				dims      string
				value     any
			}
			seen := map[string]metric{}
			lines := 0
			for line := range bytes.Lines(buf.Bytes()) {
				lines++
				assertCompliant(t, line)

				var doc map[string]any
				if err := json.Unmarshal(line, &doc); err != nil {
					t.Fatal(err)
				}
				if doc["p"] != "x" {
					t.Errorf("line %d is missing property p", lines)
				}
				meta := doc["_aws"].(map[string]any)
				for _, d := range meta["CloudWatchMetrics"].([]any) {
					dir := d.(map[string]any)
					dims := fmt.Sprint(dir["Dimensions"])
					for _, m := range dir["Metrics"].([]any) {
						name := m.(map[string]any)["Name"].(string)
						if _, dup := seen[name]; dup {
							t.Errorf("metric %s logged more than once", name)
						}
						seen[name] = metric{dir["Namespace"].(string), dims, doc[name]}
					}
				}
			}

			if lines != tc.expectedLines {
				t.Errorf("expected %d lines, got %d", tc.expectedLines, lines)
			}
			if len(seen) != tc.defaultCount+tc.contextCount {
				t.Errorf("expected %d metrics, got %d", tc.defaultCount+tc.contextCount, len(seen))
			}
			for i := range tc.defaultCount {
				want := metric{"default-ns", "[[d]]", float64(i)}
				if got := seen[fmt.Sprintf("default%d", i)]; got != want {
					t.Errorf("default%d: got %+v, want %+v", i, got, want)
				}
			}
			for i := range tc.contextCount {
				want := metric{"context-ns", "[[c]]", float64(i)}
				if got := seen[fmt.Sprintf("context%d", i)]; got != want {
					t.Errorf("context%d: got %+v, want %+v", i, got, want)
				}
			}
		})
	}
}

func assertCompliant(t *testing.T, out []byte) {
	t.Helper()
	for _, v := range specViolations(out, time.Now()) {
		t.Errorf("not spec compliant: %s\noutput: %s", v, out)
	}
}

// loggedMetric is one metric in a directive of a log line.
type loggedMetric struct {
	line      int
	namespace string
	provider  any
	value     any
}

// loggedMetrics checks every line is spec compliant, and returns each
// logged metric by name, with the Provider dimension value on its line.
func loggedMetrics(t *testing.T, out []byte) (map[string][]loggedMetric, int) {
	t.Helper()
	seen := map[string][]loggedMetric{}
	lines := 0
	for line := range bytes.Lines(out) {
		lines++
		assertCompliant(t, line)

		var doc map[string]any
		if err := json.Unmarshal(line, &doc); err != nil {
			t.Fatal(err)
		}
		meta := doc["_aws"].(map[string]any)
		for _, d := range meta["CloudWatchMetrics"].([]any) {
			dir := d.(map[string]any)
			for _, m := range dir["Metrics"].([]any) {
				name := m.(map[string]any)["Name"].(string)
				seen[name] = append(seen[name], loggedMetric{lines, dir["Namespace"].(string), doc["Provider"], doc[name]})
			}
		}
	}
	return seen, lines
}

// TestDisagreeingContextsSplit checks contexts that disagree on a dimension
// value are written on separate lines, while still splitting more than 100
// metrics, or more than 100 values, across lines.
func TestDisagreeingContextsSplit(t *testing.T) {
	t.Run("more than 100 metrics", func(t *testing.T) {
		var buf bytes.Buffer
		var errs []error
		logger := emf.New(emf.WithWriter(&buf), emf.WithoutDimensions(), emf.WithErrorHandler(func(err error) {
			errs = append(errs, err)
		})).Namespace("a").Dimension("Provider", "card")
		b := logger.NewContext().Namespace("b").Dimension("Provider", "paypal")
		c := logger.NewContext().Namespace("c").Dimension("Provider", "card")
		counts := map[string]int{"a": 150, "b": 60, "c": 30}
		for i := range counts["a"] {
			logger.Metric(fmt.Sprintf("a%d", i), i)
		}
		for i := range counts["b"] {
			b.Metric(fmt.Sprintf("b%d", i), i)
		}
		for i := range counts["c"] {
			c.Metric(fmt.Sprintf("c%d", i), i)
		}
		logger.Log()

		if len(errs) > 0 {
			t.Errorf("expected no errors, got %v", errs)
		}
		seen, lines := loggedMetrics(t, buf.Bytes())
		// a and c share 180 metrics over 2 lines, then b has its own line.
		if lines != 3 {
			t.Errorf("expected 3 lines, got %d", lines)
		}
		providers := map[string]string{"a": "card", "b": "paypal", "c": "card"}
		for ns, count := range counts {
			for i := range count {
				name := fmt.Sprintf("%s%d", ns, i)
				got := seen[name]
				if len(got) != 1 {
					t.Fatalf("%s: logged %d times, want once", name, len(got))
				}
				if got[0].namespace != ns || got[0].provider != providers[ns] || got[0].value != float64(i) {
					t.Errorf("%s: got %+v", name, got[0])
				}
				if onLastLine := got[0].line == 3; onLastLine != (ns == "b") {
					t.Errorf("%s: on line %d", name, got[0].line)
				}
			}
		}
	})

	t.Run("more than 100 values", func(t *testing.T) {
		var buf bytes.Buffer
		values := make([]float64, 250)
		for i := range values {
			values[i] = float64(i)
		}
		logger := emf.New(emf.WithWriter(&buf), emf.WithoutDimensions()).
			Namespace("a").
			Dimension("Provider", "card").
			PutValues("Latency", values)
		logger.NewContext().
			Namespace("b").
			Dimension("Provider", "paypal").
			Metric("Refunds", 1).
			Put("Latency", 1000)
		logger.Log()

		seen, lines := loggedMetrics(t, buf.Bytes())
		// Each context writes Latency's 251 values over 3 rounds.
		if lines != 6 {
			t.Errorf("expected 6 lines, got %d", lines)
		}
		want := slices.Concat(values, []float64{1000})
		for _, tc := range []struct{ namespace, provider string }{{"a", "card"}, {"b", "paypal"}} {
			var got []float64
			for _, m := range seen["Latency"] {
				if m.namespace != tc.namespace {
					continue
				}
				if m.provider != tc.provider {
					t.Errorf("Latency in %s on line %d has Provider %v", tc.namespace, m.line, m.provider)
				}
				for _, v := range m.value.([]any) {
					got = append(got, v.(float64))
				}
			}
			if !slices.Equal(got, want) {
				t.Errorf("Latency in %s: got %d values %v, want %d", tc.namespace, len(got), got, len(want))
			}
		}
		if r := seen["Refunds"]; len(r) != 1 || r[0].namespace != "b" || r[0].provider != "paypal" {
			t.Errorf("Refunds: got %+v", r)
		}
	})
}

// TestDisagreeingContextsRandom builds random contexts that often disagree
// on dimension values, and checks every line is spec compliant, every
// directive's dimensions have its own context's values, and every metric is
// written for each context that logged it.
func TestDisagreeingContextsRandom(t *testing.T) {
	t.Run("without dimensions", func(t *testing.T) {
		testDisagreeingContextsRandom(t, nil, emf.WithoutDimensions())
	})
	t.Run("merged Lambda defaults", func(t *testing.T) {
		t.Setenv("AWS_LAMBDA_FUNCTION_NAME", "fn")
		defaults := map[string]string{"ServiceName": "fn", "ServiceType": "AWS::Lambda::Function"}
		testDisagreeingContextsRandom(t, defaults, emf.WithMergedDefaultDimensions())
	})
}

// testDisagreeingContextsRandom runs TestDisagreeingContextsRandom with a
// logger option. Every context starts with the defaults, which must be
// merged into the start of every dimension set and be on every line.
func testDisagreeingContextsRandom(t *testing.T, defaults map[string]string, opt emf.LoggerOption) {
	type context struct {
		dimension func(key, value string)
		put       func(name string, value float64)
		values    map[string]string
		metrics   map[string]bool
	}

	for seed := range 500 {
		r := rand.New(rand.NewPCG(uint64(seed), 0))
		var buf bytes.Buffer
		logger := emf.New(emf.WithWriter(&buf), opt).Namespace("c0")
		contexts := []*context{{
			dimension: func(k, v string) { logger.Dimension(k, v) },
			put:       func(n string, v float64) { logger.Put(n, v) },
		}}
		for i := range 1 + r.IntN(4) {
			c := logger.NewContext().Namespace(fmt.Sprintf("c%d", i+1))
			contexts = append(contexts, &context{
				dimension: func(k, v string) { c.Dimension(k, v) },
				put:       func(n string, v float64) { c.Put(n, v) },
			})
		}
		for _, c := range contexts {
			c.values, c.metrics = maps.Clone(defaults), map[string]bool{}
			if c.values == nil {
				c.values = map[string]string{}
			}
		}

		for range r.IntN(60) {
			c := contexts[r.IntN(len(contexts))]
			if r.IntN(3) == 0 {
				key, value := fmt.Sprintf("d%d", r.IntN(3)), fmt.Sprintf("v%d", r.IntN(3))
				c.dimension(key, value)
				c.values[key] = value
				continue
			}
			// Up to 130 names, some with more than 100 values, so lines
			// are also split by metric count and by rounds of values.
			name := fmt.Sprintf("m%d", r.IntN(130))
			for range 1 + r.IntN(2)*r.IntN(120) {
				c.put(name, 1)
			}
			c.metrics[name] = true
		}
		logger.Log()

		logged := map[string]bool{} // namespace and metric name
		for line := range bytes.Lines(buf.Bytes()) {
			assertCompliant(t, line)
			var doc map[string]any
			if err := json.Unmarshal(line, &doc); err != nil {
				t.Fatal(err)
			}
			for key, value := range defaults {
				if doc[key] != value {
					t.Fatalf("seed %d: line has %s=%v, want %s\n%s", seed, key, doc[key], value, line)
				}
			}
			for _, d := range doc["_aws"].(map[string]any)["CloudWatchMetrics"].([]any) {
				dir := d.(map[string]any)
				namespace := dir["Namespace"].(string)
				var i int
				if _, err := fmt.Sscanf(namespace, "c%d", &i); err != nil {
					t.Fatal(err)
				}
				for _, set := range dir["Dimensions"].([]any) {
					if defaults != nil {
						keys := set.([]any)
						if len(keys) < 2 || keys[0] != "ServiceName" || keys[1] != "ServiceType" {
							t.Fatalf("seed %d: set %v of %s doesn't start with the defaults\n%s", seed, keys, namespace, line)
						}
					}
					for _, k := range set.([]any) {
						key := k.(string)
						if want := contexts[i].values[key]; doc[key] != want {
							t.Fatalf("seed %d: %s has %s=%v on its line, want %s\n%s", seed, namespace, key, doc[key], want, buf.Bytes())
						}
					}
				}
				for _, m := range dir["Metrics"].([]any) {
					logged[namespace+"/"+m.(map[string]any)["Name"].(string)] = true
				}
			}
		}
		for i, c := range contexts {
			for name := range c.metrics {
				if !logged[fmt.Sprintf("c%d/%s", i, name)] {
					t.Fatalf("seed %d: metric %s of c%d not logged\n%s", seed, name, i, buf.Bytes())
				}
			}
		}
	}
}

// TestMergedDefaultsLimit checks the 30-key limit of a dimension set counts
// the Lambda default dimensions merged into it.
func TestMergedDefaultsLimit(t *testing.T) {
	keys := func(n int, extra ...string) []emf.Dimension {
		var dims []emf.Dimension
		for i := range n {
			dims = append(dims, emf.NewDimension(fmt.Sprintf("d%d", i), "v"))
		}
		for _, key := range extra {
			dims = append(dims, emf.NewDimension(key, "v"))
		}
		return dims
	}
	tcs := []struct {
		name     string
		lambda   bool
		opts     []emf.LoggerOption
		dims     []emf.Dimension
		reported bool
	}{
		{name: "28 keys merged", lambda: true, opts: []emf.LoggerOption{emf.WithMergedDefaultDimensions()}, dims: keys(28)},
		{name: "29 keys merged", lambda: true, opts: []emf.LoggerOption{emf.WithMergedDefaultDimensions()}, dims: keys(29), reported: true},
		{name: "29 keys and ServiceName merged", lambda: true, opts: []emf.LoggerOption{emf.WithMergedDefaultDimensions()}, dims: keys(28, "ServiceName")},
		{name: "28 keys and both defaults merged", lambda: true, opts: []emf.LoggerOption{emf.WithMergedDefaultDimensions()}, dims: keys(28, "ServiceType", "ServiceName")},
		{name: "30 keys not merged", lambda: true, dims: keys(30)},
		{name: "30 keys merged outside Lambda", opts: []emf.LoggerOption{emf.WithMergedDefaultDimensions()}, dims: keys(30)},
		{name: "30 keys merged without dimensions", lambda: true, opts: []emf.LoggerOption{emf.WithMergedDefaultDimensions(), emf.WithoutDimensions()}, dims: keys(30)},
	}

	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			if tc.lambda {
				t.Setenv("AWS_LAMBDA_FUNCTION_NAME", "fn")
			}
			var buf bytes.Buffer
			var errs []error
			opts := append([]emf.LoggerOption{emf.WithWriter(&buf), emf.WithErrorHandler(func(err error) {
				errs = append(errs, err)
			})}, tc.opts...)
			logger := emf.New(opts...)
			logger.DimensionSet(tc.dims...).Metric("good", 1)
			logger.NewContext().Namespace("other").DimensionSet(tc.dims...).Metric("other", 1)
			logger.Log()

			assertCompliant(t, buf.Bytes())
			if !bytes.Contains(buf.Bytes(), []byte(`"good":1`)) {
				t.Errorf("valid metric missing from output: %s", buf.Bytes())
			}
			added := bytes.Contains(buf.Bytes(), []byte(`"d0"`))
			if tc.reported {
				if len(errs) != 2 || added {
					t.Fatalf("expected the set skipped and reported in both contexts, got %v\n%s", errs, buf.Bytes())
				}
				for _, err := range errs {
					if !errors.Is(err, emf.ErrInvalid) || !strings.Contains(err.Error(), "default dimensions merged") {
						t.Errorf("unexpected error %v", err)
					}
				}
				return
			}
			if len(errs) > 0 || !added {
				t.Errorf("expected the set added without errors, got %v\n%s", errs, buf.Bytes())
			}
		})
	}
}

// TestMergedDefaultsAreDimensions checks ServiceName and ServiceType still
// count as dimensions when they are merged into each set, so a metric or
// property can't use their names.
func TestMergedDefaultsAreDimensions(t *testing.T) {
	t.Setenv("AWS_LAMBDA_FUNCTION_NAME", "fn")
	tcs := map[string]func(*emf.Logger){
		"metric ServiceName":           func(l *emf.Logger) { l.Metric("ServiceName", 1) },
		"context metric ServiceType":   func(l *emf.Logger) { l.NewContext().Metric("ServiceType", 1) },
		"property ServiceName":         func(l *emf.Logger) { l.Property("ServiceName", "p") },
		"property ServiceType":         func(l *emf.Logger) { l.Dimension("Service", "s").Property("ServiceType", "p") },
		"metric replacing a dimension": func(l *emf.Logger) { l.Dimension("ServiceName", "mine").Metric("ServiceName", 1) },
	}

	for name, given := range tcs {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			var errs []error
			logger := emf.New(emf.WithWriter(&buf), emf.WithMergedDefaultDimensions(), emf.WithErrorHandler(func(err error) {
				errs = append(errs, err)
			}))
			given(logger)
			logger.Metric("good", 1).Log()

			if len(errs) != 1 || !errors.Is(errs[0], emf.ErrInvalid) {
				t.Fatalf("expected 1 error, got %v", errs)
			}
			assertCompliant(t, buf.Bytes())
			if !bytes.Contains(buf.Bytes(), []byte(`"ServiceType":"AWS::Lambda::Function"`)) {
				t.Errorf("default dimension missing from output: %s", buf.Bytes())
			}
		})
	}
}

// TestLambdaEnvReadByNew checks the Lambda environment is read once, by New.
// Contexts made later use the same defaults, even if the environment changed
// in between, so their dimensions agree with the logger's properties.
func TestLambdaEnvReadByNew(t *testing.T) {
	opts := map[string][]emf.LoggerOption{
		"separate set": nil,
		"merged":       {emf.WithMergedDefaultDimensions()},
	}
	for name, opt := range opts {
		t.Run(name+", set after New", func(t *testing.T) {
			t.Setenv("AWS_LAMBDA_FUNCTION_NAME", "")
			var buf bytes.Buffer
			logger := emf.New(append([]emf.LoggerOption{emf.WithWriter(&buf)}, opt...)...)
			t.Setenv("AWS_LAMBDA_FUNCTION_NAME", "late")
			logger.NewContext().Namespace("c").Metric("m", 1)
			logger.Log()

			assertCompliant(t, buf.Bytes())
			if bytes.Contains(buf.Bytes(), []byte("ServiceName")) {
				t.Errorf("context made after New has Lambda defaults: %s", buf.Bytes())
			}
		})
		t.Run(name+", unset after New", func(t *testing.T) {
			t.Setenv("AWS_LAMBDA_FUNCTION_NAME", "fn")
			var buf bytes.Buffer
			logger := emf.New(append([]emf.LoggerOption{emf.WithWriter(&buf)}, opt...)...)
			t.Setenv("AWS_LAMBDA_FUNCTION_NAME", "")
			logger.NewContext().Namespace("c").Metric("m", 1)
			logger.Log()

			assertCompliant(t, buf.Bytes())
			var doc struct {
				ServiceName string
				AWS         struct {
					CloudWatchMetrics []emf.MetricDirective
				} `json:"_aws"`
			}
			if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
				t.Fatal(err)
			}
			want := []emf.DimensionSet{{"ServiceName", "ServiceType"}}
			if doc.ServiceName != "fn" || len(doc.AWS.CloudWatchMetrics) != 1 ||
				!reflect.DeepEqual(doc.AWS.CloudWatchMetrics[0].Dimensions, want) {
				t.Errorf("context made after New doesn't have the defaults New read: %s", buf.Bytes())
			}
		})
	}
}
