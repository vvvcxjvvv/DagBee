package dagbee

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

func TestSubflow_Basic(t *testing.T) {
	d := NewDAG("parent")
	d.AddNode("prepare", func(_ context.Context, dctx *DAGContext) error {
		dctx.Set("count", 3)
		return nil
	})

	d.AddNode("pipeline", nil,
		NodeWithSubflow(func(ctx context.Context, dctx *DAGContext) (*DAG, error) {
			count, _ := GetTyped[int](dctx, "count")

			sub := NewDAG("child", WithMaxConcurrency(2))
			sub.AddNode("init", func(_ context.Context, dctx *DAGContext) error {
				dctx.Set("items", []string{})
				return nil
			})

			deps := make([]string, 0, count)
			for i := 0; i < count; i++ {
				name := fmt.Sprintf("item_%d", i)
				deps = append(deps, name)
				sub.AddNode(name, func(_ context.Context, dctx *DAGContext) error {
					return nil
				}, NodeWithDependsOn("init"), NodeWithCritical(false))
			}
			sub.AddNode("collect", func(_ context.Context, dctx *DAGContext) error {
				dctx.Set("done", true)
				return nil
			}, NodeWithDependsOn(deps...))
			return sub, nil
		}),
		NodeWithDependsOn("prepare"),
	)

	d.AddNode("finalize", func(_ context.Context, dctx *DAGContext) error {
		dctx.Set("finished", true)
		return nil
	}, NodeWithDependsOn("pipeline"))

	result := NewEngine().Run(context.Background(), d)
	if result.Status != StatusSuccess {
		t.Fatalf("expected success, got %s: %v", result.Status, result.Error)
	}

	// Check subflow result
	pipelineNR := result.NodeResult("pipeline")
	if pipelineNR == nil {
		t.Fatal("missing pipeline result")
	}
	if pipelineNR.SubflowResult == nil {
		t.Fatal("missing subflow result")
	}
	sub := pipelineNR.SubflowResult
	if sub.Status != StatusSuccess {
		t.Fatalf("expected subflow success, got %s", sub.Status)
	}
	if len(sub.Results) != 5 { // init + 3 items + collect
		t.Fatalf("expected 5 subflow results, got %d", len(sub.Results))
	}
	if sub.NodeResult("collect") == nil {
		t.Fatal("missing collect result in subflow")
	}
}

func TestSubflow_Nested(t *testing.T) {
	d := NewDAG("root")
	d.AddNode("a", nil,
		NodeWithSubflow(func(ctx context.Context, dctx *DAGContext) (*DAG, error) {
			sub := NewDAG("level1")
			sub.AddNode("b", nil,
				NodeWithSubflow(func(ctx context.Context, dctx *DAGContext) (*DAG, error) {
					sub2 := NewDAG("level2")
					sub2.AddNode("c", func(_ context.Context, dctx *DAGContext) error {
						dctx.Set("deep", "value")
						return nil
					})
					return sub2, nil
				}),
			)
			return sub, nil
		}),
	)

	eng := NewEngine(EngineWithMaxSubflowDepth(5))
	result := eng.Run(context.Background(), d)
	if result.Status != StatusSuccess {
		t.Fatalf("expected success, got %s: %v", result.Status, result.Error)
	}

	// Verify nested subflow results
	level1 := result.NodeResult("a").SubflowResult
	if level1 == nil {
		t.Fatal("missing level1 subflow result")
	}
	level2 := level1.NodeResult("b").SubflowResult
	if level2 == nil {
		t.Fatal("missing level2 subflow result")
	}
	if level2.NodeResult("c") == nil {
		t.Fatal("missing level2 node c result")
	}
}

func TestSubflow_MaxDepth(t *testing.T) {
	d := NewDAG("root")
	d.AddNode("a", nil,
		NodeWithSubflow(func(ctx context.Context, dctx *DAGContext) (*DAG, error) {
			sub := NewDAG("child")
			sub.AddNode("b", nil,
				NodeWithSubflow(func(ctx context.Context, dctx *DAGContext) (*DAG, error) {
					sub2 := NewDAG("grandchild")
					sub2.AddNode("c", func(_ context.Context, _ *DAGContext) error { return nil })
					return sub2, nil
				}),
			)
			return sub, nil
		}),
	)

	eng := NewEngine(EngineWithMaxSubflowDepth(1))
	result := eng.Run(context.Background(), d)
	// depth 0: parent, depth 1: child — grandchild at depth 2 exceeds maxDepth 1
	if result.Status != StatusFailed {
		t.Fatalf("expected failed due to depth limit, got %s", result.Status)
	}
}

