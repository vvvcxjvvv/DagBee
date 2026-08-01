# Subflow 支持改造计划

> 原生 SubflowNode 节点类型，支持运行时动态生成子 DAG 并递归执行。

---

## 目标

允许节点在执行时动态生成子 DAG，递归嵌入当前执行树，共享 DAGContext 和 worker pool，子图结果嵌套在父节点结果中。

## 现状

DAG 是静态构建的：`AddNode` 注册所有节点和边，`Validate` 做环检测，`Engine.Run` 拿到完整 DAG 后一次性调度。运行期间无法向 scheduler 动态添加节点。

用户可在 NodeFunc 内手动 `NewEngine().Run(ctx, subDAG)` 嵌入子 DAG，但存在五个问题：

1. **DAGContext 不共享** — 子 DAG 创建自己的 DAGContext，父数据需手动传递。
2. **Worker pool 不共享** — 子 DAG 独立 worker pool，并发度不协调，可能 goroutine 爆炸。
3. **结果不可见** — 子 DAG 的 NodeResult 藏在父节点结果里，父 DagResult 无法看到子节点执行细节。
4. **Hooks 断裂** — 子 DAG 用自己的 HookChain，父 hooks 无法观测子节点生命周期。
5. **超时/取消可传递** — ctx 透传，唯一做对的部分。

---

## 核心设计原则

| 原则 | 说明 |
|------|------|
| DAGContext 共享 | 父→子透传同一个 dctx，数据零拷贝传递 |
| Worker pool 共享 | 整个执行树共享一个 worker pool，总并发度始终 ≤ maxConcurrency |
| Work-stealing | event loop 等待 doneCh 时主动消费 readyCh，避免 subflow 死锁 |
| 结果嵌套 | 子 DagResult 挂在父 NodeResult.SubflowResult 上，可递归访问 |
| Hooks 继承 | 父 hooks 自动串联到子 DAG，子 DAG 可追加自己的 hooks |
| 深度限制 | maxSubflowDepth 防止无限递归，默认 10 层 |

---

## 改动拆解

### 1. 提取 `executeDAG`（engine.go）

将 `Run` 中的核心执行逻辑抽成可重入方法：

```go
func (e *Engine) executeDAG(
    ctx context.Context,       // 透传，含超时/取消
    d *DAG,                    // 当前层 DAG
    dctx *DAGContext,          // 共享，父→子透传
    wp *workerPool,            // 共享 worker pool
    parentHooks *HookChain,    // 父 hooks，nil 表示顶层
    depth int,                 // 递归深度
    logger Logger,
) *DagResult
```

`Run` 瘦身为：

1. `d.Validate()`
2. 创建 `dctx`
3. 创建 worker pool（per-Run，整个执行树共享）
4. 调用 `executeDAG(ctx, d, dctx, wp, nil, 0, logger)`
5. `wp.stop()` + `wp.wait()`
6. `d.hooks.OnDAGComplete(ctx, result)` + 日志
7. 返回

`executeDAG` 负责：

1. 创建 `dagCtx`（带当前层超时，`context.WithTimeout` 取 min(父超时, 子超时)）
2. 创建 `doneCh`（buffer = 当前层节点数）
3. 初始化 `pending`、`started`、`scheduler`
4. 主事件循环（含 work-stealing）
5. 返回 DagResult（不调用 OnDAGComplete，由顶层 Run 统一调用）

### 2. Worker pool 共享 + Work-stealing

#### 2.1 共享 worker pool

worker pool 提升到 Run 级别，整个执行树共享。不再每层 `executeDAG` 创建独立 pool。

```
Run
 ├─ wp (workers = maxConcurrency)
 └─ executeDAG(d, depth=0)
      ├─ launchReady → wp.readyCh <- execTask
      ├─ worker 执行 execTask
      │    ├─ 普通节点: Fn(ctx, dctx) → doneCh
      │    └─ subflow 节点: executeDAG(subDAG, depth+1)
      │         ├─ launchReady → wp.readyCh <- execTask  ← 共享同一个 wp
      │         ├─ worker 执行 execTask
      │         └─ 子 DAG doneCh → 子 event loop
      └─ doneCh → 父 event loop
```

