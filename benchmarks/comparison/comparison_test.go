package comparison

import (
	"context"
	"fmt"
	"runtime"
	"testing"

	gotaskflow "github.com/noneback/go-taskflow"
	dagbee "github.com/vvvcxjvvv/DagBee"
)

var (
	dagbeeNoop   = func(context.Context, *dagbee.DAGContext) error { return nil }
	taskflowNoop = func() {}
)

// BenchmarkComparison measures end-to-end execution through each framework's
// public API. Graph construction is excluded; execution setup, scheduling,
// completion tracking, and teardown are included.
func BenchmarkComparison(b *testing.B) {
	concurrency := runtime.NumCPU()

	for _, nodes := range []int{32, 128, 512} {
		b.Run(fmt.Sprintf("Wide/N%d/DagBee", nodes), benchmarkDagBeeWide(nodes, concurrency))
		b.Run(fmt.Sprintf("Wide/N%d/go-taskflow", nodes), benchmarkTaskflowWide(nodes, concurrency))

		b.Run(fmt.Sprintf("Deep/N%d/DagBee", nodes), benchmarkDagBeeDeep(nodes, concurrency))
		b.Run(fmt.Sprintf("Deep/N%d/go-taskflow", nodes), benchmarkTaskflowDeep(nodes, concurrency))
	}

	for _, branches := range []int{16, 64, 256} {
		b.Run(fmt.Sprintf("FanOutFanIn/N%d/DagBee", branches), benchmarkDagBeeFanOutFanIn(branches, concurrency))
		b.Run(fmt.Sprintf("FanOutFanIn/N%d/go-taskflow", branches), benchmarkTaskflowFanOutFanIn(branches, concurrency))
	}

	b.Run("Subflow/DagBee", benchmarkDagBeeSubflow(concurrency))
	b.Run("Subflow/go-taskflow", benchmarkTaskflowSubflow(concurrency))
}

func benchmarkDagBeeWide(nodes, concurrency int) func(*testing.B) {
	return func(b *testing.B) {
		d := dagbee.NewDAG("wide", dagbee.WithMaxConcurrency(concurrency))
		for i := 0; i < nodes; i++ {
			mustAddDagBeeNode(d, fmt.Sprintf("N%d", i), dagbeeNoop)
		}
		benchmarkDagBeeRun(b, d)
	}
}

func benchmarkTaskflowWide(nodes, concurrency int) func(*testing.B) {
	return func(b *testing.B) {
		tf := gotaskflow.NewTaskFlow("wide")
		for i := 0; i < nodes; i++ {
			tf.NewTask(fmt.Sprintf("N%d", i), taskflowNoop)
		}
		benchmarkTaskflowRun(b, tf, concurrency)
	}
}

func benchmarkDagBeeDeep(nodes, concurrency int) func(*testing.B) {
	return func(b *testing.B) {
		d := dagbee.NewDAG("deep", dagbee.WithMaxConcurrency(concurrency))
		mustAddDagBeeNode(d, "N0", dagbeeNoop)
		for i := 1; i < nodes; i++ {
			mustAddDagBeeNode(
				d,
				fmt.Sprintf("N%d", i),
				dagbeeNoop,
				dagbee.NodeWithDependsOn(fmt.Sprintf("N%d", i-1)),
			)
		}
		benchmarkDagBeeRun(b, d)
	}
}

func benchmarkTaskflowDeep(nodes, concurrency int) func(*testing.B) {
	return func(b *testing.B) {
		tf := gotaskflow.NewTaskFlow("deep")
		previous := tf.NewTask("N0", taskflowNoop)
		for i := 1; i < nodes; i++ {
			next := tf.NewTask(fmt.Sprintf("N%d", i), taskflowNoop)
			previous.Precede(next)
			previous = next
		}
		benchmarkTaskflowRun(b, tf, concurrency)
	}
}

