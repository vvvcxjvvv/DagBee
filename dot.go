package dagbee

import (
	"fmt"
	"sort"
	"strings"
)

// ExportDOT returns a Graphviz digraph representation of the DAG topology.
//
// Node types are distinguished by shape and color:
//   - Normal node:    ellipse, white fill
//   - Critical node:  ellipse, light-red fill
//   - Condition node: diamond, light-yellow fill
//   - Route node:     box, light-blue fill
//   - Subflow node:   folder, light-green fill
//
// Route edges (from a node with RouteFn) are drawn dashed to distinguish
// them from plain dependency edges.
func (d *DAG) ExportDOT() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "digraph %q {\n", d.name)
	sb.WriteString("  rankdir=LR;\n")
	sb.WriteString("  node [fontname=\"Helvetica\"];\n")
	sb.WriteString("  edge [fontname=\"Helvetica\"];\n\n")

	// Sort node names for deterministic output.
	names := make([]string, 0, len(d.nodes))
	for name := range d.nodes {
		names = append(names, name)
	}
	sort.Strings(names)

	// Emit nodes with type-specific styling.
	for _, name := range names {
		n := d.nodes[name]
		shape, fillcolor, style := dotNodeStyle(n)
		label := name
		if n.Critical {
			label = name + "\n(critical)"
		}
		if n.RouteFn != nil {
			label = name + "\n(route)"
		}
		if n.ConditionFn != nil {
			label = name + "\n(condition)"
		}
		if n.SubflowFn != nil {
			label = name + "\n(subflow)"
		}
		fmt.Fprintf(&sb, "  %q [label=%q, shape=%s, fillcolor=%q, style=%s];\n",
			name, label, shape, fillcolor, style)
	}
	sb.WriteString("\n")

	// Emit edges, marking route edges as dashed.
	for _, from := range names {
		tos := make([]string, len(d.edges[from]))
		copy(tos, d.edges[from])
		sort.Strings(tos)
		for _, to := range tos {
			if routeMap, isRoute := d.routeEdges[from]; isRoute {
				indexes := routeIndexesForTarget(routeMap, to)
				fmt.Fprintf(&sb, "  %q -> %q [style=dashed, color=blue, label=%q];\n",
					from, to, formatRouteIndexes(indexes))
			} else {
				fmt.Fprintf(&sb, "  %q -> %q;\n", from, to)
			}
		}
	}

	sb.WriteString("}\n")
	return sb.String()
}

// dotNodeStyle returns the Graphviz shape, fill color, and style for a node
// based on its type (subflow > route > condition > critical > normal).
func dotNodeStyle(n *Node) (shape, fillcolor, style string) {
	switch {
	case n.SubflowFn != nil:
		return "folder", "lightgreen", "filled"
	case n.RouteFn != nil:
		return "box", "lightblue", "filled"
	case n.ConditionFn != nil:
		return "diamond", "lightyellow", "filled"
	case n.Critical:
		return "ellipse", "lightcoral", "filled"
	default:
		return "ellipse", "white", "filled"
	}
}