- 总并发执行数始终 ≤ `maxConcurrency`
- `launchReady` 非阻塞投递，worker 满时节点排队在 scheduler 中

#### 2.2 Work-stealing 解决死锁

**问题**：subflow 节点同步阻塞在 `executeDAG` 的 event loop 中，占用 worker slot。若所有 worker 都在等 subflow 完成，子 DAG 的 `launchReady` 向 `wp.readyCh` 投递全部失败（走 `default`），子节点永远排队，**死锁**。

```
maxConcurrency=2:
  worker1 → executeDAG(subA) → 等 subA 的 doneCh
  worker2 → executeDAG(subB) → 等 subB 的 doneCh
  subA/subB 的 launchReady → wp.readyCh send 失败 → 子节点排队 → 永久 hang
```

**修复**：event loop 等待 `doneCh` 时主动消费 `wp.readyCh`，帮忙执行任意层的就绪任务。这是 Taskflow `corun` 的本质——等待者主动参与执行。

```go
for completed < total {
    select {
    case nr := <-doneCh:
        // 处理结果、传播依赖、launchReady
    case task := <-wp.readyCh:
        // work-stealing: 帮忙执行任意层的就绪任务
        task.doneCh <- task.exec(task.node)
    case <-dagCtx.Done():
        // 超时/取消
    }
}
```

效果：

- worker1 阻塞在 subA 的 event loop，select 中消费 `wp.readyCh`，执行 subB 的子节点
- worker2 阻塞在 subB 的 event loop，同理消费 subA 的子节点
- 子节点被推进，doneCh 有结果，event loop 解除阻塞
- 零死锁，不引入新 goroutine，不需要异步状态机

**额外收益**：work-stealing 提升资源利用率。普通场景中，event loop 等待 doneCh 的空闲时间可用于执行其他就绪节点，减少 worker 空转。

#### 2.3 execTask 结构

worker pool 的 `readyCh` 从 `chan *Node` 改为 `chan *execTask`，支持 per-DAG `doneCh`：

```go
type execTask struct {
    node   *Node
    doneCh chan<- *NodeResult
    exec   func(*Node) *NodeResult
}

type workerPool struct {
    readyCh chan *execTask
    // ...其余字段不变
}

func (wp *workerPool) worker() {
    defer wp.wg.Done()
    for t := range wp.readyCh {
        t.doneCh <- t.exec(t.node)
    }
}
```

`launchReady` 改为投递 `execTask`：

```go
launchReady := func() {
    for scheduler.Len() > 0 {
        select {
        case wp.readyCh <- &execTask{
            node:   scheduler.Peek(),
            doneCh: doneCh,
            exec:   func(node *Node) *NodeResult {
                return e.executeNode(dagCtx, node, dctx, d, wp, depth, logger)
            },
        }:
            node := scheduler.Dequeue()
            started[node.Name] = true
        default:
            return
        }
    }
}
```

work-stealing 分支消费的是同一个 `wp.readyCh`，结构一致：

```go
case task := <-wp.readyCh:
    task.doneCh <- task.exec(task.node)
```

### 3. Node 类型扩展（node.go）

```go
// SubflowFunc 动态生成子 DAG。
// ctx 透传超时/取消信号；dctx 与父 DAG 共享。
// 返回 nil DAG 表示跳过子图（等同空子图，节点标记 Success）。
type SubflowFunc func(ctx context.Context, dctx *DAGContext) (*DAG, error)

type Node struct {
    // ...现有字段...
    SubflowFn SubflowFunc // 非 nil 时为 subflow 节点，Fn 被忽略
}

func NodeWithSubflow(fn SubflowFunc) NodeOption {
    return func(n *Node) { n.SubflowFn = fn }
}
```

### 4. AddNode 放宽（dag.go）

当前 `AddNode` 在 options 应用前检查 `fn == nil`，需改为在 options 应用后检查 `Fn` 和 `SubflowFn` 都为 nil：

