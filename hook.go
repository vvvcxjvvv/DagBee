package dagbee

import "context"

// Hook defines lifecycle callbacks for DAG execution.
// Implementations must be safe for concurrent invocation.
type Hook interface {
	BeforeNode(ctx context.Context, nodeName string)
	AfterNode(ctx context.Context, nodeName string, result *NodeResult)
	OnNodeSkip(ctx context.Context, nodeName string, reason string)
	OnDAGComplete(ctx context.Context, result *DagResult)
}

// NoopHook is a default Hook implementation that does nothing.
type NoopHook struct{}

func (NoopHook) BeforeNode(context.Context, string)             {}
func (NoopHook) AfterNode(context.Context, string, *NodeResult) {}
func (NoopHook) OnNodeSkip(context.Context, string, string)     {}
func (NoopHook) OnDAGComplete(context.Context, *DagResult)      {}

// HookChain chains multiple hooks and invokes them in registration order.
type HookChain struct {
	hooks []Hook
}

// NewHookChain creates a chain from the given hooks.
func NewHookChain(hooks ...Hook) *HookChain {
	return &HookChain{hooks: hooks}
}

// Add appends a hook to the chain.
func (c *HookChain) Add(h Hook) {
	c.hooks = append(c.hooks, h)
}

func (c *HookChain) BeforeNode(ctx context.Context, nodeName string) {
	for _, h := range c.hooks {
		h.BeforeNode(ctx, nodeName)
	}
}

func (c *HookChain) AfterNode(ctx context.Context, nodeName string, result *NodeResult) {
	for _, h := range c.hooks {
		h.AfterNode(ctx, nodeName, result)
	}
}

func (c *HookChain) OnNodeSkip(ctx context.Context, nodeName string, reason string) {
	for _, h := range c.hooks {
		h.OnNodeSkip(ctx, nodeName, reason)
	}
}

func (c *HookChain) OnDAGComplete(ctx context.Context, result *DagResult) {
	for _, h := range c.hooks {
		h.OnDAGComplete(ctx, result)
	}
}

// Len returns the number of hooks in the chain.
func (c *HookChain) Len() int {
	return len(c.hooks)
}
