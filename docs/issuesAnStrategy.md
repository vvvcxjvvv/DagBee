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
