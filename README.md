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
- **SharedStore** — concurrency-safe key-value store for passing data between nodes, with generics support
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

    d.AddNode("A", func(ctx context.Context, s *dagbee.SharedStore) error {
        s.Set("greeting", "hello")
        return nil
    }, dagbee.NodeWithPriority(10))

    d.AddNode("B", func(ctx context.Context, s *dagbee.SharedStore) error {
        s.Set("name", "world")
        return nil
    }, dagbee.NodeWithPriority(5))

    d.AddNode("C", func(ctx context.Context, s *dagbee.SharedStore) error {
        g, _ := dagbee.GetTyped[string](s, "greeting")
        n, _ := dagbee.GetTyped[string](s, "name")
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
    - name: "recall_cf"
      timeout: 200ms
      retry: { count: 2, interval: 50ms, strategy: "fixed" }
      critical: true
      priority: 10
      depends_on: []
    - name: "merge_and_rank"
      timeout: 500ms
      critical: true
      depends_on: ["recall_cf"]
```

```go
registry := map[string]dagbee.NodeFunc{
    "recall_cf":      myRecallCF,
    "merge_and_rank": myMergeAndRank,
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
│ Go API   │───►│ DAG          │─────►│ Engine        │─────►│ SharedStore │
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
├── store.go            SharedStore: concurrent key-value store
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
├── store_test.go       SharedStore concurrency safety tests
├── benchmark_test.go   Performance benchmarks
│
│── Examples & Docs ──────────────────────────────────
├── examples/
│   ├── simple/         Minimal 3-node example
│   └── recommend/      Full recommendation pipeline with YAML config
│       ├── main.go
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
