# Issues & Strategy

> 本文档记录 DagBee 开发过程中遇到的关键问题、策略抉择与落地实现。

---

## #1 DAGContext 全局锁拆分

### 当前问题

`DAGContext` 使用单一 `sync.RWMutex` 保护整个 `map[string]interface{}`。在 fan-out 场景下（如推荐引擎 128 路召回并行写入各自结果），大量节点并发 `Set` **不同的 key**，但竞争的是同一把写锁——所有写操作串行化，延迟随并发度线性增长。

这是热点锁问题，而非热点 key 问题：DAG 的典型写入模式是每个节点写自己的 key 一次，key 之间天然不冲突，冲突来源是锁的粒度。

### 策略抉择

#### 方案 A：哈希分桶分区锁（Sharded Map）

将 map 拆成 N 个 shard，每个 shard 拥有独立的 `sync.RWMutex`。key 通过 FNV-1a hash 分配到 shard。

| 优点 | 缺点 |
|------|------|
| 不同 key 的写操作完全并行，直接解决 fan-out 写锁串行 | hash 碰撞的 key 仍同桶串行（DAG 场景几乎不发生） |
| API 不变，调用方零感知 | `Keys()`/`Len()`/`Reset()` 需遍历所有 shard，开销放大 |
| 改动集中在一个文件，不涉及 engine 层 | shard 数为静态选择，极端核心数下可能偏优或偏劣 |
| 实现成熟、性能可预测 | N 个独立 map 对象带来轻微 GC 和内存开销 |

#### 方案 B：`sync.Map`

用标准库 `sync.Map` 替换 `map` + `RWMutex`。

| 优点 | 缺点 |
|------|------|
| 零自定义代码 | 针对"写一次、读多次"优化，DAG 模式是"写一次、读一两次"，收益有限 |
| 读路径在 key 稳定后无锁 | 写密集场景经常比 `RWMutex` 更慢 |
| | `Len()` 在 Go 1.24 前是 O(n) 遍历 |
| | `Range()` 拿 `Keys()` 效率低 |

#### 方案 C：节点私有命名空间 + 引擎合并

每个节点执行时写入本地 map，节点完成后由 engine 合并进全局 context。下游节点启动前其依赖的输出已合并完成。

| 优点 | 缺点 |
|------|------|
| 节点执行期间零锁竞争，比分片锁更进一步 | engine 层耦合，需在 `executeNode` 中注入私有域并做 merge |
| 天然符合 DAG 的数据流语义 | `Get` 需查两层（私有域 + 全局），语义复杂化 |
| | `Keys()`/`Len()` 语义需重新定义 |
| | 内存开销随并发节点数增长 |

#### 方案 D：per-key `atomic.Pointer`

每个 key 的 value 用 `atomic.Pointer` 存储，CAS 替换。

| 优点 | 缺点 |
|------|------|
| 同 key 读写无锁 | key 到 `atomic.Pointer` 的映射本身仍需锁保护，问题转移 |
| | 需要预分配固定 key 集合，不现实 |

#### 抉择

选择 **方案 A：哈希分桶分区锁**。

理由：DAG fan-out 中不同节点写不同 key，分片后各 shard 独立无竞争，直接消除瓶颈。API 零变更，改动集中可控。方案 B 不匹配 DAG 写入模式；方案 C 架构复杂度过高，收益边际递减；方案 D 不具可行性。

### 落地实现

**文件**: `dagcontext.go`

核心设计：

```go
type dctxShard struct {
    mu   sync.RWMutex
    data map[string]interface{}
}

type DAGContext struct {
    shards []*dctxShard
    mask   uint64 // len(shards) - 1
}
```

