package dagbee

import (
	"container/heap"
	"sync"
)

// SchedulerStrategy defines the interface for node scheduling policies.
type SchedulerStrategy interface {
	Enqueue(nodes ...*Node)
	Dequeue() *Node
	Peek() *Node
	Len() int
}

// priorityScheduler implements SchedulerStrategy using a max-heap
// ordered by Node.Priority (higher value = dequeued first).
type priorityScheduler struct {
	mu   sync.Mutex
	heap nodeHeap
}

func newPriorityScheduler() *priorityScheduler {
	s := &priorityScheduler{}
	heap.Init(&s.heap)
	return s
}

func (s *priorityScheduler) Enqueue(nodes ...*Node) {
	s.mu.Lock()
	for _, n := range nodes {
		heap.Push(&s.heap, n)
	}
	s.mu.Unlock()
}

func (s *priorityScheduler) Dequeue() *Node {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.heap.Len() == 0 {
		return nil
	}
	return heap.Pop(&s.heap).(*Node)
}

func (s *priorityScheduler) Peek() *Node {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.heap.Len() == 0 {
		return nil
	}
	return s.heap[0]
}

func (s *priorityScheduler) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.heap.Len()
}

// nodeHeap implements heap.Interface, ordered by descending Priority.
type nodeHeap []*Node

func (h nodeHeap) Len() int            { return len(h) }
func (h nodeHeap) Less(i, j int) bool  { return h[i].Priority > h[j].Priority }
func (h nodeHeap) Swap(i, j int)       { h[i], h[j] = h[j], h[i] }
func (h *nodeHeap) Push(x interface{}) { *h = append(*h, x.(*Node)) }

func (h *nodeHeap) Pop() interface{} {
	old := *h
	n := len(old)
	item := old[n-1]
	old[n-1] = nil // avoid memory leak
	*h = old[:n-1]
	return item
}
