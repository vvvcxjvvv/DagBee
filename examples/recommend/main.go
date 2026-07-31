package main

import (
	"context"
	"fmt"
	"math/rand"
	"sort"
	"time"

	"dagbee"
)

// ================================================================
// Data types
// ================================================================

// Item carries item ID + feature detail through the pipeline.
type Item struct {
	ID       string
	Title    string
	Category string
	CTR      float64
	CVR      float64
	eCPM     float64
	Score    float64
}

// itemIDs is the shared type for passing []string between nodes.
type itemIDs []string

func main() {
	registry := map[string]dagbee.NodeFunc{
		"recall_cf":   recallCF,
		"recall_vec":  recallVec,
		"recall_hot":  recallHot,
		"merge":       merge,
		"fill_detail": fillDetail,
		"filter":      filter,
		"score_ctr":   scoreCTR,
		"score_cvr":   scoreCVR,
		"fuse_rank":   fuseRank,
		"rerank":      rerank,
	}

	d, err := dagbee.LoadDAGFromYAML("examples/recommend/pipeline.yaml", registry)
	if err != nil {
		panic(err)
	}

	fmt.Println("=== Recommend Pipeline Topology ===")
	fmt.Println(d.Visualize())

	fmt.Println("=== Running ===")
	result := dagbee.NewEngine(
		dagbee.EngineWithLogger(dagbee.NewStdLogger()),
	).Run(context.Background(), d)

	fmt.Println("\n=== Result ===")
	fmt.Printf("Status:   %s\n", result.Status)
	fmt.Printf("Duration: %s\n", result.Duration)
	fmt.Printf("Success:  %d / Failed: %d / Skipped: %d\n",
		result.SuccessCount(), result.FailedCount(), result.SkippedCount())

	nodeOrder := []string{
		"recall_cf", "recall_vec", "recall_hot",
		"merge", "fill_detail", "filter",
		"score_ctr", "score_cvr",
		"fuse_rank", "rerank",
	}
	for _, name := range nodeOrder {
		nr := result.NodeResult(name)
		if nr != nil {
			fmt.Printf("  %-16s %s  %s\n", name, nr.Status, nr.Duration)
		}
	}
}

// ================================================================
// Phase 1: Multi-channel recall (parallel)
// ================================================================

func recallCF(ctx context.Context, store *dagbee.SharedStore) error {
	time.Sleep(time.Duration(40+rand.Intn(60)) * time.Millisecond)
	store.Set("recall_cf", itemIDs{"i1", "i2", "i3", "i4", "i5"})
	return nil
}

func recallVec(ctx context.Context, store *dagbee.SharedStore) error {
	time.Sleep(time.Duration(30+rand.Intn(50)) * time.Millisecond)
	store.Set("recall_vec", itemIDs{"i3", "i5", "i6", "i7"})
	return nil
}

func recallHot(ctx context.Context, store *dagbee.SharedStore) error {
	time.Sleep(time.Duration(20+rand.Intn(30)) * time.Millisecond)
	store.Set("recall_hot", itemIDs{"i8", "i1", "i9"})
	return nil
}

// ================================================================
// Phase 2: Merge + dedup
// ================================================================

func merge(ctx context.Context, store *dagbee.SharedStore) error {
	seen := make(map[string]bool)
	var merged itemIDs
	for _, key := range []string{"recall_cf", "recall_vec", "recall_hot"} {
		if raw, ok := store.Get(key); ok {
			for _, id := range raw.(itemIDs) {
				if !seen[id] {
					seen[id] = true
					merged = append(merged, id)
				}
			}
		}
	}
	store.Set("merged", merged)
	return nil
}

// ================================================================
// Phase 3: Fill detail (load item features for ranking models)
// ================================================================

func fillDetail(ctx context.Context, store *dagbee.SharedStore) error {
	merged, _ := dagbee.GetTyped[itemIDs](store, "merged")
	categories := []string{"electronics", "books", "fashion", "food", "home"}
	items := make([]Item, 0, len(merged))
	for _, id := range merged {
		items = append(items, Item{
			ID:       id,
			Title:    "Item " + id,
			Category: categories[rand.Intn(len(categories))],
		})
	}
	store.Set("items", items)
	return nil
}

// ================================================================
// Phase 4: Filter (blacklist / exposed / stock)
// ================================================================

func filter(ctx context.Context, store *dagbee.SharedStore) error {
	items, _ := dagbee.GetTyped[[]Item](store, "items")
	blacklist := map[string]bool{"i9": true}
	exposed := map[string]bool{"i4": true}
	var filtered []Item
	for _, it := range items {
		if blacklist[it.ID] || exposed[it.ID] {
			continue
		}
		filtered = append(filtered, it)
	}
	store.Set("filtered", filtered)
	return nil
}

// ================================================================
// Phase 5: Multi-model estimation (parallel)
// ================================================================

func scoreCTR(ctx context.Context, store *dagbee.SharedStore) error {
	items, _ := dagbee.GetTyped[[]Item](store, "filtered")
	scores := make(map[string]float64)
	for _, it := range items {
		scores[it.ID] = rand.Float64()
	}
	store.Set("scores_ctr", scores)
	return nil
}

func scoreCVR(ctx context.Context, store *dagbee.SharedStore) error {
	items, _ := dagbee.GetTyped[[]Item](store, "filtered")
	scores := make(map[string]float64)
	for _, it := range items {
		scores[it.ID] = rand.Float64()
	}
	store.Set("scores_cvr", scores)
	return nil
}

// ================================================================
// Phase 6: Multi-objective fusion (eCPM)
// ================================================================

func fuseRank(ctx context.Context, store *dagbee.SharedStore) error {
	items, _ := dagbee.GetTyped[[]Item](store, "filtered")
	ctr, _ := dagbee.GetTyped[map[string]float64](store, "scores_ctr")
	cvr, _ := dagbee.GetTyped[map[string]float64](store, "scores_cvr")

	for i := range items {
		items[i].CTR = ctr[items[i].ID]
		items[i].CVR = cvr[items[i].ID]
		bid := 1.0 + rand.Float64()*4.0
		items[i].eCPM = bid * items[i].CTR * items[i].CVR * 1000
		items[i].Score = items[i].eCPM
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].eCPM > items[j].eCPM
	})
	store.Set("ranked", items)
	return nil
}

// ================================================================
// Phase 7: Rerank (diversity + business rules)
// ================================================================

func rerank(ctx context.Context, store *dagbee.SharedStore) error {
	items, _ := dagbee.GetTyped[[]Item](store, "ranked")
	// Exploration decay to avoid filter bubble.
	for i := range items {
		items[i].Score = items[i].eCPM * (1.0 - 0.02*float64(i))
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].Score > items[j].Score
	})

	fmt.Println("\n=== Recommendation Results ===")
	fmt.Printf("%-4s  %-6s  %-12s  %-8s  %-8s  %-10s  %-10s\n",
		"Pos", "Item", "Category", "pCTR", "pCVR", "eCPM", "FinalScore")
	for i, it := range items {
		fmt.Printf("  %-2d   %-6s  %-12s  %.4f    %.4f    %-10.2f  %-10.4f\n",
			i+1, it.ID, it.Category, it.CTR, it.CVR, it.eCPM, it.Score)
	}
	store.Set("final_result", items)
	return nil
}