- **shard 数**: `roundupPow2(runtime.NumCPU() * 4)`，2 的幂使取模可用位运算 `hash & mask`。
- **hash 函数**: FNV-1a 64-bit，分布均匀、计算快、无外部依赖。
- **`Set`/`Get`**: 定位 shard → 加对应锁 → 操作 map。不同 key 落不同 shard 时完全并行。
- **`Keys`/`Len`/`Reset`**: 遍历所有 shard 各加锁。这些方法不在热路径上（执行结束后或跨 Run 复用时调用），开销可忽略。
- **`GetTyped`/`MustGet`**: 基于 `Get`，无需额外适配。

新增 benchmark `BenchmarkDAGContext_DistinctKeyContention` 验证 fan-out 写场景性能。

**基准测试结果**（Apple M1 Pro, 10 core）:

```
BenchmarkDAGContext_DistinctKeyContention/writers=8-10     9500592    125.3 ns/op    16 B/op    1 allocs/op
BenchmarkDAGContext_DistinctKeyContention/writers=32-10   14716898     80.71 ns/op   16 B/op    1 allocs/op
BenchmarkDAGContext_DistinctKeyContention/writers=128-10  16432802     73.70 ns/op   16 B/op    1 allocs/op
BenchmarkDAGContext_HotKeyContention/readersPerWrite=1-10  4483478    276.8 ns/op     4 B/op    0 allocs/op
BenchmarkDAGContext_HotKeyContention/readersPerWrite=4-10  9052657    152.5 ns/op     1 B/op    0 allocs/op
BenchmarkDAGContext_HotKeyContention/readersPerWrite=16-10 12658460    82.12 ns/op    0 B/op    0 allocs/op
```

Distinct-key 写入吞吐随并发度提升而提升（更多 writer → 更高并行度），证实分片锁有效消除了 fan-out 写锁瓶颈。Hot-key 竞争场景性能保持稳定，未因分片引入退化。

### Q&A

**Q1: shard 数为什么是 `roundupPow2(runtime.NumCPU() * 4)`？**

三个因素叠加：

1. **为什么是 2 的幂** — shard 定位用 `hash & mask` 替代 `hash % n`，位运算比取模快 3-5 倍。`Set`/`Get` 是每次节点执行的热路径，省下的周期会累积。`roundupPow2` 把任意值向上取到最近的 2 的幂。

2. **为什么基数是 `NumCPU`** — shard 数至少要 >= 并发写线程数，否则多个线程必然挤在同一 shard 排队，分片失去意义。DAG 引擎并发度受 `maxConcurrency` 约束，通常不超过 `NumCPU`，所以以 `NumCPU` 为下限。

3. **为什么乘 4** — 只用 `NumCPU` 个 shard 在数学上不够。8 核、8 shard、8 个并发写不同 key，生日碰撞概率约 40%。乘 4 后变 32 shard，同样 8 个并发写碰撞概率降到约 6%。更大的乘数（8、16）收益递减且带来内存浪费和 cache 局部性下降，`*4` 是实践中广泛使用的经验值。

**Q2: 哈希分桶分区锁有什么缺点？**

1. **Hash 碰撞导致同桶串行** — 两个 key 碰巧落入同一 shard 时写操作仍串行。实际影响可忽略：DAG 场景每个节点只写自己的 key 一次，碰撞窗口是毫秒级写操作。但若用户写大量同前缀 key 且 hash 分布不均，可能退化。

2. **`Keys()`/`Len()`/`Reset()` 开销放大** — 原来遍历一个 map，现在要遍历 N 个 shard 各加锁。这些方法不在热路径上（执行结束后或跨 Run 复用时调用），shard 数为几十时开销可忽略。

3. **内存开销** — 每个 shard 一个 `sync.RWMutex`（8 bytes）+ 一个 map header（约 48 bytes）。N=64 时额外约几 KB，可忽略。

4. **shard 数为静态选择** — 没有运行时调参机制。选 16 在 256 核机器上可能不够，选 256 在 4 核机器上浪费内存且 cache 局部性变差。`NumCPU * 4` 对 4-64 核覆盖合理，极端值有折损但不致劣化。

