package emf_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/dangrier/aws-embedded-metrics-golang/emf"
)

// TestResetDimensions checks what ResetDimensions and SetDimensions report,
// and what they leave in the output: which names are free again, which
// properties are written again, and how sets passed to SetDimensions are
// checked.
func TestResetDimensions(t *testing.T) {
	dims := func(kv ...string) []emf.Dimension {
		var d []emf.Dimension
		for i := 0; i < len(kv); i += 2 {
			d = append(d, emf.NewDimension(kv[i], kv[i+1]))
		}
		return d
	}
	tcs := []struct {
		name     string
		lambda   bool
		given    func(*emf.Logger)
		reported []string // a part of each error, in order
		want     []string // in the output
		notWant  []string // not in the output
	}{
		{
			name:  "removed dimension name used by a metric",
			given: func(l *emf.Logger) { l.Dimension("dup", "v").ResetDimensions(true).Metric("dup", 7) },
			want:  []string{`"dup":7`, `"Dimensions":[[]]`},
		},
		{
			name:  "removed dimension name used by a property",
			given: func(l *emf.Logger) { l.Dimension("dup", "v").ResetDimensions(true).Property("dup", "p") },
			want:  []string{`"dup":"p"`},
		},
		{
			name: "name still a dimension of another context",
			given: func(l *emf.Logger) {
				l.NewContext().Dimension("dup", "w").Metric("ctx", 1)
				l.Dimension("dup", "v").ResetDimensions(true).Metric("dup", 7).Property("dup", "p")
			},
			reported: []string{`metric "dup": name is already a dimension`, `property "dup": key is already a dimension`},
			want:     []string{`"dup":"w"`},
		},
		{
			name: "property replaced by a removed dimension is written again",
			given: func(l *emf.Logger) {
				l.Property("dup", "p").Dimension("dup", "v").ResetDimensions(true)
			},
			reported: []string{`dimension "dup" replaces the property`},
			want:     []string{`"dup":"p"`},
		},
		{
			name: "property replaced again after a reset is reported again",
			given: func(l *emf.Logger) {
				l.Property("dup", "p").Dimension("dup", "v").ResetDimensions(true).Dimension("dup", "w")
			},
			reported: []string{`dimension "dup" replaces the property`, `dimension "dup" replaces the property`},
			want:     []string{`"dup":"w"`},
		},
		{
			name: "property still replaced in another context",
			given: func(l *emf.Logger) {
				l.Property("dup", "p").Dimension("dup", "v")
				l.NewContext().Dimension("dup", "v").Metric("ctx", 1)
				l.ResetDimensions(true)
			},
			reported: []string{`dimension "dup" replaces the property`},
			want:     []string{`"dup":"v"`},
			notWant:  []string{`"dup":"p"`},
		},
		{
			name: "property skipped as a dimension isn't restored",
			given: func(l *emf.Logger) {
				l.Dimension("dup", "v").Property("dup", "p").ResetDimensions(true)
			},
			reported: []string{`property "dup": key is already a dimension`},
			notWant:  []string{`"dup"`},
		},
		{
			name: "SetDimensions keeping a key that replaced a property",
			given: func(l *emf.Logger) {
				l.Property("dup", "p").Dimension("dup", "v").SetDimensions(true, dims("dup", "w", "a", "b"))
			},
			reported: []string{`dimension "dup" replaces the property`},
			want:     []string{`"dup":"w"`, `"Dimensions":[["dup","a"]]`},
		},
		{
			name: "SetDimensions with a new key that replaces a property",
			given: func(l *emf.Logger) {
				l.Property("dup", "p").Dimension("a", "b").SetDimensions(true, dims("dup", "v"), dims("dup", "v", "a", "b"))
			},
			reported: []string{`dimension "dup" replaces the property`},
			want:     []string{`"dup":"v"`, `"Dimensions":[["dup"],["dup","a"]]`},
		},
		{
			name: "SetDimensions skips an invalid set and keeps the others",
			given: func(l *emf.Logger) {
				l.SetDimensions(true, dims("a", "b"), dims("c", "d", ":bad", "v"), dims("e", "f"))
			},
			reported: []string{`skipped dimension set: key ":bad"`},
			want:     []string{`"Dimensions":[["a"],["e"]]`, `"a":"b"`, `"e":"f"`},
			notWant:  []string{`"c"`},
		},
		{
			name:     "SetDimensions skips a set with a metric's name",
			given:    func(l *emf.Logger) { l.Metric("m", 1).SetDimensions(true, dims("m", "v"), dims("a", "b")) },
			reported: []string{`key "m" is already a metric`},
			want:     []string{`"Dimensions":[["a"]]`, `"m":1`},
		},
		{
			name: "SetDimensions removes repeated sets and keys",
			given: func(l *emf.Logger) {
				l.SetDimensions(true, dims("a", "1", "b", "2", "a", "3"), dims("b", "4", "a", "5"), dims("c", "6"))
			},
			want: []string{`"Dimensions":[["a","b"],["c"]]`, `"a":"5"`, `"b":"4"`},
		},
		{
			name: "SetDimensions with an empty set",
			given: func(l *emf.Logger) {
				l.Dimension("a", "b").SetDimensions(true, nil)
			},
			want:    []string{`"Dimensions":[[]]`},
			notWant: []string{`"a"`},
		},
		{
			name:   "Lambda default names stay reserved after dropping the defaults",
			lambda: true,
			given: func(l *emf.Logger) {
				l.ResetDimensions(false).Metric("ServiceName", 1).Property("ServiceType", "p")
				l.NewContext().SetDimensions(false).Metric("ServiceType", 2)
			},
			reported: []string{
				`metric "ServiceName": name is already a dimension`,
				`property "ServiceType": key is already a dimension`,
				`metric "ServiceType": name is already a dimension`,
			},
			notWant: []string{`"ServiceName"`, `"ServiceType"`},
		},
		{
			name:   "Lambda default names can still be your own dimensions",
			lambda: true,
			given: func(l *emf.Logger) {
				l.SetDimensions(false, dims("ServiceName", "mine"))
			},
			want:    []string{`"ServiceName":"mine"`, `"Dimensions":[["ServiceName"]]`},
			notWant: []string{`"ServiceType"`},
		},
		{
			name:   "defaults written again after being dropped",
			lambda: true,
			given: func(l *emf.Logger) {
				l.SetDimensions(false, dims("a", "b")).ResetDimensions(true).Dimension("c", "d")
			},
			want:    []string{`"Dimensions":[["ServiceName","ServiceType"],["c"]]`, `"ServiceName":"fn"`},
			notWant: []string{`"a"`},
		},
		{
			name:   "Lambda properties kept after dropping the defaults",
			lambda: true,
			given:  func(l *emf.Logger) { l.ResetDimensions(false) },
			want:   []string{`"functionVersion":"1"`, `"Dimensions":[[]]`},
		},
	}

	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			if tc.lambda {
				t.Setenv("AWS_LAMBDA_FUNCTION_NAME", "fn")
				t.Setenv("AWS_LAMBDA_FUNCTION_VERSION", "1")
			}
			var buf bytes.Buffer
			var errs []error
			opts := []emf.LoggerOption{emf.WithWriter(&buf), emf.WithErrorHandler(func(err error) {
				errs = append(errs, err)
			})}
			if !tc.lambda {
				opts = append(opts, emf.WithoutDimensions())
			}
			logger := emf.New(opts...)
			tc.given(logger)
			logger.Metric("good", 1).Log()

			if len(errs) != len(tc.reported) {
				t.Fatalf("expected %d errors, got %d: %v", len(tc.reported), len(errs), errs)
			}
			for i, err := range errs {
				if !errors.Is(err, emf.ErrInvalid) || !strings.Contains(err.Error(), tc.reported[i]) {
					t.Errorf("error %d: expected ErrInvalid containing %q, got %v", i, tc.reported[i], err)
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
			for _, notWant := range tc.notWant {
				if bytes.Contains(buf.Bytes(), []byte(notWant)) {
					t.Errorf("expected no %s in output: %s", notWant, buf.Bytes())
				}
			}
		})
	}
}

