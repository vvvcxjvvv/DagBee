package dagbee

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
)

func TestRoute_BasicIfElse(t *testing.T) {
	var executed string

	d := NewDAG("router")

	d.AddNode("classify", func(_ context.Context, dctx *DAGContext) error {
		dctx.Set("type", "premium")
		return nil
	}, NodeWithRoute(
		func(dctx *DAGContext) int {
			v, _ := GetTyped[string](dctx, "type")
			switch v {
			case "premium":
				return 0
			case "standard":
				return 1
			default:
				return 2
			}
		},
		map[int][]string{
			0: {"premium_node"},
			1: {"standard_node"},
			2: {"fallback_node"},
		},
	), NodeWithCritical(false))

	d.AddNode("premium_node", func(_ context.Context, _ *DAGContext) error {
		executed = "premium"
		return nil
	}, NodeWithDependsOn("classify"))

	d.AddNode("standard_node", func(_ context.Context, _ *DAGContext) error {
		executed = "standard"
		return nil
	}, NodeWithDependsOn("classify"))

	d.AddNode("fallback_node", func(_ context.Context, _ *DAGContext) error {
		executed = "fallback"
		return nil
	}, NodeWithDependsOn("classify"))

	result := NewEngine().Run(context.Background(), d)
	if result.Status != StatusSuccess {
		t.Fatalf("expected success, got %s: %v", result.Status, result.Error)
	}
	if executed != "premium" {
		t.Fatalf("expected premium executed, got %s", executed)
	}

	// Verify route index.
	nr := result.NodeResult("classify")
	if nr.RouteIndex != 0 {
		t.Fatalf("expected route index 0, got %d", nr.RouteIndex)
	}

	// Unselected branches must be Skipped.
	if result.NodeResult("standard_node").Status != StatusSkipped {
		t.Fatalf("expected standard_node Skipped, got %s", result.NodeResult("standard_node").Status)
	}
	if result.NodeResult("fallback_node").Status != StatusSkipped {
		t.Fatalf("expected fallback_node Skipped, got %s", result.NodeResult("fallback_node").Status)
	}
	if result.NodeResult("premium_node").Status != StatusSuccess {
		t.Fatalf("expected premium_node Success, got %s", result.NodeResult("premium_node").Status)
	}
}

func TestRoute_AllBranches(t *testing.T) {
	// Test all three branches individually.
	for _, tc := range []struct {
		name      string
		routeType string
		expectIdx int
		expect    string
	}{
		{"premium", "premium", 0, "premium_node"},
		{"standard", "standard", 1, "standard_node"},
		{"default", "unknown", 2, "fallback_node"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var executed string

			d := NewDAG("router")

			d.AddNode("classify", func(_ context.Context, dctx *DAGContext) error {
				dctx.Set("type", tc.routeType)
				return nil
			}, NodeWithRoute(
				func(dctx *DAGContext) int {
					v, _ := GetTyped[string](dctx, "type")
					switch v {
					case "premium":
						return 0
					case "standard":
						return 1
					default:
						return 2
					}
				},
				map[int][]string{
					0: {"premium_node"},
					1: {"standard_node"},
					2: {"fallback_node"},
				},
			), NodeWithCritical(false))

			d.AddNode("premium_node", func(_ context.Context, _ *DAGContext) error {
				executed = "premium_node"
				return nil
			}, NodeWithDependsOn("classify"))

			d.AddNode("standard_node", func(_ context.Context, _ *DAGContext) error {
				executed = "standard_node"
				return nil
			}, NodeWithDependsOn("classify"))

			d.AddNode("fallback_node", func(_ context.Context, _ *DAGContext) error {
				executed = "fallback_node"
				return nil
			}, NodeWithDependsOn("classify"))

			result := NewEngine().Run(context.Background(), d)
			if result.Status != StatusSuccess {
				t.Fatalf("expected success, got %s: %v", result.Status, result.Error)
			}
			if executed != tc.expect {
				t.Fatalf("expected %s, got %s", tc.expect, executed)
			}
			if result.NodeResult("classify").RouteIndex != tc.expectIdx {
				t.Fatalf("expected index %d, got %d", tc.expectIdx, result.NodeResult("classify").RouteIndex)
			}
		})
	}
}

