package dagbee

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// ExportDOT returns an execution-aware Graphviz digraph.
//
// Unlike DAG.ExportDOT, which exports the pre-execution static topology, this
// method overlays node status, condition results, selected route branches,
// durations, and recursively instantiated Subflow DAGs. Call it before
// ReleaseDagResult; releasing the result also releases its retained topology.
func (r *DagResult) ExportDOT() string {
	var sb strings.Builder
	name := r.DagName
	if name == "" && r.dag != nil {
		name = r.dag.name
	}

	fmt.Fprintf(&sb, "digraph %q {\n", name)
	sb.WriteString("  rankdir=LR;\n")
	sb.WriteString("  compound=true;\n")
	sb.WriteString("  node [fontname=\"Helvetica\"];\n")
	sb.WriteString("  edge [fontname=\"Helvetica\"];\n\n")

	if r.dag == nil {
		writeResultOnlyDOT(&sb, r, "root", "  ")
	} else {
		writeExecutionDAG(&sb, r.dag, r, "root", "  ")
	}

	sb.WriteString("}\n")
	return sb.String()
}

func writeExecutionDAG(sb *strings.Builder, d *DAG, result *DagResult, prefix, indent string) {
	names := sortedDAGNodeNames(d)

	for _, name := range names {
		n := d.nodes[name]
		nr := result.Results[name]
		shape, fillcolor, _ := dotNodeStyle(n)
		borderColor, fontColor, penWidth := executionStatusStyle(nr)
		fmt.Fprintf(sb,
			"%s%q [label=%q, shape=%s, fillcolor=%q, style=filled, color=%q, fontcolor=%q, penwidth=%.1f];\n",
			indent, executionNodeID(prefix, name), executionNodeLabel(n, nr), shape,
			fillcolor, borderColor, fontColor, penWidth,
		)
	}

	for _, from := range names {
		tos := append([]string(nil), d.edges[from]...)
		sort.Strings(tos)
		for _, to := range tos {
			attrs := executionEdgeAttributes(d, result, from, to)
			fmt.Fprintf(sb, "%s%q -> %q%s;\n",
				indent, executionNodeID(prefix, from), executionNodeID(prefix, to), attrs)
		}
	}

	for _, name := range names {
		nr := result.Results[name]
		if nr == nil || nr.SubflowResult == nil || nr.SubflowResult.dag == nil {
			continue
		}

		childResult := nr.SubflowResult
		childDAG := childResult.dag
		childPrefix := prefix + "/" + name
		fmt.Fprintf(sb, "\n%ssubgraph %q {\n", indent, "cluster_"+childPrefix)
		fmt.Fprintf(sb, "%s  label=%q;\n", indent, "Subflow: "+childDAG.name)
		fmt.Fprintf(sb, "%s  color=%q;\n", indent, "#60a5fa")
		fmt.Fprintf(sb, "%s  style=%q;\n", indent, "rounded,dashed")
		fmt.Fprintf(sb, "%s  penwidth=1.5;\n", indent)
		writeExecutionDAG(sb, childDAG, childResult, childPrefix, indent+"  ")
		fmt.Fprintf(sb, "%s}\n", indent)

		for _, root := range dagRootNodeNames(childDAG) {
			fmt.Fprintf(sb,
				"%s%q -> %q [style=dotted, color=%q, label=%q, lhead=%q];\n",
				indent,
				executionNodeID(prefix, name),
				executionNodeID(childPrefix, root),
				"#16a34a",
				"subflow",
				"cluster_"+childPrefix,
			)
		}
	}
}

func writeResultOnlyDOT(sb *strings.Builder, result *DagResult, prefix, indent string) {
	names := make([]string, 0, len(result.Results))
	for name := range result.Results {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		nr := result.Results[name]
		borderColor, fontColor, penWidth := executionStatusStyle(nr)
		label := name + "\n[" + nr.Status.String() + "]"
		fmt.Fprintf(sb,
			"%s%q [label=%q, shape=ellipse, fillcolor=%q, style=filled, color=%q, fontcolor=%q, penwidth=%.1f];\n",
			indent, executionNodeID(prefix, name), label, "white", borderColor, fontColor, penWidth,
		)
	}
}