// TestSetDimensionsLimit checks the 30-key limit of sets passed to
// SetDimensions counts the merged Lambda defaults only when they are kept.
func TestSetDimensionsLimit(t *testing.T) {
	keys := func(n int) []emf.Dimension {
		var dims []emf.Dimension
		for i := range n {
			dims = append(dims, emf.NewDimension(fmt.Sprintf("d%d", i), "v"))
		}
		return dims
	}
	tcs := []struct {
		name       string
		merged     bool
		useDefault bool
		keys       int
		reported   string
	}{
		{name: "28 keys merged", merged: true, useDefault: true, keys: 28},
		{name: "29 keys merged", merged: true, useDefault: true, keys: 29, reported: "default dimensions merged"},
		{name: "30 keys merged, defaults dropped", merged: true, keys: 30},
		{name: "31 keys merged, defaults dropped", merged: true, keys: 31, reported: "more than 30 keys"},
		{name: "30 keys not merged", useDefault: true, keys: 30},
		{name: "31 keys not merged", useDefault: true, keys: 31, reported: "more than 30 keys"},
	}

	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("AWS_LAMBDA_FUNCTION_NAME", "fn")
			var buf bytes.Buffer
			var errs []error
			opts := []emf.LoggerOption{emf.WithWriter(&buf), emf.WithErrorHandler(func(err error) {
				errs = append(errs, err)
			})}
			if tc.merged {
				opts = append(opts, emf.WithMergedDefaultDimensions())
			}
			logger := emf.New(opts...)
			logger.SetDimensions(tc.useDefault, keys(tc.keys), []emf.Dimension{emf.NewDimension("Service", "s")}).Metric("good", 1)
			logger.NewContext().Namespace("other").SetDimensions(tc.useDefault, keys(tc.keys)).Metric("other", 1)
			logger.Log()

			assertCompliant(t, buf.Bytes())
			if !bytes.Contains(buf.Bytes(), []byte(`"Service":"s"`)) {
				t.Errorf("valid set missing from output: %s", buf.Bytes())
			}
			added := bytes.Contains(buf.Bytes(), []byte(`"d0"`))
			if tc.reported == "" {
				if len(errs) > 0 || !added {
					t.Errorf("expected the set added without errors, got %v\n%s", errs, buf.Bytes())
				}
				return
			}
			if len(errs) != 2 || added {
				t.Fatalf("expected the set skipped and reported in both contexts, got %v\n%s", errs, buf.Bytes())
			}
			for _, err := range errs {
				if !errors.Is(err, emf.ErrInvalid) || !strings.Contains(err.Error(), tc.reported) {
					t.Errorf("expected ErrInvalid containing %q, got %v", tc.reported, err)
				}
			}
		})
	}
}

