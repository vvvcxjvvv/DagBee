你是一位资深Go语言后端架构师，现在需要帮我开发一个名为 **DagBee** 的**轻量级、生产可用的DAG框架**。请参考以下要求设计和实现：

---

### 一、项目背景与核心约束

1. **技术栈**：纯Go语言实现，**尽量不引入第三方依赖**（如需用，仅允许用标准库或极其轻量的库，如`golang.org/x/sync`）；
2. **定位**：轻量级，嵌入推荐引擎微服务使用，无需独立部署调度系统。要求**低延迟、高可用、无冗余调度开销**；
3. **性能目标**：
   - DAG 构建开销：构建一个 20 节点的 DAG 应在 **< 1ms**；
   - 调度开销：每个节点的调度延迟应 **< 100μs**（不含节点自身执行时间）；
   - 内存占用：单次 DAG 执行的框架额外内存开销 **< 1KB/节点**；
   - 支持**对象池复用**（`sync.Pool`）：DAG、NodeResult 等结构可复用，减少 GC 压力。

---

### 二、核心功能需求（按优先级排序）

#### 1. 基础DAG能力（P0 必须）

- 支持**节点注册**：每个节点包含`名称`、`执行函数`、`依赖节点列表`；
- 支持**依赖关系管理**：添加有向边，自动**检测循环依赖**并报错；
- 支持**拓扑排序**：基于Kahn算法或DFS生成合法的执行顺序；
- 支持**DAG可视化**：输出简单的拓扑结构字符串（用于调试）；
- 支持**节点可插拔、动态调整**：可通过 YAML 配置定义和修改节点编排；
- 支持**编程式构建**（Go 代码 Builder 模式）和**声明式构建**（YAML 配置）两种方式。

#### 2. 执行调度能力（P0 必须）

- 支持**串行+并行混合执行**：
  - 有强依赖的节点**串行执行**（保证逻辑正确）；
  - 无依赖的节点**并行执行**（用Go协程，提升效率）；
- 支持**并发度控制**：限制最大并行协程数，避免资源耗尽；
- 支持**上下文传递**：用`context.Context`控制超时、取消；
- 支持**节点优先级（Priority）**：当多个节点同时就绪时，优先调度高优先级节点（优先级可在 YAML 中配置）；
- 支持**Panic 恢复**：每个节点执行时自动 `recover`，将 panic 转化为 error，避免单节点 crash 导致整个服务宕机。Panic 信息记入 NodeResult 的错误信息中；
- 支持**优雅停机（Graceful Shutdown）**：收到取消信号时，等待正在执行的节点完成（或超时强制终止），不再调度新节点，与 `context.Context` 的 cancel 机制深度集成。

#### 3. 容错与降级能力（P0 生产必须）

- 支持**节点级超时控制**：每个节点可配置独立超时时间；
- 支持**节点级重试**：每个节点可配置重试次数、重试间隔（支持固定间隔与指数退避）；
- 支持**失败策略**：
  - 「快速失败」：核心节点（`critical: true`）失败，立即终止整个DAG；
  - 「降级继续」：非核心节点（`critical: false`）失败，跳过该节点，继续执行后续流程；
- 支持**降级兜底数据**：节点失败时可返回预设的兜底数据（通过节点配置 `FallbackFunc` 提供）。

#### 4. 数据传递（P1 重要）

- 框架层提供线程安全的 **SharedStore（共享数据仓库）**：
  - key-value 存取，支持泛型辅助函数或类型断言辅助函数；
  - 读写并发安全（细粒度锁或 `sync.Map`）；
  - 贯穿整个 DAG 执行生命周期；
- 业务层允许用户自定义强类型的结构体作为节点间传递的数据载体；
- SharedStore 在 DAG 执行前初始化，执行后可整体读取。

#### 5. 可观测性（P1 重要）

- 支持**执行状态追踪**：记录每个节点的`开始时间`、`结束时间`、`执行状态`（Success / Failed / Retried / Skipped / Panicked）、`错误信息`、`重试次数`；
- 支持**钩子函数（Hooks）**：
  - `BeforeNode`：节点执行前回调；
  - `AfterNode`：节点执行后回调（含执行结果）；
  - `OnNodeSkip`：节点被跳过时回调（降级场景）；
  - `OnDAGComplete`：DAG 整体执行完成后回调；
