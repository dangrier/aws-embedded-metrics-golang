package emf_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/dangrier/aws-embedded-metrics-golang/emf"
)

// logLines logs with a fresh logger and returns each line decoded, plus any
// errors reported. Every line must be spec compliant.
func logLines(t *testing.T, given func(*emf.Logger)) ([]map[string]any, []error) {
	t.Helper()
	var buf bytes.Buffer
	var errs []error
	logger := emf.New(emf.WithWriter(&buf), emf.WithoutDimensions(), emf.WithErrorHandler(func(err error) {
		errs = append(errs, err)
	}))
	given(logger)
	logger.Log()

	var lines []map[string]any
	for line := range bytes.Lines(buf.Bytes()) {
		assertCompliant(t, line)
		var doc map[string]any
		if err := json.Unmarshal(line, &doc); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, doc)
	}
	return lines, errs
}

// definitions returns every metric definition in a line, as
// "namespace/name unit resolution".
func definitions(doc map[string]any) []string {
	var out []string
	for _, d := range doc["_aws"].(map[string]any)["CloudWatchMetrics"].([]any) {
		dir := d.(map[string]any)
		for _, m := range dir["Metrics"].([]any) {
			def := m.(map[string]any)
			out = append(out, fmt.Sprintf("%s/%s %v %v", dir["Namespace"], def["Name"], def["Unit"], def["StorageResolution"]))
		}
	}
	return out
}

func TestRepeatedMetricNames(t *testing.T) {
	tcs := []struct {
		name         string
		given        func(*emf.Logger)
		expected     map[string]any // root values
		expectedDefs []string
		errors       int
	}{
		{
			name:         "same name collects values",
			given:        func(l *emf.Logger) { l.Metric("Latency", 10).Metric("Latency", 20) },
			expected:     map[string]any{"Latency": []any{10.0, 20.0}},
			expectedDefs: []string{"aws-embedded-metrics/Latency None <nil>"},
		},
		{
			name:         "int and float values mix",
			given:        func(l *emf.Logger) { l.Metric("m", 1).MetricFloat("m", 2.5).Put("m", 4) },
			expected:     map[string]any{"m": []any{1.0, 2.5, 4.0}},
			expectedDefs: []string{"aws-embedded-metrics/m None <nil>"},
		},
		{
			name:         "different unit is skipped",
			given:        func(l *emf.Logger) { l.MetricAs("m", 1, emf.Count).MetricAs("m", 2, emf.Seconds) },
			expected:     map[string]any{"m": 1.0},
			expectedDefs: []string{"aws-embedded-metrics/m Count <nil>"},
			errors:       1,
		},
		{
			name:         "different resolution is skipped",
			given:        func(l *emf.Logger) { l.Put("m", 1).Put("m", 2, emf.HighResolution()) },
			expected:     map[string]any{"m": 1.0},
			expectedDefs: []string{"aws-embedded-metrics/m None <nil>"},
			errors:       1,
		},
		{
			name: "same name in two contexts shares values",
			given: func(l *emf.Logger) {
				l.Metric("m", 1)
				l.NewContext().Namespace("other").Metric("m", 2)
			},
			expected:     map[string]any{"m": []any{1.0, 2.0}},
			expectedDefs: []string{"aws-embedded-metrics/m None <nil>", "other/m None <nil>"},
		},
		{
			name: "different unit in another context is skipped",
			given: func(l *emf.Logger) {
				l.MetricAs("Latency", 10, emf.Milliseconds)
				l.NewContext().Namespace("other").MetricAs("Latency", 2, emf.Seconds)
			},
			expected:     map[string]any{"Latency": 10.0},
			expectedDefs: []string{"aws-embedded-metrics/Latency Milliseconds <nil>"},
			errors:       1,
		},
		{
			name: "different resolution in another context is skipped",
			given: func(l *emf.Logger) {
				l.NewContext().Namespace("other").Put("m", 1, emf.HighResolution())
				l.Put("m", 2)
			},
			expected:     map[string]any{"m": 1.0},
			expectedDefs: []string{"other/m None 1"},
			errors:       1,
		},
	}

	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			lines, errs := logLines(t, tc.given)
			if len(errs) != tc.errors {
				t.Fatalf("expected %d errors, got %v", tc.errors, errs)
			}
			if len(lines) != 1 {
				t.Fatalf("expected 1 line, got %d", len(lines))
			}
			for name, want := range tc.expected {
				if got := lines[0][name]; fmt.Sprint(got) != fmt.Sprint(want) {
					t.Errorf("%s: got %v, want %v", name, got, want)
				}
			}
			if got := definitions(lines[0]); fmt.Sprint(got) != fmt.Sprint(tc.expectedDefs) {
				t.Errorf("definitions: got %v, want %v", got, tc.expectedDefs)
			}
		})
	}
}