```go
func (d *DAG) AddNode(name string, fn NodeFunc, opts ...NodeOption) error {
    node := &Node{Name: name, Critical: true}
    for _, opt := range opts {
        opt(node)
    }
    if fn != nil {
        node.Fn = fn
    }
    if node.Fn == nil && node.SubflowFn == nil {
        return fmt.Errorf("%w: %s", ErrNodeFuncNil, name)
    }
    // ...后续注册逻辑不变...
}
```

### 5. NodeResult 扩展（result.go）

```go
type NodeResult struct {
    // ...现有字段...
    SubflowResult *DagResult // subflow 节点的子 DAG 执行结果，普通节点为 nil
}
```

`Reset()` 递归释放所有嵌套层级：

```go
func (r *NodeResult) Reset() {
    // ...现有逻辑...
    if r.SubflowResult != nil {
        releaseDagResultRecursive(r.SubflowResult)
        r.SubflowResult = nil
    }
}

// releaseDagResultRecursive 递归释放 DagResult 及其所有嵌套 SubflowResult。
func releaseDagResultRecursive(r *DagResult) {
    for k, nr := range r.Results {
        if nr.SubflowResult != nil {
            releaseDagResultRecursive(nr.SubflowResult)
        }
        releaseNodeResult(nr)
        delete(r.Results, k)
    }
}
```

`DagResult.Reset()` 调用 `releaseDagResultRecursive` 确保多层嵌套（A→B→C）不残留指针。

### 6. executeNode 改造（engine.go）

新增 `wp` 和 `depth` 参数，在 condition gate 之后插入 subflow 分支：

```go
func (e *Engine) executeNode(
    ctx context.Context,
    n *Node,
    dctx *DAGContext,
    d *DAG,
    wp *workerPool,      // 新增：共享 worker pool
    depth int,           // 新增：递归深度
    logger Logger,
) (nr *NodeResult) {
    // ...panic recovery + BeforeNode + condition gate...

    // Subflow 分支
    if n.SubflowFn != nil {
        subDAG, err := n.SubflowFn(ctx, dctx)
        if err != nil {
            nr.Status = StatusFailed
            nr.Error = fmt.Errorf("subflow %q construction failed: %w", n.Name, err)
            return nr
        }
        if subDAG == nil {
            nr.Status = StatusSuccess // 空子图，视为成功
            return nr
        }
        if err := subDAG.Validate(); err != nil {
            nr.Status = StatusFailed
            nr.Error = fmt.Errorf("subflow %q validation failed: %w", n.Name, err)
            return nr
        }
        if depth >= e.maxSubflowDepth {
            nr.Status = StatusFailed
            nr.Error = fmt.Errorf("subflow %q exceeds max depth %d", n.Name, e.maxSubflowDepth)
            return nr
        }

        // 递归执行子 DAG，共享 wp 和 dctx
        // executeDAG 的 event loop 通过 work-stealing 避免死锁
        subResult := e.executeDAG(ctx, subDAG, dctx, wp, d.hooks, depth+1, logger)
        nr.SubflowResult = subResult

        if subResult.Status == StatusFailed {
            nr.Status = StatusFailed
            nr.Error = subResult.Error
        } else {
            nr.Status = StatusSuccess
        }
        return nr
    }

    // 普通节点：executeWithRetries（现有逻辑不变）
    // ...
}
```

**panic 安全**：`SubflowFn` 的调用在 `executeNode` 的 `defer recover()` 覆盖范围内。若 `SubflowFn` 内部 panic，会被 recover 捕获，NodeResult 标记为 `StatusPanicked`，worker 正常释放。不存在 panic 击穿 recover 的问题。

### 7. Hooks 继承（engine.go）

`executeDAG` 接收 `parentHooks *HookChain`，合并到子 DAG 的 hooks：

```go
func (e *Engine) executeDAG(..., parentHooks *HookChain, ...) *DagResult {
    if parentHooks != nil {
        for _, h := range parentHooks.hooks {
            d.hooks.Add(h)
        }
    }
    // ...后续执行逻辑...
}
```

继承链为线性增长（非指数）：

