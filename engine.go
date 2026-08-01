package dagbee

import (
	"context"
	"fmt"
	"runtime"
	"sync/atomic"
	"time"
)

// defaultMaxSubflowDepth is the default maximum nesting depth for subflows.
const defaultMaxSubflowDepth = 10

// Engine orchestrates the execution of a DAG: validation, topological
// scheduling, concurrency control, retry/fallback, and result collection.
type Engine struct {
	dctxShards      int
	maxSubflowDepth int
	logger          Logger
}

// NewEngine creates an Engine with the given options.
func NewEngine(opts ...EngineOption) *Engine {
	e := &Engine{
		logger:          noopLogger{},
		maxSubflowDepth: defaultMaxSubflowDepth,
	}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// Run executes the DAG and returns the aggregated result.
// The context controls the overall lifetime; cancelling it triggers graceful shutdown.
func (e *Engine) Run(ctx context.Context, d *DAG) *DagResult {
	result := AcquireDagResult()
	result.DagName = d.name
	result.StartTime = time.Now()

	// --- Validate ---
	if err := d.Validate(); err != nil {
		result.Status = StatusFailed
		result.Error = err
		result.EndTime = time.Now()
		result.Duration = result.EndTime.Sub(result.StartTime)
		return result
	}

	// --- DAGContext ---
	var dctx *DAGContext
	n := e.dctxShards
	if n > 0 {
		dctx = newDAGContextWithShards(n)
	} else {
		dctx = NewDAGContext()
	}

	logger := d.logger
	if _, ok := logger.(noopLogger); ok {
		logger = e.logger
	}

	// --- Shared worker pool (per-Run, entire execution tree) ---
	maxConc := d.maxConcurrency
	if maxConc <= 0 {
		maxConc = runtime.NumCPU()
	}
	wp := newWorkerPool(maxConc)
	wp.start()

	result = e.executeDAG(ctx, d, dctx, wp, nil, 0, logger)

	wp.stop()
	wp.wait()

	// --- Finalize ---
	d.hooks.OnDAGComplete(ctx, result)
	logger.Info("DAG completed",
		"dag", d.name,
		"status", result.Status,
		"duration", result.Duration,
		"success", result.SuccessCount(),
		"failed", result.FailedCount(),
		"skipped", result.SkippedCount(),
	)

	return result
}

// executeDAG runs a single DAG layer within the execution tree. It is
// reentrant: subflow nodes call it recursively with a child DAG, sharing
// the same dctx and worker pool. The event loop uses work-stealing to
// avoid deadlock when workers are blocked in nested subflow event loops.
func (e *Engine) executeDAG(
	ctx context.Context,
	d *DAG,
	dctx *DAGContext,
	wp *workerPool,
	parentHooks *HookChain,
	depth int,
	logger Logger,
) *DagResult {
	result := AcquireDagResult()
	result.DagName = d.name
	result.StartTime = time.Now()

	// --- Context with optional DAG-level timeout ---
	// context.WithTimeout automatically takes min(parent timeout, child timeout).
	dagCtx, dagCancel := context.WithCancel(ctx)
	if d.timeout > 0 {
		dagCtx, dagCancel = context.WithTimeout(ctx, d.timeout)
	}
	defer dagCancel()

	// --- Merge parent hooks ---
	if parentHooks != nil {
		for _, h := range parentHooks.hooks {
			d.hooks.Add(h)
		}
	}

	total := len(d.nodes)

	// --- Pending dependency counts (atomic int32 per node) ---
	pending := make(map[string]*int32, total)
	for name := range d.nodes {
		count := int32(len(d.reverseEdges[name]))
		pending[name] = &count
	}

	doneCh := make(chan *NodeResult, total)
	started := make(map[string]bool, total)
	scheduler := newPriorityScheduler()
	var dagFailed int32

	// Enqueue all nodes with zero in-degree.
	for name, count := range pending {
		if *count == 0 {
			scheduler.Enqueue(d.nodes[name])
		}
	}

	// dispatchReady sends the next ready node to a worker via wp.readyCh.
	// Called from the event loop's select alongside doneCh.
	dispatchReady := func() *execTask {
		if scheduler.Len() == 0 {
			return nil
		}
		return &execTask{
			node:   scheduler.Peek(),
			doneCh: doneCh,
			exec: func(node *Node) *NodeResult {
				return e.executeNode(dagCtx, node, dctx, d, wp, depth, logger)
			},
		}
	}

	// commitDispatch removes the peeked node from the scheduler and marks
	// it as started. Called after a successful readyCh send.
	commitDispatch := func() {
		node := scheduler.Dequeue()
		started[node.Name] = true
	}

	// Initial dispatch of zero-in-degree nodes.
	nextTask := dispatchReady()

	// --- Main event loop ---
	// The event loop selects on:
	//   - doneCh: process completed node results
	//   - wp.readyCh send: dispatch a ready node to an idle worker
	//   - stealCh (depth > 0 only): steal and execute a task from another layer
	//   - dagCtx.Done(): timeout/cancellation
	//
	// The readyCh send case handles both initial dispatch and post-completion
	// dispatch. It is nil when there are no ready nodes, disabling the case.
	//
	// At depth > 0, stealCh = wp.readyCh (enabling work-stealing to prevent
	// deadlock when all workers are blocked in subflow event loops).
	// At depth 0, stealCh = nil (the Run goroutine is not a worker).
	var stealCh chan *execTask
	if depth > 0 {
		stealCh = wp.readyCh
	}

	var readySendCh chan *execTask
	if nextTask != nil {
		readySendCh = wp.readyCh
	}

	completed := 0
	for completed < total {
		select {
		case readySendCh <- nextTask:
			commitDispatch()
			nextTask = dispatchReady()
			readySendCh = wp.readyCh
			if nextTask == nil {
				readySendCh = nil
			}

		case nr := <-doneCh:
			completed++
			result.Results[nr.NodeName] = nr

			// Critical failure → cancel the entire DAG.
			if (nr.Status == StatusFailed || nr.Status == StatusPanicked) &&
				d.nodes[nr.NodeName] != nil && d.nodes[nr.NodeName].Critical {
				atomic.StoreInt32(&dagFailed, 1)
				if result.Error == nil {
					result.Error = fmt.Errorf("critical node %q failed: %w", nr.NodeName, nr.Error)
				}
				dagCancel()
				for name := range d.nodes {
					if !started[name] && result.Results[name] == nil {
						started[name] = true
						completed++
						skipNR := acquireNodeResult()
						skipNR.NodeName = name
						skipNR.Status = StatusSkipped
						result.Results[name] = skipNR
						d.hooks.OnNodeSkip(dagCtx, name, "DAG cancelled due to critical node failure")
					}
				}
				readySendCh = nil
				nextTask = nil
				continue
			}

			// Propagate completion to downstream nodes.
			if atomic.LoadInt32(&dagFailed) == 0 {
				for _, downstream := range d.edges[nr.NodeName] {
					if started[downstream] {
						continue
					}
					newCount := atomic.AddInt32(pending[downstream], -1)
					if newCount == 0 {
						scheduler.Enqueue(d.nodes[downstream])
					}
				}
			}
			// Prepare next dispatch if not already pending.
			if nextTask == nil {
				nextTask = dispatchReady()
				if nextTask != nil {
					readySendCh = wp.readyCh
				}
			}

		case task := <-stealCh:
			// Work-stealing (depth > 0 only): execute a ready task from any
			// DAG layer while waiting for our own doneCh. Prevents deadlock
			// when all pool workers are blocked in subflow event loops.
			task.doneCh <- task.exec(task.node)

		case <-dagCtx.Done():
			if atomic.CompareAndSwapInt32(&dagFailed, 0, 1) && result.Error == nil {
				result.Error = dagCtx.Err()
			}
			for name := range d.nodes {
				if !started[name] && result.Results[name] == nil {
					started[name] = true
					completed++
					skipNR := acquireNodeResult()
					skipNR.NodeName = name
					skipNR.Status = StatusSkipped
					result.Results[name] = skipNR
					d.hooks.OnNodeSkip(dagCtx, name, "DAG context done")
				}
			}
			readySendCh = nil
			nextTask = nil
		}
	}

	// --- Finalize result ---
	result.EndTime = time.Now()
	result.Duration = result.EndTime.Sub(result.StartTime)
	if atomic.LoadInt32(&dagFailed) != 0 {
		result.Status = StatusFailed
	} else {
		result.Status = StatusSuccess
	}

	return result
}

// executeNode runs a single node with panic recovery, condition checking,
// retry logic, fallback handling, and subflow execution. Returns a fully
// populated NodeResult.
func (e *Engine) executeNode(
	ctx context.Context,
	n *Node,
	dctx *DAGContext,
	d *DAG,
	wp *workerPool,
	depth int,
	logger Logger,
) (nr *NodeResult) {
	nr = acquireNodeResult()
	nr.NodeName = n.Name
	nr.Status = StatusRunning
	nr.StartTime = time.Now()

	// Panic recovery — ensures the goroutine never crashes the process.
	defer func() {
		if r := recover(); r != nil {
			nr.Status = StatusPanicked
			nr.Error = &PanicError{
				NodeName:   n.Name,
				Value:      r,
				Stacktrace: capturePanicStack(),
			}
			logger.Error("node panicked", "node", n.Name, "panic", r)
		}
		nr.EndTime = time.Now()
		nr.Duration = nr.EndTime.Sub(nr.StartTime)
		d.hooks.AfterNode(ctx, n.Name, nr)
	}()

	d.hooks.BeforeNode(ctx, n.Name)

	// Condition gate.
	if n.ConditionFn != nil && !n.ConditionFn(dctx) {
		nr.Status = StatusSkipped
		d.hooks.OnNodeSkip(ctx, n.Name, "condition not met")
		return nr
	}

	// --- Subflow branch ---
	if n.SubflowFn != nil {
		subDAG, err := n.SubflowFn(ctx, dctx)
		if err != nil {
			nr.Status = StatusFailed
			nr.Error = fmt.Errorf("subflow %q construction failed: %w", n.Name, err)
			return nr
		}
		if subDAG == nil {
			nr.Status = StatusSuccess // empty subflow
			return nr
		}
		if err := subDAG.Validate(); err != nil {
			nr.Status = StatusFailed
			nr.Error = fmt.Errorf("subflow %q validation failed: %w", n.Name, err)
			return nr
		}
		if depth >= e.maxSubflowDepth {
			nr.Status = StatusFailed
			nr.Error = fmt.Errorf("subflow %q exceeds max depth %d", n.Name, e.maxSubflowDepth)
			return nr
		}

		// Recursively execute child DAG with shared dctx and wp.
		// The child's event loop uses work-stealing to avoid deadlock.
		subResult := e.executeDAG(ctx, subDAG, dctx, wp, d.hooks, depth+1, logger)
		nr.SubflowResult = subResult

		if subResult.Status == StatusFailed {
			nr.Status = StatusFailed
			nr.Error = subResult.Error
		} else {
			nr.Status = StatusSuccess
		}
		return nr
	}

	// --- Normal node: execute with retries ---
	retries, err := e.executeWithRetries(ctx, n, dctx, logger)
	nr.RetryCount = retries

	if err == nil {
		if retries > 0 {
			nr.Status = StatusRetried
		} else {
			nr.Status = StatusSuccess
		}
		return nr
	}

	// All attempts exhausted — try fallback.
	if n.FallbackFn != nil {
		if fallbackErr := n.FallbackFn(ctx, dctx); fallbackErr == nil {
			logger.Info("node fallback succeeded", "node", n.Name)
			nr.Status = StatusSuccess
			nr.Error = nil
			return nr
		}
		logger.Warn("node fallback also failed", "node", n.Name)
	}

	nr.Status = StatusFailed
	nr.Error = err
	return nr
}

// executeWithRetries runs the node function up to 1 + RetryCount times.
// Returns (retries_performed, last_error).
func (e *Engine) executeWithRetries(
	ctx context.Context,
	n *Node,
	dctx *DAGContext,
	logger Logger,
) (retryCount int, err error) {
	maxAttempts := 1 + n.RetryCount

	for attempt := 0; attempt < maxAttempts; attempt++ {
		// Wait for retry interval (skipped on first attempt).
		if attempt > 0 {
			interval := e.retryInterval(n, attempt)
			timer := time.NewTimer(interval)
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				return attempt - 1, ctx.Err()
			}
			logger.Info("retrying node",
				"node", n.Name, "attempt", attempt+1, "max", maxAttempts)
		}

		err = e.executeAttempt(ctx, n, dctx)
		if err == nil {
			return attempt, nil
		}

		// Don't retry if context is already done.
		if ctx.Err() != nil {
			return attempt, err
		}
	}

	return maxAttempts - 1, err
}

// executeAttempt runs the node function once, wrapping it with a per-attempt timeout.
func (e *Engine) executeAttempt(ctx context.Context, n *Node, dctx *DAGContext) error {
	if n.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, n.Timeout)
		defer cancel()
	}
	return n.Fn(ctx, dctx)
}

// retryInterval calculates the wait duration before the given retry attempt.
func (e *Engine) retryInterval(n *Node, attempt int) time.Duration {
	switch n.RetryStrategy {
	case RetryExponential:
		return n.RetryInterval * time.Duration(1<<uint(attempt-1))
	default: // RetryFixed
		return n.RetryInterval
	}
}
