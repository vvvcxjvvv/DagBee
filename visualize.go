package dagbee

import (
	"fmt"
	"sort"
	"strings"
)

// Visualize returns a human-readable text representation of the DAG topology,
// showing parallel layers and edges. Useful for debugging and logging.
func (d *DAG) Visualize() string {
	var sb strings.Builder

	fmt.Fprintf(&sb, "DAG: %s (%d nodes", d.name, len(d.nodes))
	if d.timeout > 0 {
		fmt.Fprintf(&sb, ", timeout: %s", d.timeout)
	}
	if d.maxConcurrency > 0 {
		fmt.Fprintf(&sb, ", concurrency: %d", d.maxConcurrency)
	}
	sb.WriteString(")\n\n")

	layers, err := d.topologicalLayers()
	if err != nil {
		fmt.Fprintf(&sb, "ERROR: %v\n", err)
		return sb.String()
	}

	for i, layer := range layers {
		names := make([]string, len(layer))
		for j, n := range layer {
			names[j] = n.Name
		}
		sort.Strings(names)

		label := "parallel"
		if len(layer) == 1 {
			label = "serial"
		}

		nodeStrs := make([]string, len(names))
		for j, name := range names {
			nodeStrs[j] = fmt.Sprintf("[%s]", name)
		}

		fmt.Fprintf(&sb, "Layer %d (%s): %s\n", i, label, strings.Join(nodeStrs, " "))
	}

	// Collect and sort edges.
	sb.WriteString("\nEdges:\n")
	edgeKeys := make([]string, 0, len(d.edges))
	for from := range d.edges {
		edgeKeys = append(edgeKeys, from)
	}
	sort.Strings(edgeKeys)

	maxFromLen := 0
	for _, from := range edgeKeys {
		if len(from) > maxFromLen {
			maxFromLen = len(from)
		}
	}

	for _, from := range edgeKeys {
		tos := make([]string, len(d.edges[from]))
		copy(tos, d.edges[from])
		sort.Strings(tos)
		for _, to := range tos {
			padding := strings.Repeat("─", maxFromLen-len(from)+2)
			fmt.Fprintf(&sb, "  %s %s► %s\n", from, padding, to)
		}
	}

	if len(edgeKeys) == 0 {
		sb.WriteString("  (none)\n")
	}

	return sb.String()
}
