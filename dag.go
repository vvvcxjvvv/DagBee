package dagbee

import (
	"fmt"
	"time"
)

// DAG represents a directed acyclic graph of executable nodes.
type DAG struct {
	name           string
	nodes          map[string]*Node
	edges          map[string][]string // from -> [downstream names]
	reverseEdges   map[string][]string // to   -> [upstream names]
	maxConcurrency int
	timeout        time.Duration
	hooks          *HookChain
	logger         Logger
}

// NewDAG creates a new DAG with the given name and options.
func NewDAG(name string, opts ...DAGOption) *DAG {
	d := &DAG{
		name:         name,
		nodes:        make(map[string]*Node),
		edges:        make(map[string][]string),
		reverseEdges: make(map[string][]string),
		hooks:        NewHookChain(),
		logger:       noopLogger{},
	}
	for _, opt := range opts {
		opt(d)
	}
	return d
}

// Name returns the DAG name.
func (d *DAG) Name() string { return d.name }

// NodeCount returns the number of registered nodes.
func (d *DAG) NodeCount() int { return len(d.nodes) }

// Timeout returns the DAG-level timeout.
func (d *DAG) Timeout() time.Duration { return d.timeout }

// MaxConcurrency returns the configured maximum concurrency.
func (d *DAG) MaxConcurrency() int { return d.maxConcurrency }

// AddNode registers a node with the given name, function, and options.
// Dependencies declared via NodeWithDependsOn are recorded as edges.
func (d *DAG) AddNode(name string, fn NodeFunc, opts ...NodeOption) error {
	if fn == nil {
		return fmt.Errorf("%w: %s", ErrNodeFuncNil, name)
	}
	if _, exists := d.nodes[name]; exists {
		return fmt.Errorf("%w: %s", ErrDuplicateNode, name)
	}

	node := &Node{
		Name:     name,
		Fn:       fn,
		Critical: true, // default to critical
	}
	for _, opt := range opts {
		opt(node)
	}

	d.nodes[name] = node

	for _, dep := range node.DependsOn {
		d.edges[dep] = append(d.edges[dep], name)
		d.reverseEdges[name] = append(d.reverseEdges[name], dep)
	}

	return nil
}

// Validate checks the DAG for structural correctness:
//   - at least one node
//   - all dependency references resolve to registered nodes
//   - no cycles (via Kahn's algorithm)
func (d *DAG) Validate() error {
	if len(d.nodes) == 0 {
		return ErrEmptyDAG
	}
	for name, node := range d.nodes {
		for _, dep := range node.DependsOn {
			if _, ok := d.nodes[dep]; !ok {
				return fmt.Errorf("%w: node %q depends on %q", ErrDependencyMissing, name, dep)
			}
		}
	}
	_, err := d.topologicalSort()
	return err
}

// topologicalSort produces a valid execution order using Kahn's algorithm.
// Returns ErrCycleDetected if the graph contains a cycle.
func (d *DAG) topologicalSort() ([]*Node, error) {
	inDegree := make(map[string]int, len(d.nodes))
	for name := range d.nodes {
		inDegree[name] = len(d.reverseEdges[name])
	}

	queue := make([]*Node, 0, len(d.nodes))
	for name, deg := range inDegree {
		if deg == 0 {
			queue = append(queue, d.nodes[name])
		}
	}

	sorted := make([]*Node, 0, len(d.nodes))
	for len(queue) > 0 {
		node := queue[0]
		queue = queue[1:]
		sorted = append(sorted, node)

		for _, downstream := range d.edges[node.Name] {
			inDegree[downstream]--
			if inDegree[downstream] == 0 {
				queue = append(queue, d.nodes[downstream])
			}
		}
	}

	if len(sorted) != len(d.nodes) {
		return nil, ErrCycleDetected
	}
	return sorted, nil
}

// topologicalLayers groups nodes into layers where all nodes in the same
// layer can execute in parallel (their dependencies are in earlier layers).
func (d *DAG) topologicalLayers() ([][]*Node, error) {
	inDegree := make(map[string]int, len(d.nodes))
	for name := range d.nodes {
		inDegree[name] = len(d.reverseEdges[name])
	}

	var layers [][]*Node
	remaining := len(d.nodes)

	for remaining > 0 {
		var layer []*Node
		for name, deg := range inDegree {
			if deg == 0 {
				layer = append(layer, d.nodes[name])
			}
		}
		if len(layer) == 0 {
			return nil, ErrCycleDetected
		}

		for _, node := range layer {
			delete(inDegree, node.Name)
			for _, downstream := range d.edges[node.Name] {
				inDegree[downstream]--
			}
		}

		remaining -= len(layer)
		layers = append(layers, layer)
	}

	return layers, nil
}

// GetNode returns a node by name, or nil if not found.
func (d *DAG) GetNode(name string) *Node {
	return d.nodes[name]
}

// Nodes returns all registered nodes (unordered).
func (d *DAG) Nodes() []*Node {
	nodes := make([]*Node, 0, len(d.nodes))
	for _, n := range d.nodes {
		nodes = append(nodes, n)
	}
	return nodes
}