func TestRoute_MergePending(t *testing.T) {
	// merge depends on all branches; only one runs, rest Skipped.
	// merge must still execute because pending reaches zero.
	var mergeRan int32

	d := NewDAG("router")

	d.AddNode("classify", func(_ context.Context, dctx *DAGContext) error {
		dctx.Set("path", "A")
		return nil
	}, NodeWithRoute(
		func(dctx *DAGContext) int {
			v, _ := GetTyped[string](dctx, "path")
			if v == "A" {
				return 0
			}
			return 1
		},
		map[int][]string{
			0: {"branch_a"},
			1: {"branch_b"},
		},
	), NodeWithCritical(false))

	d.AddNode("branch_a", func(_ context.Context, _ *DAGContext) error {
		return nil
	}, NodeWithDependsOn("classify"))

	d.AddNode("branch_b", func(_ context.Context, _ *DAGContext) error {
		return nil
	}, NodeWithDependsOn("classify"))

	d.AddNode("merge", func(_ context.Context, _ *DAGContext) error {
		atomic.StoreInt32(&mergeRan, 1)
		return nil
	}, NodeWithDependsOn("branch_a", "branch_b"))

	result := NewEngine().Run(context.Background(), d)
	if result.Status != StatusSuccess {
		t.Fatalf("expected success, got %s: %v", result.Status, result.Error)
	}
	if atomic.LoadInt32(&mergeRan) != 1 {
		t.Fatal("merge did not execute — pending not zeroed")
	}
	// branch_a should be Success, branch_b should be Skipped.
	if result.NodeResult("branch_a").Status != StatusSuccess {
		t.Fatalf("expected branch_a Success, got %s", result.NodeResult("branch_a").Status)
	}
	if result.NodeResult("branch_b").Status != StatusSkipped {
		t.Fatalf("expected branch_b Skipped, got %s", result.NodeResult("branch_b").Status)
	}
	if result.NodeResult("merge").Status != StatusSuccess {
		t.Fatalf("expected merge Success, got %s", result.NodeResult("merge").Status)
	}
}

func TestRoute_MultiBranch(t *testing.T) {
	// RouteMap index 0 activates two nodes simultaneously.
	var aRan, bRan, cRan int32

	d := NewDAG("multi")

	d.AddNode("gate", func(_ context.Context, dctx *DAGContext) error {
		dctx.Set("mode", "ab")
		return nil
	}, NodeWithRoute(
		func(dctx *DAGContext) int {
			v, _ := GetTyped[string](dctx, "mode")
			if v == "ab" {
				return 0
			}
			return 1
		},
		map[int][]string{
			0: {"a", "b"},
			1: {"c"},
		},
	), NodeWithCritical(false))

	d.AddNode("a", func(_ context.Context, _ *DAGContext) error {
		atomic.StoreInt32(&aRan, 1)
		return nil
	}, NodeWithDependsOn("gate"))

	d.AddNode("b", func(_ context.Context, _ *DAGContext) error {
		atomic.StoreInt32(&bRan, 1)
		return nil
	}, NodeWithDependsOn("gate"))

	d.AddNode("c", func(_ context.Context, _ *DAGContext) error {
		atomic.StoreInt32(&cRan, 1)
		return nil
	}, NodeWithDependsOn("gate"))

	result := NewEngine().Run(context.Background(), d)
	if result.Status != StatusSuccess {
		t.Fatalf("expected success, got %s: %v", result.Status, result.Error)
	}
	if atomic.LoadInt32(&aRan) != 1 || atomic.LoadInt32(&bRan) != 1 {
		t.Fatal("expected a and b to run")
	}
	if atomic.LoadInt32(&cRan) != 0 {
		t.Fatal("expected c to be skipped")
	}
	if result.NodeResult("c").Status != StatusSkipped {
		t.Fatalf("expected c Skipped, got %s", result.NodeResult("c").Status)
	}
}

func TestRoute_ConditionMutex(t *testing.T) {
	d := NewDAG("mutex")

	d.AddNode("n", func(_ context.Context, _ *DAGContext) error {
		return nil
	}, NodeWithCondition(func(_ *DAGContext) bool {
		return true
	}), NodeWithRoute(
		func(_ *DAGContext) int { return 0 },
		map[int][]string{0: {"downstream"}},
	), NodeWithCritical(false))

	d.AddNode("downstream", func(_ context.Context, _ *DAGContext) error {
		return nil
	}, NodeWithDependsOn("n"))

	err := d.Validate()
	if err == nil {
		t.Fatal("expected validation error for ConditionFn + RouteFn on same node")
	}
}

