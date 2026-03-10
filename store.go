package dagbee

import (
	"fmt"
	"sync"
)

// SharedStore provides a concurrency-safe key-value store for passing data
// between nodes during DAG execution. It uses sync.RWMutex rather than
// sync.Map because DAG execution involves frequent writes of new keys,
// where RWMutex performs better and provides clearer semantics.
type SharedStore struct {
	mu   sync.RWMutex
	data map[string]interface{}
}

// NewSharedStore creates an empty SharedStore ready for use.
func NewSharedStore() *SharedStore {
	return &SharedStore{
		data: make(map[string]interface{}),
	}
}

// Set stores a value under the given key, overwriting any existing value.
func (s *SharedStore) Set(key string, value interface{}) {
	s.mu.Lock()
	s.data[key] = value
	s.mu.Unlock()
}

// Get retrieves a value by key. Returns (value, true) if found, or (nil, false) otherwise.
func (s *SharedStore) Get(key string) (interface{}, bool) {
	s.mu.RLock()
	v, ok := s.data[key]
	s.mu.RUnlock()
	return v, ok
}

// MustGet retrieves a value by key, panicking if the key does not exist.
func (s *SharedStore) MustGet(key string) interface{} {
	v, ok := s.Get(key)
	if !ok {
		panic(fmt.Sprintf("dagbee: key %q not found in SharedStore", key))
	}
	return v
}

// Keys returns all keys currently stored (in no particular order).
func (s *SharedStore) Keys() []string {
	s.mu.RLock()
	keys := make([]string, 0, len(s.data))
	for k := range s.data {
		keys = append(keys, k)
	}
	s.mu.RUnlock()
	return keys
}

// Len returns the number of entries in the store.
func (s *SharedStore) Len() int {
	s.mu.RLock()
	n := len(s.data)
	s.mu.RUnlock()
	return n
}

// Reset removes all entries, preparing the store for reuse.
func (s *SharedStore) Reset() {
	s.mu.Lock()
	for k := range s.data {
		delete(s.data, k)
	}
	s.mu.Unlock()
}

// GetTyped retrieves a value from the store with compile-time type safety (Go 1.18+).
// Returns an error if the key is missing or the stored type does not match T.
func GetTyped[T any](s *SharedStore, key string) (T, error) {
	v, ok := s.Get(key)
	if !ok {
		var zero T
		return zero, fmt.Errorf("dagbee: key %q not found in SharedStore", key)
	}
	typed, ok := v.(T)
	if !ok {
		var zero T
		return zero, fmt.Errorf("dagbee: key %q has type %T, want %T", key, v, zero)
	}
	return typed, nil
}