func TestSubflow_PanicRecovery(t *testing.T) {
	// Node is critical by default: subflow panic → StatusPanicked → DAG fails.
	d := NewDAG("parent")
	d.AddNode("a", nil,
		NodeWithSubflow(func(ctx context.Context, dctx *DAGContext) (*DAG, error) {
			panic("subflow construction panic")
		}),
	)

	result := NewEngine().Run(context.Background(), d)
	if result.Status != StatusFailed {
		t.Fatalf("expected failed (critical node panicked), got %s", result.Status)
	}
	nr := result.NodeResult("a")
	if nr.Status != StatusPanicked {
		t.Fatalf("expected panicked, got %s", nr.Status)
	}
}

func TestSubflow_PanicInConstruction(t *testing.T) {
	d := NewDAG("parent")
	d.AddNode("a", nil,
		NodeWithSubflow(func(ctx context.Context, dctx *DAGContext) (*DAG, error) {
			panic("boom")
		}),
		NodeWithCritical(false),
	)

	result := NewEngine().Run(context.Background(), d)
	if result.Status != StatusSuccess {
		t.Fatalf("expected parent success with non-critical subflow panic, got %s", result.Status)
	}
	nr := result.NodeResult("a")
	if nr.Status != StatusPanicked {
		t.Fatalf("expected panicked, got %s", nr.Status)
	}
}

func TestSubflow_DAGContextShared(t *testing.T) {
	d := NewDAG("parent")
	d.AddNode("set_data", func(_ context.Context, dctx *DAGContext) error {
		dctx.Set("shared_key", "parent_value")
		return nil
	})

	d.AddNode("read_data", nil,
		NodeWithSubflow(func(ctx context.Context, dctx *DAGContext) (*DAG, error) {
			sub := NewDAG("child")
			sub.AddNode("read", func(_ context.Context, dctx *DAGContext) error {
				v, ok := dctx.Get("shared_key")
				if !ok || v != "parent_value" {
					return fmt.Errorf("expected shared_key=parent_value, got %v ok=%v", v, ok)
				}
				dctx.Set("child_key", "child_value")
				return nil
			})
			return sub, nil
		}),
		NodeWithDependsOn("set_data"),
	)

	d.AddNode("verify", func(_ context.Context, dctx *DAGContext) error {
		v, ok := dctx.Get("child_key")
		if !ok || v != "child_value" {
			return fmt.Errorf("expected child_key=child_value, got %v ok=%v", v, ok)
		}
		return nil
	}, NodeWithDependsOn("read_data"))

	result := NewEngine().Run(context.Background(), d)
	if result.Status != StatusSuccess {
		t.Fatalf("expected success, got %s: %v", result.Status, result.Error)
	}
}

