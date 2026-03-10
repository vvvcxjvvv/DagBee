package main

import (
	"context"
	"fmt"
	"math/rand"
	"time"

	"dagbee"
)

func main() {
	registry := map[string]dagbee.NodeFunc{
		"recall_cf":      recallCF,
		"recall_content": recallContent,
		"recall_hot":     recallHot,
		"merge_and_rank": mergeAndRank,
		"filter":         filter,
		"fill_detail":    fillDetail,
	}

	d, err := dagbee.LoadDAGFromYAML("examples/recommend/pipeline.yaml", registry)
	if err != nil {
		panic(err)
	}

	d = dagbee.NewDAG(d.Name(),
		dagbee.WithMaxConcurrency(d.MaxConcurrency()),
		dagbee.WithTimeout(d.Timeout()),
		dagbee.WithLogger(dagbee.NewStdLogger()),
	)
	for name, fn := range registry {
		orig := d.GetNode(name)
		if orig != nil {
			continue
		}
		_ = fn
	}

	// Rebuild from YAML with logger attached.
	d2, err := dagbee.LoadDAGFromYAML("examples/recommend/pipeline.yaml", registry)
	if err != nil {
		panic(err)
	}

	fmt.Println("=== Recommend Pipeline Topology ===")
	fmt.Println(d2.Visualize())

	fmt.Println("=== Running ===")
	result := dagbee.NewEngine(dagbee.EngineWithLogger(dagbee.NewStdLogger())).Run(context.Background(), d2)

	fmt.Println("\n=== Result ===")
	fmt.Printf("Status:   %s\n", result.Status)
	fmt.Printf("Duration: %s\n", result.Duration)
	fmt.Printf("Success:  %d / Failed: %d / Skipped: %d\n",
		result.SuccessCount(), result.FailedCount(), result.SkippedCount())

	for _, name := range []string{
		"recall_cf", "recall_content", "recall_hot",
		"merge_and_rank", "filter", "fill_detail",
	} {
		nr := result.NodeResult(name)
		if nr != nil {
			fmt.Printf("  %-16s %s  %s\n", name, nr.Status, nr.Duration)
		}
	}
}

func recallCF(ctx context.Context, store *dagbee.SharedStore) error {
	time.Sleep(time.Duration(50+rand.Intn(100)) * time.Millisecond)
	store.Set("recall_cf", []string{"item1", "item2", "item3"})
	return nil
}

func recallContent(ctx context.Context, store *dagbee.SharedStore) error {
	time.Sleep(time.Duration(30+rand.Intn(80)) * time.Millisecond)
	store.Set("recall_content", []string{"item4", "item5"})
	return nil
}

func recallHot(ctx context.Context, store *dagbee.SharedStore) error {
	time.Sleep(time.Duration(20+rand.Intn(60)) * time.Millisecond)
	store.Set("recall_hot", []string{"item6"})
	return nil
}

func mergeAndRank(ctx context.Context, store *dagbee.SharedStore) error {
	var merged []string
	for _, key := range []string{"recall_cf", "recall_content", "recall_hot"} {
		if items, ok := store.Get(key); ok {
			merged = append(merged, items.([]string)...)
		}
	}
	time.Sleep(time.Duration(40+rand.Intn(60)) * time.Millisecond)
	store.Set("ranked", merged)
	return nil
}

func filter(ctx context.Context, store *dagbee.SharedStore) error {
	ranked, _ := dagbee.GetTyped[[]string](store, "ranked")
	var filtered []string
	for _, item := range ranked {
		if item != "item6" { // simulate filtering
			filtered = append(filtered, item)
		}
	}
	time.Sleep(20 * time.Millisecond)
	store.Set("filtered", filtered)
	return nil
}

func fillDetail(ctx context.Context, store *dagbee.SharedStore) error {
	filtered, _ := dagbee.GetTyped[[]string](store, "filtered")
	time.Sleep(30 * time.Millisecond)
	store.Set("final_result", filtered)
	fmt.Printf("  [fill_detail] final items: %v\n", filtered)
	return nil
}
