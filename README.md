# DagBee

A lightweight, production-ready DAG (Directed Acyclic Graph) execution framework for Go. Designed to be embedded in latency-sensitive microservices such as recommendation engines — no external scheduler, no infrastructure dependencies.

## Features

- **Parallel + serial mixed execution** — nodes with no dependency run concurrently; dependent nodes run in order
- **Concurrency control** — configurable max parallelism via semaphore
- **Priority scheduling** — ready nodes are dispatched by priority (higher = first)
- **Per-node timeout & retry** — independent timeout, retry count, and backoff strategy (fixed / exponential) per node
- **Failure strategies** — critical nodes abort the DAG; non-critical nodes degrade gracefully
- **Fallback functions** — provide default data when a node fails
- **Panic recovery** — a panicking node never crashes the process
- **Graceful shutdown** — context cancellation stops scheduling and waits for running nodes
- **DAGContext** — concurrency-safe key-value store for passing data between nodes, with generics support
- **Lifecycle hooks** — BeforeNode / AfterNode / OnNodeSkip / OnDAGComplete
- **Logger interface** — plug in zap, logrus, or any structured logger
- **YAML configuration** — declare topology and node settings in YAML; register functions in Go
- **Conditional execution** — skip nodes based on runtime predicates
- **Visualization** — text-based topological layer output for debugging
- **Object pooling** — `sync.Pool` reuse of DagResult / NodeResult to reduce GC pressure
- **Near-zero framework overhead** — ~7μs to build a 20-node DAG, ~1.3μs scheduling per node, ~360B memory per node

## Installation

```bash
go get github.com/vvvcxjvvv/DagBee@latest
```

## Quick Start

```go
package main

import (
    "context"
    "fmt"
    "time"

	"github.com/vvvcxjvvv/DagBee"
)

func main() {
    d := dagbee.NewDAG("example",
        dagbee.WithMaxConcurrency(4),
        dagbee.WithTimeout(5*time.Second),
    )

    d.AddNode("A", func(ctx context.Context, dctx *dagbee.DAGContext) error {
        dctx.Set("greeting", "hello")
        return nil
    }, dagbee.NodeWithPriority(10))

    d.AddNode("B", func(ctx context.Context, dctx *dagbee.DAGContext) error {
        dctx.Set("name", "world")
        return nil
    }, dagbee.NodeWithPriority(5))

    d.AddNode("C", func(ctx context.Context, dctx *dagbee.DAGContext) error {
        g, _ := dagbee.GetTyped[string](dctx, "greeting")
        n, _ := dagbee.GetTyped[string](dctx, "name")
        fmt.Printf("%s, %s!\n", g, n)
        return nil
    },
        dagbee.NodeWithDependsOn("A", "B"),
        dagbee.NodeWithTimeout(200*time.Millisecond),
    )

    result := dagbee.NewEngine().Run(context.Background(), d)
    fmt.Println("Status:", result.Status) // Success
}
```

## YAML Configuration

Define the topology in YAML, register the Go functions separately:

```yaml
# examples/recommend/pipeline.yaml
dag:
  name: "recommend-pipeline"
  max_concurrency: 8
  timeout: 5s
  nodes:
    # Multi-channel recall (parallel)
    - name: "recall_cf"
      timeout: 200ms
      retry: { count: 2, interval: 50ms, strategy: "fixed" }
      critical: true
      priority: 10
      depends_on: []
    - name: "recall_vec"
      timeout: 200ms
      critical: true
      depends_on: []
    - name: "recall_hot"
      timeout: 150ms
      critical: false
      depends_on: []
    # Merge + dedup
    - name: "merge"
      depends_on: ["recall_cf", "recall_vec", "recall_hot"]
    # Fill item features for ranking models
    - name: "fill_detail"
      depends_on: ["merge"]
    # Filter (blacklist / exposed / stock)
    - name: "filter"
      depends_on: ["fill_detail"]
    # Multi-model estimation (parallel)
    - name: "score_ctr"
      timeout: 300ms
      retry: { count: 1, interval: 100ms, strategy: "exponential" }
      critical: true
      depends_on: ["filter"]
    - name: "score_cvr"
      timeout: 400ms
      critical: false
      depends_on: ["filter"]
    # Multi-objective fusion (eCPM)
    - name: "fuse_rank"
      depends_on: ["score_ctr", "score_cvr"]
    # Rerank (diversity + business rules)
    - name: "rerank"
      depends_on: ["fuse_rank"]
```

```go
registry := map[string]dagbee.NodeFunc{
    "recall_cf":   myRecallCF,
    "recall_vec":  myRecallVec,
    "recall_hot":  myRecallHot,
    "merge":       myMerge,
    "fill_detail": myFillDetail,
    "filter":      myFilter,
    "score_ctr":   myScoreCTR,
    "score_cvr":   myScoreCVR,
    "fuse_rank":   myFuseRank,
    "rerank":      myRerank,
}
d, err := dagbee.LoadDAGFromYAML("examples/recommend/pipeline.yaml", registry)
```