func TestPutOptions(t *testing.T) {
	lines, errs := logLines(t, func(l *emf.Logger) {
		l.Put("Standard", 1).
			Put("Latency", 12.5, emf.Unit(emf.Milliseconds), emf.HighResolution()).
			PutValues("Sizes", []float64{1, 2, 3}, emf.Unit(emf.Bytes))
		l.NewContext().Namespace("ctx").
			Put("CtxHigh", 1, emf.HighResolution()).
			PutValues("CtxValues", []float64{4, 5})
	})
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	want := []string{
		"aws-embedded-metrics/Standard None <nil>",
		"aws-embedded-metrics/Latency Milliseconds 1",
		"aws-embedded-metrics/Sizes Bytes <nil>",
		"ctx/CtxHigh None 1",
		"ctx/CtxValues None <nil>",
	}
	if got := definitions(lines[0]); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("definitions: got %v, want %v", got, want)
	}
	if got := fmt.Sprint(lines[0]["Sizes"], lines[0]["CtxValues"]); got != "[1 2 3] [4 5]" {
		t.Errorf("values: got %s", got)
	}
}

func TestPutValuesErrors(t *testing.T) {
	tcs := []struct {
		name     string
		given    func(*emf.Logger)
		expected string // "m" value, or "" if not logged
		errors   int
	}{
		{"no values", func(l *emf.Logger) { l.PutValues("m", nil) }, "", 1},
		{"invalid name reported once", func(l *emf.Logger) { l.PutValues("", []float64{1, 2, 3}) }, "", 1},
		{"unknown unit reported once", func(l *emf.Logger) { l.PutValues("m", []float64{1, 2}, emf.Unit("Parsecs")) }, "", 1},
		{"bad values skipped", func(l *emf.Logger) { l.PutValues("m", []float64{1, math.NaN(), 2, math.Inf(1)}) }, "[1 2]", 2},
	}

	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			lines, errs := logLines(t, func(l *emf.Logger) {
				tc.given(l)
				l.Metric("other", 1) // so there is always a line
			})
			if len(errs) != tc.errors {
				t.Fatalf("expected %d errors, got %v", tc.errors, errs)
			}
			for _, err := range errs {
				if !errors.Is(err, emf.ErrInvalid) {
					t.Errorf("error %v does not wrap ErrInvalid", err)
				}
			}
			got := ""
			if v, ok := lines[0]["m"]; ok {
				got = fmt.Sprint(v)
			}
			if got != tc.expected {
				t.Errorf("m: got %q, want %q", got, tc.expected)
			}
		})
	}
}

// TestSplitsLargeValueLists checks a metric with more than 100 values is
// split across lines, with every value logged once and in order.
func TestSplitsLargeValueLists(t *testing.T) {
	tcs := []struct {
		name          string
		values        int // values for metric "big"
		others        int // other metrics with one value each
		expectedLines int
	}{
		{"100 values", 100, 0, 1},
		{"101 values", 101, 0, 2},
		{"250 values", 250, 0, 3},
		{"150 values and 150 metrics", 150, 150, 3}, // 151 metrics in round one, then 1
	}

	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			lines, errs := logLines(t, func(l *emf.Logger) {
				l.Property("p", "x")
				values := make([]float64, tc.values)
				for i := range values {
					values[i] = float64(i)
				}
				l.PutValues("big", values, emf.Unit(emf.Milliseconds))
				for i := range tc.others {
					l.Metric(fmt.Sprintf("m%d", i), i)
				}
			})
			if len(errs) != 0 {
				t.Fatalf("unexpected errors: %v", errs)
			}
			if len(lines) != tc.expectedLines {
				t.Fatalf("expected %d lines, got %d", tc.expectedLines, len(lines))
			}

			var big []float64
			others := 0
			for _, line := range lines {
				if line["p"] != "x" {
					t.Error("line is missing property p")
				}
				for _, def := range definitions(line) {
					name := strings.Fields(strings.TrimPrefix(def, "aws-embedded-metrics/"))[0]
					switch v := line[name].(type) {
					case []any:
						if name != "big" {
							t.Errorf("%s logged as a list", name)
						}
						for _, x := range v {
							big = append(big, x.(float64))
						}
					case float64:
						if name == "big" {
							big = append(big, v)
						} else {
							others++
						}
					}
				}
			}
			if len(big) != tc.values {
				t.Fatalf("expected %d values for big, got %d", tc.values, len(big))
			}
			for i, v := range big {
				if v != float64(i) {
					t.Fatalf("value %d is %v, values are out of order", i, v)
				}
			}
			if others != tc.others {
				t.Errorf("expected %d other metrics, got %d", tc.others, others)
			}
		})
	}
}