5. **GC 压力略增** — N 个独立 map 对象比单 map 多出 N-1 个 GC 可达对象。shard 数为几十时可忽略，数百以上会有可测量的 STW 影响。

**Q3: 高并发场景性能如何？**

分片锁性能取决于 **shard 数与并发度的比值**：

```
并发写线程数 W，shard 数 S，key 均匀分布

S >> W 时：几乎无竞争，接近无锁性能
S ≈ W 时：轻度竞争，偶尔串行
S << W 时：退化，接近全局锁
```

以 `S = NumCPU * 4` 为例：

| CPU | Shard | 16 并发写 | 64 并发写 |
|-----|-------|----------|----------|
| 4   | 16    | S≈W，轻微竞争 | S<W，中度竞争 |
| 8   | 32    | S>W，几乎无竞争 | S=W，轻微竞争 |
| 32  | 128   | 无竞争 | 无竞争 |

对比单锁方案：无论多少 CPU，16 并发写全部串行。分片锁最差情况下也只是退回到单锁水平，不会更差。

真实 fan-out 场景（如 128 路召回并行写）的提升是数量级的：128 个节点写 128 个不同 key，单锁排 128 次队，32-shard 方案每个 shard 平均 4 个 key，排队 4 次，理论吞吐提升约 32 倍。

**Q4: 是否需要让 shard 数可配置？**

`defaultShardCount()` 作为包级私有函数、被 `NewDAGContext()` 隐式调用，是合理的默认值。但以下场景需要可配置：

- **测试**：shard=1 可做确定性验证，排除 hash 分布干扰。
- **超大规模部署**：256+ 核机器上 `NumCPU*4` 可能不够。
- **内存敏感场景**：嵌入式或低配环境下减少 shard 数降低开销。

因此提供 `EngineWithDAGContextShards(n int)` 作为 `EngineOption`，用户可在创建 Engine 时指定 shard 数。`n=0`（默认）使用 `NumCPU*4`，`n<1` 被忽略，非 2 的幂值支持但用 modulo 替代 bitmask（略慢）。

```go
// 默认 shard 数
eng := NewEngine()

// 自定义 shard 数
eng := NewEngine(EngineWithDAGContextShards(1))   // 单 shard，测试用
eng := NewEngine(EngineWithDAGContextShards(256)) // 高并发场景
```

设计上不直接暴露 `NewDAGContextWithShards`，因为 `DAGContext` 由 Engine 内部创建，配置项通过 `EngineOption` 穿透更符合现有 API 模式。

---

## #2 单 goroutine 事件循环性能瓶颈

### 当前问题

`Engine.Run` 的所有调度逻辑——`doneCh` 接收、依赖计数递减、`scheduler.Enqueue`/`Dequeue`、`launchReady` 批量启动——都在单个主 goroutine 中串行执行。节点执行本身是并行的（每个节点一个 `go func()`），但调度决策是单线程的。

具体瓶颈点：

1. **`launchReady` 串行批量启动** — 1024 个节点同时就绪时，`launchReady` 在一个 `for` 循环里串行做 1024 次 `Dequeue` + `go func()`，期间主循环无法消费 `doneCh`。
2. **`scheduler` 冗余锁** — `priorityScheduler` 有 `sync.Mutex`，但 `Enqueue`/`Dequeue`/`Len` 只在主 goroutine 调用，无并发访问。1024 节点 = 4096 次无意义锁操作。
3. **per-node goroutine 创建开销** — 每个节点 `go func()` 创建一个新 goroutine，初始栈 2KB，1024 节点 = 2MB 栈分配 + GC 压力。benchmark 显示 1024 节点 3245 allocs。
4. **`doneCh` buffer = total 的内存浪费** — 1000 节点预分配 1000 容量的 channel buffer。

非问题项：`doneCh` buffer=total 保证写端不阻塞，不会反压；依赖传播 O(下游数) 通常很小；`started` map 仅主 goroutine 访问无竞争。

### 策略抉择

#### 方案 A：移除 scheduler 冗余锁

