module github.com/dangrier/aws-embedded-metrics-golang

go 1.27

require (
	github.com/google/jsonschema-go v0.4.3
	github.com/kinbiko/jsonassert v1.2.0
	golang.org/x/perf v0.0.0-20260929162123-406019bb8b68
)

require github.com/aclements/go-moremath v0.0.0-20210112150236-f10218a38794 // indirect

tool github.com/dangrier/aws-embedded-metrics-golang/internal/cmd/benchcompare
