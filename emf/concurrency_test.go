package emf_test

import (
	"bytes"
	"fmt"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/dangrier/aws-embedded-metrics-golang/emf"
)

// syncBuffer is a bytes.Buffer that is safe to write from several
// goroutines, so the test only finds races inside the logger.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// TestConcurrentUse uses one logger and its contexts from many goroutines
// at once. Run with -race (just test-race) to catch data races.
func TestConcurrentUse(t *testing.T) {
	var out syncBuffer
	var errMu sync.Mutex
	errCount := 0
	logger := emf.New(emf.WithWriter(&out), emf.WithoutDimensions(), emf.WithErrorHandler(func(error) {
		errMu.Lock()
		errCount++
		errMu.Unlock()
	}))

	const (
		goroutines = 8
		iterations = 25
	)
	var wg sync.WaitGroup
	for g := range goroutines {
		wg.Go(func() {
			ctx := logger.NewContext().Namespace(fmt.Sprintf("ns%d", g))
			for i := range iterations {
				name := fmt.Sprintf("g%d_m%d", g, i)
				logger.Metric(name, i).
					MetricFloat(name+"_f", float64(i)).
					Metrics(map[string]int{name + "_map": i}).
					Property(fmt.Sprintf("g%d_p%d", g, i), "x").
					Dimension(fmt.Sprintf("g%d_d%d", g, i), "v")
				ctx.Metric(name+"_ctx", i).Dimension(fmt.Sprintf("g%d_cd%d", g, i), "v")
				logger.MetricFloat(name+"_bad", math.NaN()) // reported, not logged
				if i%5 == 0 {
					logger.Log()
				}
			}
		})
	}
	wg.Wait()
	logger.Log()

	for line := range bytes.Lines(out.buf.Bytes()) {
		assertCompliant(t, line)
	}
	if want := goroutines * iterations; errCount != want {
		t.Errorf("expected %d reported errors, got %d", want, errCount)
	}
}

// TestErrorHandlerCanUseLogger checks the error handler can call back into
// the logger without deadlocking.
func TestErrorHandlerCanUseLogger(t *testing.T) {
	var buf bytes.Buffer
	var logger *emf.Logger
	logger = emf.New(emf.WithWriter(&buf), emf.WithoutDimensions(), emf.WithErrorHandler(func(error) {
		logger.Metric("errors", 1)
	}))

	done := make(chan struct{})
	go func() {
		logger.MetricFloat("bad", math.NaN()).Log()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("deadlock: error handler could not use the logger")
	}
	if !bytes.Contains(buf.Bytes(), []byte(`"errors":1`)) {
		t.Errorf("metric from error handler missing: %s", buf.Bytes())
	}
}