- 支持**Logger 接口抽象**：定义 `Logger` 接口，默认提供标准库 `log` 实现，用户可注入自定义 Logger（如 zap、logrus 等）；
- 钩子函数中提供充足上下文信息（node name、dag id、耗时、错误等），方便接入 Prometheus / OpenTelemetry。

#### 6. 条件执行与分支逻辑（P2 可选，推荐实现）

- 支持**条件节点（Conditional Node）**：根据上游节点的输出结果，动态决定下游执行哪条分支；
- 支持**跳过节点（Skip）**：当条件不满足时，标记节点为 `Skipped` 而非 `Failed`，不影响后续依赖。

#### 7. 子DAG / DAG 嵌套（P2 可选）

- 支持将一个完整 DAG 作为另一个 DAG 中的一个节点执行（子DAG）；
- 子DAG 共享父 DAG 的 Context 和 SharedStore，但拥有独立的执行状态追踪；
- 可用于封装可复用的子流程（如"召回子流程"、"排序子流程"）。

#### 8. Dry-Run 模式（P2 可选）

- 支持 Dry-Run 模式：只验证 DAG 拓扑、依赖关系、配置合法性，不实际执行节点函数；
- Dry-Run 输出完整的执行计划（拓扑顺序、并行分组、预估关键路径等）。

#### 9. 简单 Web UI（P2 可选）

- 提供一个**内嵌的轻量级 Web UI**（基于 Go 标准库 `net/http` + 内嵌静态资源 `embed`），无需额外前端构建工具；
- 核心页面：
  - **DAG 拓扑视图**：以可视化图形展示节点和依赖关系（可基于简单的 SVG 或 Canvas 渲染）；
  - **执行记录列表**：展示最近的 DAG 执行记录，包含状态、总耗时、成功/失败节点数；
  - **执行详情页**：展示单次执行中每个节点的状态、耗时、错误信息，支持甘特图形式展示并行度；
- 作为可选模块，通过 `dagbee.WithWebUI(":8080")` 一行代码启用，不启用时零开销；
- 仅用于开发调试和内部监控，不承载生产流量。

---

### 三、YAML 配置 Schema

框架应支持通过 YAML 文件声明式定义 DAG。示例 Schema 如下：

```yaml
dag:
  name: "recommend-pipeline"
  max_concurrency: 8
  timeout: 5s

  nodes:
    - name: "recall_cf"
      timeout: 200ms
      retry:
        count: 2
        interval: 50ms
        strategy: "fixed"       # fixed | exponential
      critical: true            # 核心节点，失败则终止整个DAG
      priority: 10              # 优先级，数值越大越优先
      depends_on: []

    - name: "recall_content"
      timeout: 200ms
      retry:
        count: 1
        interval: 50ms
        strategy: "fixed"
      critical: false           # 非核心节点，失败可降级
      priority: 5
      depends_on: []

    - name: "recall_hot"
      timeout: 150ms
      critical: false
      priority: 3
      depends_on: []

    - name: "merge_and_rank"
      timeout: 500ms
      critical: true
      priority: 10
      depends_on:
        - "recall_cf"
        - "recall_content"
        - "recall_hot"

    - name: "filter"
      timeout: 200ms
      critical: true
      depends_on:
        - "merge_and_rank"

    - name: "fill_detail"
      timeout: 300ms
      critical: false
      depends_on:
        - "filter"
```

---

### 四、概念模型

| 概念 | 说明 |
|------|------|
| **Node** | DAG 中的最小执行单元，封装一个具体的业务逻辑。每个 Node 有唯一标识（name）。 |
| **Edge** | 节点间的有向依赖关系，表示执行顺序约束。A → B 表示 B 依赖 A 的输出。 |
| **DAG** | 由 Node 和 Edge 构成的有向无环图，代表一个完整的任务编排。 |
| **Engine** | DAG 的执行引擎，负责拓扑排序、并发调度、结果收集。 |
| **Scheduler** | 从 Engine 中拆出的调度策略模块，负责就绪节点的优先级排序、并发度管控，方便后续替换调度策略。 |
| **SharedStore** | 节点间的共享数据仓库，提供并发安全的 key-value 读写，贯穿整个 DAG 执行生命周期。 |
| **NodeResult** | 节点的执行结果，包含输出数据、执行耗时、错误信息、执行状态等。 |
| **DagResult** | DAG 整体的执行结果，包含所有节点的 NodeResult、总耗时、执行状态等。 |
| **Hook** | 生命周期钩子，包括 BeforeNode / AfterNode / OnNodeSkip / OnDAGComplete，用于监控与扩展。 |