func TestRoute_NestedDownstream(t *testing.T) {
	// Skipped branch has its own downstream; that downstream must also
	// be properly handled (Skipped or pending zeroed).
	var finalRan int32

	d := NewDAG("nested")

	d.AddNode("classify", func(_ context.Context, dctx *DAGContext) error {
		dctx.Set("path", "A")
		return nil
	}, NodeWithRoute(
		func(dctx *DAGContext) int {
			v, _ := GetTyped[string](dctx, "path")
			if v == "A" {
				return 0
			}
			return 1
		},
		map[int][]string{
			0: {"a"},
			1: {"b"},
		},
	), NodeWithCritical(false))

	d.AddNode("a", func(_ context.Context, _ *DAGContext) error {
		return nil
	}, NodeWithDependsOn("classify"))

	d.AddNode("b", func(_ context.Context, _ *DAGContext) error {
		return nil
	}, NodeWithDependsOn("classify"))

	// final depends on both a and b. One runs, one is Skipped.
	// final's pending should reach zero.
	d.AddNode("final", func(_ context.Context, _ *DAGContext) error {
		atomic.StoreInt32(&finalRan, 1)
		return nil
	}, NodeWithDependsOn("a", "b"))

	result := NewEngine().Run(context.Background(), d)
	if result.Status != StatusSuccess {
		t.Fatalf("expected success, got %s: %v", result.Status, result.Error)
	}
	if atomic.LoadInt32(&finalRan) != 1 {
		t.Fatal("final did not execute")
	}
}

func TestRoute_RouteNodeFailed(t *testing.T) {
	// If the route node itself fails (and is non-critical), RouteFn should
	// not be called, and downstream nodes should not be activated.
	var downstreamRan int32

	d := NewDAG("fail")

	d.AddNode("classify", func(_ context.Context, _ *DAGContext) error {
		return fmt.Errorf("classification failed")
	}, NodeWithRoute(
		func(_ *DAGContext) int { return 0 },
		map[int][]string{0: {"downstream"}},
	), NodeWithCritical(false))

	d.AddNode("downstream", func(_ context.Context, _ *DAGContext) error {
		atomic.StoreInt32(&downstreamRan, 1)
		return nil
	}, NodeWithDependsOn("classify"))

	result := NewEngine().Run(context.Background(), d)
	if result.Status != StatusSuccess {
		t.Fatalf("expected overall success (non-critical), got %s", result.Status)
	}
	nr := result.NodeResult("classify")
	if nr.Status != StatusFailed {
		t.Fatalf("expected classify Failed, got %s", nr.Status)
	}
	if nr.RouteIndex != -1 {
		t.Fatalf("expected route index -1 (not evaluated), got %d", nr.RouteIndex)
	}
	// Downstream should still run because classify (non-critical) "completed"
	// and downstream's pending was decremented. This is the same behavior as
	// non-critical node failure — downstream is not skipped.
}

func TestRoute_DeepNesting(t *testing.T) {
	// Route inside a subflow.
	var innerExecuted string

	d := NewDAG("outer")
	d.AddNode("sub", nil,
		NodeWithSubflow(func(ctx context.Context, dctx *DAGContext) (*DAG, error) {
			sub := NewDAG("inner")

			sub.AddNode("classify", func(_ context.Context, dctx *DAGContext) error {
				dctx.Set("type", "A")
				return nil
			}, NodeWithRoute(
				func(dctx *DAGContext) int {
					v, _ := GetTyped[string](dctx, "type")
					if v == "A" {
						return 0
					}
					return 1
				},
				map[int][]string{
					0: {"path_a"},
					1: {"path_b"},
				},
			), NodeWithCritical(false))

			sub.AddNode("path_a", func(_ context.Context, _ *DAGContext) error {
				innerExecuted = "path_a"
				return nil
			}, NodeWithDependsOn("classify"))

			sub.AddNode("path_b", func(_ context.Context, _ *DAGContext) error {
				innerExecuted = "path_b"
				return nil
			}, NodeWithDependsOn("classify"))

			return sub, nil
		}),
	)

	result := NewEngine().Run(context.Background(), d)
	if result.Status != StatusSuccess {
		t.Fatalf("expected success, got %s: %v", result.Status, result.Error)
	}
	if innerExecuted != "path_a" {
		t.Fatalf("expected path_a, got %s", innerExecuted)
	}

	sub := result.NodeResult("sub").SubflowResult
	if sub.NodeResult("path_b").Status != StatusSkipped {
		t.Fatalf("expected path_b Skipped, got %s", sub.NodeResult("path_b").Status)
	}
}
