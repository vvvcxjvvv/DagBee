package dagbee

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

// Engine orchestrates the execution of a DAG: validation, topological
// scheduling, concurrency control, retry/fallback, and result collection.
type Engine struct {
	logger Logger
}

// NewEngine creates an Engine with the given options.
func NewEngine(opts ...EngineOption) *Engine {
	e := &Engine{logger: noopLogger{}}
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

	// --- Context with optional DAG-level timeout ---
	dagCtx, dagCancel := context.WithCancel(ctx)
	if d.timeout > 0 {
		dagCtx, dagCancel = context.WithTimeout(ctx, d.timeout)
	}
	defer dagCancel()

	store := NewSharedStore()

	logger := d.logger
	if _, ok := logger.(noopLogger); ok {
		logger = e.logger
	}

	total := len(d.nodes)

	// --- Pending dependency counts (atomic int32 per node) ---
	pending := make(map[string]*int32, total)
	for name := range d.nodes {
		count := int32(len(d.reverseEdges[name]))
		pending[name] = &count
	}

	// --- Concurrency control ---
	maxConc := d.maxConcurrency
	if maxConc <= 0 {
		maxConc = runtime.NumCPU()
	}
	sem := make(chan struct{}, maxConc)
	doneCh := make(chan *NodeResult, total)

	var wg sync.WaitGroup
	started := make(map[string]bool, total)
	scheduler := newPriorityScheduler()
	var dagFailed int32

	// Enqueue all nodes with zero in-degree.
	for name, count := range pending {
		if *count == 0 {
			scheduler.Enqueue(d.nodes[name])
		}
	}

	// launchReady starts as many ready nodes as the semaphore allows.
	// Called from the main event loop goroutine only — no concurrent access to `started`.
	launchReady := func() {
		for scheduler.Len() > 0 {
			// Try to acquire a semaphore slot without blocking.
			select {
			case sem <- struct{}{}:
				node := scheduler.Dequeue()
				if node == nil {
					<-sem
					return
				}
				started[node.Name] = true
				wg.Add(1)
				go func(n *Node) {
					defer wg.Done()
					nr := e.executeNode(dagCtx, n, store, d, logger)
					<-sem // release semaphore BEFORE sending result
					doneCh <- nr
				}(node)
			default:
				return // semaphore full — will retry after next completion
			}
		}
	}

	launchReady()

	// --- Main event loop ---
	completed := 0
	for completed < total {
		select {
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
				// Mark all unstarted nodes as skipped.
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
				launchReady()
			}

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
		}
	}

	wg.Wait()

	// --- Finalize result ---
	result.EndTime = time.Now()
	result.Duration = result.EndTime.Sub(result.StartTime)
	if atomic.LoadInt32(&dagFailed) != 0 {
		result.Status = StatusFailed
	} else {
		result.Status = StatusSuccess
	}

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

// executeNode runs a single node with panic recovery, condition checking,
// retry logic, and fallback handling. Returns a fully populated NodeResult.
func (e *Engine) executeNode(
	ctx context.Context,
	n *Node,
	store *SharedStore,
	d *DAG,
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

	// Condition gate (P2 feature).
	if n.ConditionFn != nil && !n.ConditionFn(store) {
		nr.Status = StatusSkipped
		d.hooks.OnNodeSkip(ctx, n.Name, "condition not met")
		return nr
	}

	// Execute with retries.
	retries, err := e.executeWithRetries(ctx, n, store, logger)
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
		if fallbackErr := n.FallbackFn(ctx, store); fallbackErr == nil {
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
	store *SharedStore,
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

		err = e.executeAttempt(ctx, n, store)
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
func (e *Engine) executeAttempt(ctx context.Context, n *Node, store *SharedStore) error {
	if n.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, n.Timeout)
		defer cancel()
	}
	return n.Fn(ctx, store)
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
