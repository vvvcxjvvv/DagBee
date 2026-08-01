# Subflow 支持改造计划

> 原生 SubflowNode 节点类型，支持运行时动态生成子 DAG 并递归执行。

---

> **架构更新 (2026-08)**：Subflow 调度已从同步阻塞模型重构为异步 Join 模型。
> Worker 不再阻塞等待子图完成，改为后台 goroutine 异步执行 + doneCh 回填结果。
> stealCh work-stealing 机制已删除。详见下方 [异步调度重构](#异步调度重构) 小节
> 及 `docs/issuesAndStrategy.md` #4。

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

##### 问题本质：共享无缓冲 channel 上的循环等待

Subflow 节点同步阻塞在 `executeDAG` 的 event loop 中。当所有 pool worker 都在等 subflow 完成时，子 DAG 的子节点无人执行，形成死锁。

关键在于 worker pool 的 `readyCh` 是**无缓冲 channel**：

- **生产端**：subflow 的 event loop 通过 `select { case readySendCh <- nextTask: ... }` 向 `readyCh` 投递子节点任务。无缓冲意味着 send 成功**当且仅当**有 goroutine 正在 `receive`
- **消费端**：pool worker 通过 `for t := range wp.readyCh` 接收任务。但所有 worker 都阻塞在各自的 subflow event loop 中，没有人在 `range wp.readyCh` 上等待

结果：send 端和 receive 端都被占用的 event loop 阻塞，channel 两端无人匹配，`doneCh` 永远没有结果，**循环等待 → 死锁**。

##### 死锁场景图解

以 `maxConcurrency=2` 为例，父 DAG 有两个并发的 subflow 节点：

```
┌─────────────────────────────────────────────────────────────────┐
│                      Worker Pool (2 workers)                     │
│                                                                  │
│  ┌──────────────────────┐    ┌──────────────────────┐           │
│  │     Worker 0         │    │     Worker 1         │           │
│  │  (阻塞在 subA 的     │    │  (阻塞在 subB 的     │           │
│  │   event loop 中)     │    │   event loop 中)     │           │
│  │                      │    │                      │           │
│  │  select {            │    │  select {            │           │
│  │   case doneCh_A <-   │    │   case doneCh_B <-   │           │
│  │   case readyCh <- t  │    │   case readyCh <- t  │  ← send   │
│  │   case dagCtx.Done   │    │   case dagCtx.Done   │    端     │
│  │  }                   │    │  }                   │           │
│  └──────────────────────┘    └──────────────────────┘           │
│           │                           │                          │
│           │  subA 想投递子节点任务     │  subB 想投递子节点任务   │
│           │  case readyCh <- task     │  case readyCh <- task   │
│           ▼                           ▼                          │
│  ┌──────────────────────────────────────────────────┐           │
│  │              wp.readyCh (无缓冲)                   │           │
│  │  send 端：subA/subB 的 event loop                 │           │
│  │  receive 端：??? 无人接收（worker 都在 event loop）│  ← 死锁   │
│  └──────────────────────────────────────────────────┘           │
│                                                                  │
│  正常的 worker: for t := range wp.readyCh { ... }               │
│  ↑ 这两个 worker 不在这里，它们在各自的 event loop 中            │
└─────────────────────────────────────────────────────────────────┘
```

##### 修复方案：event loop 的 steal case

在 event loop 的 `select` 中增加一个 receive 分支，让等待 `doneCh` 的 event loop 同时能从 `readyCh` 接收任务：

```go
var stealCh chan *execTask
if depth > 0 {
    stealCh = wp.readyCh  // subflow 层启用 work-stealing
}
// depth = 0 时 stealCh = nil，select 中此 case 永不触发

for completed < total {
    select {
    case readySendCh <- nextTask:
        // 投递自己的子节点任务给空闲 worker（send 端）
    case nr := <-doneCh:
        // 接收子节点完成结果
    case task := <-stealCh:
        // 窃取并执行任意层的就绪任务（receive 端）
        task.doneCh <- task.exec(task.node)
    case <-dagCtx.Done():
        // 超时/取消
    }
}
```

同一个 `wp.readyCh`，event loop 既是 **send 端**（投递自己的任务）又是 **receive 端**（帮别人执行任务）。两个 subflow 的 event loop 互为生产者和消费者，匹配成功。

##### 修复后图解

```
┌─────────────────────────────────────────────────────────────────┐
│                      Worker Pool (2 workers)                     │
│                                                                  │
│  ┌──────────────────────┐    ┌──────────────────────┐           │
│  │     Worker 0         │    │     Worker 1         │           │
│  │  (subA event loop)   │    │  (subB event loop)   │           │
│  │                      │    │                      │           │
│  │  select {            │    │  select {            │           │
│  │   case readyCh <- t  │───►│   case readyCh <- t  │───┐      │
│  │   case doneCh_A <-   │    │   case doneCh_B <-   │   │      │
│  │   case stealCh <- t  │◄───│   case stealCh <- t  │◄──┘      │
│  │  }                   │    │  }                   │           │
│  └──────────────────────┘    └──────────────────────┘           │
│                                                                  │
│  stealCh = wp.readyCh (depth > 0 时)                            │
│                                                                  │
│  subA send 子节点 → readyCh → subB 的 stealCh receive 并执行     │
│  subB send 子节点 → readyCh → subA 的 stealCh receive 并执行     │
│                                                                  │
│  两个 event loop 互为生产者和消费者，交替推进                     │
└─────────────────────────────────────────────────────────────────┘
```

##### 分步推演

`maxConcurrency=2`，subA 有子节点 A1、A2，subB 有子节点 B1、B2：

```
Step 1: 初始状态
  worker0 → executeDAG(subA) → 子节点 A1 就绪 → dispatchReady 生成 taskA1
  worker1 → executeDAG(subB) → 子节点 B1 就绪 → dispatchReady 生成 taskB1

  worker0 的 select: 尝试 send taskA1 到 readyCh
  worker1 的 select: 尝试 send taskB1 到 readyCh

  ┌─────────────────────────────────────────────────────┐
  │  readyCh (无缓冲)                                   │
  │  worker0: case readyCh <- taskA1  (send, 等接收者)  │
  │  worker1: case readyCh <- taskB1  (send, 等接收者)  │
  │  receive 端: worker0/1 的 stealCh = readyCh         │
  └─────────────────────────────────────────────────────┘

Step 2: 匹配成功
  Go select 随机选择一个就绪 case。
  假设 worker0 的 send 和 worker1 的 stealCh 匹配：

  worker1: case task := <-stealCh 命中 → 接收到 taskA1
           → taskA1.exec(nodeA1) 同步执行 A1
           → taskA1.doneCh <- resultA1  结果发回 subA 的 doneCh

  worker0: case readySendCh <- taskA1 命中 → send 成功
           → commitDispatch() A1 标记 started
           → dispatchReady 生成 taskA2

Step 3: 结果回流
  subA 的 doneCh 收到 resultA1
  worker0 的 select: case nr := <-doneCh 命中
    → completed++
    → 传播依赖（如果 A2 依赖 A1，A2 的 pending 归零入队）
    → dispatchReady 准备投递 taskA2

Step 4: 交替推进
  worker0 尝试 send taskA2
  worker1 执行完 A1 后回到 select，stealCh 可接收
  → worker1 再次 steal，执行 A2

  同时 worker1 也在尝试 send taskB1
  worker0 的 stealCh 可接收
  → worker0 steal，执行 B1

  两个 subflow 的子节点被交替执行：
    A1 (by worker1) → B1 (by worker0) → A2 (by worker1) → B2 (by worker0)
    或者其他交错顺序，取决于 Go select 的随机调度

Step 5: 全部完成
  subA: A1 ✓, A2 ✓ → doneCh 全部处理 → completed == total → 循环结束
  subB: B1 ✓, B2 ✓ → doneCh 全部处理 → completed == total → 循环结束
  两个 event loop 退出 → worker0、worker1 释放回 pool
```

##### 为什么只在 depth > 0 启用

depth = 0 时 event loop 跑在 `Run` 的 goroutine 上，不是 pool worker。如果它 steal 并执行任务，总并发 = `maxConcurrency`（pool workers）+ 1（Run goroutine），超出限制。

depth > 0 时 event loop 跑在某个 pool worker 上。这个 worker 当前处于"等待"状态（阻塞在 select），没有执行节点。它通过 steal case 执行一个任务，只是从"等待"切换到"执行"，占用自己的 worker slot。总并发不变。

```go
var stealCh chan *execTask
if depth > 0 {
    stealCh = wp.readyCh  // subflow: 启用
}
// depth = 0: stealCh = nil → select 中此 case 永不触发（nil channel 永久阻塞）
```

##### 额外收益

work-stealing 不仅防死锁，还提升资源利用率。即使没有 subflow 死锁场景，event loop 等待 `doneCh` 时的空闲时间也可用于执行其他就绪节点，减少 worker 空转。

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

详细图解和分步推演见上方 [2.2 Work-stealing 解决死锁](#22-work-stealing-解决死锁) 小节。

核心结论：

1. **死锁根因**：无缓冲 `readyCh` 需要 send 端和 receive 端同时就绪。所有 worker 阻塞在 subflow event loop 中时，没有 goroutine 在 `range readyCh` 上等待，send 永远失败
2. **修复原理**：event loop 的 `stealCh = readyCh`（depth > 0 时），让等待 `doneCh` 的 event loop 同时充当 `readyCh` 的消费者，两个 subflow 互为生产者和消费者
3. **并发安全**：steal 的 goroutine 是 pool worker，当前处于"等待"状态，执行任务只是切换到"执行"状态，总并发不超限
4. **验证测试**：`TestSubflow_DeadlockAvoidance` 用 `maxConcurrency=2` + 双 subflow 验证不死锁，30 次连续运行零失败

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


---

## 异步调度重构

### 改造前：同步阻塞 + stealCh

Worker 拿到 subflow 节点后同步调用 `executeDAG`，整个子图跑完才释放。通过
`stealCh = wp.readyCh`（depth > 0 时）被动窃取任务防死锁。

```
Worker ──► executeNode(subflow)
         └─► SubflowFn() 构图
            └─► executeDAG(childDAG) [阻塞，worker 不释放]
                 ├─ event loop: select {
                 │    case readyCh <- task   // 投递子节点
                 │    case doneCh <- nr      // 接收子节点完成
                 │    case stealCh <- task   // 窃取其他层任务执行
                 │    case ctx.Done()
                 │  }
                 └─ 子图全部完成 → 返回 subResult
         └─► 回填 SubflowResult → 返回 NodeResult → worker 释放
```

**问题**：
1. Worker 整个子图执行期间被占用，高并发多层 subflow 时可用 worker 急剧减少
2. stealCh 是补丁不根治：顶层无窃取、窃取长任务拖慢自身、无任务可偷时卡死
3. 递归 executeDAG 调用，栈深度随嵌套层级增长

### 改造后：异步 Join 模型

Worker 仅同步执行 SubflowFn 构图，然后启动后台 goroutine 执行子 DAG，worker
立即释放。子图在 goroutine 中调度（不占 worker slot），完成后通过 doneCh
通知父 DAG。

```
Worker ──► executeNode(subflow)
         ├─► SubflowFn() 构图 [同步, 带 panic recovery]
         ├─► go func() {                    [异步, worker 立即释放]
         │      executeDAG(childDAG)
         │      → 子节点通过 wp.readyCh 由空闲 worker 执行
         │      → 子图全部完成 → doneCh <- NodeResult
         │  }()
         └─► return nil  [worker 跳过 doneCh 发送, 回到 readyCh]
```

**Event loop 简化**（3 路 select，删除 stealCh）：

```go
for completed < total {
    select {
    case readySendCh <- nextTask:  // dispatch ready node to worker
    case nr := <-doneCh:           // process completion (incl. async subflow)
    case <-ctxDone:                // timeout/cancellation
    }
}
```

### 关键设计决策

**1. executeNode 返回 nil 表示异步**

```go
func (e *Engine) executeNode(..., doneCh chan<- *NodeResult) *NodeResult
```

- 普通节点：返回完整 NodeResult，worker 发送到 doneCh
- Subflow 节点：返回 nil，worker 跳过发送（goroutine 稍后发送）

Worker 适配：

```go
for t := range wp.readyCh {
    if nr := t.exec(t.node); nr != nil {
        t.doneCh <- nr
    }
}
```

**2. 命名返回值陷阱**

原始实现使用命名返回值 `(nr *NodeResult)`，subflow 分支 `return nil` 会将 `nr`
设为 nil。Goroutine 闭包捕获 `nr` 变量，此时为 nil 指针 → 运行时 panic。

修复：改用 `*NodeResult` 返回类型（非命名），subflow 分支用 `result := nr`
局部拷贝传给 goroutine 闭包。

**3. dagCtx.Done() 忙等待修复**

原实现在 `case <-dagCtx.Done()` 触发后不 nil 化 channel 变量，后续循环中
`Done()` 始终就绪导致 select 忙等待。改为首次触发后 `ctxDone = nil`，利用
nil-channel 永久阻塞特性禁用该 case。

### 死锁分析：为什么异步模型不需要 stealCh

**同步模型死锁根因**：无缓冲 `readyCh` 需要 send 端和 receive 端同时就绪。
所有 worker 阻塞在 subflow event loop 中时，没有 goroutine 在 `range readyCh`
上等待，send 永远失败 → 循环等待 → 死锁。

**异步模型为什么不会死锁**：

1. Worker 执行完 SubflowFn 后立即返回 nil，回到 `range readyCh`
2. 子图 goroutine 通过 `readySendCh <- nextTask` 投递子节点
3. 空闲 worker 从 `range readyCh` 接收并执行子节点
4. 子节点完成发往子图的 `doneCh`，子图 goroutine 处理结果

```
maxConcurrency=1 场景：
  Worker 执行 SubflowFn → return nil → 回到 range readyCh
  子图 goroutine → readySendCh <- childTask → Worker 接收执行
  Worker 执行完 childTask → doneCh <- result → 回到 range readyCh
  子图 goroutine ← doneCh ← 处理 result → 投递下一个 childTask
  ...
  子图全部完成 → doneCh <- parentResult → 父 DAG 继续
```

Worker 在 SubflowFn 执行和子节点执行之间自由切换，不会被任何单个 DAG 层阻塞。

### 性能对比

| 维度 | 同步 + stealCh | 异步 Join |
|------|---------------|-----------|
| worker 占用 | 整个子图期间 | 仅 SubflowFn |
| select 分支 | 4 路 | 3 路 |
| 递归栈深度 | O(depth) | O(1)（goroutine 独立栈） |
| goroutine 数 | N workers | N workers + M active subflows |
| maxConcurrency=1 可用性 | 会死锁（1 worker 被 subflow 阻塞，无 worker 执行子节点，stealCh 也不能用因为 depth=0） | 正常工作（worker 释放后执行子节点） |

### 测试验证

| 测试 | 场景 |
|------|------|
| `TestSubflow_AsyncMaxConcurrency1` | maxConcurrency=1，subflow + sibling 并发 |
| `TestSubflow_DeepNesting` | 5 层嵌套不栈溢出 |
| `TestSubflow_DeadlockAvoidance` | maxConcurrency=2，双 subflow 并发 |
| `-race -count=5` | 56 测试 × 5 轮零竞争 |
