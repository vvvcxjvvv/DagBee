package dagbee

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// --- DOT tests ---

func TestExportDOT_BasicDAG(t *testing.T) {
	d := NewDAG("test")
	d.AddNode("A", func(_ context.Context, _ *DAGContext) error { return nil })
	d.AddNode("B", func(_ context.Context, _ *DAGContext) error { return nil },
		NodeWithDependsOn("A"))
	d.AddNode("C", func(_ context.Context, _ *DAGContext) error { return nil },
		NodeWithDependsOn("A"))
	d.AddNode("D", func(_ context.Context, _ *DAGContext) error { return nil },
		NodeWithDependsOn("B", "C"))

	dot := d.ExportDOT()
	if !strings.Contains(dot, "digraph") {
		t.Fatal("expected digraph header")
	}
	if !strings.Contains(dot, `"A"`) {
		t.Fatal("missing node A")
	}
	if !strings.Contains(dot, `"A" -> "B"`) {
		t.Fatal("missing edge A->B")
	}
}

func TestExportDOT_CriticalNode(t *testing.T) {
	d := NewDAG("test")
	d.AddNode("A", func(_ context.Context, _ *DAGContext) error { return nil },
		NodeWithCritical(true))
	dot := d.ExportDOT()
	if !strings.Contains(dot, "lightcoral") {
		t.Fatal("critical node should have lightcoral fill")
	}
	if !strings.Contains(dot, "critical") {
		t.Fatal("missing critical label")
	}
}

func TestExportDOT_ConditionNode(t *testing.T) {
	d := NewDAG("test")
	d.AddNode("A", func(_ context.Context, _ *DAGContext) error { return nil },
		NodeWithCondition(func(_ *DAGContext) bool { return true }),
		NodeWithCritical(false))
	dot := d.ExportDOT()
	if !strings.Contains(dot, "diamond") {
		t.Fatal("condition node should have diamond shape")
	}
	if !strings.Contains(dot, "lightyellow") {
		t.Fatal("condition node should have lightyellow fill")
	}
}

func TestExportDOT_RouteNode(t *testing.T) {
	d := NewDAG("test")
	d.AddNode("classify", func(_ context.Context, _ *DAGContext) error { return nil },
		NodeWithRoute(func(_ *DAGContext) int { return 0 }, map[int][]string{0: {"a"}, 1: {"b"}}),
		NodeWithCritical(false))
	d.AddNode("a", func(_ context.Context, _ *DAGContext) error { return nil }, NodeWithDependsOn("classify"))
	d.AddNode("b", func(_ context.Context, _ *DAGContext) error { return nil }, NodeWithDependsOn("classify"))

	dot := d.ExportDOT()
	if !strings.Contains(dot, "box") {
		t.Fatal("route node should have box shape")
	}
	if !strings.Contains(dot, "lightblue") {
		t.Fatal("route node should have lightblue fill")
	}
	// Route edges should be dashed
	if !strings.Contains(dot, "style=dashed") {
		t.Fatal("route edges should be dashed")
	}
}

func TestExportDOT_SubflowNode(t *testing.T) {
	d := NewDAG("test")
	d.AddNode("sub", nil,
		NodeWithSubflow(func(_ context.Context, _ *DAGContext) (*DAG, error) { return nil, nil }),
		NodeWithCritical(false))
	dot := d.ExportDOT()
	if !strings.Contains(dot, "folder") {
		t.Fatal("subflow node should have folder shape")
	}
	if !strings.Contains(dot, "lightgreen") {
		t.Fatal("subflow node should have lightgreen fill")
	}
}

func TestExportDOT_GraphvizNewline(t *testing.T) {
	d := NewDAG("newline")
	d.AddNode("gate", func(_ context.Context, _ *DAGContext) error { return nil },
		NodeWithCondition(func(_ *DAGContext) bool { return true }))

	dot := d.ExportDOT()
	if strings.Contains(dot, `\\n`) {
		t.Fatalf("DOT label contains a double-escaped newline: %s", dot)
	}
	if !strings.Contains(dot, `\n(condition)`) {
		t.Fatalf("DOT label is missing the Graphviz newline escape: %s", dot)
	}
}

