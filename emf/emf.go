// Package emf writes CloudWatch metrics in the AWS Embedded Metric Format
// (EMF). Each Log call writes JSON log lines, and CloudWatch Logs turns them
// into metrics, so no CloudWatch API calls are needed.
//
// Create a Logger with New, add metrics, dimensions and properties, then call
// Log:
//
//	emf.New().
//		Namespace("shop").
//		Dimension("Service", "checkout").
//		MetricAs("OrderTotal", 120, emf.Count).
//		Log()
//
// Output always follows the EMF specification. Input that would break it is
// skipped and reported to the WithErrorHandler handler, and the rest is still
// logged. A Logger is safe to use from several goroutines at once.
//
// The specification is at
// https://docs.aws.amazon.com/AmazonCloudWatch/latest/monitoring/CloudWatch_Embedded_Metric_Format_Specification.html
package emf

import "encoding/json"

// Metadata struct as defined in AWS Embedded Metrics Format spec.
type Metadata struct {
	Timestamp    int64             `json:"Timestamp"`
	Metrics      []MetricDirective `json:"CloudWatchMetrics"`
	LogGroupName string            `json:"LogGroupName,omitempty"`
}

// MetricDirective struct as defined in AWS Embedded Metrics Format spec.
type MetricDirective struct {
	Namespace  string             `json:"Namespace"`
	Dimensions []DimensionSet     `json:"Dimensions"`
	Metrics    []MetricDefinition `json:"Metrics"`
}

// MarshalJSON keeps the output spec compliant when there are no dimensions.
// The spec requires at least one DimensionSet, and allows a DimensionSet to
// be empty, so missing dimensions are written as [[]] instead of null.
func (d MetricDirective) MarshalJSON() ([]byte, error) {
	type directive MetricDirective

	sets := make([]DimensionSet, 0, len(d.Dimensions))
	for _, set := range d.Dimensions {
		if set == nil {
			set = DimensionSet{}
		}
		sets = append(sets, set)
	}
	if len(sets) == 0 {
		sets = append(sets, DimensionSet{})
	}
	d.Dimensions = sets

	return json.Marshal(directive(d))
}

// DimensionSet as defined in AWS Embedded Metrics Format spec.
type DimensionSet []string

// MetricDefinition struct as defined in AWS Embedded Metrics Format spec.
type MetricDefinition struct {
	Name              string     `json:"Name"`
	Unit              MetricUnit `json:"Unit,omitempty"`
	StorageResolution int        `json:"StorageResolution,omitempty"`
}
