# DagBee and go-taskflow Benchmark

This module compares end-to-end execution through the public APIs of DagBee
and `noneback/go-taskflow` v1.2.0.

The comparison module requires Go 1.21.6 or later because that is the minimum
version declared by go-taskflow v1.2.0. DagBee's main module remains on Go 1.19.

## Run

```bash
cd benchmarks/comparison
go test -run '^$' -bench '^BenchmarkComparison$' -benchmem -count 5
```

Run on an otherwise idle machine. Compare results from the same command and
host; results from different hardware or Go versions are not comparable.

## Method

- Both graphs are constructed before the benchmark timer starts.
- Both frameworks are compiled into the same benchmark binary by the same Go
  compiler; module `go` directives only declare compatibility requirements.
- Both frameworks use `runtime.NumCPU()` as the concurrency limit.
- Both reuse their public engine or executor object across iterations.
- Timed work includes execution setup, scheduling, completion, and teardown.
- DagBee result release is included because it is required by its public API.
- Node functions are empty to isolate framework overhead.
- DOT, tracing, profiling, hooks, logging, retries, and shared-data operations
  are disabled.

The suite covers independent wide graphs, serial deep graphs, fan-out/fan-in,
and one nested subflow. The subflow result is not a scheduler-only comparison:
DagBee rebuilds and validates its dynamic child DAG on every run, while
go-taskflow instantiates and retains its child graph after the first run.

The benchmark measures feature-inclusive public API cost, not equivalent
internal scheduler primitives. Use `ns/op`, `B/op`, and `allocs/op` together;
do not treat one number as an overall framework quality score.

## Reference Run

Apple M1 Pro, darwin/arm64, Go 1.24.3, `-benchtime=1s -count=3`.
Values are the median of three runs and are provided only as a regression
baseline for this machine.

| Topology | Size | DagBee | go-taskflow | Lower `ns/op` |
| --- | ---: | ---: | ---: | --- |
| Wide | 32 nodes | 48.3 us/op | 19.9 us/op | go-taskflow, 2.4x |
| Wide | 128 nodes | 193.7 us/op | 78.6 us/op | go-taskflow, 2.5x |
| Wide | 512 nodes | 839.1 us/op | 416.9 us/op | go-taskflow, 2.0x |
| Deep | 32 nodes | 46.3 us/op | 67.2 us/op | DagBee, 1.4x |
| Deep | 128 nodes | 158.6 us/op | 276.2 us/op | DagBee, 1.7x |
| Deep | 512 nodes | 620.1 us/op | 1,096.4 us/op | DagBee, 1.8x |
| Fan-out/fan-in | 16 branches | 32.7 us/op | 14.9 us/op | go-taskflow, 2.2x |
| Fan-out/fan-in | 64 branches | 106.4 us/op | 44.3 us/op | go-taskflow, 2.4x |
| Fan-out/fan-in | 256 branches | 496.9 us/op | 222.1 us/op | go-taskflow, 2.2x |
| Subflow | 3 child nodes | 20.2 us/op | 9.0 us/op | go-taskflow, 2.2x |

DagBee allocates more bytes per operation in every scenario because every run
creates execution context and result state. At larger node counts it performs
fewer allocations than go-taskflow, but retains a higher total byte count.