func TestExportDOT_ExecutionDynamicTopology(t *testing.T) {
	d := NewDAG("outer")
	d.AddNode("gate", func(_ context.Context, _ *DAGContext) error { return nil },
		NodeWithCondition(func(_ *DAGContext) bool { return true }))
	d.AddNode("route", func(_ context.Context, _ *DAGContext) error { return nil },
		NodeWithDependsOn("gate"),
		NodeWithRoute(func(_ *DAGContext) int { return 0 }, map[int][]string{
			0: {"sub"},
			1: {"skipped"},
		}),
		NodeWithCritical(false))
	d.AddNode("sub", nil,
		NodeWithDependsOn("route"),
		NodeWithSubflow(func(_ context.Context, _ *DAGContext) (*DAG, error) {
			child := NewDAG("inner")
			child.AddNode("child-a", func(_ context.Context, _ *DAGContext) error { return nil })
			child.AddNode("child-b", func(_ context.Context, _ *DAGContext) error { return nil },
				NodeWithDependsOn("child-a"))
			return child, nil
		}))
	d.AddNode("skipped", func(_ context.Context, _ *DAGContext) error { return nil },
		NodeWithDependsOn("route"))

	result := NewEngine().Run(context.Background(), d)
	defer ReleaseDagResult(result)
	dot := result.ExportDOT()

	expected := []string{
		`condition=true`,
		`route=0`,
		`Subflow: inner`,
		`subgraph "cluster_root/sub"`,
		`"root/sub/child-a"`,
		`"root/sub/child-a" -> "root/sub/child-b"`,
		`"root/route" -> "root/sub" [style=bold`,
		`"root/route" -> "root/skipped" [style=dashed`,
		`route not selected`,
	}
	for _, part := range expected {
		if !strings.Contains(dot, part) {
			t.Fatalf("execution DOT missing %q:\n%s", part, dot)
		}
	}
	if strings.Contains(dot, `\\n`) {
		t.Fatalf("execution DOT contains a double-escaped newline: %s", dot)
	}
}

func TestExportDOT_ExecutionConditionFalse(t *testing.T) {
	d := NewDAG("condition-false")
	d.AddNode("gate", func(_ context.Context, _ *DAGContext) error { return nil },
		NodeWithCondition(func(_ *DAGContext) bool { return false }),
		NodeWithCritical(false))

	result := NewEngine().Run(context.Background(), d)
	defer ReleaseDagResult(result)
	dot := result.ExportDOT()
	for _, part := range []string{"[Skipped]", "condition=false", "condition not met"} {
		if !strings.Contains(dot, part) {
			t.Fatalf("execution DOT missing %q: %s", part, dot)
		}
	}
}

// --- Chrome Trace tests ---

func TestExportChromeTrace_Basic(t *testing.T) {
	d := NewDAG("trace-test")
	d.AddNode("A", func(_ context.Context, _ *DAGContext) error { return nil })
	d.AddNode("B", func(_ context.Context, _ *DAGContext) error { return nil },
		NodeWithDependsOn("A"))

	result := NewEngine().Run(context.Background(), d)
	traceJSON, err := result.ExportChromeTrace()
	if err != nil {
		t.Fatalf("export trace failed: %v", err)
	}

	var tf traceFile
	if err := json.Unmarshal([]byte(traceJSON), &tf); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if len(tf.TraceEvents) < 3 { // 1 dag + 2 nodes
		t.Fatalf("expected at least 3 events, got %d", len(tf.TraceEvents))
	}

	// First event should be the DAG-level event.
	dagEvent := tf.TraceEvents[0]
	if dagEvent.Name != "trace-test" {
		t.Fatalf("expected dag name, got %s", dagEvent.Name)
	}
	if dagEvent.Phase != tracePhaseComplete {
		t.Fatalf("expected complete phase, got %s", dagEvent.Phase)
	}
}

func TestExportChromeTrace_SkippedNode(t *testing.T) {
	d := NewDAG("skip-test")
	d.AddNode("A", func(_ context.Context, _ *DAGContext) error { return nil })
	d.AddNode("B", func(_ context.Context, _ *DAGContext) error { return nil },
		NodeWithDependsOn("A"),
		NodeWithCondition(func(_ *DAGContext) bool { return false }),
		NodeWithCritical(false))

	result := NewEngine().Run(context.Background(), d)
	traceJSON, _ := result.ExportChromeTrace()

	var tf traceFile
	json.Unmarshal([]byte(traceJSON), &tf)

	foundSkip := false
	for _, e := range tf.TraceEvents {
		if e.Name == "B" && e.Phase == tracePhaseInstant {
			if e.Args["skip_reason"] != "condition not met" {
				t.Fatalf("unexpected skip reason: %v", e.Args["skip_reason"])
			}
			if e.Args["condition_matched"] != false {
				t.Fatalf("expected condition_matched=false, got %v", e.Args["condition_matched"])
			}
			foundSkip = true
		}
	}
	if !foundSkip {
		t.Fatal("expected instant event for skipped node B")
	}
}

