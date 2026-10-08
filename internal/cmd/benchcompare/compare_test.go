package main

import (
	"fmt"
	"math"
	"strings"
	"testing"
)

var limits = map[string]float64{"sec/op": 20, "B/op": 10, "allocs/op": 0}

// bench returns benchmark output for one benchmark, one line per run.
func bench(name string, nsPerOp []float64, bytesPerOp, allocsPerOp int) string {
	var b strings.Builder
	for _, ns := range nsPerOp {
		fmt.Fprintf(&b, "Benchmark%s-8\t1000\t%g ns/op\t%d B/op\t%d allocs/op\n", name, ns, bytesPerOp, allocsPerOp)
	}
	return b.String()
}

func steady(ns float64) []float64 {
	return []float64{ns * 0.99, ns, ns * 1.01, ns * 0.995, ns * 1.005, ns, ns * 0.98, ns * 1.02, ns, ns * 1.01}
}

func TestParse(t *testing.T) {
	s, err := parse(strings.NewReader("goos: linux\n"+bench("Log/metrics=10", []float64{100, 110}, 64, 2)), "test")
	if err != nil {
		t.Fatal(err)
	}
	if got := s[key{"Log/metrics=10-8", "sec/op"}]; len(got) != 2 || math.Abs(got[0]-100e-9) > 1e-15 {
		t.Errorf("sec/op samples: got %v", got)
	}
	if got := s[key{"Log/metrics=10-8", "allocs/op"}]; len(got) != 2 || got[0] != 2 {
		t.Errorf("allocs/op samples: got %v", got)
	}
}

func TestRegressions(t *testing.T) {
	tcs := []struct {
		name     string
		base     string
		head     string
		expected []string // units that should regress
	}{
		{
			name: "no change",
			base: bench("Log", steady(1000), 1024, 20),
			head: bench("Log", steady(1000), 1024, 20),
		},
		{
			name:     "one more allocation",
			base:     bench("Log", steady(1000), 1024, 20),
			head:     bench("Log", steady(1000), 1024, 21),
			expected: []string{"allocs/op"},
		},
		{
			name:     "much slower",
			base:     bench("Log", steady(1000), 1024, 20),
			head:     bench("Log", steady(1500), 1024, 20),
			expected: []string{"sec/op"},
		},
		{
			name: "slightly slower, under the limit",
			base: bench("Log", steady(1000), 1024, 20),
			head: bench("Log", steady(1100), 1024, 20),
		},
		{
			// The median is 85% slower, but too noisy to be significant.
			name: "slower but too noisy to be significant",
			base: bench("Log", []float64{500, 1500, 700, 1300, 1000, 900, 1100, 600, 1400, 1000}, 1024, 20),
			head: bench("Log", []float64{2600, 400, 2500, 600, 1900, 2700, 500, 2300, 700, 1800}, 1024, 20),
		},
		{
			name:     "more memory",
			base:     bench("Log", steady(1000), 1000, 20),
			head:     bench("Log", steady(1000), 1200, 20),
			expected: []string{"B/op"},
		},
		{
			name: "faster and smaller",
			base: bench("Log", steady(1000), 1024, 20),
			head: bench("Log", steady(500), 512, 10),
		},
		{
			name: "new benchmark has no base",
			base: bench("Log", steady(1000), 1024, 20),
			head: bench("Log", steady(1000), 1024, 20) + bench("New", steady(9000), 1024, 99),
		},
	}

	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			base, err := parse(strings.NewReader(tc.base), "base")
			if err != nil {
				t.Fatal(err)
			}
			head, err := parse(strings.NewReader(tc.head), "head")
			if err != nil {
				t.Fatal(err)
			}

			got := regressions(compare(base, head), limits)
			if len(got) != len(tc.expected) {
				t.Fatalf("expected regressions in %v, got %v", tc.expected, got)
			}
			for i, unit := range tc.expected {
				if !strings.Contains(got[i], " "+unit+" ") {
					t.Errorf("expected a %s regression, got %q", unit, got[i])
				}
			}
		})
	}
}