将 `priorityScheduler` 的 `sync.Mutex` 移除，所有方法变为非线程安全。

| 优点 | 缺点 |
|------|------|
| 零风险，纯收益 | 无 |
| 代码更诚实，消除误导性并发暗示 | |
| 消除 4096 次无意义锁操作 | |

#### 方案 B：worker pool 替代 per-node goroutine

预启动 N 个 worker goroutine（N = maxConcurrency），通过 `readyCh` 投递任务，worker 执行完通过 `doneCh` 回报结果。

| 优点 | 缺点 |
|------|------|
| goroutine 创建/销毁开销归零，栈复用 | worker 生命周期管理、stop 时机需谨慎处理 |
| allocs 大幅下降（1024 节点从 3245 降至 1216） | `readyCh` 需带 buffer（= worker 数），否则 worker 未就绪时 launchReady 误判为"无空闲 worker" |
| 并发度天然由 worker 数控制，无需额外 semaphore | |

#### 方案 C：批量 dequeue + 批量 go func

`launchReady` 一次性从 heap 取出所有就绪节点到 slice，然后批量 `go func()`。

| 优点 | 缺点 |
|------|------|
| 减少 `Dequeue` 的锁操作次数 | 和当前实现差别不大，瓶颈不在 Dequeue 而在 `go func()` 本身 |
| | 不消除 goroutine 创建开销 |

#### 方案 D：channel 驱动的独立 dispatcher goroutine

主循环只负责 `doneCh` → 依赖传播 → `readyCh <- node`，独立 dispatcher goroutine 消费 `readyCh` 并启动 worker。

| 优点 | 缺点 |
|------|------|
| 主循环不再阻塞在 `launchReady` | 引入额外 goroutine，`started` 等共享状态需要同步 |
| 调度延迟降低 | 架构复杂度增加 |

#### 抉择

选择 **方案 A + 方案 B**。

方案 A 零风险，立即收益。方案 B 是结构性优化，消除 per-node goroutine 创建开销。方案 C 和 D 是中间态，收益有限但增加复杂度。

关键实现难点：`readyCh` 必须带 buffer（容量 = worker 数），否则 `launchReady` 的非阻塞 `select` 在 worker 尚未被调度到 `range readyCh` 时误走 `default` 分支，导致节点不被分发、主循环死锁。

### 落地实现

**文件**: `scheduler.go`、`workerpool.go`（新增）、`engine.go`

**方案 A — 移除 scheduler 锁** (`scheduler.go`)：

```go
// 所有方法移除 sync.Mutex，变为非线程安全
type priorityScheduler struct {
    heap nodeHeap  // 无 mu 字段
}
```

**方案 B — worker pool** (`workerpool.go` 新增)：

```go
type workerPool struct {
    readyCh chan *Node        // buffer = workers
    doneCh  chan<- *NodeResult
    exec    func(*Node) *NodeResult
    wg      sync.WaitGroup
    workers int
    once    sync.Once
}
```

- `start()` 预启动 N 个 worker goroutine，每个 `for node := range wp.readyCh`
- `stop()` 关闭 `readyCh`，worker 完成当前节点后退出
- `wait()` 等待所有 worker 退出

**引擎调度改造** (`engine.go`)：

```go
// 替代 per-node go func()
wp := newWorkerPool(maxConc, execFn, doneCh)
wp.start()

launchReady := func() {
    for scheduler.Len() > 0 {
        select {
        case wp.readyCh <- scheduler.Peek():  // 非阻塞投递
            node := scheduler.Dequeue()
            started[node.Name] = true
        default:
            return  // 所有 worker 忙
        }
    }
}

// 主循环结束后
wp.stop()  // 关闭 readyCh
wp.wait()  // 等待 worker 退出
```

关键改动：
- 移除 `sem` channel，并发度由 worker 数天然控制
- 移除 `sync.WaitGroup`（原 `wg`），改用 `wp.wait()`
- `maxConc` clamp 到 `total` 避免空闲 worker

