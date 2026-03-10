package dagbee

import "time"

// DAGOption configures a DAG using the functional options pattern.
type DAGOption func(*DAG)

// WithMaxConcurrency sets the maximum number of nodes that can execute in parallel.
// Defaults to runtime.NumCPU() if not set or set to 0.
func WithMaxConcurrency(n int) DAGOption {
	return func(d *DAG) { d.maxConcurrency = n }
}

// WithTimeout sets the overall DAG execution timeout.
func WithTimeout(timeout time.Duration) DAGOption {
	return func(d *DAG) { d.timeout = timeout }
}

// WithHook adds a lifecycle hook to the DAG.
func WithHook(h Hook) DAGOption {
	return func(d *DAG) { d.hooks.Add(h) }
}

// WithLogger injects a custom logger into the DAG.
func WithLogger(l Logger) DAGOption {
	return func(d *DAG) { d.logger = l }
}

// EngineOption configures an Engine using the functional options pattern.
type EngineOption func(*Engine)

// EngineWithLogger injects a custom logger into the engine.
func EngineWithLogger(l Logger) EngineOption {
	return func(e *Engine) { e.logger = l }
}