func TestSubflow_DeadlockAvoidance(t *testing.T) {
	// maxConcurrency=2, two subflow nodes running concurrently.
	// Without work-stealing, both workers block in subflow event loops
	// and child nodes never get executed → deadlock.
	d := NewDAG("deadlock-test", WithMaxConcurrency(2))

	d.AddNode("start", func(_ context.Context, dctx *DAGContext) error {
		return nil
	})

	d.AddNode("sub_a", nil,
		NodeWithSubflow(func(ctx context.Context, dctx *DAGContext) (*DAG, error) {
			sub := NewDAG("child_a")
			for i := 0; i < 3; i++ {
				name := fmt.Sprintf("a_%d", i)
				idx := i
				sub.AddNode(name, func(_ context.Context, dctx *DAGContext) error {
					dctx.Set(fmt.Sprintf("a_result_%d", idx), idx)
					return nil
				})
			}
			return sub, nil
		}),
		NodeWithDependsOn("start"),
	)

	d.AddNode("sub_b", nil,
		NodeWithSubflow(func(ctx context.Context, dctx *DAGContext) (*DAG, error) {
			sub := NewDAG("child_b")
			for i := 0; i < 3; i++ {
				name := fmt.Sprintf("b_%d", i)
				idx := i
				sub.AddNode(name, func(_ context.Context, dctx *DAGContext) error {
					dctx.Set(fmt.Sprintf("b_result_%d", idx), idx)
					return nil
				})
			}
			return sub, nil
		}),
		NodeWithDependsOn("start"),
	)

	d.AddNode("end", func(_ context.Context, dctx *DAGContext) error {
		return nil
	}, NodeWithDependsOn("sub_a", "sub_b"))

	// Use a timeout to detect deadlock
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := NewEngine().Run(ctx, d)
	if result.Status != StatusSuccess {
		t.Fatalf("expected success, got %s: %v (possible deadlock)", result.Status, result.Error)
	}

	// Verify subflow results
	subA := result.NodeResult("sub_a").SubflowResult
	subB := result.NodeResult("sub_b").SubflowResult
	if subA == nil || subB == nil {
		t.Fatal("missing subflow results")
	}
	if len(subA.Results) != 3 || len(subB.Results) != 3 {
		t.Fatalf("expected 3 results each, got %d and %d", len(subA.Results), len(subB.Results))
	}
}

func TestSubflow_EmptyDAG(t *testing.T) {
	d := NewDAG("parent")
	d.AddNode("a", nil,
		NodeWithSubflow(func(ctx context.Context, dctx *DAGContext) (*DAG, error) {
			return nil, nil // nil DAG = skip subflow
		}),
	)

	result := NewEngine().Run(context.Background(), d)
	if result.Status != StatusSuccess {
		t.Fatalf("expected success, got %s", result.Status)
	}
	nr := result.NodeResult("a")
	if nr.Status != StatusSuccess {
		t.Fatalf("expected success, got %s", nr.Status)
	}
	if nr.SubflowResult != nil {
		t.Fatal("expected nil SubflowResult for empty subflow")
	}
}

func TestSubflow_ConstructionError(t *testing.T) {
	d := NewDAG("parent")
	d.AddNode("a", nil,
		NodeWithSubflow(func(ctx context.Context, dctx *DAGContext) (*DAG, error) {
			return nil, fmt.Errorf("construction failed")
		}),
		NodeWithCritical(false),
	)

	result := NewEngine().Run(context.Background(), d)
	if result.Status != StatusSuccess {
		t.Fatalf("expected parent success with non-critical failure, got %s", result.Status)
	}
	nr := result.NodeResult("a")
	if nr.Status != StatusFailed {
		t.Fatalf("expected failed, got %s", nr.Status)
	}
}

func TestSubflow_ConcurrencyRespected(t *testing.T) {
	var running int32
	var maxRunning int32

	d := NewDAG("parent", WithMaxConcurrency(2))
	d.AddNode("sub", nil,
		NodeWithSubflow(func(ctx context.Context, dctx *DAGContext) (*DAG, error) {
			sub := NewDAG("child", WithMaxConcurrency(2))
			for i := 0; i < 6; i++ {
				name := fmt.Sprintf("n%d", i)
				sub.AddNode(name, func(_ context.Context, _ *DAGContext) error {
					cur := atomic.AddInt32(&running, 1)
					for {
						old := atomic.LoadInt32(&maxRunning)
						if cur <= old || atomic.CompareAndSwapInt32(&maxRunning, old, cur) {
							break
						}
					}
					time.Sleep(20 * time.Millisecond)
					atomic.AddInt32(&running, -1)
					return nil
				})
			}
			return sub, nil
		}),
	)

	result := NewEngine().Run(context.Background(), d)
	if result.Status != StatusSuccess {
		t.Fatalf("expected success, got %s: %v", result.Status, result.Error)
	}
	// The subflow has maxConcurrency=2 and 6 nodes.
	// With work-stealing, inline execution may briefly allow 3 concurrent
	// (2 workers + 1 inline). But the subflow's own maxConcurrency is 2.
	// The worker pool's maxConcurrency is also 2 (parent DAG).
	// Work-stealing at depth>0 means the blocked worker (running the subflow
	// event loop) can execute child nodes inline. This is 1 worker (inline)
	// + at most 1 other worker (the second pool worker, which may also be
	// running child nodes via the shared readyCh). So max 2 concurrent.
	if atomic.LoadInt32(&maxRunning) > 3 {
		t.Fatalf("expected max ~2-3 concurrent, got %d", maxRunning)
	}
}