func TestExportChromeTrace_RouteArgs(t *testing.T) {
	d := NewDAG("route-trace")
	d.AddNode("classify", func(_ context.Context, dctx *DAGContext) error {
		dctx.Set("type", "A")
		return nil
	}, NodeWithRoute(func(dctx *DAGContext) int {
		v, _ := GetTyped[string](dctx, "type")
		if v == "A" {
			return 0
		}
		return 1
	}, map[int][]string{0: {"a"}, 1: {"b"}}), NodeWithCritical(false))
	d.AddNode("a", func(_ context.Context, _ *DAGContext) error { return nil }, NodeWithDependsOn("classify"))
	d.AddNode("b", func(_ context.Context, _ *DAGContext) error { return nil }, NodeWithDependsOn("classify"))

	result := NewEngine().Run(context.Background(), d)
	traceJSON, _ := result.ExportChromeTrace()

	var tf traceFile
	json.Unmarshal([]byte(traceJSON), &tf)

	for _, e := range tf.TraceEvents {
		if e.Name == "classify" && e.Args != nil {
			idx, ok := e.Args["route_index"]
			if !ok {
				t.Fatal("missing route_index in classify args")
			}
			if idx != float64(0) {
				t.Fatalf("expected route_index 0, got %v", idx)
			}
			return
		}
	}
	t.Fatal("classify event not found or missing args")
}

func TestExportChromeTrace_SubflowNested(t *testing.T) {
	d := NewDAG("outer")
	d.AddNode("sub", nil,
		NodeWithSubflow(func(ctx context.Context, dctx *DAGContext) (*DAG, error) {
			sub := NewDAG("inner")
			sub.AddNode("child", func(_ context.Context, _ *DAGContext) error { return nil })
			return sub, nil
		}),
	)

	result := NewEngine().Run(context.Background(), d)
	traceJSON, _ := result.ExportChromeTrace()

	var tf traceFile
	json.Unmarshal([]byte(traceJSON), &tf)

	// Should have events for: outer dag, sub node, inner dag, child node
	names := make(map[string]bool)
	for _, e := range tf.TraceEvents {
		names[e.Name] = true
	}
	if !names["outer"] {
		t.Fatal("missing outer dag event")
	}
	if !names["sub"] {
		t.Fatal("missing sub node event")
	}
	if !names["inner"] {
		t.Fatal("missing inner dag event")
	}
	if !names["child"] {
		t.Fatal("missing child node event")
	}
}

// --- Flamegraph tests ---

func TestExportFlamegraph_Basic(t *testing.T) {
	d := NewDAG("flame-test")
	d.AddNode("A", func(_ context.Context, _ *DAGContext) error {
		time.Sleep(10 * time.Millisecond)
		return nil
	})
	d.AddNode("B", func(_ context.Context, _ *DAGContext) error {
		time.Sleep(5 * time.Millisecond)
		return nil
	}, NodeWithDependsOn("A"))

	result := NewEngine().Run(context.Background(), d)
	fg := result.ExportFlamegraph()

	lines := strings.Split(fg, "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(lines))
	}

	// Slowest node first.
	if !strings.HasPrefix(lines[0], "flame-test;A ") {
		t.Fatalf("expected A first (slowest), got %s", lines[0])
	}
	if !strings.HasPrefix(lines[1], "flame-test;B ") {
		t.Fatalf("expected B second, got %s", lines[1])
	}
}

func TestExportFlamegraph_SubflowNested(t *testing.T) {
	d := NewDAG("outer")
	d.AddNode("sub", nil,
		NodeWithSubflow(func(ctx context.Context, dctx *DAGContext) (*DAG, error) {
			sub := NewDAG("inner")
			sub.AddNode("child", func(_ context.Context, _ *DAGContext) error {
				time.Sleep(5 * time.Millisecond)
				return nil
			})
			return sub, nil
		}),
	)

	result := NewEngine().Run(context.Background(), d)
	fg := result.ExportFlamegraph()

	// Should contain: outer;sub ... and outer;sub;inner;child ...
	if !strings.Contains(fg, "outer;sub;") {
		t.Fatalf("missing sub line: %s", fg)
	}
	if !strings.Contains(fg, "outer;sub;inner;child ") {
		t.Fatalf("missing nested child line: %s", fg)
	}
}

func TestExportFlamegraph_SortOrder(t *testing.T) {
	d := NewDAG("sort-test")
	d.AddNode("fast", func(_ context.Context, _ *DAGContext) error { return nil })
	d.AddNode("slow", func(_ context.Context, _ *DAGContext) error {
		time.Sleep(20 * time.Millisecond)
		return nil
	})

	result := NewEngine().Run(context.Background(), d)
	fg := result.ExportFlamegraph()
	lines := strings.Split(fg, "\n")

	// slow should be first (longer duration).
	if !strings.HasPrefix(lines[0], "sort-test;slow ") {
		t.Fatalf("expected slow first, got %s", lines[0])
	}
}

// --- Integration: all three formats ---

