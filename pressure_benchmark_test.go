package dagbee

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

func BenchmarkEngineRun_WideDAG(b *testing.B) {
	for _, nodes := range []int{32, 256, 1024} {
		b.Run(fmt.Sprintf("nodes=%d", nodes), func(b *testing.B) {
			d := buildWideDAG("wide", nodes, minInt(nodes, runtime.NumCPU()*2), noop)
			eng := NewEngine()
			ctx := context.Background()

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				result := eng.Run(ctx, d)
				ReleaseDagResult(result)
			}
		})
	}
}

func BenchmarkEngineRun_DeepDAG(b *testing.B) {
	for _, nodes := range []int{32, 256, 1024} {
		b.Run(fmt.Sprintf("nodes=%d", nodes), func(b *testing.B) {
			d := buildDeepDAG("deep", nodes, noop)
			eng := NewEngine()
			ctx := context.Background()

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				result := eng.Run(ctx, d)
				ReleaseDagResult(result)
			}
		})
	}
}

func BenchmarkEngineRun_FanOutFanIn(b *testing.B) {
	for _, branches := range []int{16, 128, 512} {
		b.Run(fmt.Sprintf("branches=%d", branches), func(b *testing.B) {
			d := buildFanOutFanInDAG(
				"fanout",
				branches,
				minInt(branches, runtime.NumCPU()*2),
				noop,
				makeStoreWriteNode("joined", branches),
			)
			eng := NewEngine()
			ctx := context.Background()

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				result := eng.Run(ctx, d)
				ReleaseDagResult(result)
			}
		})
	}
}

func BenchmarkEngineRun_RetryAmplification(b *testing.B) {
	for _, cfg := range []struct {
		nodes         int
		flakyEvery    int
		retryCount    int
		maxConcurrent int
	}{
		{nodes: 32, flakyEvery: 4, retryCount: 1, maxConcurrent: 8},
		{nodes: 128, flakyEvery: 4, retryCount: 2, maxConcurrent: 16},
	} {
		name := fmt.Sprintf("nodes=%d/flakyEvery=%d/retries=%d", cfg.nodes, cfg.flakyEvery, cfg.retryCount)
		b.Run(name, func(b *testing.B) {
			d := buildRetryAmplificationDAG(cfg.nodes, cfg.flakyEvery, cfg.retryCount, cfg.maxConcurrent)
			eng := NewEngine()
			ctx := context.Background()

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				result := eng.Run(ctx, d)
				ReleaseDagResult(result)
			}
		})
	}
}

func BenchmarkEngineRun_ParallelRequests(b *testing.B) {
	for _, parallelism := range []int{1, 4, 16} {
		b.Run(fmt.Sprintf("parallelism=%d", parallelism), func(b *testing.B) {
			d := buildParallelRequestDAG(32, 8, 200*time.Microsecond)
			eng := NewEngine()
			ctx := context.Background()

			b.ReportAllocs()
			b.SetParallelism(parallelism)
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					result := eng.Run(ctx, d)
					ReleaseDagResult(result)
				}
			})
		})
	}
}

func BenchmarkSharedStore_HotKeyContention(b *testing.B) {
	for _, readersPerWrite := range []int{1, 4, 16} {
		b.Run(fmt.Sprintf("readersPerWrite=%d", readersPerWrite), func(b *testing.B) {
			store := NewSharedStore()
			var seq uint64

			b.ReportAllocs()
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					n := atomic.AddUint64(&seq, 1)
					if n%uint64(readersPerWrite+1) == 0 {
						store.Set("hot", n)
						continue
					}
					store.Get("hot")
				}
			})
		})
	}
}

func buildWideDAG(name string, nodes, maxConcurrency int, fn NodeFunc) *DAG {
	d := NewDAG(name, WithMaxConcurrency(maxConcurrency))
	for i := 0; i < nodes; i++ {
		_ = d.AddNode(fmt.Sprintf("N%d", i), fn, NodeWithPriority(nodes-i))
	}
	return d
}