**基准测试对比**（Apple M1 Pro, 10 core, 3 次取中位数）:

| Benchmark | 优化前 ns/op | 优化后 ns/op | 提升 | 优化前 allocs | 优化后 allocs | allocs 降幅 |
|-----------|-------------|-------------|------|-------------|-------------|------------|
| WideDAG 1024 | 1,256,000 | 1,123,000 | 11% | 3,245 | 1,216 | 63% |
| FanOutFanIn 512 | 806,000 | 747,000 | 7% | 2,986 | 1,978 | 34% |
| ParallelReq p=4 | 57,375 | 54,300 | 5% | 413 | 354 | 14% |
| ParallelReq p=16 | 38,893 | 36,400 | 6% | 412 | 353 | 14% |

WideDAG 1024 节点的 allocs 降幅最大（63%），因为消除了 1024 次 goroutine 创建。延迟提升 5-11%，noop 节点场景下调度开销占主导，真实有工作量的节点场景下提升比例更高。


---

## #3 Subflow 嵌套子图支持

### 当前问题

DAG 是静态构建的：`AddNode` 注册所有节点和边，`Validate` 做环检测，`Engine.Run` 拿到完整 DAG 后一次性调度。运行期间无法向 scheduler 动态添加节点。

用户可在 NodeFunc 内手动 `NewEngine().Run(ctx, subDAG)` 嵌入子 DAG，但存在五个问题：

1. **DAGContext 不共享** — 子 DAG 创建自己的 DAGContext，父数据需手动传递。
2. **Worker pool 不共享** — 子 DAG 独立 worker pool，并发度不协调，可能 goroutine 爆炸。
3. **结果不可见** — 子 DAG 的 NodeResult 藏在父节点结果里，父 DagResult 无法看到子节点执行细节。
4. **Hooks 断裂** — 子 DAG 用自己的 HookChain，父 hooks 无法观测子节点生命周期。
5. **超时/取消可传递** — ctx 透传，唯一做对的部分。

### 策略抉择

#### 方案 A：手动嵌入（保持现状）

用户在 NodeFunc 内手动 `NewEngine().Run(ctx, subDAG)`。

| 优点 | 缺点 |
|------|------|
| 零框架改动 | DAGContext 不共享，手动传数据繁琐 |
| 覆盖简单封装场景 | Worker pool 独立，并发度不协调 |
| | 结果不可见，调试困难 |
| | Hooks 断裂 |

#### 方案 B：RunSubflow 辅助函数

封装手动步骤，共享 dctx、串联 hooks、封装数据传递。

| 优点 | 缺点 |
|------|------|
| 改动小，仅新增辅助函数 | Worker pool 仍独立 |
| DAGContext 共享 | 子结果仍不在父 DagResult 中 |
| Hooks 串联 | |

#### 方案 C：原生 SubflowNode + 共享 worker pool + work-stealing

新增 `SubflowFunc` 节点类型，递归调用 `executeDAG`，共享 dctx 和 worker pool。Event loop 使用 work-stealing 避免死锁。

| 优点 | 缺点 |
|------|------|
| 子结果完全可见，调试友好 | Engine 核心改动大 |
| 共享 worker pool 和 DAGContext | 嵌套深度控制需配置 |
| Hooks 天然继承 | |
| Work-stealing 避免死锁 | |

#### 方案 D：运行时动态节点注入

允许节点执行时向当前 DAG 注入新节点。

| 优点 | 缺点 |
|------|------|
| 最灵活，真正"运行时构图" | 破坏 DAG 静态性，环检测无法预验证 |
| 适合递归任务 | 调度器核心改动极大 |
| | 终止条件不确定，可能无限递归 |

#### 抉择

选择 **方案 C**。

方案 A 已能工作但体验差。方案 B 是方案 C 的子集，收益有限。方案 D 破坏 DAG 静态性，风险过高。方案 C 提供原生 subflow 支持，通过共享 worker pool + work-stealing 解决并发度和死锁问题。

