package emf_test

import (
	"fmt"
	"io"
	"testing"

	"github.com/dangrier/aws-embedded-metrics-golang/emf"
)

// BenchmarkTypicalEvent builds and logs a typical event: a namespace, two
// dimensions, a property and four metrics.
func BenchmarkTypicalEvent(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		emf.New(emf.WithWriter(io.Discard), emf.WithoutDimensions()).
			Namespace("app").
			Dimension("Service", "api").
			Dimension("Operation", "GetItem").
			Property("requestId", "989ffbf8-9ace-4817-a57c-e4dd734019ee").
			Metric("Requests", 1).
			MetricAs("Errors", 0, emf.Count).
			MetricFloatAs("Latency", 12.5, emf.Milliseconds).
			MetricFloatAs("Size", 2048, emf.Bytes).
			Log()
	}
}

// BenchmarkLog measures Log on a logger that already holds metrics,
// including sizes that are split across several lines.
func BenchmarkLog(b *testing.B) {
	for _, n := range []int{1, 10, 100, 250} {
		b.Run(fmt.Sprintf("metrics=%d", n), func(b *testing.B) {
			logger := emf.New(emf.WithWriter(io.Discard), emf.WithoutDimensions()).
				Dimension("Service", "api")
			for i := range n {
				logger.Metric(fmt.Sprintf("m%d", i), i)
			}

			b.ReportAllocs()
			for b.Loop() {
				logger.Log()
			}
		})
	}
}

// BenchmarkAddMetrics measures adding metrics, including the spec checks.
func BenchmarkAddMetrics(b *testing.B) {
	names := make([]string, 100)
	for i := range names {
		names[i] = fmt.Sprintf("m%d", i)
	}

	b.ReportAllocs()
	for b.Loop() {
		logger := emf.New(emf.WithWriter(io.Discard), emf.WithoutDimensions())
		for i, name := range names {
			logger.MetricFloatAs(name, float64(i), emf.Milliseconds)
		}
	}
}

// BenchmarkLogParallel measures many goroutines logging through one shared
// logger, which contends on its lock.
func BenchmarkLogParallel(b *testing.B) {
	logger := emf.New(emf.WithWriter(io.Discard), emf.WithoutDimensions()).
		Dimension("Service", "api").
		Metric("Requests", 1).
		MetricFloatAs("Latency", 12.5, emf.Milliseconds)

	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			logger.Log()
		}
	})
}
