package emf_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/dangrier/aws-embedded-metrics-golang/emf"
)

// flushedLine is the part of a log line the flush tests look at.
type flushedLine struct {
	doc       map[string]any
	timestamp int64
}

// flushedLines checks every line is spec compliant and decodes it.
func flushedLines(t *testing.T, out []byte) []flushedLine {
	t.Helper()
	var lines []flushedLine
	for line := range bytes.Lines(out) {
		assertCompliant(t, line)
		var doc map[string]any
		dec := json.NewDecoder(bytes.NewReader(line))
		dec.UseNumber()
		if err := dec.Decode(&doc); err != nil {
			t.Fatal(err)
		}
		ts, err := doc["_aws"].(map[string]any)["Timestamp"].(json.Number).Int64()
		if err != nil {
			t.Fatal(err)
		}
		lines = append(lines, flushedLine{doc, ts})
	}
	return lines
}

// TestFlushReusesLogger checks the case from #31: a metric logged once
// before each of two Flush calls is written as 1 both times, not as [1,1]
// the second time.
func TestFlushReusesLogger(t *testing.T) {
	var buf bytes.Buffer
	var errs []error
	logger := emf.New(emf.WithWriter(&buf), emf.WithoutDimensions(),
		emf.WithErrorHandler(func(err error) { errs = append(errs, err) }))
	for range 3 {
		logger.Metric("Requests", 1)
		logger.Flush()
	}
	lines := flushedLines(t, buf.Bytes())
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d: %s", len(lines), buf.Bytes())
	}
	for _, line := range lines {
		if got := line.doc["Requests"]; got != json.Number("1") {
			t.Errorf("expected Requests 1, got %v", got)
		}
	}
	if len(errs) != 0 {
		t.Errorf("expected no errors, got %v", errs)
	}

	// Log after a flush has nothing to write until a value is added.
	buf.Reset()
	logger.Log()
	if buf.Len() != 0 {
		t.Errorf("expected Log after Flush to write nothing, got %s", buf.Bytes())
	}
}

// TestFlushSplitValues checks a metric with more than 100 values is split
// as Log splits it, and none of its values are written again by the next
// flush.
func TestFlushSplitValues(t *testing.T) {
	var buf bytes.Buffer
	logger := emf.New(emf.WithWriter(&buf), emf.WithoutDimensions())
	values := make([]float64, 250)
	for i := range values {
		values[i] = float64(i)
	}
	logger.PutValues("Latency", values).Flush()
	if lines := flushedLines(t, buf.Bytes()); len(lines) != 3 {
		t.Errorf("expected 3 lines, got %d", len(lines))
	}

	buf.Reset()
	logger.PutValues("Latency", values[:2]).Flush()
	lines := flushedLines(t, buf.Bytes())
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}
	if got := fmt.Sprint(lines[0].doc["Latency"]); got != "[0 1]" {
		t.Errorf("expected Latency [0 1], got %s", got)
	}
}

// TestFlushWithoutMetrics checks Flush with no metrics writes nothing and
// reports nothing, as Log does, but still ends the unit of work: the
// properties are cleared, and the dimensions with
// WithResetDimensionsOnFlush.
func TestFlushWithoutMetrics(t *testing.T) {
	var buf bytes.Buffer
	var errs []error
	logger := emf.New(emf.WithWriter(&buf), emf.WithoutDimensions(), emf.WithResetDimensionsOnFlush(),
		emf.WithTimestamp(time.Now().Add(-15*24*time.Hour)),
		emf.WithErrorHandler(func(err error) { errs = append(errs, err) }))
	logger.Property("requestId", "a").Dimension("Region", "au").Flush()
	logger.NewContext().Namespace("empty")
	logger.Flush()
	if buf.Len() != 0 {
		t.Errorf("expected no output, got %s", buf.Bytes())
	}
	if len(errs) != 0 {
		t.Errorf("expected no errors, got %v", errs)
	}

	logger.SetTimestamp(time.Now()).Metric("Requests", 1).Flush()
	lines := flushedLines(t, buf.Bytes())
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}
	for _, key := range []string{"requestId", "Region"} {
		if _, ok := lines[0].doc[key]; ok {
			t.Errorf("expected %s to be cleared by the empty flush: %s", key, buf.Bytes())
		}
	}
}