- Level 0: hooks = [A, B]
- Level 1: hooks = [A, B, C]（子追加 C）
- Level 2: hooks = [A, B, C, D]（孙追加 D）

每层 `Add` 拷贝的是 Hook 接口指针（8 bytes），不是 Hook 对象。5 hooks × 3 层 = 最深链 15 entry，内存和执行开销可忽略。

### 8. 深度限制（options.go + engine.go）

```go
type Engine struct {
    dctxShards      int
    maxSubflowDepth int
    logger          Logger
}

func EngineWithMaxSubflowDepth(n int) EngineOption {
    return func(e *Engine) {
        if n >= 1 {
            e.maxSubflowDepth = n
        }
    }
}
```

`NewEngine` 默认值：

```go
e := &Engine{logger: noopLogger{}, maxSubflowDepth: 10}
```

### 9. 子图超时（executeDAG 内部）

`context.WithTimeout(parentCtx, subTimeout)` 自动取 min(父超时, 子超时)。父 3s + 子 1s → 子在 1s 时超时退出，父继续执行其他节点。这是 Go context 的标准行为，无需额外处理：

```go
dagCtx, dagCancel := context.WithCancel(ctx)  // ctx 来自父
if d.timeout > 0 {
    dagCtx, dagCancel = context.WithTimeout(ctx, d.timeout)  // 叠加子自身超时
}
```

---

## 风险矩阵

| 级别 | 风险 | 评估 | 应对 |
|------|------|------|------|
| **P0** | 递归阻塞死锁 | 成立 | event loop work-stealing：等待 doneCh 时消费 readyCh，等待者主动参与执行 |
| **P0** | 无全局并发上限 | 成立 | worker pool 共享 per-Run，总并发度始终 ≤ maxConcurrency |
| ~~P0~~ | 循环 Subflow 栈溢出 | 高估 | maxSubflowDepth 默认 10，Go 栈可增长到 1GB，远不达溢出 |
| ~~P0~~ | 共享 DAGContext map 写崩溃 | 事实错误 | 分片 RWMutex 已保证线程安全 |
| **P2** | 嵌套 DagResult 内存泄漏 | 成立 | releaseDagResultRecursive 递归释放所有层级 |
| ~~P1~~ | SubflowFn panic 无捕获 | 事实错误 | executeNode defer recover 覆盖整个函数体 |
| **P2** | SubflowFn 阻塞 worker | 同普通 NodeFunc | work-stealing 缓解；用户代码阻塞非框架缺陷 |
| **P2** | 无 timeout 时永久 Hang | 非 subflow 特有 | ctx 透传保证有 timeout 时正确传播；建议默认 timeout |
| ~~P1~~ | 子图超时不生效 | 事实错误 | context.WithTimeout 自动取 min(父, 子) |
| **P2** | 子图运行时 Validate 开销 | 成立但影响小 | O(V+E) 微秒级，动态子图必须验证 |
| **P2** | Hooks 重复注册 | 理论成立，实际影响极小 | 线性增长非指数，5 hooks × 3 层 = 15 entry |
| **P2** | 观测日志/可视化层级割裂 | 成立 | 后续迭代，depth 前缀日志 |
| **P2** | dctx key 命名冲突 | 成立 | 文档约定 + 后续可加命名空间前缀 |
| **P2** | 子图失败粒度单一 | 成立 | 后续迭代，扩展 ErrorType 枚举 |
| **P2** | API 语义割裂 | 成立 | 后续迭代，AddSubflow 专用 API |
| **P2** | 无 Detach 异步模式 | 成立 | 后续迭代 |

### 死锁场景详解

以 `maxConcurrency=2` 为例，两个 subflow 节点并发执行：