func TestObservability_AllFormats(t *testing.T) {
	d := NewDAG("full-obs",
		WithMaxConcurrency(4),
		WithTimeout(5*time.Second),
	)

	d.AddNode("fetch", func(_ context.Context, dctx *DAGContext) error {
		time.Sleep(5 * time.Millisecond)
		dctx.Set("type", "premium")
		return nil
	}, NodeWithCritical(true))

	d.AddNode("route", func(_ context.Context, _ *DAGContext) error {
		return nil
	}, NodeWithRoute(func(dctx *DAGContext) int {
		v, _ := GetTyped[string](dctx, "type")
		if v == "premium" {
			return 0
		}
		return 1
	}, map[int][]string{0: {"premium"}, 1: {"standard"}}),
		NodeWithDependsOn("fetch"),
		NodeWithCritical(false))

	d.AddNode("premium", func(_ context.Context, _ *DAGContext) error {
		time.Sleep(10 * time.Millisecond)
		return nil
	}, NodeWithDependsOn("route"))

	d.AddNode("standard", func(_ context.Context, _ *DAGContext) error {
		return nil
	}, NodeWithDependsOn("route"))

	d.AddNode("output", func(_ context.Context, _ *DAGContext) error {
		return nil
	}, NodeWithDependsOn("premium", "standard"))

	// DOT should work pre-execution.
	dot := d.ExportDOT()
	if !strings.Contains(dot, "digraph") {
		t.Fatal("DOT export failed")
	}
	if !strings.Contains(dot, "style=dashed") {
		t.Fatal("route edges should be dashed in DOT")
	}

	result := NewEngine().Run(context.Background(), d)
	if result.Status != StatusSuccess {
		t.Fatalf("expected success, got %s: %v", result.Status, result.Error)
	}

	// Chrome Trace.
	traceJSON, err := result.ExportChromeTrace()
	if err != nil {
		t.Fatalf("trace export failed: %v", err)
	}
	var tf traceFile
	if err := json.Unmarshal([]byte(traceJSON), &tf); err != nil {
		t.Fatalf("invalid trace JSON: %v", err)
	}
	if len(tf.TraceEvents) < 6 {
		t.Fatalf("expected at least 6 events, got %d", len(tf.TraceEvents))
	}

	// Flamegraph.
	fg := result.ExportFlamegraph()
	if !strings.Contains(fg, "full-obs;") {
		t.Fatal("flamegraph missing dag name prefix")
	}
	if !strings.Contains(fg, "full-obs;route ") {
		t.Fatal("flamegraph missing route node")
	}

	// Verify route info in trace.
	for _, e := range tf.TraceEvents {
		if e.Name == "route" && e.Args != nil {
			if e.Args["route_index"] != float64(0) {
				t.Fatalf("expected route_index 0, got %v", e.Args["route_index"])
			}
			return
		}
	}
	t.Fatal("route event missing route_index in args")
}

func TestExportDOT_NormalNode(t *testing.T) {
	d := NewDAG("test")
	d.AddNode("A", func(_ context.Context, _ *DAGContext) error { return nil },
		NodeWithCritical(false))
	dot := d.ExportDOT()
	if !strings.Contains(dot, "white") {
		t.Fatal("non-critical normal node should have white fill")
	}
	if !strings.Contains(dot, "ellipse") {
		t.Fatal("normal node should have ellipse shape")
	}
}

func TestExportChromeTrace_EmptyDAG(t *testing.T) {
	r := &DagResult{DagName: "empty", Results: map[string]*NodeResult{}}
	traceJSON, err := r.ExportChromeTrace()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if traceJSON == "" {
		t.Fatal("expected non-empty trace JSON")
	}
}

func TestExportFlamegraph_EmptyDAG(t *testing.T) {
	r := &DagResult{DagName: "empty", Results: map[string]*NodeResult{}}
	fg := r.ExportFlamegraph()
	if fg != "" {
		t.Fatalf("expected empty flamegraph, got %s", fg)
	}
}

func TestExportChromeTrace_RetryCount(t *testing.T) {
	d := NewDAG("retry-test")
	d.AddNode("flaky", func(_ context.Context, _ *DAGContext) error {
		return fmt.Errorf("fail")
	}, NodeWithRetry(2, 1*time.Millisecond),
		NodeWithCritical(false))

	result := NewEngine().Run(context.Background(), d)
	traceJSON, _ := result.ExportChromeTrace()

	var tf traceFile
	json.Unmarshal([]byte(traceJSON), &tf)

	for _, e := range tf.TraceEvents {
		if e.Name == "flaky" && e.Args != nil {
			if e.Args["retries"] == nil {
				t.Fatal("expected retries in args")
			}
			return
		}
	}
}