// TestFlushTimestamp checks each Flush writes the time it is called, unless
// a timestamp was set with WithTimestamp or SetTimestamp, which is kept
// across flushes. Log still writes the time of New. The clock is fake, so
// the times are exact.
func TestFlushTimestamp(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var buf bytes.Buffer
		created := time.Now()
		logger := emf.New(emf.WithWriter(&buf), emf.WithoutDimensions())

		time.Sleep(time.Hour)
		logger.Metric("m", 1).Flush()
		time.Sleep(time.Hour)
		logger.Metric("m", 1).Flush()
		logger.Metric("m", 1).Log()
		set := time.Now().Add(-time.Minute)
		logger.SetTimestamp(set)
		time.Sleep(time.Hour)
		logger.Flush()
		logger.Metric("m", 1).Flush()
		logger.Metric("m", 1).Log()

		want := []time.Time{
			created.Add(time.Hour),
			created.Add(2 * time.Hour),
			created, // Log writes the time of New
			set,     // SetTimestamp applies to the values already added
			set,     // and is kept across flushes
			set,     // and is written by Log
		}
		lines := flushedLines(t, buf.Bytes())
		if len(lines) != len(want) {
			t.Fatalf("expected %d lines, got %d: %s", len(want), len(lines), buf.Bytes())
		}
		for i, line := range lines {
			if line.timestamp != want[i].UnixMilli() {
				t.Errorf("line %d: expected timestamp %v, got %v", i, want[i], time.UnixMilli(line.timestamp))
			}
		}
	})

	t.Run("WithTimestamp is kept across flushes", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var buf bytes.Buffer
			set := time.Now().Add(-time.Hour)
			logger := emf.New(emf.WithWriter(&buf), emf.WithoutDimensions(), emf.WithTimestamp(set))
			for range 3 {
				time.Sleep(time.Hour)
				logger.Metric("m", 1).Flush()
			}
			// SetTimestamp replaces it.
			logger.SetTimestamp(set.Add(time.Minute)).Metric("m", 1).Flush()

			lines := flushedLines(t, buf.Bytes())
			if len(lines) != 4 {
				t.Fatalf("expected 4 lines, got %d", len(lines))
			}
			for i, line := range lines {
				want := set
				if i == 3 {
					want = set.Add(time.Minute)
				}
				if line.timestamp != want.UnixMilli() {
					t.Errorf("line %d: expected timestamp %v, got %v", i, want, time.UnixMilli(line.timestamp))
				}
			}
		})
	})
}

// TestSetTimestampWindow checks a timestamp set with SetTimestamp outside
// the window CloudWatch accepts is reported when a line is written, not
// when it is set, once per Flush or Log, and the line is still written.
func TestSetTimestampWindow(t *testing.T) {
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
			logger := emf.New(emf.WithWriter(&buf), emf.WithoutDimensions(),
				emf.WithErrorHandler(func(err error) { errs = append(errs, err) }))
			logger.SetTimestamp(time.Now().Add(tc.offset))
			if len(errs) != 0 {
				t.Fatalf("expected no errors when the timestamp is set, got %v", errs)
			}

			// 150 metrics are written on two lines, but reported once.
			for i := range 150 {
				logger.Metric(fmt.Sprintf("m%d", i), i)
			}
			logger.Flush()
			logger.Metric("m", 1).Log()

			if n := bytes.Count(buf.Bytes(), []byte("\n")); n != 3 {
				t.Errorf("expected 3 lines, got %d", n)
			}
			if !tc.reported {
				if len(errs) != 0 {
					t.Errorf("expected no errors, got %v", errs)
				}
				flushedLines(t, buf.Bytes())
				return
			}
			if len(errs) != 2 {
				t.Fatalf("expected 1 error for Flush and 1 for Log, got %v", errs)
			}
			for _, err := range errs {
				if !errors.Is(err, emf.ErrInvalid) || !strings.Contains(err.Error(), "timestamp") {
					t.Errorf("expected a timestamp error that wraps ErrInvalid, got %v", err)
				}
			}
		})
	}
}

// TestFlushProperties checks Flush clears properties set with Property and
// PropertyValue, writes the Lambda properties on every line, and doesn't
// report a dimension or metric as replacing a property that a flush
// cleared (#47).
func TestFlushProperties(t *testing.T) {
	for k, v := range lambdaEnv {
		t.Setenv(k, v)
	}
	t.Setenv("_X_AMZN_TRACE_ID", "Root=1-5759e988-bd862e3fe1be46a994272793;Sampled=1")
	var buf bytes.Buffer
	var errs []error
	logger := emf.New(emf.WithWriter(&buf), emf.WithErrorHandler(func(err error) { errs = append(errs, err) }))

	// A metric and a dimension that replace properties are reported.
	logger.Property("Orders", "p").PropertyValue("Region", 1).
		Metric("Orders", 1).Dimension("Region", "au").
		MetricAs("memorySize", 256, emf.Megabytes) // replaces a Lambda property, not reported
	logger.Flush()
	if len(errs) != 2 {
		t.Fatalf("expected 2 errors, got %v", errs)
	}

	// After the flush, the properties are gone, so nothing is replaced.
	errs = nil
	logger.Metric("Orders", 2).Dimension("Region", "eu").Metric("Requests", 1)
	logger.Flush()
	if len(errs) != 0 {
		t.Errorf("expected no errors after the flush, got %v", errs)
	}

	lines := flushedLines(t, buf.Bytes())
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(lines))
	}
	for i, line := range lines {
		for _, key := range []string{"executionEnvironment", "functionVersion", "logStreamId", "traceId"} {
			if _, ok := line.doc[key]; !ok {
				t.Errorf("line %d: Lambda property %s missing", i, key)
			}
		}
	}
	if got := lines[1].doc["memorySize"]; got != "128" {
		t.Errorf("expected the memorySize property to be written again, got %v", got)
	}
	if got := lines[1].doc["Orders"]; got != json.Number("2") {
		t.Errorf("expected Orders 2, got %v", got)
	}
}

