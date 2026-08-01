# Subflow 异步调度优化方案

> 对标 C++ Taskflow corun、go-taskflow 全局队列、Temporal Child Workflow，将同步阻塞式 subflow 改造为异步投递式，消除 worker 阻塞和 stealCh 补丁。

> **状态：已实现** (2026-08)。56 个测试全部通过，`-race -count=5` 零失败。
> 详见 `docs/issuesAndStrategy.md` #4 和 `docs/subflow-design.md` 末尾的「异步调度重构」小节。

---

## 业内实现对比

### C++ Taskflow：corun 协作式等待

Taskflow 的 subflow 节点执行时，worker 调用 `rt.corun()` 等待子任务完成。关键点：

- **worker 不阻塞**：corun 让 worker 进入 work-stealing 循环，持续从全局队列或其他 worker 的本地队列偷取任务执行
- **栈帧保留**：corun 保留调用者的栈帧，子任务完成后直接恢复执行，局部变量状态完整
- **隐式 join**：runtime scope 内 spawn 的所有 async 任务在 scope 结束时自动 join，不需要手动管理
- **BWSQ 本地队列**：每个 worker 有独立的 Binomial Work-Stealing Queue，空闲时跨线程偷取，实现负载均衡

核心思想：**等待者参与执行**。和 DagBee 当前的 stealCh 方案理念一致，但 Taskflow 有真正的 work-stealing queue 而非共享 channel。

### go-taskflow：全局队列 + condition variable

go-taskflow 的 subflow 也是同步阻塞式——`invokeSubflow` 在 worker goroutine 内调用 `scheduleGraph` → `invokeGraph`，后者通过 `sync.Cond` 等待子图完成：

```go
func (e *innerExecutorImpl) invokeGraph(g *eGraph, parentSpan *span) bool {
    for {
        g.scheCond.L.Lock()
        e.mu.Lock()
        for !g.recyclable() && e.wq.Len() == 0 && !g.canceled.Load() {
            e.mu.Unlock()
            g.scheCond.Wait()  // ← 阻塞等待，不参与执行
            e.mu.Lock()
        }
        // ...
        node := e.wq.Pop()
        e.mu.Unlock()
        e.invokeNode(node, parentSpan)  // ← 有任务就执行
    }
}
```

和 DagBee 类似：worker 阻塞在 `invokeGraph` 里，有任务时从全局队列 pop 执行，无任务时 `Cond.Wait()` 睡眠。但 go-taskflow 用 `sync.Cond` 而非 channel，唤醒机制不同。

go-taskflow 的注释明确建议 `concurrency` 必须大于 subflow 数量，否则会死锁——它没有 work-stealing。

### Temporal：Child Workflow 异步 + Future

Temporal 的 child workflow 完全异步：

```go
childFuture := workflow.ExecuteChildWorkflow(ctx, childWorkflow, params)
// 父 workflow 不阻塞，可以继续做其他事
// 需要结果时：
var result ChildResp
childFuture.Get(ctx, &result)  // ← 此时才等待
```

- child workflow 在独立的 task queue 上执行，和父 workflow 完全解耦
- 父通过 Future/Promise 模式异步等待结果
- 支持 `ParentClosePolicy`：父结束时 child 可以 ABANDON（继续运行）、TERMINATE、REQUEST_CANCELLATION

### 对比总结

| 维度 | C++ Taskflow | go-taskflow | Temporal | DagBee（当前） |
|------|-------------|-------------|----------|---------------|
| subflow 执行模型 | 同步 corun | 同步阻塞 | 异步 Future | 同步阻塞 |
| worker 阻塞 | 不阻塞（stealing） | 阻塞（Cond.Wait） | 不阻塞 | 阻塞（select steal） |
| 队列模型 | per-worker BWSQ | 全局队列 | 分布式 task queue | 全局无缓冲 channel |
| 负载均衡 | 主动偷取 | 无 | 平台调度 | 被动 stealCh |
| 死锁风险 | 无 | 有（concurrency > subflow数） | 无 | 无（stealCh 兜底） |
| 栈深度 | 递归 corun | 递归 invokeGraph | 无递归 | 递归 executeDAG |

---

## 优化目标

