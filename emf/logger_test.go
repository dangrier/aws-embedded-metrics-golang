package emf_test

import (
	"bytes"
	"os"
	"testing"

	"github.com/dangrier/aws-embedded-metrics-golang/emf"
	"github.com/kinbiko/jsonassert"
)

// emfTestCase builds a logger, calls the API and compares the output
// with the expected JSON in testdata.
type emfTestCase struct {
	name     string
	new      func(*bytes.Buffer) *emf.Logger
	env      map[string]string
	given    func(*emf.Logger)
	expected string
}

var emfTestCases = []emfTestCase{
	{
		name: "default namespace, int metric",
		given: func(logger *emf.Logger) {
			logger.Metric("foo", 33)
		},
		expected: "testdata/1.json",
	},
	{
		name: "default namespace, float metric",
		given: func(logger *emf.Logger) {
			logger.MetricFloat("foo", 33.66)
		},
		expected: "testdata/2.json",
	},
	{
		name: "custom namespace, int and float metrics",
		given: func(logger *emf.Logger) {
			logger.Namespace("galaxy").MetricFloat("foo", 33.66).Metric("bar", 666)
		},
		expected: "testdata/3.json",
	},
	{
		name: "custom namespace, int and float metrics, custom units",
		given: func(logger *emf.Logger) {
			logger.Namespace("galaxy").
				MetricFloatAs("foo", 33.66, emf.Milliseconds).
				MetricAs("bar", 666, emf.Count)
		},
		expected: "testdata/4.json",
	},
	{
		name: "new context, default namespace, int and float metrics, custom units",
		given: func(logger *emf.Logger) {
			logger.NewContext().
				MetricFloatAs("foo", 33.66, emf.Milliseconds).
				MetricAs("bar", 666, emf.Count)
		},
		expected: "testdata/5.json",
	},
	{
		name: "new context, custom namespace, int and float metrics, custom units",
		given: func(logger *emf.Logger) {
			logger.NewContext().Namespace("galaxy").
				MetricFloatAs("foo", 33.66, emf.Bits).
				MetricAs("bar", 666, emf.BytesSecond)
		},
		expected: "testdata/6.json",
	},
	{
		name: "default and custom contexts, different metrics names",
		given: func(logger *emf.Logger) {
			logger.NewContext().MetricFloatAs("foo", 33.66, emf.Bits)
			logger.MetricAs("bar", 666, emf.BytesSecond)
		},
		expected: "testdata/7.json",
	},
	{
		name: "set property",
		given: func(logger *emf.Logger) {
			logger.Property("aaa", "666").Metric("foo", 33)
		},
		expected: "testdata/8.json",
	},
	{
		name: "set default properties for lambda",
		env: map[string]string{
			"AWS_LAMBDA_FUNCTION_NAME":        "some-func-name",
			"AWS_EXECUTION_ENV":               "golang",
			"AWS_LAMBDA_FUNCTION_MEMORY_SIZE": "128",
			"AWS_LAMBDA_FUNCTION_VERSION":     "1",
			"AWS_LAMBDA_LOG_STREAM_NAME":      "log/stream",
		},
		given: func(logger *emf.Logger) {
			logger.Metric("foo", 33)
		},
		expected: "testdata/9.json",
	},
	{
		name: "not sampled trace",
		env: map[string]string{
			"_X_AMZN_TRACE_ID": "foo",
		},
		given: func(logger *emf.Logger) {
			logger.Metric("foo", 33)
		},
		expected: "testdata/10.json",
	},
	{
		name: "sampled trace",
		env: map[string]string{
			"_X_AMZN_TRACE_ID": "foo,Sampled=1,bar",
		},
		given: func(logger *emf.Logger) {
			logger.Metric("foo", 33)
		},
		expected: "testdata/11.json",
	},
	{
		name: "one dimension",
		given: func(logger *emf.Logger) {
			logger.Dimension("a", "b").Metric("c", 11)
		},
		expected: "testdata/12.json",
	},
	{
		name: "two dimensions",
		given: func(logger *emf.Logger) {
			logger.Dimension("a", "b").Dimension("o", "p").Metric("c", 11)
		},
		expected: "testdata/13.json",
	},
	{
		name: "one dimension set",
		given: func(logger *emf.Logger) {
			logger.
				DimensionSet(
					emf.NewDimension("a", "b"),
					emf.NewDimension("o", "p")).
				Metric("c", 11)
		},
		expected: "testdata/14.json",
	},
	{
		name: "two dimension sets",
		given: func(logger *emf.Logger) {
			logger.
				DimensionSet(
					emf.NewDimension("a", "b"),
					emf.NewDimension("c", "d")).
				DimensionSet(
					emf.NewDimension("1", "2"),
					emf.NewDimension("3", "4")).
				Metric("foo", 22)
		},
		expected: "testdata/15.json",
	},
	{
		name: "default and custom contexts, multiple dimensions/dimension sets",
		given: func(logger *emf.Logger) {
			logger.NewContext().
				Dimension("CC", "DD").
				DimensionSet(
					emf.NewDimension("gg", "hh"),
					emf.NewDimension("kk", "jj")).
				MetricFloatAs("foo", 33.66, emf.Bits)
			logger.
				Dimension("AA", "BB").
				DimensionSet(
					emf.NewDimension("ww", "ee"),
					emf.NewDimension("rr", "tt")).
				MetricAs("bar", 666, emf.BytesSecond)
		},
		expected: "testdata/16.json",
	},
	{
		name: "default properties and dimensions for lambda are ignored",
		new: func(buf *bytes.Buffer) *emf.Logger {
			return emf.New(emf.WithoutDimensions(), emf.WithWriter(buf))
		},
		env: map[string]string{
			"AWS_LAMBDA_FUNCTION_NAME":        "some-func-name",
			"AWS_EXECUTION_ENV":               "golang",
			"AWS_LAMBDA_FUNCTION_MEMORY_SIZE": "128",
			"AWS_LAMBDA_FUNCTION_VERSION":     "1",
			"AWS_LAMBDA_LOG_STREAM_NAME":      "log/stream",
		},
		given: func(logger *emf.Logger) {
			logger.Metric("foo", 33)
		},
		expected: "testdata/17.json",
	},
	{
		name: "with log group",
		new: func(buf *bytes.Buffer) *emf.Logger {
			return emf.New(emf.WithLogGroup("test_log_group"), emf.WithWriter(buf))
		},
		given: func(logger *emf.Logger) {
			logger.Metric("foo", 33)
		},
		expected: "testdata/18.json",
	},
	{
		name: "repeated dimension, last value wins",
		new: func(buf *bytes.Buffer) *emf.Logger {
			return emf.New(emf.WithoutDimensions(), emf.WithWriter(buf))
		},
		given: func(logger *emf.Logger) {
			logger.Dimension("Service", "checkout").Metric("A", 1).Dimension("Service", "cart")
		},
		expected: "testdata/19.json",
	},
	{
		name: "repeated dimension set in another order",
		new: func(buf *bytes.Buffer) *emf.Logger {
			return emf.New(emf.WithoutDimensions(), emf.WithWriter(buf))
		},
		given: func(logger *emf.Logger) {
			logger.
				DimensionSet(emf.NewDimension("A", "a"), emf.NewDimension("B", "b")).
				DimensionSet(emf.NewDimension("B", "b2"), emf.NewDimension("A", "a2")).
				Metric("m", 1)
		},
		expected: "testdata/20.json",
	},
	{
		name: "repeated key in one dimension set",
		new: func(buf *bytes.Buffer) *emf.Logger {
			return emf.New(emf.WithoutDimensions(), emf.WithWriter(buf))
		},
		given: func(logger *emf.Logger) {
			logger.DimensionSet(emf.NewDimension("A", "x"), emf.NewDimension("A", "y")).Metric("m", 1)
		},
		expected: "testdata/21.json",
	},
	{
		name: "lambda default dimension set added again",
		env: map[string]string{
			"AWS_LAMBDA_FUNCTION_NAME":        "some-func-name",
			"AWS_EXECUTION_ENV":               "golang",
			"AWS_LAMBDA_FUNCTION_MEMORY_SIZE": "128",
			"AWS_LAMBDA_FUNCTION_VERSION":     "1",
			"AWS_LAMBDA_LOG_STREAM_NAME":      "log/stream",
		},
		given: func(logger *emf.Logger) {
			logger.
				DimensionSet(
					emf.NewDimension("ServiceType", "AWS::Lambda::Function"),
					emf.NewDimension("ServiceName", "some-func-name")).
				Metric("foo", 33)
		},
		expected: "testdata/9.json",
	},
	{
		name: "same dimension set in two contexts",
		new: func(buf *bytes.Buffer) *emf.Logger {
			return emf.New(emf.WithoutDimensions(), emf.WithWriter(buf))
		},
		given: func(logger *emf.Logger) {
			logger.Dimension("A", "a").Dimension("A", "a").Metric("m", 1)
			logger.NewContext().Namespace("other").Dimension("A", "a").Dimension("A", "a").Metric("n", 2)
		},
		expected: "testdata/22.json",
	},
	{
		name: "contexts disagree on a dimension value",
		new: func(buf *bytes.Buffer) *emf.Logger {
			return emf.New(emf.WithoutDimensions(), emf.WithWriter(buf))
		},
		given: func(logger *emf.Logger) {
			logger.NewContext().Namespace("a").Dimension("Provider", "card").Metric("Declined", 1)
			logger.NewContext().Namespace("b").Dimension("Provider", "paypal").Metric("Refunds", 2)
		},
		expected: "testdata/23.json",
	},
	{
		name: "contexts agree on dimension values",
		new: func(buf *bytes.Buffer) *emf.Logger {
			return emf.New(emf.WithoutDimensions(), emf.WithWriter(buf))
		},
		given: func(logger *emf.Logger) {
			logger.NewContext().Namespace("a").Dimension("Provider", "card").Dimension("Region", "au").Metric("Declined", 1)
			logger.NewContext().Namespace("b").Dimension("Provider", "card").Dimension("Stage", "prod").Metric("Refunds", 2)
		},
		expected: "testdata/24.json",
	},
	{
		name: "default context disagrees with a new context",
		new: func(buf *bytes.Buffer) *emf.Logger {
			return emf.New(emf.WithoutDimensions(), emf.WithWriter(buf))
		},
		given: func(logger *emf.Logger) {
			ctx := logger.NewContext().Namespace("b").Dimension("Provider", "paypal").Metric("Refunds", 2)
			logger.Namespace("a").Dimension("Provider", "card").Metric("Declined", 1)
			ctx.Metric("Chargebacks", 3)
		},
		expected: "testdata/25.json",
	},
	{
		name: "three contexts, two agree",
		new: func(buf *bytes.Buffer) *emf.Logger {
			return emf.New(emf.WithoutDimensions(), emf.WithWriter(buf))
		},
		given: func(logger *emf.Logger) {
			logger.NewContext().Namespace("a").Dimension("Provider", "card").Metric("Declined", 1)
			logger.NewContext().Namespace("b").Dimension("Provider", "paypal").Metric("Refunds", 2)
			logger.NewContext().Namespace("c").Dimension("Provider", "card").Metric("Approved", 3)
		},
		expected: "testdata/26.json",
	},
	{
		name: "metric name shared by contexts that disagree",
		new: func(buf *bytes.Buffer) *emf.Logger {
			return emf.New(emf.WithoutDimensions(), emf.WithWriter(buf))
		},
		given: func(logger *emf.Logger) {
			logger.Namespace("a").Dimension("Provider", "card").Put("Latency", 10).Metric("Declined", 1)
			logger.NewContext().Namespace("b").Dimension("Provider", "paypal").Put("Latency", 20)
		},
		expected: "testdata/27.json",
	},
	{
		name: "dimension replaces a property on every line",
		new: func(buf *bytes.Buffer) *emf.Logger {
			return emf.New(emf.WithoutDimensions(), emf.WithWriter(buf))
		},
		given: func(logger *emf.Logger) {
			logger.Property("Provider", "property").
				DimensionSet(emf.NewDimension("Provider", "card"), emf.NewDimension("Region", "au")).
				Metric("Declined", 1)
			logger.NewContext().
				DimensionSet(emf.NewDimension("Provider", "paypal"), emf.NewDimension("Region", "au")).
				Metric("Refunds", 2)
			// Disagrees with both on Region, and has no Provider of its own.
			logger.NewContext().Dimension("Region", "nz").Metric("Approved", 3)
		},
		expected: "testdata/28.json",
	},
	{
		name: "context without metrics doesn't change another's value",
		new: func(buf *bytes.Buffer) *emf.Logger {
			return emf.New(emf.WithoutDimensions(), emf.WithWriter(buf))
		},
		given: func(logger *emf.Logger) {
			logger.Dimension("Provider", "card").Metric("Declined", 1)
			logger.NewContext().Dimension("Provider", "paypal").Dimension("Region", "au")
		},
		expected: "testdata/29.json",
	},
	{
		name: "lambda contexts disagree on another dimension",
		env: map[string]string{
			"AWS_LAMBDA_FUNCTION_NAME":        "some-func-name",
			"AWS_EXECUTION_ENV":               "golang",
			"AWS_LAMBDA_FUNCTION_MEMORY_SIZE": "128",
			"AWS_LAMBDA_FUNCTION_VERSION":     "1",
			"AWS_LAMBDA_LOG_STREAM_NAME":      "log/stream",
		},
		given: func(logger *emf.Logger) {
			logger.Dimension("Provider", "card").Metric("Declined", 1)
			logger.NewContext().Dimension("Provider", "paypal").Metric("Refunds", 2)
		},
		expected: "testdata/30.json",
	},
	{
		name: "dimension and metric replace properties",
		new: func(buf *bytes.Buffer) *emf.Logger {
			return emf.New(emf.WithoutDimensions(), emf.WithWriter(buf))
		},
		given: func(logger *emf.Logger) {
			logger.Property("Service", "prop").Dimension("Service", "dim")
			logger.Property("Latency", "prop").Metric("Latency", 5)
		},
		expected: "testdata/31.json",
	},
	{
		name: "property values of each JSON type",
		new: func(buf *bytes.Buffer) *emf.Logger {
			return emf.New(emf.WithoutDimensions(), emf.WithWriter(buf))
		},
		given: func(logger *emf.Logger) {
			type user struct {
				ID     int    `json:"id"`
				Name   string `json:"name,omitempty"`
				Email  string `json:"email,omitempty"`
				Secret string `json:"-"`
			}
			logger.PropertyValue("statusCode", 503).
				PropertyValue("ratio", 0.25).
				PropertyValue("big", int64(1)<<53).
				PropertyValue("cached", true).
				PropertyValue("missing", nil).
				PropertyValue("requestId", "abc").
				PropertyValue("tags", []string{"beta", "eu"}).
				PropertyValue("empty", []int{}).
				PropertyValue("headers", map[string]any{"accept": "json", "retries": 2}).
				PropertyValue("user", user{ID: 7, Name: "ana", Secret: "hidden"}).
				Metric("Requests", 1)
		},
		expected: "testdata/32.json",
	},
	{
		name: "Property and PropertyValue replace each other",
		new: func(buf *bytes.Buffer) *emf.Logger {
			return emf.New(emf.WithoutDimensions(), emf.WithWriter(buf))
		},
		given: func(logger *emf.Logger) {
			logger.Property("a", "string").PropertyValue("a", 1)
			logger.PropertyValue("b", 2).Property("b", "string")
			logger.PropertyValue("c", []int{1}).PropertyValue("c", map[string]int{"n": 3})
			logger.Metric("Requests", 1)
		},
		expected: "testdata/33.json",
	},
	{
		name: "merged defaults, no dimension sets",
		new:  newMerged,
		env:  lambdaEnv,
		given: func(logger *emf.Logger) {
			logger.Metric("foo", 33)
		},
		expected: "testdata/9.json",
	},
	{
		name: "merged defaults, one dimension set",
		new:  newMerged,
		env:  lambdaEnv,
		given: func(logger *emf.Logger) {
			logger.Dimension("Service", "checkout").Metric("Orders", 1)
		},
		expected: "testdata/34.json",
	},
	{
		name: "merged defaults, several dimension sets",
		new:  newMerged,
		env:  lambdaEnv,
		given: func(logger *emf.Logger) {
			logger.
				Dimension("Service", "checkout").
				DimensionSet(emf.NewDimension("Region", "au"), emf.NewDimension("Stage", "prod")).
				Metric("Orders", 1)
		},
		expected: "testdata/35.json",
	},
	{
		name: "merged defaults, set already has ServiceName",
		new:  newMerged,
		env:  lambdaEnv,
		given: func(logger *emf.Logger) {
			// Both sets become [ServiceName ServiceType Operation], so it is
			// written once. The last ServiceName value wins.
			logger.
				DimensionSet(emf.NewDimension("Operation", "Get"), emf.NewDimension("ServiceName", "custom")).
				Dimension("Operation", "Get").
				Metric("Orders", 1)
		},
		expected: "testdata/36.json",
	},
	{
		name: "merged defaults, default set added by hand",
		new:  newMerged,
		env:  lambdaEnv,
		given: func(logger *emf.Logger) {
			logger.
				DimensionSet(
					emf.NewDimension("ServiceType", "AWS::Lambda::Function"),
					emf.NewDimension("ServiceName", "some-func-name")).
				Dimension("Service", "checkout").
				Metric("Orders", 1)
		},
		expected: "testdata/37.json",
	},
	{
		name: "merged defaults, contexts disagree on another dimension",
		new:  newMerged,
		env:  lambdaEnv,
		given: func(logger *emf.Logger) {
			logger.Dimension("Provider", "card").Metric("Declined", 1)
			logger.NewContext().Dimension("Provider", "paypal").Metric("Refunds", 2)
			logger.NewContext().Namespace("other").Metric("Approved", 3)
		},
		expected: "testdata/38.json",
	},
	{
		name: "merged defaults, contexts agree",
		new:  newMerged,
		env:  lambdaEnv,
		given: func(logger *emf.Logger) {
			logger.Dimension("Provider", "card").Metric("Declined", 1)
			logger.NewContext().Namespace("other").Dimension("Region", "au").Metric("Refunds", 2)
		},
		expected: "testdata/39.json",
	},
	{
		name: "merged defaults, without dimensions",
		new: func(buf *bytes.Buffer) *emf.Logger {
			return emf.New(emf.WithMergedDefaultDimensions(), emf.WithoutDimensions(), emf.WithWriter(buf))
		},
		env: lambdaEnv,
		given: func(logger *emf.Logger) {
			logger.Metric("foo", 33)
		},
		expected: "testdata/17.json",
	},
	{
		name: "merged defaults, outside Lambda",
		new:  newMerged,
		given: func(logger *emf.Logger) {
			logger.Dimension("a", "b").Metric("c", 11)
		},
		expected: "testdata/12.json",
	},
	{
		name: "reset without defaults",
		env:  lambdaEnv,
		given: func(logger *emf.Logger) {
			logger.Dimension("Service", "checkout").ResetDimensions(false).Metric("Orders", 1)
		},
		expected: "testdata/40.json",
	},
	{
		name: "reset with defaults",
		env:  lambdaEnv,
		given: func(logger *emf.Logger) {
			logger.Dimension("Service", "checkout").ResetDimensions(true).Metric("foo", 33)
		},
		expected: "testdata/9.json",
	},
	{
		name: "reset without defaults, then with defaults",
		env:  lambdaEnv,
		given: func(logger *emf.Logger) {
			logger.ResetDimensions(false).ResetDimensions(true).Metric("foo", 33)
		},
		expected: "testdata/9.json",
	},
	{
		name: "merged defaults, reset without defaults",
		new:  newMerged,
		env:  lambdaEnv,
		given: func(logger *emf.Logger) {
			logger.Dimension("Service", "checkout").ResetDimensions(false).Metric("Orders", 1)
		},
		expected: "testdata/40.json",
	},
	{
		name: "merged defaults, reset with defaults",
		new:  newMerged,
		env:  lambdaEnv,
		given: func(logger *emf.Logger) {
			logger.Dimension("Service", "checkout").ResetDimensions(true).Metric("foo", 33)
		},
		expected: "testdata/9.json",
	},
	{
		name: "reset outside Lambda",
		given: func(logger *emf.Logger) {
			logger.Dimension("Service", "checkout").ResetDimensions(true).Metric("foo", 33)
		},
		expected: "testdata/1.json",
	},
	{
		name: "reset without defaults, outside Lambda",
		given: func(logger *emf.Logger) {
			logger.Dimension("Service", "checkout").ResetDimensions(false).Metric("foo", 33)
		},
		expected: "testdata/1.json",
	},
	{
		name: "dimension added after reset",
		given: func(logger *emf.Logger) {
			logger.Dimension("a", "x").Dimension("o", "p").ResetDimensions(true).Dimension("a", "b").Metric("c", 11)
		},
		expected: "testdata/12.json",
	},
	{
		name: "dimension added after reset without defaults",
		env:  lambdaEnv,
		given: func(logger *emf.Logger) {
			logger.Dimension("Old", "x").ResetDimensions(false).Dimension("Service", "checkout").Metric("Orders", 1)
		},
		expected: "testdata/41.json",
	},
	{
		name: "set dimensions without defaults, one set",
		env:  lambdaEnv,
		given: func(logger *emf.Logger) {
			logger.Dimension("Old", "x").
				SetDimensions(false, []emf.Dimension{emf.NewDimension("Service", "checkout")}).
				Metric("Orders", 1)
		},
		expected: "testdata/41.json",
	},
	{
		name: "set dimensions with defaults, several sets",
		env:  lambdaEnv,
		given: func(logger *emf.Logger) {
			logger.Dimension("Old", "x").
				SetDimensions(true,
					[]emf.Dimension{emf.NewDimension("Service", "checkout")},
					[]emf.Dimension{emf.NewDimension("Region", "au"), emf.NewDimension("Stage", "prod")},
					[]emf.Dimension{emf.NewDimension("Service", "checkout")}).
				Metric("Orders", 1)
		},
		expected: "testdata/42.json",
	},
	{
		name: "merged defaults, set dimensions with defaults, several sets",
		new:  newMerged,
		env:  lambdaEnv,
		given: func(logger *emf.Logger) {
			logger.Dimension("Old", "x").
				SetDimensions(true,
					[]emf.Dimension{emf.NewDimension("Service", "checkout")},
					[]emf.Dimension{emf.NewDimension("Region", "au"), emf.NewDimension("Stage", "prod")}).
				Metric("Orders", 1)
		},
		expected: "testdata/35.json",
	},
	{
		name: "merged defaults, set dimensions without defaults",
		new:  newMerged,
		env:  lambdaEnv,
		given: func(logger *emf.Logger) {
			logger.SetDimensions(false, []emf.Dimension{emf.NewDimension("Service", "checkout")}).Metric("Orders", 1)
		},
		expected: "testdata/41.json",
	},
	{
		name: "set dimensions without defaults, no sets",
		env:  lambdaEnv,
		given: func(logger *emf.Logger) {
			logger.Dimension("Service", "checkout").SetDimensions(false).Metric("Orders", 1)
		},
		expected: "testdata/40.json",
	},
	{
		name: "set dimensions with defaults, no sets",
		env:  lambdaEnv,
		given: func(logger *emf.Logger) {
			logger.Dimension("Service", "checkout").SetDimensions(true).Metric("foo", 33)
		},
		expected: "testdata/9.json",
	},
	{
		name: "set dimensions outside Lambda",
		given: func(logger *emf.Logger) {
			logger.Dimension("Old", "x").
				SetDimensions(true,
					[]emf.Dimension{emf.NewDimension("a", "b"), emf.NewDimension("c", "d")},
					[]emf.Dimension{emf.NewDimension("1", "2"), emf.NewDimension("3", "4")}).
				Metric("foo", 22)
		},
		expected: "testdata/15.json",
	},
	{
		name: "contexts that disagreed share a line after a reset",
		new: func(buf *bytes.Buffer) *emf.Logger {
			return emf.New(emf.WithoutDimensions(), emf.WithWriter(buf))
		},
		given: func(logger *emf.Logger) {
			logger.Dimension("Provider", "card").Metric("Declined", 1)
			logger.NewContext().Namespace("other").Dimension("Provider", "paypal").Metric("Refunds", 2).ResetDimensions(false)
		},
		expected: "testdata/43.json",
	},
	{
		name: "context drops the defaults, the default context keeps them",
		env:  lambdaEnv,
		given: func(logger *emf.Logger) {
			logger.Metric("Orders", 1)
			logger.NewContext().Namespace("other").
				SetDimensions(false, []emf.Dimension{emf.NewDimension("Region", "au")}).
				Metric("Refunds", 2)
		},
		expected: "testdata/44.json",
	},
	{
		name: "removed dimension names used by a metric and a property",
		given: func(logger *emf.Logger) {
			logger.Dimension("Requests", "x").Dimension("Region", "au").ResetDimensions(true).
				Metric("Requests", 1).
				Property("Region", "eu")
		},
		expected: "testdata/45.json",
	},
	{
		name: "property written again once the dimension that replaced it is removed",
		given: func(logger *emf.Logger) {
			logger.Property("Service", "p").Dimension("Service", "checkout").ResetDimensions(true).Metric("Orders", 1)
		},
		expected: "testdata/46.json",
	},
}

