package main

import (
	"fmt"
	"io"
	"math"
	"slices"
	"strings"

	"golang.org/x/perf/benchfmt"
	"golang.org/x/perf/benchmath"
)

// key identifies one measurement: a benchmark and a unit.
type key struct {
	name, unit string
}

// samples holds every measured value for each benchmark and unit.
type samples map[key][]float64

// parse reads Go benchmark output.
func parse(r io.Reader, fileName string) (samples, error) {
	s := samples{}
	reader := benchfmt.NewReader(r, fileName)
	for reader.Scan() {
		switch rec := reader.Result().(type) {
		case *benchfmt.SyntaxError:
			return nil, rec
		case *benchfmt.Result:
			for _, v := range rec.Values {
				k := key{string(rec.Name.Full()), v.Unit}
				s[k] = append(s[k], v.Value)
			}
		}
	}
	return s, reader.Err()
}

// row is the comparison of one benchmark and unit.
type row struct {
	key
	base, head  float64 // medians
	change      float64 // percent change from base to head
	p           float64
	significant bool
}

// compare compares every benchmark and unit found in both base and head,
// using the same test as benchstat (Mann-Whitney U, no assumptions about
// the distribution).
func compare(base, head samples) []row {
	var rows []row
	for k, headValues := range head {
		baseValues, ok := base[k]
		if !ok {
			continue
		}
		b := benchmath.NewSample(baseValues, &benchmath.DefaultThresholds)
		h := benchmath.NewSample(headValues, &benchmath.DefaultThresholds)
		c := benchmath.AssumeNothing.Compare(b, h)

		r := row{
			key:         k,
			base:        benchmath.AssumeNothing.Summary(b, 0.95).Center,
			head:        benchmath.AssumeNothing.Summary(h, 0.95).Center,
			p:           c.P,
			significant: c.P < c.Alpha,
		}
		switch r.base {
		case r.head:
			r.change = 0
		case 0:
			r.change = math.Inf(1)
		default:
			r.change = (r.head - r.base) / r.base * 100
		}
		rows = append(rows, r)
	}

	slices.SortFunc(rows, func(a, b row) int {
		if c := strings.Compare(a.unit, b.unit); c != 0 {
			return c
		}
		return strings.Compare(a.name, b.name)
	})
	return rows
}

// regressions returns a description of every significant increase over
// the limit for its unit. Units without a limit are not checked.
func regressions(rows []row, limits map[string]float64) []string {
	var out []string
	for _, r := range rows {
		limit, ok := limits[r.unit]
		if ok && r.significant && r.change > limit {
			out = append(out, fmt.Sprintf("%s: %s %+.2f%% (limit +%g%%)", r.name, r.unit, r.change, limit))
		}
	}
	return out
}

// formatChange shows the change, or "~" when it is not significant.
func formatChange(r row) string {
	if !r.significant {
		return "~"
	}
	return fmt.Sprintf("%+.2f%%", r.change)
}