```
初始: worker1 空闲, worker2 空闲
launchReady → wp.readyCh <- subA_task, wp.readyCh <- subB_task

worker1 取到 subA_task → executeNode(subA) → SubflowFn 生成 subDAG_A
  → executeDAG(subDAG_A, depth=1)
    → launchReady → wp.readyCh <- subA_child1_task
    → event loop: select { doneCh | wp.readyCh | dagCtx.Done() }

worker2 取到 subB_task → executeNode(subB) → SubflowFn 生成 subDAG_B
  → executeDAG(subDAG_B, depth=1)
    → launchReady → wp.readyCh <- subB_child1_task
    → event loop: select { doneCh | wp.readyCh | dagCtx.Done() }

此时 worker1、worker2 都在各自 event loop 的 select 中等待。

无 work-stealing: subA_child1_task 和 subB_child1_task 在 wp.readyCh 中
  → 没有 worker 消费 readyCh（都在 event loop 等 doneCh）
  → doneCh 永远没结果 → 死锁

有 work-stealing: worker1 的 event loop select 命中 case task := <-wp.readyCh
  → 执行 subB_child1_task → 结果送 subB 的 doneCh
  → worker2 的 event loop 命中 case nr := <-doneCh → 推进 subB 调度
  → worker2 的 event loop select 命中 case task := <-wp.readyCh
  → 执行 subA_child1_task → 结果送 subA 的 doneCh
  → 两个子 DAG 交替推进 → 全部完成 → event loop 退出 → worker 释放
```

---

## 改动文件清单

| 文件 | 改动 | 说明 |
|------|------|------|
| `engine.go` | 提取 `executeDAG`，event loop 加 work-stealing，`executeNode` 加 subflow 分支和 depth/wp 参数 | 核心改动 |
| `node.go` | 新增 `SubflowFunc`、`Node.SubflowFn`、`NodeWithSubflow` | 类型扩展 |
| `result.go` | `NodeResult.SubflowResult` 字段，`releaseDagResultRecursive` 递归释放 | 结果嵌套 |
| `dag.go` | `AddNode` 放宽 nil 检查，允许 subflow 节点 fn 为 nil | 入口适配 |
| `options.go` | `EngineWithMaxSubflowDepth` | 配置项 |
| `workerpool.go` | `readyCh` 类型从 `chan *Node` 改为 `chan *execTask`，支持 per-DAG doneCh | 共享 pool 适配 |

---

## 用户 API 示例

```go
d := NewDAG("parent")
d.AddNode("fetch", fetchData)

d.AddNode("pipeline", nil,
    NodeWithSubflow(func(ctx context.Context, dctx *DAGContext) (*DAG, error) {
        // 运行时根据数据决定子图结构
        count, _ := GetTyped[int](dctx, "partition_count")

        sub := NewDAG("partitions", WithMaxConcurrency(4))
        sub.AddNode("collect", collectFn)
        for i := 0; i < count; i++ {
            name := fmt.Sprintf("partition_%d", i)
            sub.AddNode(name, processPartition,
                NodeWithDependsOn("collect"),
                NodeWithPriority(count-i),
            )
        }
        sub.AddNode("merge", mergeFn,
            NodeWithDependsOn(/* all partition nodes */...),
        )
        return sub, nil
    }),
    NodeWithDependsOn("fetch"),
)

d.AddNode("output", outputFn, NodeWithDependsOn("pipeline"))

eng := NewEngine(EngineWithMaxSubflowDepth(5))
result := eng.Run(ctx, d)

// 访问子图结果
subResult := result.NodeResult("pipeline").SubflowResult
for name, nr := range subResult.Results {
    fmt.Printf("  %s: %s\n", name, nr.Status)
}
```

---

## 实现顺序

1. **workerpool.go** — `*Node` → `*execTask`，支持 per-DAG doneCh
2. **node.go** — `SubflowFunc`、`SubflowFn`、`NodeWithSubflow`
3. **dag.go** — `AddNode` 放宽 nil 检查
4. **result.go** — `SubflowResult` 字段 + `releaseDagResultRecursive` 递归释放
5. **options.go** — `EngineWithMaxSubflowDepth`
6. **engine.go** — 提取 `executeDAG`，event loop 加 work-stealing，`executeNode` 加 subflow 分支
7. **测试** — subflow 基本、嵌套、深度限制、panic 恢复、并发度验证、死锁验证（maxConcurrency=2 + 双 subflow）
8. **文档更新** — README、doc.go、design-prompt.md、issuesAnStrategy.md