1. **消除 worker 阻塞**：subflow 节点不再同步阻塞 worker，worker 投递子图后立即释放
2. **删除 stealCh**：不需要窃取兜底，简化 event loop
3. **降低栈深度**：不递归调用 `executeDAG`，子图节点直接进入全局调度
4. **保持现有 API 不变**：`NodeWithSubflow`、`SubflowResult`、共享 `dctx` 等

---

## 核心设计：异步 Join 模式

### 执行流程

```
当前（同步）:
  worker → executeNode(subflow) → SubflowFn → executeDAG(subDAG) [阻塞] → 返回结果

优化后（异步）:
  worker → executeNode(subflow)
    → SubflowFn 构建子 DAG
    → 子 DAG 的入度为 0 的节点批量推入 wp.readyCh
    → 创建 JoinTracker 跟踪子图完成数
    → worker 立即返回，subflow 节点标记为 "waiting"
    → 子图节点由其他空闲 worker 执行
    → 子图全部完成 → JoinTracker 通过 doneCh 通知父 DAG
    → 父 DAG event loop 收到通知，回填 SubflowResult，传播依赖
```

### JoinTracker 设计

```go
// subflowTracker tracks the completion of a child DAG's nodes.
// When all child nodes complete, it sends the child DagResult to
// the parent's doneCh, unblocking the parent's event loop.
type subflowTracker struct {
    childDAG    *DAG
    childResult *DagResult
    completed   int32  // atomic counter
    total       int    // total nodes in child DAG
    parentDoneCh chan<- *NodeResult  // parent's doneCh
    parentNr    *NodeResult          // pre-allocated parent NodeResult
    mu          sync.Mutex
}

// onComplete is called when a child node finishes.
// When all child nodes are done, it finalizes the child DagResult
// and sends the parent NodeResult to the parent's doneCh.
func (t *subflowTracker) onComplete(nr *NodeResult) {
    t.childResult.Results[nr.NodeName] = nr
    if atomic.AddInt32(&t.completed, 1) == int32(t.total) {
        // All child nodes done — finalize and notify parent.
        t.childResult.Status = StatusSuccess // or check for failures
        t.parentNr.SubflowResult = t.childResult
        t.parentNr.Status = StatusSuccess
        t.parentDoneCh <- t.parentNr
    }
}
```

### executeNode 改造

subflow 分支不再递归调用 `executeDAG`，而是：

```go
if n.SubflowFn != nil {
    subDAG, err := n.SubflowFn(ctx, dctx)
    // ... validation, depth check ...

    // Create tracker for child DAG completion.
    tracker := &subflowTracker{
        childDAG:     subDAG,
        childResult:  AcquireDagResult(),
        total:        len(subDAG.nodes),
        parentDoneCh: doneCh,         // parent's doneCh
        parentNr:     nr,              // this node's NodeResult
    }

    // Create a child doneCh for child node results.
    childDoneCh := make(chan *NodeResult, len(subDAG.nodes))

    // Start a child event loop goroutine that drains childDoneCh
    // and calls tracker.onComplete for each result.
    go e.runChildEventLoop(dagCtx, subDAG, dctx, wp, d.hooks, depth+1, logger, childDoneCh, tracker)

    // Dispatch child DAG's zero-in-degree nodes to the shared worker pool.
    // Worker is released immediately — no blocking.
    return nil // signal "not done yet, waiting for child"
}
```

### runChildEventLoop

子图有自己独立的 event loop goroutine，但不占用 worker slot——它只是一个轻量级调度器：

```go
func (e *Engine) runChildEventLoop(
    dagCtx context.Context,
    subDAG *DAG,
    dctx *DAGContext,
    wp *workerPool,
    parentHooks *HookChain,
    depth int,
    logger Logger,
    childDoneCh chan *NodeResult,
    tracker *subflowTracker,
) {
    // Same event loop as executeDAG, but:
    // 1. No stealCh needed — this goroutine is not a worker
    // 2. When all child nodes complete, call tracker.onComplete
    // 3. Child nodes are dispatched to shared wp.readyCh
    //    (workers pick them up)
    // ...
}
```

### event loop 简化

删除 `stealCh` 后，event loop 只剩三路 select：

```go
select {
case readySendCh <- nextTask:
    // dispatch to worker
case nr := <-doneCh:
    // process result (includes subflow completion via tracker)
case <-dagCtx.Done():
    // timeout/cancel
}
```

---

## 关键问题与解法

### 问题 1：子图 event loop goroutine 不占 worker，但谁来执行子节点？

