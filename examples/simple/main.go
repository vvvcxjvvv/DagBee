package main

import (
	"context"
	"fmt"
	"time"

	"dagbee"
)

func main() {
	d := dagbee.NewDAG("simple-pipeline",
		dagbee.WithMaxConcurrency(4),
		dagbee.WithTimeout(10*time.Second),
		dagbee.WithLogger(dagbee.NewStdLogger()),
	)

	// A and B run in parallel; C depends on both.
	d.AddNode("A", func(ctx context.Context, dctx *dagbee.DAGContext) error {
		time.Sleep(100 * time.Millisecond)
		dctx.Set("a_result", 42)
		fmt.Println("  [A] done")
		return nil
	}, dagbee.NodeWithPriority(10))

	d.AddNode("B", func(ctx context.Context, dctx *dagbee.DAGContext) error {
		time.Sleep(80 * time.Millisecond)
		dctx.Set("b_result", "hello")
		fmt.Println("  [B] done")
		return nil
	}, dagbee.NodeWithPriority(5))

	d.AddNode("C", func(ctx context.Context, dctx *dagbee.DAGContext) error {
		a, _ := dagbee.GetTyped[int](dctx, "a_result")
		b, _ := dagbee.GetTyped[string](dctx, "b_result")
		fmt.Printf("  [C] received a=%d, b=%q\n", a, b)
		dctx.Set("c_result", fmt.Sprintf("merged(%d, %s)", a, b))
		return nil
	}, dagbee.NodeWithDependsOn("A", "B"), dagbee.NodeWithPriority(10))

	fmt.Println("=== DAG Topology ===")
	fmt.Println(d.Visualize())

	fmt.Println("=== Running ===")
	result := dagbee.NewEngine().Run(context.Background(), d)

	fmt.Println("\n=== Result ===")
	fmt.Printf("Status:   %s\n", result.Status)
	fmt.Printf("Duration: %s\n", result.Duration)
	for _, name := range []string{"A", "B", "C"} {
		nr := result.NodeResult(name)
		fmt.Printf("  %-4s %s  %s\n", name, nr.Status, nr.Duration)
	}
}
