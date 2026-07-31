package main

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"dagbee"
)

// MapReduce pipeline:
//
//   split ──► map_0 ──┐
//          ├► map_1 ──┼► shuffle_0 ──► reduce_0 ──┐
//          └► map_2 ──┘                           ├► merge
//                       shuffle_1 ──► reduce_1 ──┘
//
// The classic word-count example: split input into chunks, count
// words in parallel per chunk (map), hash-partition into buckets
// (shuffle), aggregate each bucket (reduce), then merge final output.

const (
	numMappers  = 3
	numReducers = 2
)

// wordCount is the shared type for reduce → merge data passing.
type wordCount struct {
	Word  string
	Count int
}

func main() {
	input := `the quick brown fox jumps over the lazy dog
the fox and the dog are friends the dog jumps over the fox
brown dog lazy fox the quick brown dog the lazy fox jumps`

	d := dagbee.NewDAG("word-count-mapreduce",
		dagbee.WithMaxConcurrency(numMappers+numReducers),
	)

	// --- Phase 1: Split ---
	d.AddNode("split", func(ctx context.Context, store *dagbee.SharedStore) error {
		words := strings.Fields(input)
		size := (len(words) + numMappers - 1) / numMappers
		chunks := make([][]string, numMappers)
		for i := 0; i < numMappers; i++ {
			start := i * size
			end := start + size
			if end > len(words) {
				end = len(words)
			}
			chunks[i] = words[start:end]
		}
		store.Set("chunks", chunks)
		return nil
	}, dagbee.NodeWithPriority(100))

	// --- Phase 2: Map (parallel per chunk) ---
	mapDeps := make([]string, 0, numMappers)
	for i := 0; i < numMappers; i++ {
		idx := i
		name := fmt.Sprintf("map_%d", idx)
		mapDeps = append(mapDeps, name)

		d.AddNode(name, func(ctx context.Context, store *dagbee.SharedStore) error {
			chunks, _ := dagbee.GetTyped[[][]string](store, "chunks")
			partitions := make([]map[string]int, numReducers)
			for r := 0; r < numReducers; r++ {
				partitions[r] = make(map[string]int)
			}
			for _, w := range chunks[idx] {
				r := hashPartition(w, numReducers)
				partitions[r][w]++
			}
			store.Set(fmt.Sprintf("map_%d_partitions", idx), partitions)
			return nil
		}, dagbee.NodeWithDependsOn("split"), dagbee.NodeWithPriority(50-idx))
	}

	// --- Phase 3: Shuffle (collect from all mappers per partition) ---
	shuffleDeps := make([]string, 0, numReducers)
	for r := 0; r < numReducers; r++ {
		rIdx := r
		name := fmt.Sprintf("shuffle_%d", rIdx)
		shuffleDeps = append(shuffleDeps, name)

		d.AddNode(name, func(ctx context.Context, store *dagbee.SharedStore) error {
			merged := make(map[string]int)
			for m := 0; m < numMappers; m++ {
				partitions, _ := dagbee.GetTyped[[]map[string]int](store, fmt.Sprintf("map_%d_partitions", m))
				for w, c := range partitions[rIdx] {
					merged[w] += c
				}
			}
			store.Set(fmt.Sprintf("shuffle_%d_merged", rIdx), merged)
			return nil
		}, dagbee.NodeWithDependsOn(mapDeps...), dagbee.NodeWithPriority(20-rIdx))
	}

	// --- Phase 4: Reduce (sort per partition, produce []wordCount) ---
	reduceDeps := make([]string, 0, numReducers)
	for r := 0; r < numReducers; r++ {
		rIdx := r
		name := fmt.Sprintf("reduce_%d", rIdx)
		reduceDeps = append(reduceDeps, name)

		d.AddNode(name, func(ctx context.Context, store *dagbee.SharedStore) error {
			merged, _ := dagbee.GetTyped[map[string]int](store, fmt.Sprintf("shuffle_%d_merged", rIdx))
			keys := make([]string, 0, len(merged))
			for k := range merged {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			entries := make([]wordCount, 0, len(keys))
			for _, k := range keys {
				entries = append(entries, wordCount{Word: k, Count: merged[k]})
			}
			store.Set(fmt.Sprintf("reduce_%d_result", rIdx), entries)
			return nil
		}, dagbee.NodeWithDependsOn(fmt.Sprintf("shuffle_%d", r)), dagbee.NodeWithPriority(10-r))
	}

	// --- Phase 5: Merge final results ---
	d.AddNode("merge", func(ctx context.Context, store *dagbee.SharedStore) error {
		final := make(map[string]int)
		for r := 0; r < numReducers; r++ {
			entries, _ := dagbee.GetTyped[[]wordCount](store, fmt.Sprintf("reduce_%d_result", r))
			for _, e := range entries {
				final[e.Word] += e.Count
			}
		}
		keys := make([]string, 0, len(final))
		for k := range final {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		fmt.Println("=== Word Count Results ===")
		for _, w := range keys {
			fmt.Printf("  %-12s %d\n", w, final[w])
		}
		store.Set("final_counts", final)
		return nil
	}, dagbee.NodeWithDependsOn(reduceDeps...), dagbee.NodeWithPriority(1))

	// --- Visualize & Run ---
	fmt.Println("=== DAG Topology ===")
	fmt.Println(d.Visualize())

	fmt.Println("=== Running MapReduce ===")
	result := dagbee.NewEngine().Run(context.Background(), d)

	fmt.Println("\n=== Execution Result ===")
	fmt.Printf("Status:   %s\n", result.Status)
	fmt.Printf("Duration: %s\n", result.Duration)
	fmt.Printf("Success:  %d / Failed: %d / Skipped: %d\n",
		result.SuccessCount(), result.FailedCount(), result.SkippedCount())

	nodeOrder := []string{"split"}
	for i := 0; i < numMappers; i++ {
		nodeOrder = append(nodeOrder, fmt.Sprintf("map_%d", i))
	}
	for r := 0; r < numReducers; r++ {
		nodeOrder = append(nodeOrder, fmt.Sprintf("shuffle_%d", r))
		nodeOrder = append(nodeOrder, fmt.Sprintf("reduce_%d", r))
	}
	nodeOrder = append(nodeOrder, "merge")

	fmt.Println("\nNode details:")
	for _, name := range nodeOrder {
		nr := result.NodeResult(name)
		if nr != nil {
			fmt.Printf("  %-12s %s  %s\n", name, nr.Status, nr.Duration)
		}
	}
}

func hashPartition(word string, buckets int) int {
	h := 0
	for _, c := range word {
		h = 31*h + int(c)
	}
	if h < 0 {
		h = -h
	}
	return h % buckets
}
