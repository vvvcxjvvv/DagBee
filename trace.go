package dagbee

import (
	"encoding/json"
	"fmt"
)

// TraceEventPhase is the Chrome Trace event phase (B=begin, E=end, X=complete).
type TraceEventPhase string

const (
	tracePhaseComplete TraceEventPhase = "X" // complete event with dur
	tracePhaseInstant  TraceEventPhase = "i" // instant event
)

// TraceEvent is a single event in the Chrome Trace JSON format.
// https://docs.google.com/document/d/1CvAClvFfyA5R-PhYUmn5OOQtYMH4h6I0nSsKchNAySU
type TraceEvent struct {
	Name      string                 `json:"name"`
	Cat       string                 `json:"cat"`
	Phase     TraceEventPhase        `json:"ph"`
	Timestamp int64                  `json:"ts"` // microseconds
	Duration  int64                  `json:"dur,omitempty"`
	TrackID   int                    `json:"tid"`
	ProcessID int                    `json:"pid"`
	Args      map[string]interface{} `json:"args,omitempty"`
}

// traceFile is the top-level Chrome Trace JSON structure.
type traceFile struct {
	TraceEvents []TraceEvent `json:"traceEvents"`
}

// ExportChromeTrace returns a Chrome Trace JSON string for the DAG execution.
// The output can be loaded in chrome://tracing or Perfetto for timeline
// analysis of node start/end times, durations, and parallelism.
//
// Each node produces a complete event (phase "X") with ts/dur in microseconds.
// Skipped nodes produce instant events. Route nodes include the selected
// branch index in args. Subflow results are flattened with depth-encoded
// track IDs.
func (r *DagResult) ExportChromeTrace() (string, error) {
	events := r.collectTraceEvents(0, 0)
	tf := traceFile{TraceEvents: events}
	data, err := json.MarshalIndent(tf, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal trace: %w", err)
	}
	return string(data), nil
}

// collectTraceEvents recursively collects trace events from a DagResult
// and its nested SubflowResults. depth is the nesting level (0=top-level),
// used to assign unique track IDs per subflow layer.
func (r *DagResult) collectTraceEvents(pid, depth int) []TraceEvent {
	var events []TraceEvent

	// DAG-level complete event.
	if !r.StartTime.IsZero() {
		events = append(events, TraceEvent{
			Name:      r.DagName,
			Cat:       "dag",
			Phase:     tracePhaseComplete,
			Timestamp: r.StartTime.UnixMicro(),
			Duration:  r.Duration.Microseconds(),
			TrackID:   depth,
			ProcessID: pid,
		})
	}

	// Sort node names for deterministic output.
	names := make([]string, 0, len(r.Results))
	for name := range r.Results {
		names = append(names, name)
	}
	sortStrings(names)

	for _, name := range names {
		nr := r.Results[name]
		trackID := depth*1000 + hashName(name)%1000

		if nr.Status == StatusSkipped {
			args := map[string]interface{}{
				"status": "skipped",
			}
			if nr.SkipReason != "" {
				args["skip_reason"] = nr.SkipReason
			}
			if nr.ConditionEvaluated {
				args["condition_matched"] = nr.ConditionMatched
			}
			events = append(events, TraceEvent{
				Name:      name,
				Cat:       "node",
				Phase:     tracePhaseInstant,
				Timestamp: nr.StartTime.UnixMicro(),
				TrackID:   trackID,
				ProcessID: pid,
				Args:      args,
			})
			continue
		}

		args := map[string]interface{}{
			"status": nr.Status.String(),
		}
		if nr.RetryCount > 0 {
			args["retries"] = nr.RetryCount
		}
		if nr.RouteIndex >= 0 {
			args["route_index"] = nr.RouteIndex
		}
		if nr.ConditionEvaluated {
			args["condition_matched"] = nr.ConditionMatched
		}
		if nr.Error != nil {
			args["error"] = nr.Error.Error()
		}

		events = append(events, TraceEvent{
			Name:      name,
			Cat:       "node",
			Phase:     tracePhaseComplete,
			Timestamp: nr.StartTime.UnixMicro(),
			Duration:  nr.Duration.Microseconds(),
			TrackID:   trackID,
			ProcessID: pid,
			Args:      args,
		})

		// Recurse into subflow results.
		if nr.SubflowResult != nil {
			subEvents := nr.SubflowResult.collectTraceEvents(pid, depth+1)
			events = append(events, subEvents...)
		}
	}

	return events
}

// ExportFlamegraph returns a text-based flame graph representation of the
// DAG execution. Each line follows the format:
//
//	DagName;NodeName duration_us
//
// Subflow nodes are expanded recursively, using the parent node name as a
// prefix to show nesting. Lines are sorted by total duration (descending)
// to surface slow nodes at the top. The output is compatible with
// Brendan Gregg's flamegraph.pl and similar tools.
func (r *DagResult) ExportFlamegraph() string {
	var lines []string
	r.collectFlamegraph(&lines, r.DagName)
	return joinLines(lines)
}

// collectFlamegraph recursively appends flamegraph lines for a DagResult
// and its nested SubflowResults. parent is the accumulated path prefix.
func (r *DagResult) collectFlamegraph(lines *[]string, parent string) {
	// Collect and sort nodes by duration descending (slowest first).
	type nodeEntry struct {
		name string
		nr   *NodeResult
	}
	entries := make([]nodeEntry, 0, len(r.Results))
	for name, nr := range r.Results {
		entries = append(entries, nodeEntry{name, nr})
	}
	for i := 1; i < len(entries); i++ {
		for j := i; j > 0 && entries[j-1].nr.Duration < entries[j].nr.Duration; j-- {
			entries[j-1], entries[j] = entries[j], entries[j-1]
		}
	}

	for _, e := range entries {
		path := parent + ";" + e.name
		*lines = append(*lines, fmt.Sprintf("%s %d", path, e.nr.Duration.Microseconds()))

		if e.nr.SubflowResult != nil {
			e.nr.SubflowResult.collectFlamegraph(lines, path+";"+e.nr.SubflowResult.DagName)
		}
	}
}

// --- helpers ---

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

func hashName(s string) int {
	h := uint64(14695981039346656037) // FNV offset
	for _, c := range s {
		h ^= uint64(c)
		h *= 1099511628211
	}
	return int(h % 1000)
}
func joinLines(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	var sb []byte
	for i, l := range lines {
		if i > 0 {
			sb = append(sb, '\n')
		}
		sb = append(sb, l...)
	}
	return string(sb)
}