子节点通过 `wp.readyCh` 投递给共享 worker pool 的空闲 worker。子图 event loop 只负责调度（入队、传播依赖、收集结果），不执行节点。这和当前模型的区别：

- 当前：subflow 的 event loop 跑在 worker 上，既调度又（通过 stealCh）执行
- 优化后：subflow 的 event loop 跑在独立 goroutine 上，只调度；执行由 pool worker 负责

### 问题 2：所有 worker 都在执行其他任务时，子图节点排队等待

这是正确行为——并发度受 `maxConcurrency` 控制。子图节点和父图节点竞争同一批 worker slot，优先级由 scheduler 决定。不会死锁，因为 worker 执行完任务后回到 `range readyCh`，会拿到排队的子图节点。

### 问题 3：子图 event loop goroutine 的生命周期

子图全部完成后，`runChildEventLoop` 退出，goroutine 自动回收。不需要 `wp.stop()` / `wp.wait()`。goroutine 数量 = 活跃的 subflow 层数，通常很少。

### 问题 4：失败传播

子图中关键节点失败时，`runChildEventLoop` 取消子图的 `dagCtx`，标记剩余节点为 Skipped，然后通过 `tracker.onComplete` 通知父 DAG。父 DAG 根据 `childResult.Status` 设置 subflow 节点的状态。

### 问题 5：ctx 传播

`dagCtx` 仍然通过参数透传。子图的 `context.WithTimeout` 仍然取 min(父超时, 子超时)。父取消时子图通过 ctx 收到取消信号。

---

## 改动文件清单

| 文件 | 改动 | 说明 |
|------|------|------|
| `engine.go` | `executeNode` subflow 分支改为异步投递；新增 `runChildEventLoop`；删除 `stealCh` | 核心改动 |
| `engine.go` | `executeDAG` event loop 删除 `stealCh` 分支和 `depth > 0` 判断 | 简化 |
| `workerpool.go` | 无结构改动 | readyCh 保持无缓冲 |
| 新增 `subflow_tracker.go` | `subflowTracker` 结构和 `onComplete` 逻辑 | Join 等待器 |
| `result.go` | 无改动 | SubflowResult 已支持 |
| `node.go` | 无改动 | SubflowFn 已支持 |
| `options.go` | 无改动 | maxSubflowDepth 已支持 |

---

## 对比当前方案

| 维度 | 当前（同步 + stealCh） | 优化后（异步 Join） |
|------|----------------------|-------------------|
| worker 占用 | 整个子图执行期间 | 仅 SubflowFn 构图阶段 |
| 死锁防护 | stealCh 兜底 | 不需要，worker 不阻塞 |
| event loop | 4 路 select（含 stealCh） | 3 路 select |
| 栈深度 | 递归 executeDAG | 无递归 |
| goroutine 数 | worker 数（固定） | worker 数 + 活跃 subflow 层数 |
| 调度公平性 | stealCh 随机窃取 | 子图节点和父图节点公平竞争 worker |
| 改动量 | 已完成 | 中等（engine.go + 新增 tracker） |

---

## 实现顺序

1. **`subflow_tracker.go`** — `subflowTracker` 结构、`onComplete` 逻辑
2. **`engine.go`** — `runChildEventLoop`（从 `executeDAG` 提取，去掉 stealCh）
3. **`engine.go`** — `executeNode` subflow 分支改为异步投递
4. **`engine.go`** — `executeDAG` 删除 `stealCh` 相关代码
5. **测试** — 现有 10 个 subflow 测试全部通过 + 新增异步行为验证
6. **文档更新** — subflow-design.md、issuesAndStrategy.md

---

## 风险评估

| 风险 | 级别 | 应对 |
|------|------|------|
| 子图 event loop goroutine 泄漏 | P2 | tracker.onComplete 保证 goroutine 退出；dagCtx.Done() 兜底 |
| 子图节点和父图节点竞争 worker | 低 | 正确行为，优先级由 scheduler 控制 |
| SubflowResult 内存管理 | 已解决 | releaseDagResultRecursive 已实现 |
| 异步完成通知丢失 | P1 | childDoneCh buffer = 子节点数，保证不阻塞 |
| ctx 取消传播 | 无风险 | context.WithTimeout 标准行为 |
| 嵌套深度 | 无风险 | maxSubflowDepth 限制，无递归调用栈 |
