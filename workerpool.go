package dagbee

import "sync"

// execTask wraps a node with its target doneCh and exec function, allowing
// a shared worker pool to route results to the correct DAG layer.
type execTask struct {
	node   *Node
	doneCh chan<- *NodeResult
	exec   func(*Node) *NodeResult
}

// workerPool runs a fixed number of worker goroutines that pull execTasks
// from readyCh, execute them, and send results to the task's doneCh.
// The pool is shared across the entire execution tree (parent + all subflows),
// so total concurrency is always bounded by the worker count.
//
// readyCh is unbuffered: sends block until a worker is ready to receive.
// The event loop uses a select on readyCh send to dispatch tasks only when
// a worker is idle, avoiding buffer accumulation.
type workerPool struct {
	readyCh chan *execTask
	wg      sync.WaitGroup
	workers int
	once    sync.Once
}

func newWorkerPool(workers int) *workerPool {
	return &workerPool{
		readyCh: make(chan *execTask),
		workers: workers,
	}
}

func (wp *workerPool) start() {
	for i := 0; i < wp.workers; i++ {
		wp.wg.Add(1)
		go wp.worker()
	}
}

func (wp *workerPool) worker() {
	defer wp.wg.Done()
	for t := range wp.readyCh {
		// exec returns nil when the node is an async subflow — the result
		// will be sent to doneCh later by a background goroutine.
		if nr := t.exec(t.node); nr != nil {
			t.doneCh <- nr
		}
	}
}

// stop closes readyCh, causing all workers to exit after finishing their
// current task. Callers should ensure no further sends to readyCh occur.
func (wp *workerPool) stop() {
	wp.once.Do(func() { close(wp.readyCh) })
}

// wait blocks until all workers have exited.
func (wp *workerPool) wait() {
	wp.wg.Wait()
}