---

### 五、代码与交付要求

#### 1. 项目结构

```
dagbee/
│
│── Core（核心层）───────────────────────────────
├── dag.go              // DAG 定义、节点注册、边管理、环检测
├── node.go             // Node 定义、执行函数签名、节点配置
│
│── Engine（执行层）─────────────────────────────
├── engine.go           // 执行引擎：拓扑排序、调度入口、结果收集
├── scheduler.go        // 调度策略：优先级、并发度、就绪队列管理
│
│── Data（数据层）───────────────────────────────
├── store.go            // SharedStore：并发安全的节点间数据传递
├── result.go           // NodeResult / DagResult 定义与对象池
│
│── Support（支撑层）────────────────────────────
├── config.go           // YAML 配置解析与校验
├── options.go          // Functional Options 模式
├── hook.go             // 钩子函数接口与默认实现
├── errors.go           // 统一错误类型定义
├── logger.go           // Logger 接口抽象与默认实现
├── visualize.go        // DAG 拓扑可视化输出
├── doc.go              // 包文档与文件分层说明
│
│── Tests（测试）────────────────────────────────
├── dag_test.go         // DAG 构建、环检测单元测试
├── engine_test.go      // 引擎调度、并发执行、容错逻辑测试
├── store_test.go       // SharedStore 并发安全测试
├── benchmark_test.go   // 关键路径性能基准测试
│
│── Examples & Docs（示例与文档）─────────────────
├── examples/
│   ├── simple/         // 简单 DAG 示例
│   └── recommend/      // 推荐引擎场景完整示例
│       ├── main.go
│       └── pipeline.yaml
├── docs/
│   ├── design-prompt.md
│   └── dag-frameworks-research.md
│
├── go.mod
├── go.sum
└── README.md
```

#### 2. 代码规范

- 完整的 Go 文档注释（`/* */` 或 `//`）；
- 变量、函数命名清晰，符合 Go 语言规范；
- 并发安全（用 `sync.Mutex`、`sync.RWMutex`、`sync.Map` 等保证共享数据安全）；
- 采用 **Builder 模式 + Functional Options** 构建 DAG，兼顾可读性和扩展性。

#### 3. 测试要求

- **单元测试**：核心模块（拓扑排序、环检测、调度引擎、重试逻辑、SharedStore）需有完整的单元测试，覆盖率 > 80%；
- **基准测试（Benchmark）**：提供关键路径的性能基准测试（DAG 构建耗时、调度延迟、并发执行吞吐量）；
- **集成测试**：提供端到端的 DAG 执行集成测试用例，覆盖正常流程、超时、重试、降级、Panic 恢复等场景。

#### 4. API 使用示例

框架应支持如下风格的使用方式（编程式）：

```go
// 构建 DAG
d := dagbee.NewDAG("recommend-pipeline",
    dagbee.WithMaxConcurrency(8),
    dagbee.WithTimeout(5*time.Second),
)

// 注册节点
d.AddNode("recall_cf", recallCFFunc,
    dagbee.NodeWithTimeout(200*time.Millisecond),
    dagbee.NodeWithRetry(2, 50*time.Millisecond),
    dagbee.NodeWithCritical(true),
    dagbee.NodeWithPriority(10),
)

d.AddNode("recall_content", recallContentFunc,
    dagbee.NodeWithTimeout(200*time.Millisecond),
    dagbee.NodeWithCritical(false),
    dagbee.NodeWithFallback(fallbackRecallContent),
)

d.AddNode("merge_and_rank", mergeRankFunc,
    dagbee.NodeWithTimeout(500*time.Millisecond),
    dagbee.NodeWithCritical(true),
    dagbee.NodeWithDependsOn("recall_cf", "recall_content"),
)

// 执行
result := dagbee.NewEngine().Run(ctx, d)

// 读取结果
fmt.Println(result.Status)                         // Success / Failed
fmt.Println(result.NodeResult("recall_cf").Duration)
fmt.Println(result.NodeResult("merge_and_rank").Output)
```

---

### 六、节点执行函数签名

```go
// NodeFunc 是节点的执行函数签名
// ctx: 携带超时控制和取消信号的 context
// store: 共享数据仓库，用于读取上游数据和写入本节点输出
// 返回 error：nil 表示成功，非 nil 触发重试或降级逻辑
type NodeFunc func(ctx context.Context, store *SharedStore) error
```

---

现在，请开始设计并实现这个轻量级 DAG 框架 **DagBee**。
