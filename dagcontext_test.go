package dagbee

import (
	"context"
	"fmt"
	"sync"
	"testing"
)

func TestDAGContext_SetGet(t *testing.T) {
	s := NewDAGContext()
	s.Set("key", "value")

	v, ok := s.Get("key")
	if !ok || v != "value" {
		t.Fatalf("expected (value, true), got (%v, %v)", v, ok)
	}

	_, ok = s.Get("missing")
	if ok {
		t.Fatal("expected false for missing key")
	}
}

func TestDAGContext_MustGet(t *testing.T) {
	s := NewDAGContext()
	s.Set("k", 42)

	v := s.MustGet("k")
	if v != 42 {
		t.Fatalf("expected 42, got %v", v)
	}
}

func TestDAGContext_MustGet_Panics(t *testing.T) {
	s := NewDAGContext()

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for missing key")
		}
	}()
	s.MustGet("no-such-key")
}

func TestGetTyped(t *testing.T) {
	s := NewDAGContext()
	s.Set("num", 100)

	v, err := GetTyped[int](s, "num")
	if err != nil {
		t.Fatal(err)
	}
	if v != 100 {
		t.Fatalf("expected 100, got %d", v)
	}
}

func TestGetTyped_WrongType(t *testing.T) {
	s := NewDAGContext()
	s.Set("num", "not-an-int")

	_, err := GetTyped[int](s, "num")
	if err == nil {
		t.Fatal("expected type mismatch error")
	}
}

func TestGetTyped_Missing(t *testing.T) {
	s := NewDAGContext()
	_, err := GetTyped[int](s, "missing")
	if err == nil {
		t.Fatal("expected missing key error")
	}
}

func TestDAGContext_Keys(t *testing.T) {
	s := NewDAGContext()
	s.Set("a", 1)
	s.Set("b", 2)

	keys := s.Keys()
	if len(keys) != 2 {
		t.Fatalf("expected 2 keys, got %d", len(keys))
	}
}

func TestDAGContext_Len(t *testing.T) {
	s := NewDAGContext()
	if s.Len() != 0 {
		t.Fatal("expected empty context")
	}
	s.Set("a", 1)
	if s.Len() != 1 {
		t.Fatalf("expected len 1, got %d", s.Len())
	}
}

func TestDAGContext_Reset(t *testing.T) {
	s := NewDAGContext()
	s.Set("a", 1)
	s.Set("b", 2)
	s.Reset()

	if s.Len() != 0 {
		t.Fatalf("expected empty context after reset, got %d", s.Len())
	}
}

func TestDAGContext_ConcurrentReadWrite(t *testing.T) {
	s := NewDAGContext()
	const goroutines = 100
	const iterations = 100

	var wg sync.WaitGroup
	wg.Add(goroutines * 2)

	// Writers
	for g := 0; g < goroutines; g++ {
		go func(id int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				s.Set("shared", id*iterations+i)
			}
		}(g)
	}

	// Readers
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				s.Get("shared")
			}
		}()
	}

	wg.Wait()

	// If we got here without a race detector complaint, concurrency is safe.
	if s.Len() != 1 {
		t.Fatalf("expected 1 key, got %d", s.Len())
	}
}

func TestNewDAGContextWithShards(t *testing.T) {
	dctx := newDAGContextWithShards(1)
	if len(dctx.shards) != 1 {
		t.Fatalf("expected 1 shard, got %d", len(dctx.shards))
	}

	dctx = newDAGContextWithShards(64)
	if len(dctx.shards) != 64 {
		t.Fatalf("expected 64 shards, got %d", len(dctx.shards))
	}

	// non-power-of-two
	dctx = newDAGContextWithShards(7)
	if len(dctx.shards) != 7 {
		t.Fatalf("expected 7 shards, got %d", len(dctx.shards))
	}

	// clamped to 1
	dctx = newDAGContextWithShards(0)
	if len(dctx.shards) != 1 {
		t.Fatalf("expected 1 shard for n=0, got %d", len(dctx.shards))
	}
}

func TestDAGContext_NonPow2ShardDistribution(t *testing.T) {
	dctx := newDAGContextWithShards(7)
	// Write 100 keys, verify they distribute across shards (not all in one).
	for i := 0; i < 100; i++ {
		dctx.Set(fmt.Sprintf("key_%d", i), i)
	}
	used := 0
	for _, s := range dctx.shards {
		s.mu.RLock()
		if len(s.data) > 0 {
			used++
		}
		s.mu.RUnlock()
	}
	if used < 3 {
		t.Fatalf("expected keys spread across >= 3 shards, got %d", used)
	}
}

func TestEngineWithDAGContextShards(t *testing.T) {
	eng := NewEngine(EngineWithDAGContextShards(1))
	if eng.dctxShards != 1 {
		t.Fatalf("expected dctxShards=1, got %d", eng.dctxShards)
	}

	// n < 1 is ignored
	eng = NewEngine(EngineWithDAGContextShards(0))
	if eng.dctxShards != 0 {
		t.Fatalf("expected dctxShards=0, got %d", eng.dctxShards)
	}

	// Verify engine uses configured shard count
	eng = NewEngine(EngineWithDAGContextShards(1))
	d := NewDAG("test")
	d.AddNode("A", func(_ context.Context, dctx *DAGContext) error {
		// With 1 shard, all keys go to the same shard
		dctx.Set("a", 1)
		return nil
	})
	result := eng.Run(context.Background(), d)
	if result.Status != StatusSuccess {
		t.Fatalf("expected success, got %s", result.Status)
	}
	ReleaseDagResult(result)
}
