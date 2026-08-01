package dagbee

import (
	"context"
	"time"
)

// NodeStatus represents the execution state of a node.
type NodeStatus int

const (
	StatusPending  NodeStatus = iota // not yet scheduled
	StatusRunning                    // currently executing
	StatusSuccess                    // completed without error
	StatusFailed                     // all attempts failed
	StatusRetried                    // succeeded after one or more retries
	StatusSkipped                    // skipped (condition unmet or DAG cancelled)
	StatusPanicked                   // recovered from a panic
)

func (s NodeStatus) String() string {
	switch s {
	case StatusPending:
		return "Pending"
	case StatusRunning:
		return "Running"
	case StatusSuccess:
		return "Success"
	case StatusFailed:
		return "Failed"
	case StatusRetried:
		return "Retried"
	case StatusSkipped:
		return "Skipped"
	case StatusPanicked:
		return "Panicked"
	default:
		return "Unknown"
	}
}

// IsTerminal returns true if the status represents a final state.
func (s NodeStatus) IsTerminal() bool {
	switch s {
	case StatusSuccess, StatusFailed, StatusRetried, StatusSkipped, StatusPanicked:
		return true
	default:
		return false
	}
}

// RetryStrategy determines how retry intervals are calculated.
type RetryStrategy int

const (
	RetryFixed       RetryStrategy = iota // fixed interval between retries
	RetryExponential                      // exponential backoff: interval * 2^(attempt-1)
)

func (s RetryStrategy) String() string {
	switch s {
	case RetryFixed:
		return "fixed"
	case RetryExponential:
		return "exponential"
	default:
		return "unknown"
	}
}

// NodeFunc is the execution function signature for a DAG node.
//   - ctx carries timeout/cancellation signals
//   - dctx is the DAG shared context for reading upstream outputs and writing this node's output
//   - a nil return indicates success; a non-nil error triggers retry or fallback logic
type NodeFunc func(ctx context.Context, dctx *DAGContext) error

// SubflowFunc dynamically generates a child DAG at runtime.
// ctx carries timeout/cancellation signals; dctx is shared with the parent DAG.
// Return a nil DAG to skip the subflow (node marked Success).
// Return an error to fail the node immediately (no retry).
type SubflowFunc func(ctx context.Context, dctx *DAGContext) (*DAG, error)

// Node represents a single execution unit in the DAG.
type Node struct {
	Name          string
	Fn            NodeFunc
	DependsOn     []string
	Timeout       time.Duration // per-attempt timeout; 0 means inherit DAG timeout
	RetryCount    int
	RetryInterval time.Duration
	RetryStrategy RetryStrategy
	Critical      bool // true: failure aborts the DAG; false: failure degrades gracefully
	Priority      int  // higher value = higher scheduling priority
	FallbackFn    NodeFunc
	ConditionFn   func(*DAGContext) bool // when non-nil, node runs only if this returns true
	SubflowFn     SubflowFunc            // when non-nil, node is a subflow node; Fn is ignored
	RouteFn       func(*DAGContext) int  // when non-nil, node is a route node; RouteMap selects downstream branches
	RouteMap      map[int][]string       // route index → downstream node names; used with RouteFn
}

// NodeOption configures a Node using the functional options pattern.
type NodeOption func(*Node)

// NodeWithTimeout sets the per-attempt timeout for the node.
func NodeWithTimeout(d time.Duration) NodeOption {
	return func(n *Node) { n.Timeout = d }
}

// NodeWithRetry sets the retry count and interval.
func NodeWithRetry(count int, interval time.Duration) NodeOption {
	return func(n *Node) {
		n.RetryCount = count
		n.RetryInterval = interval
	}
}

// NodeWithRetryStrategy sets the retry backoff strategy (fixed or exponential).
func NodeWithRetryStrategy(s RetryStrategy) NodeOption {
	return func(n *Node) { n.RetryStrategy = s }
}

// NodeWithCritical marks the node as critical (true) or non-critical (false).
func NodeWithCritical(critical bool) NodeOption {
	return func(n *Node) { n.Critical = critical }
}

// NodeWithPriority sets the scheduling priority (higher = scheduled first).
func NodeWithPriority(priority int) NodeOption {
	return func(n *Node) { n.Priority = priority }
}

// NodeWithFallback provides a fallback function invoked when all retries are exhausted.
func NodeWithFallback(fn NodeFunc) NodeOption {
	return func(n *Node) { n.FallbackFn = fn }
}

// NodeWithDependsOn declares upstream dependencies by node name.
func NodeWithDependsOn(deps ...string) NodeOption {
	return func(n *Node) { n.DependsOn = append(n.DependsOn, deps...) }
}

// NodeWithCondition sets a predicate; the node is skipped when it returns false.
func NodeWithCondition(fn func(*DAGContext) bool) NodeOption {
	return func(n *Node) { n.ConditionFn = fn }
}

// NodeWithSubflow configures the node as a subflow that dynamically generates
// a child DAG at runtime. The child DAG shares the parent's DAGContext and
// worker pool. When set, Fn is ignored.
func NodeWithSubflow(fn SubflowFunc) NodeOption {
	return func(n *Node) { n.SubflowFn = fn }
}

// NodeWithRoute configures the node as a route node that selects downstream
// branches based on a runtime index. After the node's Fn executes, RouteFn is
// called to get an index; only the downstream nodes listed in RouteMap[index]
// are activated — all others are marked Skipped. The downstream nodes should
// also be declared via NodeWithDependsOn so that edges are registered.
//
// RouteFn and ConditionFn are mutually exclusive: a node cannot be both a
// gate (skip self) and a router (select downstream). Setting both is rejected
// by Validate.
func NodeWithRoute(fn func(*DAGContext) int, routeMap map[int][]string) NodeOption {
	return func(n *Node) {
		n.RouteFn = fn
		n.RouteMap = routeMap
	}
}