func buildDeepDAG(name string, nodes int, fn NodeFunc) *DAG {
	d := NewDAG(name, WithMaxConcurrency(1))
	for i := 0; i < nodes; i++ {
		opts := []NodeOption{NodeWithPriority(nodes - i)}
		if i > 0 {
			opts = append(opts, NodeWithDependsOn(fmt.Sprintf("N%d", i-1)))
		}
		_ = d.AddNode(fmt.Sprintf("N%d", i), fn, opts...)
	}
	return d
}

func buildFanOutFanInDAG(name string, branches, maxConcurrency int, branchFn, joinFn NodeFunc) *DAG {
	d := NewDAG(name, WithMaxConcurrency(maxConcurrency))
	_ = d.AddNode("root", noop, NodeWithPriority(branches+1))

	deps := make([]string, 0, branches)
	for i := 0; i < branches; i++ {
		nodeName := fmt.Sprintf("branch_%d", i)
		deps = append(deps, nodeName)
		_ = d.AddNode(
			nodeName,
			branchFn,
			NodeWithDependsOn("root"),
			NodeWithPriority(branches-i),
		)
	}
	_ = d.AddNode("join", joinFn, NodeWithDependsOn(deps...))
	return d
}

func buildRetryAmplificationDAG(nodes, flakyEvery, retryCount, maxConcurrency int) *DAG {
	d := NewDAG("retry-amplification", WithMaxConcurrency(maxConcurrency))
	for i := 0; i < nodes; i++ {
		name := fmt.Sprintf("N%d", i)
		if i > 0 && i%flakyEvery == 0 {
			_ = d.AddNode(
				name,
				makeStoreBackedFailThenSucceedNode(name, 1),
				NodeWithRetry(retryCount, time.Microsecond),
				NodeWithPriority(nodes-i),
			)
			continue
		}
		_ = d.AddNode(name, noop, NodeWithPriority(nodes-i))
	}
	return d
}

func buildParallelRequestDAG(branches, maxConcurrency int, branchLatency time.Duration) *DAG {
	d := NewDAG("parallel-requests", WithMaxConcurrency(maxConcurrency))
	_ = d.AddNode("root", noop, NodeWithPriority(branches+1))

	deps := make([]string, 0, branches)
	for i := 0; i < branches; i++ {
		nodeName := fmt.Sprintf("branch_%d", i)
		deps = append(deps, nodeName)
		_ = d.AddNode(
			nodeName,
			makeSleepAndWriteNode(nodeName, branchLatency, i),
			NodeWithDependsOn("root"),
			NodeWithPriority(branches-i),
		)
	}
	_ = d.AddNode("join", makeStoreReadWriteJoinNode(branches), NodeWithDependsOn(deps...))
	return d
}

func makeSleepNode(d time.Duration) NodeFunc {
	return func(ctx context.Context, _ *SharedStore) error {
		select {
		case <-time.After(d):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func makeStoreWriteNode(prefix string, branches int) NodeFunc {
	return func(_ context.Context, store *SharedStore) error {
		for i := 0; i < branches; i++ {
			store.Set(prefix+strconv.Itoa(i), i)
		}
		return nil
	}
}

func makeStoreReadWriteJoinNode(branches int) NodeFunc {
	return func(_ context.Context, store *SharedStore) error {
		total := 0
		for i := 0; i < branches; i++ {
			key := fmt.Sprintf("branch_%d", i)
			if v, ok := store.Get(key); ok {
				if iv, ok := v.(int); ok {
					total += iv
				}
			}
		}
		store.Set("join_total", total)
		return nil
	}
}

func makeSleepAndWriteNode(key string, d time.Duration, value int) NodeFunc {
	return func(ctx context.Context, store *SharedStore) error {
		select {
		case <-time.After(d):
			store.Set(key, value)
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func makeStoreBackedFailThenSucceedNode(nodeName string, failuresBeforeSuccess int32) NodeFunc {
	attemptKey := "__attempts__" + nodeName
	return func(_ context.Context, store *SharedStore) error {
		var attempts int32
		if v, ok := store.Get(attemptKey); ok {
			attempts = v.(int32)
		}
		attempts++
		store.Set(attemptKey, attempts)
		if attempts <= failuresBeforeSuccess {
			return errSyntheticRetry
		}
		return nil
	}
}

var errSyntheticRetry = errors.New("synthetic retry trigger")

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
