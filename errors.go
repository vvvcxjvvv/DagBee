package dagbee

import (
	"errors"
	"fmt"
	"runtime"
)

var (
	ErrCycleDetected     = errors.New("dagbee: cycle detected in DAG")
	ErrNodeNotFound      = errors.New("dagbee: node not found")
	ErrDuplicateNode     = errors.New("dagbee: duplicate node name")
	ErrDependencyMissing = errors.New("dagbee: dependency node does not exist")
	ErrDAGTimeout        = errors.New("dagbee: DAG execution timeout")
	ErrNodeTimeout       = errors.New("dagbee: node execution timeout")
	ErrNodePanicked      = errors.New("dagbee: node panicked")
	ErrEmptyDAG          = errors.New("dagbee: DAG has no nodes")
	ErrNodeFuncNil       = errors.New("dagbee: node function is nil")
)

// PanicError wraps a recovered panic value with node context and stack trace.
type PanicError struct {
	NodeName   string
	Value      interface{}
	Stacktrace string
}

func (e *PanicError) Error() string {
	return fmt.Sprintf("dagbee: node %q panicked: %v\n%s", e.NodeName, e.Value, e.Stacktrace)
}

func (e *PanicError) Unwrap() error {
	return ErrNodePanicked
}

func capturePanicStack() string {
	buf := make([]byte, 4096)
	n := runtime.Stack(buf, false)
	return string(buf[:n])
}
