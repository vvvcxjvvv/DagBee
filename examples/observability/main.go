package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"dagbee"
)

func main() {
	outDir := flag.String("out", "examples/observability", "directory for exported files")
	flag.Parse()

	if err := run(*outDir); err != nil {
		fmt.Fprintln(os.Stderr, "observability example:", err)
		os.Exit(1)
	}
}

func run(outDir string) error {
	d := buildDAG()
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}

	result := dagbee.NewEngine().Run(context.Background(), d)
	defer dagbee.ReleaseDagResult(result)
	if result.Status != dagbee.StatusSuccess {
		return fmt.Errorf("DAG failed: %w", result.Error)
	}
	if err := writeOutput(outDir, "dag.dot", result.ExportDOT()); err != nil {
		return err
	}

	trace, err := result.ExportChromeTrace()
	if err != nil {
		return fmt.Errorf("export Chrome Trace: %w", err)
	}
	if err := writeOutput(outDir, "trace.json", trace); err != nil {
		return err
	}
	if err := writeOutput(outDir, "flamegraph.folded", result.ExportFlamegraph()); err != nil {
		return err
	}

	fmt.Printf("Exported observability files to %s:\n", outDir)
	fmt.Println("  dag.dot")
	fmt.Println("  trace.json")
	fmt.Println("  flamegraph.folded")
	return nil
}

func buildDAG() *dagbee.DAG {
	d := dagbee.NewDAG("observability-demo",
		dagbee.WithMaxConcurrency(4),
		dagbee.WithTimeout(5*time.Second),
	)

	mustAddNode(d, "fetch-profile", func(_ context.Context, dctx *dagbee.DAGContext) error {
		time.Sleep(20 * time.Millisecond)
		dctx.Set("user_tier", "premium")
		return nil
	}, dagbee.NodeWithCritical(true))

	mustAddNode(d, "check-eligibility", func(_ context.Context, _ *dagbee.DAGContext) error {
		time.Sleep(8 * time.Millisecond)
		return nil
	},
		dagbee.NodeWithDependsOn("fetch-profile"),
		dagbee.NodeWithCondition(func(dctx *dagbee.DAGContext) bool {
			tier, _ := dagbee.GetTyped[string](dctx, "user_tier")
			return tier != "blocked"
		}),
	)

	mustAddNode(d, "select-pipeline", func(_ context.Context, _ *dagbee.DAGContext) error {
		return nil
	},
		dagbee.NodeWithDependsOn("check-eligibility"),
		dagbee.NodeWithRoute(func(dctx *dagbee.DAGContext) int {
			tier, _ := dagbee.GetTyped[string](dctx, "user_tier")
			if tier == "premium" {
				return 0
			}
			return 1
		}, map[int][]string{
			0: {"premium-ranking"},
			1: {"standard-ranking"},
		}),
		dagbee.NodeWithCritical(false),
	)

	mustAddNode(d, "premium-ranking", func(_ context.Context, _ *dagbee.DAGContext) error {
		time.Sleep(30 * time.Millisecond)
		return nil
	}, dagbee.NodeWithDependsOn("select-pipeline"))

	mustAddNode(d, "standard-ranking", func(_ context.Context, _ *dagbee.DAGContext) error {
		time.Sleep(12 * time.Millisecond)
		return nil
	}, dagbee.NodeWithDependsOn("select-pipeline"))

	mustAddNode(d, "enrich-results", nil,
		dagbee.NodeWithDependsOn("premium-ranking", "standard-ranking"),
		dagbee.NodeWithSubflow(func(_ context.Context, _ *dagbee.DAGContext) (*dagbee.DAG, error) {
			sub := dagbee.NewDAG("enrichment-subflow")
			mustAddNode(sub, "load-features", func(_ context.Context, _ *dagbee.DAGContext) error {
				time.Sleep(15 * time.Millisecond)
				return nil
			})
			mustAddNode(sub, "merge-features", func(_ context.Context, _ *dagbee.DAGContext) error {
				time.Sleep(10 * time.Millisecond)
				return nil
			}, dagbee.NodeWithDependsOn("load-features"))
			return sub, nil
		}),
	)

	return d
}

func mustAddNode(d *dagbee.DAG, name string, fn dagbee.NodeFunc, opts ...dagbee.NodeOption) {
	if err := d.AddNode(name, fn, opts...); err != nil {
		panic(fmt.Sprintf("add node %q: %v", name, err))
	}
}

func writeOutput(dir, name, content string) error {
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