func executionNodeLabel(n *Node, nr *NodeResult) string {
	lines := []string{n.Name, "(" + dotNodeKind(n) + ")"}
	if nr == nil {
		return strings.Join(lines, "\n")
	}

	lines = append(lines, "["+nr.Status.String()+"]")
	if nr.ConditionEvaluated {
		lines = append(lines, fmt.Sprintf("condition=%t", nr.ConditionMatched))
	}
	if nr.RouteIndex >= 0 {
		lines = append(lines, fmt.Sprintf("route=%d", nr.RouteIndex))
	}
	if nr.Duration > 0 {
		lines = append(lines, nr.Duration.String())
	}
	if nr.SkipReason != "" {
		lines = append(lines, nr.SkipReason)
	}
	return strings.Join(lines, "\n")
}

func executionStatusStyle(nr *NodeResult) (borderColor, fontColor string, penWidth float64) {
	if nr == nil {
		return "#64748b", "#334155", 1.0
	}
	switch nr.Status {
	case StatusSuccess:
		return "#16a34a", "#166534", 2.0
	case StatusRetried:
		return "#d97706", "#92400e", 2.0
	case StatusSkipped:
		return "#94a3b8", "#64748b", 1.5
	case StatusFailed:
		return "#dc2626", "#991b1b", 2.5
	case StatusPanicked:
		return "#7f1d1d", "#7f1d1d", 2.5
	case StatusRunning:
		return "#2563eb", "#1e40af", 2.0
	default:
		return "#64748b", "#334155", 1.0
	}
}

func executionEdgeAttributes(d *DAG, result *DagResult, from, to string) string {
	if routeMap := d.routeEdges[from]; routeMap != nil {
		indexes := routeIndexesForTarget(routeMap, to)
		label := formatRouteIndexes(indexes)
		nr := result.Results[from]
		if nr != nil && nr.RouteIndex >= 0 && routeTargetSelected(routeMap, nr.RouteIndex, to) {
			return fmt.Sprintf(" [style=bold, color=%q, penwidth=2.5, label=%q]", "#2563eb", label)
		}
		if nr != nil && nr.RouteIndex >= 0 {
			return fmt.Sprintf(" [style=dashed, color=%q, fontcolor=%q, label=%q]", "#cbd5e1", "#94a3b8", label)
		}
		return fmt.Sprintf(" [style=dashed, color=%q, label=%q]", "#2563eb", label)
	}

	if nr := result.Results[to]; nr != nil && nr.Status == StatusSkipped {
		return fmt.Sprintf(" [style=dashed, color=%q]", "#cbd5e1")
	}
	return ""
}

func routeIndexesForTarget(routeMap map[int][]string, target string) []int {
	indexes := make([]int, 0, len(routeMap))
	for index, targets := range routeMap {
		for _, name := range targets {
			if name == target {
				indexes = append(indexes, index)
				break
			}
		}
	}
	sort.Ints(indexes)
	return indexes
}

func formatRouteIndexes(indexes []int) string {
	parts := make([]string, len(indexes))
	for i, index := range indexes {
		parts[i] = strconv.Itoa(index)
	}
	return strings.Join(parts, ",")
}

func routeTargetSelected(routeMap map[int][]string, index int, target string) bool {
	for _, name := range routeMap[index] {
		if name == target {
			return true
		}
	}
	return false
}

func sortedDAGNodeNames(d *DAG) []string {
	names := make([]string, 0, len(d.nodes))
	for name := range d.nodes {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func dagRootNodeNames(d *DAG) []string {
	names := make([]string, 0)
	for name := range d.nodes {
		if len(d.reverseEdges[name]) == 0 {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func executionNodeID(prefix, name string) string {
	return prefix + "/" + name
}

func dotNodeKind(n *Node) string {
	switch {
	case n.SubflowFn != nil:
		return "subflow"
	case n.RouteFn != nil:
		return "route"
	case n.ConditionFn != nil:
		return "condition"
	case n.Critical:
		return "critical"
	default:
		return "normal"
	}
}
