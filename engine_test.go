package dagbee

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

// ---------- helpers ----------

func sleepNode(d time.Duration) NodeFunc {
	return func(ctx context.Context, store *SharedStore) error {
		select {
		case <-time.After(d):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func failNode(err error) NodeFunc {
	return func(_ context.Context, _ *SharedStore) error { return err }
}

func panicNode(val interface{}) NodeFunc {
	return func(_ context.Context, _ *SharedStore) error { panic(val) }
}

// ---------- tests ----------

func TestEngine_SimpleSerial(t *testing.T) {
	d := NewDAG("serial")
	d.AddNode("A", func(_ context.Context, s *SharedStore) error {
		s.Set("seq", "A")
		return nil
	})
	d.AddNode("B", func(_ context.Context, s *SharedStore) error {
		v, _ := GetTyped[string](s, "seq")
		s.Set("seq", v+"B")
		return nil
	}, NodeWithDependsOn("A"))
	d.AddNode("C", func(_ context.Context, s *SharedStore) error {
		v, _ := GetTyped[string](s, "seq")
		s.Set("seq", v+"C")
		return nil
	}, NodeWithDependsOn("B"))

	result := NewEngine().Run(context.Background(), d)
	if result.Status != StatusSuccess {
		t.Fatalf("expected success, got %s: %v", result.Status, result.Error)
	}
	for _, name := range []string{"A", "B", "C"} {
		if result.NodeResult(name).Status != StatusSuccess {
			t.Fatalf("node %s: expected success, got %s", name, result.NodeResult(name).Status)
		}
	}
}

func TestEngine_ParallelExecution(t *testing.T) {
	d := NewDAG("parallel", WithMaxConcurrency(4))

	var counter int32
	for i := 0; i < 4; i++ {
		name := fmt.Sprintf("N%d", i)
		d.AddNode(name, func(_ context.Context, _ *SharedStore) error {
			atomic.AddInt32(&counter, 1)
			time.Sleep(50 * time.Millisecond)
			return nil
		})
	}

	start := time.Now()
	result := NewEngine().Run(context.Background(), d)
	elapsed := time.Since(start)

	if result.Status != StatusSuccess {
		t.Fatalf("expected success, got %s", result.Status)
	}
	if atomic.LoadInt32(&counter) != 4 {
		t.Fatalf("expected 4 executions, got %d", counter)
	}
	// All 4 nodes run in parallel: should finish in ~50ms, not ~200ms.
	if elapsed > 200*time.Millisecond {
		t.Fatalf("expected parallel execution (<200ms), took %s", elapsed)
	}
}

func TestEngine_FanOutFanIn(t *testing.T) {
	d := NewDAG("fan", WithMaxConcurrency(4))
	d.AddNode("root", func(_ context.Context, s *SharedStore) error {
		s.Set("root", 1)
		return nil
	})
	for i := 0; i < 3; i++ {
		name := fmt.Sprintf("branch_%d", i)
		d.AddNode(name, func(_ context.Context, s *SharedStore) error {
			time.Sleep(30 * time.Millisecond)
			return nil
		}, NodeWithDependsOn("root"), NodeWithCritical(false))
	}
	d.AddNode("join", noop,
		NodeWithDependsOn("branch_0", "branch_1", "branch_2"))

	result := NewEngine().Run(context.Background(), d)
	if result.Status != StatusSuccess {
		t.Fatalf("expected success, got %s: %v", result.Status, result.Error)
	}
}

func TestEngine_NodeTimeout(t *testing.T) {
	d := NewDAG("timeout")
	d.AddNode("slow", func(ctx context.Context, _ *SharedStore) error {
		select {
		case <-time.After(5 * time.Second):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}, NodeWithTimeout(50*time.Millisecond), NodeWithCritical(false))

	result := NewEngine().Run(context.Background(), d)
	nr := result.NodeResult("slow")
	if nr.Status != StatusFailed {
		t.Fatalf("expected failed, got %s", nr.Status)
	}
}

func TestEngine_RetrySuccess(t *testing.T) {
	var attempts int32

	d := NewDAG("retry")
	d.AddNode("flaky", func(_ context.Context, _ *SharedStore) error {
		if atomic.AddInt32(&attempts, 1) < 3 {
			return errors.New("not yet")
		}
		return nil
	}, NodeWithRetry(3, 10*time.Millisecond))

	result := NewEngine().Run(context.Background(), d)
	if result.Status != StatusSuccess {
		t.Fatalf("expected success, got %s: %v", result.Status, result.Error)
	}
	nr := result.NodeResult("flaky")
	if nr.Status != StatusRetried {
		t.Fatalf("expected retried, got %s", nr.Status)
	}
	if nr.RetryCount < 2 {
		t.Fatalf("expected at least 2 retries, got %d", nr.RetryCount)
	}
}

func TestEngine_RetryExhausted(t *testing.T) {
	d := NewDAG("retry-fail")
	d.AddNode("always-fail", failNode(errors.New("permanent")),
		NodeWithRetry(2, 5*time.Millisecond),
		NodeWithCritical(true),
	)

	result := NewEngine().Run(context.Background(), d)
	if result.Status != StatusFailed {
		t.Fatalf("expected failed, got %s", result.Status)
	}
}

func TestEngine_CriticalFailure_CancelsDAG(t *testing.T) {
	d := NewDAG("critical", WithMaxConcurrency(4))
	d.AddNode("A", noop)
	d.AddNode("B", failNode(errors.New("boom")),
		NodeWithDependsOn("A"), NodeWithCritical(true))
	d.AddNode("C", noop, NodeWithDependsOn("B"))

	result := NewEngine().Run(context.Background(), d)
	if result.Status != StatusFailed {
		t.Fatalf("expected failed, got %s", result.Status)
	}
	if result.NodeResult("C").Status != StatusSkipped {
		t.Fatalf("expected C skipped, got %s", result.NodeResult("C").Status)
	}
}

func TestEngine_NonCriticalFailure_Continues(t *testing.T) {
	d := NewDAG("degrade", WithMaxConcurrency(4))
	d.AddNode("A", noop)
	d.AddNode("B", failNode(errors.New("degraded")),
		NodeWithDependsOn("A"), NodeWithCritical(false))
	d.AddNode("C", noop, NodeWithDependsOn("B"))

	result := NewEngine().Run(context.Background(), d)
	if result.Status != StatusSuccess {
		t.Fatalf("expected success (non-critical failure), got %s: %v", result.Status, result.Error)
	}
	if result.NodeResult("B").Status != StatusFailed {
		t.Fatalf("expected B failed, got %s", result.NodeResult("B").Status)
	}
	if result.NodeResult("C").Status != StatusSuccess {
		t.Fatalf("expected C success, got %s", result.NodeResult("C").Status)
	}
}

func TestEngine_FallbackSuccess(t *testing.T) {
	d := NewDAG("fallback")
	d.AddNode("A", failNode(errors.New("primary failed")),
		NodeWithCritical(true),
		NodeWithFallback(func(_ context.Context, s *SharedStore) error {
			s.Set("A", "fallback_data")
			return nil
		}),
	)

	result := NewEngine().Run(context.Background(), d)
	if result.Status != StatusSuccess {
		t.Fatalf("expected success via fallback, got %s: %v", result.Status, result.Error)
	}
}

func TestEngine_PanicRecovery(t *testing.T) {
	d := NewDAG("panic")
	d.AddNode("bomb", panicNode("kaboom"), NodeWithCritical(false))
	d.AddNode("safe", noop)

	result := NewEngine().Run(context.Background(), d)
	nr := result.NodeResult("bomb")
	if nr.Status != StatusPanicked {
		t.Fatalf("expected panicked, got %s", nr.Status)
	}
	var pe *PanicError
	if !errors.As(nr.Error, &pe) {
		t.Fatalf("expected PanicError, got %T", nr.Error)
	}
	if result.NodeResult("safe").Status != StatusSuccess {
		t.Fatalf("safe node should have succeeded")
	}
}

func TestEngine_DAGTimeout(t *testing.T) {
	d := NewDAG("dag-timeout", WithTimeout(80*time.Millisecond))
	d.AddNode("slow", sleepNode(5*time.Second), NodeWithCritical(true))

	start := time.Now()
	result := NewEngine().Run(context.Background(), d)
	elapsed := time.Since(start)

	if result.Status != StatusFailed {
		t.Fatalf("expected failed, got %s", result.Status)
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("expected quick timeout, took %s", elapsed)
	}
}

func TestEngine_ConcurrencyControl(t *testing.T) {
	d := NewDAG("conc", WithMaxConcurrency(2))
	var running int32
	var maxRunning int32

	for i := 0; i < 6; i++ {
		name := fmt.Sprintf("N%d", i)
		d.AddNode(name, func(_ context.Context, _ *SharedStore) error {
			cur := atomic.AddInt32(&running, 1)
			for {
				old := atomic.LoadInt32(&maxRunning)
				if cur <= old || atomic.CompareAndSwapInt32(&maxRunning, old, cur) {
					break
				}
			}
			time.Sleep(30 * time.Millisecond)
			atomic.AddInt32(&running, -1)
			return nil
		})
	}

	result := NewEngine().Run(context.Background(), d)
	if result.Status != StatusSuccess {
		t.Fatalf("expected success, got %s", result.Status)
	}
	if atomic.LoadInt32(&maxRunning) > 2 {
		t.Fatalf("expected max 2 concurrent, got %d", maxRunning)
	}
}

func TestEngine_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	d := NewDAG("cancel")
	d.AddNode("A", func(ctx context.Context, _ *SharedStore) error {
		cancel() // cancel after node A starts
		time.Sleep(20 * time.Millisecond)
		return nil
	})
	d.AddNode("B", sleepNode(5*time.Second), NodeWithDependsOn("A"))

	result := NewEngine().Run(ctx, d)
	if result.Status != StatusFailed {
		t.Fatalf("expected failed due to cancellation, got %s", result.Status)
	}
}

func TestEngine_ConditionSkip(t *testing.T) {
	d := NewDAG("cond")
	d.AddNode("A", func(_ context.Context, s *SharedStore) error {
		s.Set("flag", false)
		return nil
	})
	d.AddNode("B", func(_ context.Context, s *SharedStore) error {
		s.Set("B_ran", true)
		return nil
	},
		NodeWithDependsOn("A"),
		NodeWithCondition(func(s *SharedStore) bool {
			v, _ := GetTyped[bool](s, "flag")
			return v
		}),
		NodeWithCritical(false),
	)

	result := NewEngine().Run(context.Background(), d)
	if result.Status != StatusSuccess {
		t.Fatalf("expected success, got %s: %v", result.Status, result.Error)
	}
	if result.NodeResult("B").Status != StatusSkipped {
		t.Fatalf("expected B skipped, got %s", result.NodeResult("B").Status)
	}
}

func TestEngine_Priority(t *testing.T) {
	// With maxConcurrency=1, nodes should execute in priority order.
	d := NewDAG("prio", WithMaxConcurrency(1))

	var order []string
	addTrackingNode := func(name string, priority int) {
		d.AddNode(name, func(_ context.Context, _ *SharedStore) error {
			order = append(order, name)
			return nil
		}, NodeWithPriority(priority))
	}

	addTrackingNode("low", 1)
	addTrackingNode("mid", 5)
	addTrackingNode("high", 10)

	result := NewEngine().Run(context.Background(), d)
	if result.Status != StatusSuccess {
		t.Fatalf("expected success, got %s", result.Status)
	}
	if len(order) != 3 {
		t.Fatalf("expected 3 executions, got %d", len(order))
	}
	if order[0] != "high" {
		t.Fatalf("expected 'high' first, got %q", order[0])
	}
}

func TestEngine_Hooks(t *testing.T) {
	var beforeCalled, afterCalled, completeCalled bool

	hook := &testHook{
		beforeFn: func(_ context.Context, name string) {
			if name == "A" {
				beforeCalled = true
			}
		},
		afterFn: func(_ context.Context, name string, _ *NodeResult) {
			if name == "A" {
				afterCalled = true
			}
		},
		completeFn: func(_ context.Context, _ *DagResult) {
			completeCalled = true
		},
	}

	d := NewDAG("hooks", WithHook(hook))
	d.AddNode("A", noop)

	NewEngine().Run(context.Background(), d)

	if !beforeCalled {
		t.Fatal("BeforeNode not called")
	}
	if !afterCalled {
		t.Fatal("AfterNode not called")
	}
	if !completeCalled {
		t.Fatal("OnDAGComplete not called")
	}
}

func TestEngine_ExponentialBackoff(t *testing.T) {
	var attempts int32

	d := NewDAG("exp-backoff")
	d.AddNode("flaky", func(_ context.Context, _ *SharedStore) error {
		if atomic.AddInt32(&attempts, 1) < 3 {
			return errors.New("not yet")
		}
		return nil
	},
		NodeWithRetry(3, 10*time.Millisecond),
		NodeWithRetryStrategy(RetryExponential),
	)

	start := time.Now()
	result := NewEngine().Run(context.Background(), d)
	elapsed := time.Since(start)

	if result.Status != StatusSuccess {
		t.Fatalf("expected success, got %s", result.Status)
	}
	// With exponential: 10ms + 20ms = 30ms minimum for 2 retries.
	if elapsed < 25*time.Millisecond {
		t.Fatalf("exponential backoff too fast: %s", elapsed)
	}
}

func TestEngine_ValidationFailure(t *testing.T) {
	d := NewDAG("empty") // no nodes
	result := NewEngine().Run(context.Background(), d)
	if result.Status != StatusFailed {
		t.Fatalf("expected failed, got %s", result.Status)
	}
	if !errors.Is(result.Error, ErrEmptyDAG) {
		t.Fatalf("expected ErrEmptyDAG, got %v", result.Error)
	}
}

// ---------- test hook ----------

type testHook struct {
	beforeFn   func(context.Context, string)
	afterFn    func(context.Context, string, *NodeResult)
	skipFn     func(context.Context, string, string)
	completeFn func(context.Context, *DagResult)
}

func (h *testHook) BeforeNode(ctx context.Context, name string) {
	if h.beforeFn != nil {
		h.beforeFn(ctx, name)
	}
}
func (h *testHook) AfterNode(ctx context.Context, name string, r *NodeResult) {
	if h.afterFn != nil {
		h.afterFn(ctx, name, r)
	}
}
func (h *testHook) OnNodeSkip(ctx context.Context, name string, reason string) {
	if h.skipFn != nil {
		h.skipFn(ctx, name, reason)
	}
}
func (h *testHook) OnDAGComplete(ctx context.Context, r *DagResult) {
	if h.completeFn != nil {
		h.completeFn(ctx, r)
	}
}
