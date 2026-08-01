package dagbee

import (
	"context"
	"fmt"
	"sync"
	"testing"
)

func BenchmarkDAGBuild_20Nodes(b *testing.B) {
	for i := 0; i < b.N; i++ {
		d := NewDAG("bench", WithMaxConcurrency(8))
		for j := 0; j < 20; j++ {
			name := fmt.Sprintf("node_%d", j)
			var deps []string
			if j > 0 && j%4 == 0 {
				deps = []string{fmt.Sprintf("node_%d", j-1)}
			}
			d.AddNode(name, noop, NodeWithDependsOn(deps...), NodeWithPriority(j))
		}
		d.Validate()
	}
}

func BenchmarkEngineRun_Parallel_10(b *testing.B) {
	d := NewDAG("bench-parallel", WithMaxConcurrency(10))
	for i := 0; i < 10; i++ {
		d.AddNode(fmt.Sprintf("N%d", i), noop)
	}

	eng := NewEngine()
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		result := eng.Run(ctx, d)
		ReleaseDagResult(result)
	}
}

func BenchmarkEngineRun_Serial_10(b *testing.B) {
	d := NewDAG("bench-serial", WithMaxConcurrency(1))
	d.AddNode("N0", noop)
	for i := 1; i < 10; i++ {
		d.AddNode(fmt.Sprintf("N%d", i), noop,
			NodeWithDependsOn(fmt.Sprintf("N%d", i-1)))
	}

	eng := NewEngine()
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		result := eng.Run(ctx, d)
		ReleaseDagResult(result)
	}
}

func BenchmarkDAGContext_ConcurrentRW(b *testing.B) {
	s := NewDAGContext()
	var wg sync.WaitGroup

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			s.Set("key", i)
		}()
		go func() {
			defer wg.Done()
			s.Get("key")
		}()
		wg.Wait()
	}
}

// BenchmarkDAGContext_DistinctKeyContention simulates fan-out: many goroutines
// writing distinct keys concurrently. With sharded locks, these should proceed
// in parallel; the old single-lock design serialized all writers.
func BenchmarkDAGContext_DistinctKeyContention(b *testing.B) {
	for _, writers := range []int{8, 32, 128} {
		b.Run(fmt.Sprintf("writers=%d", writers), func(b *testing.B) {
			dctx := NewDAGContext()
			b.ReportAllocs()
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				i := 0
				for pb.Next() {
					dctx.Set(fmt.Sprintf("key_%d", i%writers), i)
					i++
				}
			})
		})
	}
}
