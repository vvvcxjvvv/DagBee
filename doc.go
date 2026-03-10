// Package dagbee provides a lightweight, production-ready DAG (Directed Acyclic
// Graph) execution framework for Go. It is designed to be embedded in
// latency-sensitive microservices such as recommendation engines, with no
// external scheduler or infrastructure dependencies.
//
// # File Organization
//
// The source files are grouped into four logical layers:
//
//	Core ─ DAG structure and node model
//	  dag.go          DAG definition, node registration, edge management, cycle detection
//	  node.go         Node type, NodeFunc signature, NodeStatus, NodeOption helpers
//
//	Engine ─ Execution and scheduling
//	  engine.go       Execution engine: validation, scheduling loop, retry/fallback, result collection
//	  scheduler.go    Priority-based ready-queue (container/heap), SchedulerStrategy interface
//
//	Data ─ Runtime data and results
//	  store.go        SharedStore: concurrency-safe key-value store for inter-node data passing
//	  result.go       NodeResult / DagResult definitions, sync.Pool object reuse
//
//	Support ─ Cross-cutting concerns
//	  config.go       YAML configuration parsing (DAGConfig, NodeConfig, LoadDAGFromYAML)
//	  options.go      Functional options: DAGOption, EngineOption
//	  hook.go         Hook interface, NoopHook, HookChain
//	  errors.go       Sentinel errors and PanicError
//	  logger.go       Logger interface, StdLogger, noopLogger
//	  visualize.go    Text-based DAG topology visualization
package dagbee