### 落地实现

**文件**: `engine.go`、`node.go`、`result.go`、`dag.go`、`options.go`、`workerpool.go`

核心设计：

```go
// SubflowFunc 动态生成子 DAG
type SubflowFunc func(ctx context.Context, dctx *DAGContext) (*DAG, error)

// Node 新增 SubflowFn 字段
type Node struct {
    // ...现有字段...
    SubflowFn SubflowFunc
}

// NodeResult 新增 SubflowResult 字段
type NodeResult struct {
    // ...现有字段...
    SubflowResult *DagResult
}
```

**关键机制**：

1. **executeDAG 可重入** — 从 `Run` 中提取，接收共享的 `dctx`、`wp`、`parentHooks`、`depth` 参数。子 DAG 递归调用，共享同一 worker pool。

2. **Worker pool 共享 + unbuffered readyCh** — 整个执行树共享一个 worker pool。`readyCh` 为无缓冲 channel，event loop 通过 `select { case readyCh <- task: ... }` 阻塞式分派，确保任务不会被卡在 buffer 中。

3. **Work-stealing 避免死锁** — depth > 0 时，event loop 的 `select` 增加 `case task := <-stealCh`（`stealCh = wp.readyCh`）。当所有 worker 阻塞在 subflow event loop 中时，等待 `doneCh` 的 event loop 主动消费 `readyCh` 执行任意层任务。depth = 0 时 `stealCh = nil`，不窃取（Run goroutine 不是 worker）。

4. **execTask 结构** — worker pool 的 `readyCh` 类型从 `chan *Node` 改为 `chan *execTask`，每个 task 携带自己的 `doneCh` 和 `exec` 函数，支持 per-DAG 结果路由。

5. **结果嵌套 + 递归释放** — `NodeResult.SubflowResult` 挂载子 DAG 结果。`releaseDagResultRecursive` 递归释放所有嵌套层级。

6. **深度限制** — `EngineWithMaxSubflowDepth(n)`，默认 10 层。

**死锁场景验证**（`TestSubflow_DeadlockAvoidance`）：

`maxConcurrency=2`，两个 subflow 节点并发执行。无 work-stealing 时两个 worker 都阻塞在 subflow event loop，子节点永远排队 → 死锁。有 work-stealing 时，每个 event loop 在等 `doneCh` 的同时消费 `readyCh`，交替执行对方子图的节点 → 全部完成。

**测试覆盖**：

| 测试 | 验证内容 |
|------|---------|
| `TestSubflow_Basic` | 基本 subflow 执行和结果访问 |
| `TestSubflow_Nested` | 多层嵌套（3 层）结果递归访问 |
| `TestSubflow_MaxDepth` | 深度限制生效 |
| `TestSubflow_PanicRecovery` | SubflowFn panic 被 recover 捕获 |
| `TestSubflow_PanicInConstruction` | 非关键节点 panic 不影响 DAG |
| `TestSubflow_DAGContextShared` | 父子共享 dctx 读写 |
| `TestSubflow_DeadlockAvoidance` | maxConcurrency=2 双 subflow 不死锁 |
| `TestSubflow_EmptyDAG` | 返回 nil DAG 视为成功 |
| `TestSubflow_ConstructionError` | 构建错误正确标记 |
| `TestSubflow_ConcurrencyRespected` | 并发度受控 |

54 个测试全部通过，30 次连续运行零失败。


---

<!--
模板（复制后填充）:

## #N 问题标题

### 当前问题

描述问题现状、影响范围、触发条件。

### 策略抉择

列出候选方案，逐一分析优缺点，用表格对比。

#### 方案 A: 名称

描述。

| 优点 | 缺点 |
|------|------|
| ... | ... |

#### 抉择

选择哪个方案，给出理由。

### 落地实现

**文件**: 修改了哪些文件

描述核心设计、关键代码结构、验证方式。
-->
