// Command benchcompare compares benchmarks between a git ref and the
// working tree, and fails on a significant performance regression.
//
// It checks out the ref in a temporary git worktree, then runs the
// benchmarks for both in alternating rounds, so machine noise affects both
// equally. Results are compared with the same statistics as benchstat.
// Only significant changes count: any increase in allocations, or memory
// or time over their limits.
//
// Run it from the repository with:
//
//	go tool benchcompare [-base main] [-rounds 10]
//
// It exits with status 1 on a regression and 2 on any other error.
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/tabwriter"
)

var errRegression = errors.New("performance regression")

func main() {
	err := run(os.Args[1:], os.Stdout, os.Stderr)
	switch {
	case errors.Is(err, errRegression):
		os.Exit(1)
	case err != nil:
		fmt.Fprintln(os.Stderr, "benchcompare:", err)
		os.Exit(2)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("benchcompare", flag.ContinueOnError)
	flags.SetOutput(stderr)
	base := flags.String("base", "main", "git ref to compare against")
	rounds := flags.Int("rounds", 10, "number of times to run the benchmarks for each side")
	benchtime := flags.String("benchtime", "200ms", "go test -benchtime for each run")
	pkg := flags.String("pkg", "./emf", "package to benchmark")
	maxTime := flags.Float64("max-time", 20, "largest allowed sec/op increase, in percent")
	maxBytes := flags.Float64("max-bytes", 10, "largest allowed B/op increase, in percent")
	maxAllocs := flags.Float64("max-allocs", 0, "largest allowed allocs/op increase, in percent")
	if err := flags.Parse(args); err != nil {
		return err
	}
	limits := map[string]float64{"sec/op": *maxTime, "B/op": *maxBytes, "allocs/op": *maxAllocs}

	root, err := output("", "git", "rev-parse", "--show-toplevel")
	if err != nil {
		return err
	}
	root = strings.TrimSpace(root)
	tmp, err := os.MkdirTemp("", "benchcompare")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	baseDir := filepath.Join(tmp, "base")
	if _, err := output(root, "git", "worktree", "add", "--quiet", "--detach", baseDir, *base); err != nil {
		return err
	}
	defer func() {
		_, _ = output(root, "git", "worktree", "remove", "--force", baseDir)
	}()

	out := &printer{w: stdout}
	progress := &printer{w: stderr}
	bench := []string{"test", *pkg, "-run", "^$", "-bench", ".", "-benchtime", *benchtime, "-count", "1"}
	var baseOut, headOut bytes.Buffer
	for i := range *rounds {
		progress.printf("round %d of %d\n", i+1, *rounds)
		for _, side := range []struct {
			dir string
			out *bytes.Buffer
		}{{baseDir, &baseOut}, {root, &headOut}} {
			result, err := output(side.dir, "go", bench...)
			if err != nil {
				return err
			}
			side.out.WriteString(result)
		}
	}

	baseSamples, err := parse(&baseOut, *base)
	if err != nil {
		return err
	}
	if len(baseSamples) == 0 {
		out.printf("No benchmarks on %s yet, so there is nothing to compare.\n", *base)
		return out.err
	}
	headSamples, err := parse(&headOut, "head")
	if err != nil {
		return err
	}

	rows := compare(baseSamples, headSamples)
	writeTable(out, rows)
	if path := os.Getenv("GITHUB_STEP_SUMMARY"); path != "" {
		if err := appendSummary(path, *base, rows); err != nil {
			return err
		}
	}

	found := regressions(rows, limits)
	if len(found) == 0 {
		out.printf("\nNo performance regressions.\n")
		return out.err
	}
	out.printf("\nPerformance regressions:\n")
	for _, r := range found {
		out.printf("  %s\n", r)
	}
	if out.err != nil {
		return out.err
	}
	return errRegression
}

// printer writes formatted output and keeps the first error.
type printer struct {
	w   io.Writer
	err error
}

func (p *printer) printf(format string, a ...any) {
	if p.err == nil {
		_, p.err = fmt.Fprintf(p.w, format, a...)
	}
}

// output runs a command in dir and returns its stdout.
func output(dir, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%s %s: %w\n%s%s", name, strings.Join(args, " "), err, out, stderr.Bytes())
	}
	return string(out), nil
}

func writeTable(p *printer, rows []row) {
	var b strings.Builder
	tw := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	tp := &printer{w: tw}
	tp.printf("benchmark\tunit\tbase\thead\tchange\tp\n")
	for _, r := range rows {
		tp.printf("%s\t%s\t%.4g\t%.4g\t%s\t%.3f\n", r.name, r.unit, r.base, r.head, formatChange(r), r.p)
	}
	_ = tw.Flush() // writes to a strings.Builder, which can't fail
	p.printf("%s", b.String())
}

func appendSummary(path, base string, rows []row) error {
	var b strings.Builder
	p := &printer{w: &b}
	p.printf("## Benchmarks: %s vs this PR\n\n", base)
	p.printf("| Benchmark | Unit | Base | Head | Change | p |\n")
	p.printf("|---|---|---:|---:|---:|---:|\n")
	for _, r := range rows {
		p.printf("| %s | %s | %.4g | %.4g | %s | %.3f |\n", r.name, r.unit, r.base, r.head, formatChange(r), r.p)
	}
	p.printf("\nChanges marked ~ are not statistically significant.\n")

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(b.String()); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
