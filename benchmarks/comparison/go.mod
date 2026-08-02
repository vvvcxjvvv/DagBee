module github.com/vvvcxjvvv/DagBee/benchmarks/comparison

go 1.21.6

require (
	github.com/noneback/go-taskflow v1.2.0
	github.com/vvvcxjvvv/DagBee v0.0.0
)

require gopkg.in/yaml.v3 v3.0.1 // indirect

replace github.com/vvvcxjvvv/DagBee => ../..
