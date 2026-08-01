// Example: Subflow — dynamic child DAG generation at runtime.
//
// This example demonstrates a recommendation pipeline where the number of
// recall channels is determined at runtime. A subflow node dynamically
// generates a child DAG with N parallel recall nodes, each fetching
// candidate items, followed by a merge node that collects all results.
//
// The child DAG shares the parent's DAGContext (data flows freely between
// parent and child) and worker pool (concurrency is globally bounded).
package main

import (
	"context"
	"fmt"
	"time"

	"github.com/vvvcxjvvv/DagBee"
)

func main() {
	d := dagbee.NewDAG("recommend-subflow",
		dagbee.WithMaxConcurrency(4),
		dagbee.WithTimeout(30*time.Second),
		dagbee.WithLogger(dagbee.NewStdLogger()),
	)

	// --- Parent DAG ---

	// Step 1: fetch request context, determine partition count at runtime.
	d.AddNode("prepare", func(_ context.Context, dctx *dagbee.DAGContext) error {
		fmt.Println("[prepare] fetching request context...")
		time.Sleep(50 * time.Millisecond)
		// In a real app, this might come from a config service or A/B platform.
		dctx.Set("partition_count", 3)
		dctx.Set("user_id", "u_12345")
		fmt.Println("[prepare] partition_count = 3")
		return nil
	})

	// Step 2: subflow node — dynamically generates a child DAG based on
	// partition_count. The child DAG runs N recall workers in parallel,
	// then merges their results.
	d.AddNode("recall", nil,
		dagbee.NodeWithSubflow(func(ctx context.Context, dctx *dagbee.DAGContext) (*dagbee.DAG, error) {
			count, _ := dagbee.GetTyped[int](dctx, "partition_count")
			fmt.Printf("[recall] building child DAG with %d partitions\n", count)

			sub := dagbee.NewDAG("recall-sub",
				dagbee.WithMaxConcurrency(3),
				dagbee.WithTimeout(10*time.Second),
			)

			// Each partition recalls items independently.
			partitionDeps := make([]string, 0, count)
			for i := 0; i < count; i++ {
				name := fmt.Sprintf("recall_p%d", i)
				partitionDeps = append(partitionDeps, name)
				idx := i

				sub.AddNode(name, func(_ context.Context, dctx *dagbee.DAGContext) error {
					time.Sleep(time.Duration(50+idx*30) * time.Millisecond)
					items := []string{fmt.Sprintf("item_%d_a", idx), fmt.Sprintf("item_%d_b", idx)}
					dctx.Set(name, items)
					fmt.Printf("  [recall_p%d] recalled %d items\n", idx, len(items))
					return nil
				}, dagbee.NodeWithPriority(count-idx)) // earlier partition = higher priority
			}

			// Merge all partition results.
			sub.AddNode("merge", func(_ context.Context, dctx *dagbee.DAGContext) error {
				var allItems []string
				for _, dep := range partitionDeps {
					items, _ := dagbee.GetTyped[[]string](dctx, dep)
					allItems = append(allItems, items...)
				}
				dctx.Set("merged_items", allItems)
				fmt.Printf("  [merge] total recalled: %d items\n", len(allItems))
				return nil
			}, dagbee.NodeWithDependsOn(partitionDeps...))

			return sub, nil
		}),
		dagbee.NodeWithDependsOn("prepare"),
	)

	// Step 3: rank the merged items.
	d.AddNode("rank", func(_ context.Context, dctx *dagbee.DAGContext) error {
		items, _ := dagbee.GetTyped[[]string](dctx, "merged_items")
		fmt.Printf("[rank] ranking %d items\n", len(items))
		time.Sleep(30 * time.Millisecond)

		// Simulate scoring — reverse the list as "ranking".
		ranked := make([]string, len(items))
		for i := range items {
			ranked[len(items)-1-i] = items[i]
		}
		dctx.Set("ranked_items", ranked)
		fmt.Println("[rank] done")
		return nil
	}, dagbee.NodeWithDependsOn("recall"))

	// Step 4: output.
	d.AddNode("output", func(_ context.Context, dctx *dagbee.DAGContext) error {
		ranked, _ := dagbee.GetTyped[[]string](dctx, "ranked_items")
		userID, _ := dagbee.GetTyped[string](dctx, "user_id")
		fmt.Printf("[output] user=%s, final recommendations: %v\n", userID, ranked)
		return nil
	}, dagbee.NodeWithDependsOn("rank"))

	fmt.Println("=== DAG Topology ===")
	fmt.Println(d.Visualize())

	fmt.Println("\n=== Running ===")
	eng := dagbee.NewEngine(dagbee.EngineWithMaxSubflowDepth(5))
	result := eng.Run(context.Background(), d)

	fmt.Println("\n=== Parent DAG Result ===")
	fmt.Printf("Status:   %s\n", result.Status)
	fmt.Printf("Duration: %s\n", result.Duration)
	for _, name := range []string{"prepare", "recall", "rank", "output"} {
		nr := result.NodeResult(name)
		fmt.Printf("  %-8s %s  %s\n", name, nr.Status, nr.Duration)
	}

	// Inspect subflow results.
	fmt.Println("\n=== Subflow Result (recall) ===")
	subResult := result.NodeResult("recall").SubflowResult
	if subResult != nil {
		fmt.Printf("Status:   %s\n", subResult.Status)
		fmt.Printf("Duration: %s\n", subResult.Duration)
		for _, name := range []string{"recall_p0", "recall_p1", "recall_p2", "merge"} {
			nr := subResult.NodeResult(name)
			if nr != nil {
				fmt.Printf("  %-10s %s  %s\n", name, nr.Status, nr.Duration)
			}
		}
	}
}
