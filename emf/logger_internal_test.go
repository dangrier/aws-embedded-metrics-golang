package emf

import (
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

// TestResetDimensionsHelper checks the helper Flush calls while holding the
// registry lock drops a context's own dimensions and keeps the Lambda
// defaults, without changing the shared defaults.
func TestResetDimensionsHelper(t *testing.T) {
	t.Setenv("AWS_LAMBDA_FUNCTION_NAME", "fn")
	l := New(WithMergedDefaultDimensions())
	c := l.NewContext().Dimension("Service", "s")
	l.Dimension("Region", "au")
	defaults := l.defaults
	set := slices.Clone(defaults.set)

	l.registry.do(func() {
		for ctx := range l.allContexts() {
			ctx.resetDimensions(true)
		}
	})

	for _, ctx := range []*Context{&l.defaultContext, c} {
		if len(ctx.dimensions) != 0 || len(ctx.metricDirective.Dimensions) != 0 || len(ctx.written) != 0 {
			t.Errorf("own dimensions kept: %+v", ctx)
		}
		if ctx.defaults != defaults {
			t.Errorf("defaults not kept")
		}
		if got := ctx.dimensionSets(); !slices.EqualFunc(got, []DimensionSet{set}, slices.Equal) {
			t.Errorf("expected only the default set, got %v", got)
		}
	}
	if l.defaults != defaults || !slices.Equal(defaults.set, set) {
		t.Errorf("shared defaults changed")
	}
}
