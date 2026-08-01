package dagbee

import "sync"

// workerPool runs a fixed number of worker goroutines that pull nodes from
// readyCh, execute them, and send results to doneCh. This replaces per-node
// goroutine creation, eliminating goroutine allocation overhead at scale.
type workerPool struct {
	readyCh chan *Node
	doneCh  chan<- *NodeResult
	exec    func(*Node) *NodeResult
	wg      sync.WaitGroup
	workers int
	once    sync.Once
}

func newWorkerPool(workers int, exec func(*Node) *NodeResult, doneCh chan<- *NodeResult) *workerPool {
	return &workerPool{
		readyCh: make(chan *Node, workers),
		doneCh:  doneCh,
		exec:    exec,
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
	for node := range wp.readyCh {
		wp.doneCh <- wp.exec(node)
	}
}

// stop closes readyCh, causing all workers to exit after finishing their
// current node. Callers should ensure no further sends to readyCh occur.
func (wp *workerPool) stop() {
	wp.once.Do(func() { close(wp.readyCh) })
}

// wait blocks until all workers have exited.
func (wp *workerPool) wait() {
	wp.wg.Wait()
}