// lambdaEnv is the environment of a Lambda function.
var lambdaEnv = map[string]string{
	"AWS_LAMBDA_FUNCTION_NAME":        "some-func-name",
	"AWS_EXECUTION_ENV":               "golang",
	"AWS_LAMBDA_FUNCTION_MEMORY_SIZE": "128",
	"AWS_LAMBDA_FUNCTION_VERSION":     "1",
	"AWS_LAMBDA_LOG_STREAM_NAME":      "log/stream",
}

// newMerged creates a logger that merges the Lambda default dimensions into
// each dimension set.
func newMerged(buf *bytes.Buffer) *emf.Logger {
	return emf.New(emf.WithMergedDefaultDimensions(), emf.WithWriter(buf))
}

// run sets up the environment, logs and returns the output.
func (tc emfTestCase) run(t *testing.T) []byte {
	t.Helper()
	for k, v := range tc.env {
		t.Setenv(k, v)
	}

	var buf bytes.Buffer
	var logger *emf.Logger
	if tc.new != nil {
		logger = tc.new(&buf)
	} else {
		logger = emf.New(emf.WithWriter(&buf))
	}
	tc.given(logger)
	logger.Log()
	return buf.Bytes()
}

func TestEmf(t *testing.T) {
	for _, tc := range emfTestCases {
		t.Run(tc.name, func(t *testing.T) {
			out := tc.run(t)

			f, err := os.ReadFile(tc.expected)
			if err != nil {
				t.Fatal("unable to read file with expected json")
			}

			// An expected array holds one object per line, in order.
			if bytes.HasPrefix(bytes.TrimSpace(f), []byte("[")) {
				out = linesAsArray(out)
			}
			jsonassert.New(t).Assertf(string(out), "%s", string(f))
		})
	}

	t.Run("no metrics set", func(t *testing.T) {
		var buf bytes.Buffer
		logger := emf.New(emf.WithWriter(&buf))
		logger.Log()

		if buf.String() != "" {
			t.Error("Buffer not empty")
		}
	})

	t.Run("new context, no metrics set", func(t *testing.T) {
		var buf bytes.Buffer
		logger := emf.New(emf.WithWriter(&buf))
		logger.NewContext().Namespace("galaxy")
		logger.Log()

		if buf.String() != "" {
			t.Error("Buffer not empty")
		}
	})
}

// linesAsArray turns the lines of out into a JSON array.
func linesAsArray(out []byte) []byte {
	var lines [][]byte
	for line := range bytes.Lines(out) {
		lines = append(lines, bytes.TrimSuffix(line, []byte("\n")))
	}
	return append(append([]byte("["), bytes.Join(lines, []byte(","))...), ']')
}
