package emf_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
)

// The files in testdata/spec are copied from the AWS EMF specification:
// https://docs.aws.amazon.com/AmazonCloudWatch/latest/monitoring/CloudWatch_Embedded_Metric_Format_Specification.html
//
// The published JSON Schema does not encode every MUST rule in the spec
// text (for example, that metric names reference numeric root members),
// so specViolations checks those rules separately.

const (
	maxEventBytes         = 1 << 20 // 1 MB
	maxMetricDefinitions  = 100
	maxMetricArrayValues  = 100
	maxDimensionValueLen  = 1024
	maxTimestampPast      = 14 * 24 * time.Hour
	maxTimestampFuture    = 2 * time.Hour
	schemaFile            = "testdata/spec/emf.schema.json"
	specExampleFile       = "testdata/spec/example.json"
	cloudWatchAgentLogKey = "LogGroupName"
)

// maxMetricMagnitude is 2^360, the largest magnitude a metric value may have.
var maxMetricMagnitude = new(big.Float).SetMantExp(big.NewFloat(1), 360)

var emfSchema = sync.OnceValue(func() *jsonschema.Resolved {
	b, err := os.ReadFile(schemaFile)
	if err != nil {
		panic(err)
	}

	var schema jsonschema.Schema
	if err := json.Unmarshal(b, &schema); err != nil {
		panic(err)
	}
	// The published schema has no $schema, but uses draft-07 style $id fragments.
	schema.Schema = "http://json-schema.org/draft-07/schema#"

	resolved, err := schema.Resolve(&jsonschema.ResolveOptions{BaseURI: "file:///" + schemaFile})
	if err != nil {
		panic(err)
	}
	return resolved
})

// specViolations returns every way a single EMF log line breaks the AWS
// spec. An empty result means the line is compliant. now is used for the
// timestamp window check.
func specViolations(line []byte, now time.Time) []string {
	var v []string
	add := func(format string, a ...any) {
		v = append(v, fmt.Sprintf(format, a...))
	}

	if len(line) > maxEventBytes {
		add("event is %d bytes, over the 1 MB limit", len(line))
	}

	// The LogEvent MUST be a JSON object with no data before or after it.
	dec := json.NewDecoder(bytes.NewReader(bytes.TrimSuffix(line, []byte("\n"))))
	dec.UseNumber()
	var root map[string]any
	if err := dec.Decode(&root); err != nil {
		add("not a JSON object: %v", err)
		return v
	}
	if _, err := dec.Token(); err != io.EOF {
		add("extra data after the JSON object")
	}

	// The schema validator wants plain decoded JSON, without json.Number.
	var plain any
	if err := json.Unmarshal(line, &plain); err == nil {
		if err := emfSchema().Validate(plain); err != nil {
			add("schema: %v", err)
		}
	}

	aws, _ := root["_aws"].(map[string]any)
	// LogGroupName is not in the spec, but the CloudWatch agent reads it.
	checkMembers(add, "_aws", aws, "Timestamp", "CloudWatchMetrics", cloudWatchAgentLogKey)

	if n, ok := aws["Timestamp"].(json.Number); ok {
		if ms, err := n.Int64(); err == nil {
			ts := time.UnixMilli(ms)
			if ts.Before(now.Add(-maxTimestampPast)) || ts.After(now.Add(maxTimestampFuture)) {
				add("Timestamp %v is outside 14 days in the past to 2 hours in the future", ts.UTC())
			}
		}
	}

	directives, _ := aws["CloudWatchMetrics"].([]any)
	definitions := 0
	for i, d := range directives {
		dir, _ := d.(map[string]any)
		path := fmt.Sprintf("CloudWatchMetrics[%d]", i)
		checkMembers(add, path, dir, "Namespace", "Dimensions", "Metrics")

		sets, _ := dir["Dimensions"].([]any)
		for _, s := range sets {
			keys, _ := s.([]any)
			for _, k := range keys {
				if key, ok := k.(string); ok {
					checkDimensionTarget(add, root, key)
				}
			}
		}

		metrics, _ := dir["Metrics"].([]any)
		definitions += len(metrics)
		for j, m := range metrics {
			def, _ := m.(map[string]any)
			checkMembers(add, fmt.Sprintf("%s.Metrics[%d]", path, j), def, "Name", "Unit", "StorageResolution")

			if name, ok := def["Name"].(string); ok {
				checkMetricTarget(add, root, name)
			}
			if r, ok := def["StorageResolution"].(json.Number); ok && r != "1" && r != "60" {
				add("metric StorageResolution is %s, should be 1 or 60", r)
			}
		}
	}
	if definitions > maxMetricDefinitions {
		add("%d metric definitions, over the limit of %d", definitions, maxMetricDefinitions)
	}

	return v
}

// checkMembers reports members the spec does not define. Spec objects
// MUST NOT contain additional members.
func checkMembers(add func(string, ...any), path string, obj map[string]any, allowed ...string) {
	for key := range obj {
		if !slices.Contains(allowed, key) {
			add("%s has member %q, which the spec does not allow", path, key)
		}
	}
}

func checkDimensionTarget(add func(string, ...any), root map[string]any, key string) {
	value, ok := root[key]
	if !ok {
		add("dimension %q has no matching root member", key)
		return
	}
	s, ok := value.(string)
	if !ok {
		add("dimension %q must be a string, got %s", key, jsonType(value))
		return
	}
	if len(s) < 1 || len(s) > maxDimensionValueLen || !printableASCII(s) || strings.TrimSpace(s) == "" {
		add("dimension %q value %q must be 1 to 1024 printable ASCII characters, not only whitespace", key, s)
	}
}

func checkMetricTarget(add func(string, ...any), root map[string]any, name string) {
	value, ok := root[name]
	if !ok {
		add("metric %q has no matching root member", name)
		return
	}
	switch val := value.(type) {
	case json.Number:
		checkMetricValue(add, name, val)
	case []any:
		if len(val) > maxMetricArrayValues {
			add("metric %q has %d values, over the limit of %d", name, len(val), maxMetricArrayValues)
		}
		for _, item := range val {
			n, ok := item.(json.Number)
			if !ok {
				add("metric %q array holds a %s, must only hold numbers", name, jsonType(item))
				continue
			}
			checkMetricValue(add, name, n)
		}
	default:
		add("metric %q must be a number or an array of numbers, got %s", name, jsonType(value))
	}
}

func checkMetricValue(add func(string, ...any), name string, n json.Number) {
	f, _, err := big.ParseFloat(string(n), 10, 1024, big.ToNearestEven)
	if err != nil {
		add("metric %q value %s is not a number", name, n)
		return
	}
	if f.Abs(f).Cmp(maxMetricMagnitude) > 0 {
		add("metric %q value %s is outside -2^360 to 2^360", name, n)
	}
}

func printableASCII(s string) bool {
	return !strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 || r > 0x7e })
}

func jsonType(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case string:
		return "string"
	case json.Number:
		return "number"
	case bool:
		return "boolean"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	default:
		return fmt.Sprintf("%T", v)
	}
}
