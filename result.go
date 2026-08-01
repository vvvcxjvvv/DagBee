package dagbee

import (
	"sync"
	"time"
)

// NodeResult captures the outcome of a single node execution.
type NodeResult struct {
	NodeName           string
	Status             NodeStatus
	StartTime          time.Time
	EndTime            time.Time
	Duration           time.Duration
	Error              error
	RetryCount         int        // number of retries actually performed (0 = first attempt succeeded)
	SubflowResult      *DagResult // subflow node's child DAG result; nil for normal nodes
	RouteIndex         int        // route node's selected branch index; -1 for non-route nodes
	ConditionEvaluated bool       // true when ConditionFn was evaluated for this node
	ConditionMatched   bool       // ConditionFn result; meaningful only when ConditionEvaluated is true
	SkipReason         string     // non-empty when the node was skipped by a condition, route, or cancellation
}

// Reset clears all fields, preparing the result for pool reuse.
func (r *NodeResult) Reset() {
	r.NodeName = ""
	r.Status = StatusPending
	r.StartTime = time.Time{}
	r.EndTime = time.Time{}
	r.Duration = 0
	r.Error = nil
	r.RetryCount = 0
	r.RouteIndex = -1
	r.ConditionEvaluated = false
	r.ConditionMatched = false
	r.SkipReason = ""
	if r.SubflowResult != nil {
		releaseDagResultRecursive(r.SubflowResult)
		r.SubflowResult = nil
	}
}

// DagResult captures the outcome of an entire DAG execution.
type DagResult struct {
	DagName   string
	Status    NodeStatus // overall: StatusSuccess or StatusFailed
	StartTime time.Time
	EndTime   time.Time
	Duration  time.Duration
	Results   map[string]*NodeResult
	Error     error // set when the DAG is aborted (critical failure or timeout)
	dag       *DAG  // retained until ReleaseDagResult for execution-aware topology export
}

// NodeResult returns the result for a specific node, or nil if not found.
func (r *DagResult) NodeResult(name string) *NodeResult {
	if r.Results == nil {
		return nil
	}
	return r.Results[name]
}

// SuccessCount returns the number of nodes that completed successfully
// (including those that succeeded after retries).
func (r *DagResult) SuccessCount() int {
	count := 0
	for _, nr := range r.Results {
		if nr.Status == StatusSuccess || nr.Status == StatusRetried {
			count++
		}
	}
	return count
}

// FailedCount returns the number of nodes that failed or panicked.
func (r *DagResult) FailedCount() int {
	count := 0
	for _, nr := range r.Results {
		if nr.Status == StatusFailed || nr.Status == StatusPanicked {
			count++
		}
	}
	return count
}

// SkippedCount returns the number of nodes that were skipped.
func (r *DagResult) SkippedCount() int {
	count := 0
	for _, nr := range r.Results {
		if nr.Status == StatusSkipped {
			count++
		}
	}
	return count
}

// Reset clears all fields and releases child NodeResults back to the pool.
func (r *DagResult) Reset() {
	r.DagName = ""
	r.Status = StatusPending
	r.StartTime = time.Time{}
	r.EndTime = time.Time{}
	r.Duration = 0
	r.Error = nil
	r.dag = nil
	for k, nr := range r.Results {
		releaseNodeResult(nr)
		delete(r.Results, k)
	}
}

// releaseDagResultRecursive recursively releases a DagResult and all nested
// SubflowResults back to their respective pools. Handles arbitrary nesting depth.
func releaseDagResultRecursive(r *DagResult) {
	r.Reset()
	dagResultPool.Put(r)
}

// ---------- sync.Pool for object reuse ----------

var nodeResultPool = sync.Pool{
	New: func() interface{} { return &NodeResult{RouteIndex: -1} },
}

var dagResultPool = sync.Pool{
	New: func() interface{} {
		return &DagResult{Results: make(map[string]*NodeResult, 16)}
	},
}

func acquireNodeResult() *NodeResult {
	return nodeResultPool.Get().(*NodeResult)
}

func releaseNodeResult(r *NodeResult) {
	r.Reset()
	nodeResultPool.Put(r)
}

// AcquireDagResult obtains a DagResult from the pool.
// Call ReleaseDagResult after consuming the result to return it.
func AcquireDagResult() *DagResult {
	return dagResultPool.Get().(*DagResult)
}

// ReleaseDagResult returns a DagResult to the pool for reuse.
func ReleaseDagResult(r *DagResult) {
	r.Reset()
	dagResultPool.Put(r)
}