## Node Options

| Option | Description |
|--------|-------------|
| `NodeWithTimeout(d)` | Per-attempt execution timeout |
| `NodeWithRetry(count, interval)` | Retry count and base interval |
| `NodeWithRetryStrategy(s)` | `RetryFixed` or `RetryExponential` |
| `NodeWithCritical(bool)` | `true` = abort DAG on failure; `false` = degrade |
| `NodeWithPriority(int)` | Scheduling priority (higher = first) |
| `NodeWithFallback(fn)` | Fallback function when all retries fail |
| `NodeWithDependsOn(names...)` | Upstream dependency declarations |
| `NodeWithCondition(fn)` | Predicate gate — skip when false |

## DAG Options

| Option | Description |
|--------|-------------|
| `WithMaxConcurrency(n)` | Max parallel goroutines (default: NumCPU) |
| `WithTimeout(d)` | Overall DAG execution timeout |
| `WithHook(h)` | Register a lifecycle hook |
| `WithLogger(l)` | Inject a custom Logger |

## Architecture

```
User Layer       Core Layer            Engine Layer            Data Layer
┌──────────┐    ┌──────────────┐      ┌───────────────┐      ┌─────────────┐
│ Go API   │───►│ DAG          │─────►│ Engine        │─────►│ DAGContext │
│ YAML Cfg │    │ Node         │      │ Scheduler     │      │ DagResult   │
└──────────┘    │ Validator    │      │ Executor      │      │ Hooks       │
                └──────────────┘      └───────────────┘      │ Logger      │
                                                             └─────────────┘
```

## Running Tests

```bash
go test -v -race ./...          # unit + integration tests
go test -bench=. -benchmem ./...  # benchmarks
```

## Pressure Benchmarks

```bash
go test -run '^$' -bench 'BenchmarkEngineRun_(WideDAG|DeepDAG|FanOutFanIn|RetryAmplification|ParallelRequests)$' -benchmem ./...
go test -run '^$' -bench 'BenchmarkDAGContext_HotKeyContention$' -benchmem ./...
go test -run '^$' -bench 'BenchmarkEngineRun_ParallelRequests$' -benchmem -cpuprofile cpu.out -memprofile mem.out ./...
```

Scenarios included:

- `BenchmarkEngineRun_WideDAG`: wide DAG scheduling overhead and burst parallelism
- `BenchmarkEngineRun_DeepDAG`: long dependency-chain scheduling overhead
- `BenchmarkEngineRun_FanOutFanIn`: fan-out/fan-in merge pressure
- `BenchmarkEngineRun_RetryAmplification`: retry-driven load amplification
- `BenchmarkEngineRun_ParallelRequests`: many concurrent requests, each running one DAG
- `BenchmarkDAGContext_HotKeyContention`: `DAGContext` hot-key lock contention

## Project Structure

```
dagbee/
│
│── Core ─────────────────────────────────────────────
├── dag.go              DAG definition, node registration, cycle detection
├── node.go             Node type, NodeFunc signature, NodeOption
│
│── Engine ───────────────────────────────────────────
├── engine.go           Execution engine: scheduling, retry, fallback
├── scheduler.go        Priority-based ready-queue (container/heap)
│
│── Data ─────────────────────────────────────────────
├── dagcontext.go            DAGContext: concurrent key-value store
├── result.go           NodeResult / DagResult + sync.Pool
│
│── Support ──────────────────────────────────────────
├── config.go           YAML configuration parser
├── options.go          Functional options (DAGOption, EngineOption)
├── hook.go             Hook interface + HookChain
├── errors.go           Sentinel errors + PanicError
├── logger.go           Logger interface + StdLogger
├── visualize.go        Text-based DAG topology visualization
├── doc.go              Package documentation & file organization guide
│
│── Tests ────────────────────────────────────────────
├── dag_test.go         DAG construction, cycle detection tests
├── engine_test.go      Engine scheduling, concurrency, fault tolerance tests
├── dagcontext_test.go       DAGContext concurrency safety tests
├── benchmark_test.go   Performance benchmarks
│
│── Examples & Docs ──────────────────────────────────
├── examples/
│   ├── simple/         Minimal 3-node example
│   ├── mapreduce/      Word-count MapReduce pipeline (split→map→shuffle→reduce→merge)
│   └── recommend/      Recommendation pipeline with YAML config
│       ├── main.go     Multi-channel recall → merge → fill_detail → filter
│       │               → CTR/CVR scoring → eCPM fusion → rerank
│       └── pipeline.yaml
├── docs/
│   ├── design-prompt.md
│   └── dag-frameworks-research.md
│
├── go.mod
├── go.sum
└── README.md
```

## License

MIT