// TestConcurrentResetDimensions resets and replaces dimensions from many
// goroutines at once, while other goroutines add dimensions and log. Every
// line must stay spec compliant, and each context's metrics must be written
// with its own Worker value or none. Run with -race (just test-race) to catch
// data races, and a snapshot that changes after the lock is released.
func TestConcurrentResetDimensions(t *testing.T) {
	for _, merged := range []bool{false, true} {
		t.Run(fmt.Sprintf("merged %v", merged), func(t *testing.T) {
			t.Setenv("AWS_LAMBDA_FUNCTION_NAME", "fn")
			var out syncBuffer
			opts := []emf.LoggerOption{emf.WithWriter(&out)}
			if merged {
				opts = append(opts, emf.WithMergedDefaultDimensions())
			}
			logger := emf.New(opts...).Dimension("Worker", "default").Metric("m", 1)

			const (
				goroutines = 8
				iterations = 50
			)
			var wg sync.WaitGroup
			for g := range goroutines {
				wg.Go(func() {
					worker := fmt.Sprintf("w%d", g)
					ctx := logger.NewContext().Namespace(worker)
					for i := range iterations {
						ctx.Metric(fmt.Sprintf("%s_m%d", worker, i), i)
						switch i % 4 {
						case 0:
							ctx.ResetDimensions(i%8 == 0).Dimension("Worker", worker)
						case 1:
							ctx.SetDimensions(i%3 == 0,
								[]emf.Dimension{emf.NewDimension("Worker", worker)},
								[]emf.Dimension{emf.NewDimension("Worker", worker), emf.NewDimension("Step", fmt.Sprint(i))})
						case 2:
							ctx.ResetDimensions(i%3 == 0)
						case 3:
							ctx.DimensionSet(emf.NewDimension("Worker", worker), emf.NewDimension("Other", "x"))
						}
						if g%2 == 0 {
							logger.ResetDimensions(i%2 == 0).Dimension("Worker", "default")
						}
						if i%5 == 0 {
							logger.Log()
						}
					}
				})
			}
			wg.Wait()
			logger.Log()

			lines := 0
			for line := range bytes.Lines(out.buf.Bytes()) {
				lines++
				assertCompliant(t, line)
				var doc struct {
					Worker string
					AWS    struct {
						CloudWatchMetrics []struct {
							Namespace  string
							Dimensions [][]string
						}
					} `json:"_aws"`
				}
				if err := json.Unmarshal(line, &doc); err != nil {
					t.Fatal(err)
				}
				for _, d := range doc.AWS.CloudWatchMetrics {
					uses := slices.ContainsFunc(d.Dimensions, func(set []string) bool { return slices.Contains(set, "Worker") })
					want := d.Namespace
					if want == "aws-embedded-metrics" {
						want = "default"
					}
					if uses && doc.Worker != want {
						t.Errorf("directive for %s on a line with Worker %s", d.Namespace, doc.Worker)
					}
				}
			}
			if lines == 0 {
				t.Error("expected output")
			}
		})
	}
}