func TestSubflow_AsyncMaxConcurrency1(t *testing.T) {
	// maxConcurrency=1: a single worker. A subflow node with a slow child
	// and a sibling node, both with zero in-degree.
	//
	// In the async model, the worker runs SubflowFn, launches the child DAG
	// goroutine, then returns nil. The worker is immediately free to pick
	// up the sibling node. Both the sibling and the child node compete for
	// the single worker slot via wp.readyCh, and both complete.
	//
	// Total wall time ~ child duration (50ms) since sibling is near-instant
	// and runs between child dispatch rounds.
	d := NewDAG("async-test", WithMaxConcurrency(1))

	d.AddNode("sub", nil,
		NodeWithSubflow(func(ctx context.Context, dctx *DAGContext) (*DAG, error) {
			sub := NewDAG("child")
			sub.AddNode("c1", func(ctx context.Context, dctx *DAGContext) error {
				time.Sleep(50 * time.Millisecond)
				dctx.Set("child_done", true)
				return nil
			})
			return sub, nil
		}),
	)

	d.AddNode("sibling", func(_ context.Context, dctx *DAGContext) error {
		dctx.Set("sibling_done", true)
		return nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	start := time.Now()
	result := NewEngine().Run(ctx, d)
	elapsed := time.Since(start)

	if result.Status != StatusSuccess {
		t.Fatalf("expected success, got %s: %v", result.Status, result.Error)
	}

	// Both nodes must have completed.
	if result.NodeResult("sibling") == nil {
		t.Fatal("sibling node never ran")
	}
	if result.NodeResult("sub") == nil || result.NodeResult("sub").SubflowResult == nil {
		t.Fatal("subflow never completed")
	}

	// With async dispatch, total time should be ~50ms (child duration),
	// not 100ms+ (child + sibling serial). Allow generous margin.
	if elapsed > 200*time.Millisecond {
		t.Fatalf("expected ~50ms, got %v — worker may be blocked", elapsed)
	}
}

func TestSubflow_DeepNesting(t *testing.T) {
	// 5 levels of nesting — verifies goroutine-based async execution
	// doesn't stack-overflow and correctly propagates results.
	const depth = 5
	d := NewDAG("root")
	d.AddNode("n0", nil,
		NodeWithSubflow(func(ctx context.Context, dctx *DAGContext) (*DAG, error) {
			return buildNestedSubflow(depth, 1, dctx), nil
		}),
	)

	eng := NewEngine(EngineWithMaxSubflowDepth(depth + 1))
	result := eng.Run(context.Background(), d)
	if result.Status != StatusSuccess {
		t.Fatalf("expected success, got %s: %v", result.Status, result.Error)
	}

	// Walk the nesting chain: n0 -> n1 -> n2 -> ... -> n{depth}
	sub := result.NodeResult("n0").SubflowResult
	for i := 1; i <= depth; i++ {
		if sub == nil {
			t.Fatalf("missing subflow at depth %d", i)
		}
		name := fmt.Sprintf("n%d", i)
		nr := sub.NodeResult(name)
		if nr == nil {
			t.Fatalf("missing node %s at depth %d", name, i)
		}
		if i < depth {
			sub = nr.SubflowResult
		}
	}
}

func buildNestedSubflow(targetDepth, currentDepth int, dctx *DAGContext) *DAG {
	name := fmt.Sprintf("n%d", currentDepth)
	sub := NewDAG(fmt.Sprintf("level%d", currentDepth))
	if currentDepth >= targetDepth {
		sub.AddNode(name, func(_ context.Context, dctx *DAGContext) error {
			dctx.Set(fmt.Sprintf("deep_%d", currentDepth), true)
			return nil
		})
		return sub
	}
	sub.AddNode(name, nil,
		NodeWithSubflow(func(ctx context.Context, dctx *DAGContext) (*DAG, error) {
			return buildNestedSubflow(targetDepth, currentDepth+1, dctx), nil
		}),
	)
	return sub
}
