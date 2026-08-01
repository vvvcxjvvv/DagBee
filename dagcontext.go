package dagbee

import (
	"fmt"
	"hash/fnv"
	"runtime"
	"sync"
)

// roundupPow2 rounds n up to the next power of two (minimum 1).
func roundupPow2(n int) int {
	if n <= 1 {
		return 1
	}
	n--
	n |= n >> 1
	n |= n >> 2
	n |= n >> 4
	n |= n >> 8
	n |= n >> 16
	n++
	return n
}

// defaultShardCount returns the default number of shards: NumCPU * 4 rounded
// up to a power of two.
func defaultShardCount() int {
	return roundupPow2(runtime.NumCPU() * 4)
}

// dctxShard holds a partition of the key-value store with its own lock.
type dctxShard struct {
	mu   sync.RWMutex
	data map[string]interface{}
}

// DAGContext provides a concurrency-safe key-value store for passing data
// between nodes during DAG execution. It uses a sharded lock design: keys
// are distributed across N independent shards by FNV-1a hash, so writes to
// different keys proceed in parallel without lock contention.
type DAGContext struct {
	shards []*dctxShard
	mask   uint64 // len(shards) - 1; shard index = hash & mask
}

// NewDAGContext creates an empty DAGContext with the default shard count.
func NewDAGContext() *DAGContext {
	return newDAGContextWithShards(defaultShardCount())
}

// newDAGContextWithShards creates an empty DAGContext with the given shard count.
// n is clamped to a minimum of 1; it need not be a power of two (shardIndex uses
// modulo, not bitmask, when n is not a power of two).
func newDAGContextWithShards(n int) *DAGContext {
	if n < 1 {
		n = 1
	}
	shards := make([]*dctxShard, n)
	for i := range shards {
		shards[i] = &dctxShard{data: make(map[string]interface{})}
	}
	return &DAGContext{
		shards: shards,
		mask:   uint64(n) - 1,
	}
}

// shardIndex returns the shard index for the given key.
// Uses bitmask when shard count is a power of two, otherwise falls back to modulo.
func (d *DAGContext) shardIndex(key string) int {
	h := fnv.New64a()
	h.Write([]byte(key))
	hash := h.Sum64()
	if isPow2(len(d.shards)) {
		return int(hash & d.mask)
	}
	return int(hash % uint64(len(d.shards)))
}

// isPow2 returns true if n is a positive power of two.
func isPow2(n int) bool {
	return n > 0 && n&(n-1) == 0
}

// shard returns the shard that owns the given key.
func (d *DAGContext) shard(key string) *dctxShard {
	return d.shards[d.shardIndex(key)]
}

// Set stores a value under the given key, overwriting any existing value.
func (d *DAGContext) Set(key string, value interface{}) {
	s := d.shard(key)
	s.mu.Lock()
	s.data[key] = value
	s.mu.Unlock()
}

// Get retrieves a value by key. Returns (value, true) if found, or (nil, false) otherwise.
func (d *DAGContext) Get(key string) (interface{}, bool) {
	s := d.shard(key)
	s.mu.RLock()
	v, ok := s.data[key]
	s.mu.RUnlock()
	return v, ok
}

// MustGet retrieves a value by key, panicking if the key does not exist.
func (d *DAGContext) MustGet(key string) interface{} {
	v, ok := d.Get(key)
	if !ok {
		panic(fmt.Sprintf("dagbee: key %q not found in DAGContext", key))
	}
	return v
}

// Keys returns all keys currently stored (in no particular order).
func (d *DAGContext) Keys() []string {
	var keys []string
	for _, s := range d.shards {
		s.mu.RLock()
		if keys == nil {
			keys = make([]string, 0, len(s.data))
		}
		for k := range s.data {
			keys = append(keys, k)
		}
		s.mu.RUnlock()
	}
	return keys
}

// Len returns the number of entries in the context.
func (d *DAGContext) Len() int {
	n := 0
	for _, s := range d.shards {
		s.mu.RLock()
		n += len(s.data)
		s.mu.RUnlock()
	}
	return n
}

// Reset removes all entries, preparing the context for reuse.
func (d *DAGContext) Reset() {
	for _, s := range d.shards {
		s.mu.Lock()
		for k := range s.data {
			delete(s.data, k)
		}
		s.mu.Unlock()
	}
}

// GetTyped retrieves a value from the context with compile-time type safety (Go 1.18+).
// Returns an error if the key is missing or the stored type does not match T.
func GetTyped[T any](d *DAGContext, key string) (T, error) {
	v, ok := d.Get(key)
	if !ok {
		var zero T
		return zero, fmt.Errorf("dagbee: key %q not found in DAGContext", key)
	}
	typed, ok := v.(T)
	if !ok {
		var zero T
		return zero, fmt.Errorf("dagbee: key %q has type %T, want %T", key, v, zero)
	}
	return typed, nil
}