func benchmarkDagBeeFanOutFanIn(branches, concurrency int) func(*testing.B) {
	return func(b *testing.B) {
		d := dagbee.NewDAG("fan-out-fan-in", dagbee.WithMaxConcurrency(concurrency))
		mustAddDagBeeNode(d, "root", dagbeeNoop)

		dependencies := make([]string, 0, branches)
		for i := 0; i < branches; i++ {
			name := fmt.Sprintf("branch-%d", i)
			dependencies = append(dependencies, name)
			mustAddDagBeeNode(d, name, dagbeeNoop, dagbee.NodeWithDependsOn("root"))
		}
		mustAddDagBeeNode(d, "join", dagbeeNoop, dagbee.NodeWithDependsOn(dependencies...))
		benchmarkDagBeeRun(b, d)
	}
}

func benchmarkTaskflowFanOutFanIn(branches, concurrency int) func(*testing.B) {
	return func(b *testing.B) {
		tf := gotaskflow.NewTaskFlow("fan-out-fan-in")
		root := tf.NewTask("root", taskflowNoop)
		join := tf.NewTask("join", taskflowNoop)

		for i := 0; i < branches; i++ {
			branch := tf.NewTask(fmt.Sprintf("branch-%d", i), taskflowNoop)
			root.Precede(branch)
			branch.Precede(join)
		}
		benchmarkTaskflowRun(b, tf, concurrency)
	}
}

func benchmarkDagBeeSubflow(concurrency int) func(*testing.B) {
	return func(b *testing.B) {
		d := dagbee.NewDAG("subflow", dagbee.WithMaxConcurrency(concurrency))
		mustAddDagBeeNode(d, "setup", dagbeeNoop)
		mustAddDagBeeNode(
			d,
			"subflow",
			nil,
			dagbee.NodeWithDependsOn("setup"),
			dagbee.NodeWithSubflow(func(context.Context, *dagbee.DAGContext) (*dagbee.DAG, error) {
				sub := dagbee.NewDAG("child")
				mustAddDagBeeNode(sub, "left", dagbeeNoop)
				mustAddDagBeeNode(sub, "right", dagbeeNoop)
				mustAddDagBeeNode(
					sub,
					"join",
					dagbeeNoop,
					dagbee.NodeWithDependsOn("left", "right"),
				)
				return sub, nil
			}),
		)
		mustAddDagBeeNode(d, "teardown", dagbeeNoop, dagbee.NodeWithDependsOn("subflow"))
		benchmarkDagBeeRun(b, d)
	}
}

func benchmarkTaskflowSubflow(concurrency int) func(*testing.B) {
	return func(b *testing.B) {
		tf := gotaskflow.NewTaskFlow("subflow")
		setup := tf.NewTask("setup", taskflowNoop)
		subflow := tf.NewSubflow("subflow", func(sub *gotaskflow.Subflow) {
			left := sub.NewTask("left", taskflowNoop)
			right := sub.NewTask("right", taskflowNoop)
			join := sub.NewTask("join", taskflowNoop)
			left.Precede(join)
			right.Precede(join)
		})
		teardown := tf.NewTask("teardown", taskflowNoop)
		setup.Precede(subflow)
		subflow.Precede(teardown)
		benchmarkTaskflowRun(b, tf, concurrency)
	}
}

func benchmarkDagBeeRun(b *testing.B, d *dagbee.DAG) {
	b.Helper()
	engine := dagbee.NewEngine()
	ctx := context.Background()
	assertDagBeeRun(b, engine, ctx, d)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		result := engine.Run(ctx, d)
		dagbee.ReleaseDagResult(result)
	}
}

func assertDagBeeRun(b *testing.B, engine *dagbee.Engine, ctx context.Context, d *dagbee.DAG) {
	b.Helper()
	result := engine.Run(ctx, d)
	defer dagbee.ReleaseDagResult(result)
	if result.Status != dagbee.StatusSuccess {
		b.Fatalf("DagBee execution failed: %v", result.Error)
	}
}

func benchmarkTaskflowRun(b *testing.B, tf *gotaskflow.TaskFlow, concurrency int) {
	b.Helper()
	executor := gotaskflow.NewExecutor(uint(concurrency))
	executor.Run(tf).Wait()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		executor.Run(tf).Wait()
	}
}

func mustAddDagBeeNode(d *dagbee.DAG, name string, fn dagbee.NodeFunc, opts ...dagbee.NodeOption) {
	if err := d.AddNode(name, fn, opts...); err != nil {
		panic(err)
	}
}