// TestFlushDimensions checks how Flush treats dimensions added with
// Dimension and SetDimensions, with and without
// WithResetDimensionsOnFlush, and that a context's choice about the Lambda
// defaults is kept either way.
func TestFlushDimensions(t *testing.T) {
	for _, reset := range []bool{false, true} {
		for _, merged := range []bool{false, true} {
			t.Run(fmt.Sprintf("reset %v merged %v", reset, merged), func(t *testing.T) {
				t.Setenv("AWS_LAMBDA_FUNCTION_NAME", "fn")
				var buf bytes.Buffer
				opts := []emf.LoggerOption{emf.WithWriter(&buf)}
				if reset {
					opts = append(opts, emf.WithResetDimensionsOnFlush())
				}
				if merged {
					opts = append(opts, emf.WithMergedDefaultDimensions())
				}
				logger := emf.New(opts...).Namespace("app")
				logger.Dimension("Region", "au")
				set := logger.NewContext().Namespace("set").
					SetDimensions(false, []emf.Dimension{emf.NewDimension("Plan", "pro")})
				kept := logger.NewContext().Namespace("kept").
					SetDimensions(true, []emf.Dimension{emf.NewDimension("Tier", "gold")})

				var units [2][]byte
				for i := range units {
					logger.Metric("Orders", 1)
					set.Metric("Invoices", 1)
					kept.Metric("Refunds", 1)
					logger.Flush()
					units[i] = bytes.Clone(buf.Bytes())
					buf.Reset()
					flushedLines(t, units[i])
				}

				defaults, region, plan, tier := `["ServiceName","ServiceType"]`, `["Region"]`, `["Plan"]`, `["Tier"]`
				if merged {
					region, tier = `["ServiceName","ServiceType","Region"]`, `["ServiceName","ServiceType","Tier"]`
				}
				first := []string{
					`"Namespace":"app","Dimensions":[` + map[bool]string{false: defaults + ",", true: ""}[merged] + region + `]`,
					`"Namespace":"set","Dimensions":[` + plan + `]`,
					`"Namespace":"kept","Dimensions":[` + map[bool]string{false: defaults + ",", true: ""}[merged] + tier + `]`,
				}
				second := first
				if reset {
					second = []string{
						`"Namespace":"app","Dimensions":[` + defaults + `]`,
						`"Namespace":"set","Dimensions":[[]]`,
						`"Namespace":"kept","Dimensions":[` + defaults + `]`,
					}
				}
				for i, want := range [2][]string{first, second} {
					for _, w := range want {
						if !bytes.Contains(units[i], []byte(w)) {
							t.Errorf("flush %d: expected %s in %s", i+1, w, units[i])
						}
					}
				}
				if reset {
					for _, key := range []string{"Region", "Plan", "Tier"} {
						if bytes.Contains(units[1], []byte(`"`+key+`":`)) {
							t.Errorf("expected %s to be removed by the flush: %s", key, units[1])
						}
					}
				}
			})
		}
	}
}

// TestFlushWriteError checks a failed write during Flush is reported, and
// the values are still cleared.
func TestFlushWriteError(t *testing.T) {
	var errs []error
	logger := emf.New(emf.WithWriter(failingWriter{}), emf.WithoutDimensions(),
		emf.WithErrorHandler(func(err error) { errs = append(errs, err) }))
	logger.Metric("Requests", 1).Flush()
	if len(errs) != 1 || errors.Is(errs[0], emf.ErrInvalid) {
		t.Fatalf("expected 1 write error, got %v", errs)
	}

	var buf bytes.Buffer
	logger2 := emf.New(emf.WithWriter(&buf), emf.WithoutDimensions())
	logger2.Metric("Requests", 1).Flush()
	logger2.Metric("Requests", 1).Flush()
	if bytes.Contains(buf.Bytes(), []byte("[1,1]")) {
		t.Errorf("values written twice: %s", buf.Bytes())
	}
}

