package emf

import (
	"bytes"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"
)

func TestNew(t *testing.T) {
	tcs := []struct {
		name     string
		opts     []LoggerOption
		expected *Logger
	}{
		{
			name: "default",
			expected: &Logger{
				out:       os.Stdout,
				timestamp: time.Now().UnixMilli(),
			},
		},
		{
			name: "with options",
			opts: []LoggerOption{
				WithWriter(os.Stderr),
				WithTimestamp(time.Now().Add(time.Hour)),
			},
			expected: &Logger{
				out:       os.Stderr,
				timestamp: time.Now().Add(time.Hour).UnixMilli(),
			},
		},
		{
			name: "without dimensions",
			opts: []LoggerOption{
				WithoutDimensions(),
			},
			expected: &Logger{
				out:               os.Stdout,
				timestamp:         time.Now().UnixMilli(),
				withoutDimensions: true,
			},
		},
		{
			name: "with merged default dimensions",
			opts: []LoggerOption{
				WithMergedDefaultDimensions(),
			},
			expected: &Logger{
				out:           os.Stdout,
				timestamp:     time.Now().UnixMilli(),
				mergeDefaults: true,
			},
		},
		{
			name: "with log group",
			opts: []LoggerOption{
				WithLogGroup("my-logs"),
			},
			expected: &Logger{
				out:          os.Stdout,
				timestamp:    time.Now().UnixMilli(),
				logGroupName: "my-logs",
			},
		},
	}

	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			actual := New(tc.opts...)
			if err := loggersEqual(actual, tc.expected); err != nil {
				t.Errorf("logger does not match: %v", err)
			}
		})
	}

}

// loggersEqual returns a non-nil error if the loggers do not match.
// It checks the fields set by LoggerOptions: writer, timestamp,
// withoutDimensions, mergeDefaults and log group.
func loggersEqual(actual, expected *Logger) error {
	if actual.out != expected.out {
		return fmt.Errorf("output does not match")
	}

	if actual.withoutDimensions != expected.withoutDimensions {
		return fmt.Errorf("withoutDimensions is %v, expected %v", actual.withoutDimensions, expected.withoutDimensions)
	}

	if actual.mergeDefaults != expected.mergeDefaults {
		return fmt.Errorf("mergeDefaults is %v, expected %v", actual.mergeDefaults, expected.mergeDefaults)
	}

	if actual.logGroupName != expected.logGroupName {
		return fmt.Errorf("logGroupName is %q, expected %q", actual.logGroupName, expected.logGroupName)
	}

	if err := approxInt64(actual.timestamp, expected.timestamp, 100 /* ms */); err != nil {
		return fmt.Errorf("timestamp %v", err)
	}

	return nil
}

func approxInt64(actual, expected, tolerance int64) error {
	diff := expected - actual
	if diff < 0 {
		diff = -diff
	}
	if diff > tolerance {
		return fmt.Errorf("value %v is out of tolerance %v±%v", actual, expected, tolerance)
	}
	return nil
}

// TestResetOwnDimensions checks the helper Flush calls while holding the
// registry lock: it drops each context's own dimensions, keeps the Lambda
// defaults for contexts that use them, doesn't bring them back for contexts
// that dropped them, and doesn't change the shared defaults.
func TestResetOwnDimensions(t *testing.T) {
	for _, merged := range []bool{false, true} {
		t.Run(fmt.Sprintf("merged %v", merged), func(t *testing.T) {
			t.Setenv("AWS_LAMBDA_FUNCTION_NAME", "fn")
			var buf bytes.Buffer
			opts := []LoggerOption{WithWriter(&buf)}
			if merged {
				opts = append(opts, WithMergedDefaultDimensions())
			}
			l := New(opts...)
			l.Dimension("Region", "au")
			kept := l.NewContext().Namespace("kept").Dimension("Service", "s")
			dropped := l.NewContext().Namespace("dropped").SetDimensions(false, []Dimension{{"Plan", "pro"}})
			defaults := l.defaults
			set := slices.Clone(defaults.set)

			l.registry.do(func() {
				for ctx := range l.allContexts() {
					ctx.resetOwnDimensions()
				}
			})

			for _, ctx := range []*Context{&l.defaultContext, kept, dropped} {
				if len(ctx.dimensions) != 0 || len(ctx.metricDirective.Dimensions) != 0 || len(ctx.written) != 0 {
					t.Errorf("%s: own dimensions kept: %+v", ctx.metricDirective.Namespace, ctx)
				}
			}
			for _, ctx := range []*Context{&l.defaultContext, kept} {
				if ctx.defaults != defaults {
					t.Errorf("%s: defaults not kept", ctx.metricDirective.Namespace)
				}
			}
			if dropped.defaults != nil {
				t.Error("dropped: defaults written again")
			}
			if l.defaults != defaults || !slices.Equal(defaults.set, set) {
				t.Error("shared defaults changed")
			}

			l.Metric("Orders", 1)
			kept.Metric("Kept", 1)
			dropped.Metric("Invoices", 1)
			l.Log()
			for _, want := range []string{
				`{"Namespace":"aws-embedded-metrics","Dimensions":[["ServiceName","ServiceType"]]`,
				`{"Namespace":"kept","Dimensions":[["ServiceName","ServiceType"]]`,
				`{"Namespace":"dropped","Dimensions":[[]]`,
			} {
				if !bytes.Contains(buf.Bytes(), []byte(want)) {
					t.Errorf("expected %s in output: %s", want, buf.Bytes())
				}
			}
		})
	}
}
