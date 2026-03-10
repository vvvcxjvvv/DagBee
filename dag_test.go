package dagbee

import (
	"context"
	"errors"
	"testing"
)

func noop(_ context.Context, _ *SharedStore) error { return nil }

func TestNewDAG(t *testing.T) {
	d := NewDAG("test")
	if d.Name() != "test" {
		t.Fatalf("expected name %q, got %q", "test", d.Name())
	}
	if d.NodeCount() != 0 {
		t.Fatalf("expected 0 nodes, got %d", d.NodeCount())
	}
}

func TestAddNode(t *testing.T) {
	d := NewDAG("test")
	err := d.AddNode("A", noop)
	if err != nil {
		t.Fatal(err)
	}
	if d.NodeCount() != 1 {
		t.Fatalf("expected 1 node, got %d", d.NodeCount())
	}
}

func TestAddNode_Duplicate(t *testing.T) {
	d := NewDAG("test")
	d.AddNode("A", noop)
	err := d.AddNode("A", noop)
	if !errors.Is(err, ErrDuplicateNode) {
		t.Fatalf("expected ErrDuplicateNode, got %v", err)
	}
}

func TestAddNode_NilFunc(t *testing.T) {
	d := NewDAG("test")
	err := d.AddNode("A", nil)
	if !errors.Is(err, ErrNodeFuncNil) {
		t.Fatalf("expected ErrNodeFuncNil, got %v", err)
	}
}

func TestValidate_Empty(t *testing.T) {
	d := NewDAG("test")
	err := d.Validate()
	if !errors.Is(err, ErrEmptyDAG) {
		t.Fatalf("expected ErrEmptyDAG, got %v", err)
	}
}

func TestValidate_MissingDependency(t *testing.T) {
	d := NewDAG("test")
	d.AddNode("A", noop, NodeWithDependsOn("B"))
	err := d.Validate()
	if !errors.Is(err, ErrDependencyMissing) {
		t.Fatalf("expected ErrDependencyMissing, got %v", err)
	}
}

func TestValidate_CycleDetection(t *testing.T) {
	d := NewDAG("test")
	d.AddNode("A", noop, NodeWithDependsOn("C"))
	d.AddNode("B", noop, NodeWithDependsOn("A"))
	d.AddNode("C", noop, NodeWithDependsOn("B"))
	err := d.Validate()
	if !errors.Is(err, ErrCycleDetected) {
		t.Fatalf("expected ErrCycleDetected, got %v", err)
	}
}

func TestValidate_SelfCycle(t *testing.T) {
	d := NewDAG("test")
	d.AddNode("A", noop, NodeWithDependsOn("A"))
	err := d.Validate()
	if !errors.Is(err, ErrCycleDetected) {
		t.Fatalf("expected ErrCycleDetected, got %v", err)
	}
}

func TestValidate_Valid(t *testing.T) {
	d := NewDAG("test")
	d.AddNode("A", noop)
	d.AddNode("B", noop, NodeWithDependsOn("A"))
	d.AddNode("C", noop, NodeWithDependsOn("A"))
	d.AddNode("D", noop, NodeWithDependsOn("B", "C"))
	if err := d.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestTopologicalSort(t *testing.T) {
	d := NewDAG("test")
	d.AddNode("A", noop)
	d.AddNode("B", noop, NodeWithDependsOn("A"))
	d.AddNode("C", noop, NodeWithDependsOn("B"))

	sorted, err := d.topologicalSort()
	if err != nil {
		t.Fatal(err)
	}
	if len(sorted) != 3 {
		t.Fatalf("expected 3 nodes, got %d", len(sorted))
	}

	idx := map[string]int{}
	for i, n := range sorted {
		idx[n.Name] = i
	}
	if idx["A"] >= idx["B"] || idx["B"] >= idx["C"] {
		t.Fatalf("invalid order: A=%d B=%d C=%d", idx["A"], idx["B"], idx["C"])
	}
}

func TestTopologicalLayers(t *testing.T) {
	d := NewDAG("test")
	d.AddNode("A", noop)
	d.AddNode("B", noop)
	d.AddNode("C", noop, NodeWithDependsOn("A", "B"))
	d.AddNode("D", noop, NodeWithDependsOn("C"))

	layers, err := d.topologicalLayers()
	if err != nil {
		t.Fatal(err)
	}
	if len(layers) != 3 {
		t.Fatalf("expected 3 layers, got %d", len(layers))
	}
	if len(layers[0]) != 2 {
		t.Fatalf("expected 2 nodes in layer 0, got %d", len(layers[0]))
	}
}

func TestVisualize(t *testing.T) {
	d := NewDAG("test", WithMaxConcurrency(4), WithTimeout(0))
	d.AddNode("A", noop)
	d.AddNode("B", noop, NodeWithDependsOn("A"))
	d.AddNode("C", noop, NodeWithDependsOn("A"))

	output := d.Visualize()
	if output == "" {
		t.Fatal("expected non-empty visualization")
	}
	if !containsSubstring(output, "Layer 0") || !containsSubstring(output, "Layer 1") {
		t.Fatalf("visualization missing layer info:\n%s", output)
	}
}

func TestGetNode(t *testing.T) {
	d := NewDAG("test")
	d.AddNode("A", noop, NodeWithPriority(5))

	n := d.GetNode("A")
	if n == nil {
		t.Fatal("expected to find node A")
	}
	if n.Priority != 5 {
		t.Fatalf("expected priority 5, got %d", n.Priority)
	}

	if d.GetNode("Z") != nil {
		t.Fatal("expected nil for non-existent node")
	}
}

func containsSubstring(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && findSubstring(s, substr))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