// TestConcurrentFlush adds values from many goroutines while others flush.
// Every value must be written exactly once, across all the flushed lines,
// including the values of a metric every context shares.
// Run with -race (just test-race) to catch data races.
func TestConcurrentFlush(t *testing.T) {
	var out syncBuffer
	logger := emf.New(emf.WithWriter(&out), emf.WithoutDimensions(), emf.WithResetDimensionsOnFlush())

	const (
		goroutines = 8
		iterations = 50
	)
	var wg sync.WaitGroup
	for g := range goroutines {
		wg.Go(func() {
			ctx := logger.NewContext().Namespace(fmt.Sprintf("ns%d", g))
			for i := range iterations {
				logger.Metric("Requests", 1).
					Property(fmt.Sprintf("g%d", g), "x").
					Dimension("Worker", fmt.Sprintf("w%d", g))
				// A key of its own, so the contexts never disagree and share
				// every line.
				ctx.Put("Latency", 1).Dimension(fmt.Sprintf("Worker%d", g), "w")
				if i%7 == 0 {
					logger.Flush()
				}
				if i%11 == 0 {
					logger.SetTimestamp(time.Now())
				}
			}
		})
	}
	wg.Wait()
	logger.Flush()

	total := map[string]int{}
	for _, line := range flushedLines(t, out.buf.Bytes()) {
		for _, name := range []string{"Requests", "Latency"} {
			switch v := line.doc[name].(type) {
			case json.Number:
				total[name]++
			case []any:
				total[name] += len(v)
			}
		}
	}
	for _, name := range []string{"Requests", "Latency"} {
		if want := goroutines * iterations; total[name] != want {
			t.Errorf("expected %d %s values, got %d", want, name, total[name])
		}
	}
}

// TestConcurrentFlushAndLog flushes and logs from many goroutines at once,
// while values, properties, dimensions and timestamps are changed. Every
// line must be spec compliant. Run with -race (just test-race) to catch
// data races.
func TestConcurrentFlushAndLog(t *testing.T) {
	t.Setenv("AWS_LAMBDA_FUNCTION_NAME", "fn")
	var out syncBuffer
	logger := emf.New(emf.WithWriter(&out), emf.WithMergedDefaultDimensions(), emf.WithResetDimensionsOnFlush())

	const (
		goroutines = 8
		iterations = 25
	)
	var wg sync.WaitGroup
	for g := range goroutines {
		wg.Go(func() {
			ctx := logger.NewContext().Namespace(fmt.Sprintf("ns%d", g))
			for i := range iterations {
				name := fmt.Sprintf("g%d_m%d", g, i%3)
				logger.Metric(name, i).
					PropertyValue(fmt.Sprintf("g%d_p", g), i).
					Dimension(fmt.Sprintf("g%d_d", g), "v")
				ctx.Put(name+"_ctx", float64(i)).SetDimensions(i%2 == 0, []emf.Dimension{emf.NewDimension("Worker", "w")})
				switch i % 4 {
				case 0:
					logger.Flush()
				case 1:
					logger.Log()
				case 2:
					logger.SetTimestamp(time.Now())
				case 3:
					logger.ResetDimensions(true)
				}
			}
		})
	}
	wg.Wait()
	logger.Flush()

	if len(flushedLines(t, out.buf.Bytes())) == 0 {
		t.Error("expected output")
	}
}

// TestFlushReadsTraceID checks each Flush writes the X-Ray trace of the
// invocation it ends, since Lambda sets it for each invocation, and leaves
// it out when that trace isn't sampled. Log doesn't read it, so it writes
// the trace read by New or the last Flush.
func TestFlushReadsTraceID(t *testing.T) {
	t.Setenv("_X_AMZN_TRACE_ID", "Root=1-first;Sampled=1")
	var buf bytes.Buffer
	logger := emf.New(emf.WithWriter(&buf), emf.WithoutDimensions())

	for _, trace := range []string{"Root=1-second;Sampled=1", "Root=1-third;Sampled=0", "Root=1-fourth;Sampled=1"} {
		t.Setenv("_X_AMZN_TRACE_ID", trace)
		logger.Metric("Requests", 1).Flush()
	}
	t.Setenv("_X_AMZN_TRACE_ID", "Root=1-fifth;Sampled=1")
	logger.Metric("Requests", 1).Log()

	want := []any{"Root=1-second;Sampled=1", nil, "Root=1-fourth;Sampled=1", "Root=1-fourth;Sampled=1"}
	lines := flushedLines(t, buf.Bytes())
	if len(lines) != len(want) {
		t.Fatalf("expected %d lines, got %d", len(want), len(lines))
	}
	for i, line := range lines {
		if got := line.doc["traceId"]; got != want[i] {
			t.Errorf("line %d: expected traceId %v, got %v", i, want[i], got)
		}
	}
}
