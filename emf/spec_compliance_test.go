package emf_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
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
		"missing _aws":             `{"m":1}`,
		"trailing data":            aws(`{"Namespace":"ns","Dimensions":[[]],"Metrics":[{"Name":"m"}]}`) + `{}`,
		"empty line":               "\n",
		"metric too big": `{"_aws":{"Timestamp":1574109732004,"CloudWatchMetrics":[` +
			`{"Namespace":"ns","Dimensions":[[]],"Metrics":[{"Name":"m"}]}]},"m":1e200}`,
		"timestamp too old": `{"_aws":{"Timestamp":1,"CloudWatchMetrics":[` +
			`{"Namespace":"ns","Dimensions":[[]],"Metrics":[{"Name":"m"}]}]},"m":1}`,
		"31 dimensions": aws(`{"Namespace":"ns","Dimensions":[[` +
			strings.TrimSuffix(strings.Repeat(`"d",`, 31), ",") + `]],"Metrics":[{"Name":"m"}]}`),
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
	f.Add("ns", "dimKey", "dimValue", "other", "intMetric", 1, "floatMetric", 1.5, "None", "prop", uint8(0))
	f.Add("aws-embedded-metrics", "Service", "api", "other", "latency", 250, "size", 1024.0, "Milliseconds", "requestId", uint8(0))
	f.Add("ns", "same", "value", "other", "same", 1, "other", 2.0, "Count", "p", uint8(0))
	f.Add("ns", "d", "v", "other", "m", 1, "f", math.NaN(), "None", "p", uint8(0))
	f.Add("ns", "d", "v", "other", "m", 1, "f", math.Inf(-1), "None", "p", uint8(0))
	f.Add("ns", "d", "v", "other", "m", 1, "f", 1e300, "None", "p", uint8(0))
	f.Add("ns", "d", "v", "other", "_aws", 1, "f", 1.0, "None", "_aws", uint8(0))
	f.Add(" ", ":d", "", "", "", 1, "\x00", 1.0, "Parsecs", "m", uint8(0))
	f.Add("ns", "d", "v", "other", "m", 1, "f", 1.0, "None", "p", uint8(98))
	f.Add("ns", "d", "v", "other", "m", 1, "f", 1.0, "None", "p", uint8(255))
	f.Add("ns", "d", "v", "v", "m", 1, "f", 1.0, "None", "p", uint8(150))
	f.Add("ns", "d", "v", "w", "m", 1, "f", 1.0, "None", "p", uint8(150))

	f.Fuzz(func(t *testing.T, namespace, dimKey, dimValue, otherValue, intName string, intValue int,
		floatName string, floatValue float64, unit, propKey string, extraMetrics uint8) {
		var buf bytes.Buffer
		var errs []error
		logger := emf.New(emf.WithWriter(&buf), emf.WithoutDimensions(), emf.WithErrorHandler(func(err error) {
			errs = append(errs, err)
		}))
		logger.
			Namespace(namespace).
			Dimension(dimKey, dimValue).
			DimensionSet(emf.NewDimension(dimKey, dimValue), emf.NewDimension(dimKey, dimValue)).
			MetricAs(intName, intValue, emf.MetricUnit(unit)).
			MetricFloatAs(floatName, floatValue, emf.MetricUnit(unit)).
			Property(propKey, "value")
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
	})
}

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
