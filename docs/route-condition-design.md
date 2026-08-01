# Route 路由分支优化方案

> 新增路由型条件节点 RouteFn + RouteMap，支持 if/else if/else 多分支路由编排，对标 C++ Taskflow Condition Task 和 go-taskflow 条件边。

---

## 问题现状

当前 `ConditionFn func(*DAGContext) bool` 是节点级门控：节点执行前检查，返回 false 则跳过自身。这能实现分支路由，但存在三个缺陷。

### 缺陷 1：N 分支 N 个重复条件函数

```go
d.AddNode("classify", classifyFn)
d.AddNode("premium", processPremium,
    NodeWithDependsOn("classify"),
    NodeWithCondition(func(dctx *DAGContext) bool {
        v, _ := GetTyped[string](dctx, "type")
        return v == "premium"
    }),
)
d.AddNode("standard", processStandard,
    NodeWithDependsOn("classify"),
    NodeWithCondition(func(dctx *DAGContext) bool {
        v, _ := GetTyped[string](dctx, "type")
        return v == "standard"
    }),
)
d.AddNode("fallback", handleFallback,
    NodeWithDependsOn("classify"),
    NodeWithCondition(func(dctx *DAGContext) bool {
        v, _ := GetTyped[string](dctx, "type")
        return v != "premium" && v != "standard"
    }),
)
```

路由逻辑分散在每个下游节点的 ConditionFn 中，每个函数重复读取 dctx、重复判断。分支越多越啰嗦，修改路由规则要改 N 处。

### 缺陷 2：路由意图不显式

classify 节点不知道自己在做路由决策。读代码的人需要逐个检查下游 ConditionFn 才能理解"这是一个三选一路由"。DAG 拓扑图也无法可视化标注路由边。

### 缺陷 3：merge 的 pending 隐含假设

```go
d.AddNode("merge", mergeFn,
    NodeWithDependsOn("premium", "standard", "fallback"),
)
```

merge 依赖三个分支。实际运行时只有一个分支执行，其余两个 Skipped。Skipped 节点通过 doneCh 回流仍会递减 merge 的 pending，所以 merge 能正常执行——但这个正确性依赖于"所有分支都依赖 classify 且都会被 Skipped 回流"这个隐含假设。如果某个分支忘记配 ConditionFn 或 DependsOn 写错，merge 的 pending 不会归零，永久 Hang。

---

## 业内实现对比

### C++ Taskflow：条件任务 + 强弱依赖

条件任务返回 `int` 索引，调度器直接 enqueue 对应后继。边分强弱：强依赖参与就绪计数，弱依赖（条件边）不参与。未被选中的弱依赖后继不激活，对下游零影响。

```cpp
auto [cond, yes, no, stop] = tf.emplace(
    [] { return 0; },       // 返回 0 = 走 yes
    [] { /* premium */ },
    [] { /* standard */ },
    [] { /* done */ }
);
cond.precede(yes, no);      // 弱依赖（条件边）
yes.precede(stop);           // 强依赖
no.precede(stop);
```

核心特点：
- 条件边不参与依赖计数，路由节点完成后调度器"传送"到选中分支
- 非选中分支对下游零影响，不存在 Skipped 概念
- 支持回边（条件任务指向自身），实现 while-loop
- 多条件任务返回 `SmallVector<int>`，同时激活多个分支

### go-taskflow：条件边 + 全局队列

go-taskflow 的 Condition 节点返回 `int`，对应后继索引。子图推入全局队列异步执行。

```go
d.AddConditionNode("cond", func() int {
    if score > 0.8 { return 0 }
    return 1
}, taskA, taskB)  // 0 → taskA, 1 → taskB
```

核心特点：
- 条件边是特殊边类型，不参与普通依赖计数
- 未选中后继自动跳过
- 不支持循环（DAG 无环约束）
- 路由逻辑集中在一个函数

### 对比总结

| 维度 | C++ Taskflow | go-taskflow | DagBee（当前） |
|------|-------------|-------------|---------------|
| 路由决策位置 | 条件任务返回索引 | 条件节点返回索引 | 每个下游各自 ConditionFn |
| 边类型 | 强弱分离 | 条件边组 | 无区分（统一 pending） |
| 未选中后继处理 | 不激活，零影响 | 自动跳过 | Skipped 回流递减 pending |
| 多分支激活 | SmallVector<int> | 不支持 | 不支持 |
| 循环支持 | 支持（条件回边） | 不支持 | 不支持 |
| 路由逻辑聚合度 | 集中 | 集中 | 分散 |

---

## 候选方案

### 方案 A：保持现状（per-node ConditionFn 路由）

不改动代码，通过文档约定路由写法。

| 优点 | 缺点 |
|------|------|
| 零代码改动 | N 分支 N 个重复 ConditionFn |
| 不影响现有 API | 路由意图不显式，可读性差 |
| | merge 的 pending 正确性依赖隐含假设 |
| | 修改路由规则需改 N 处 |
| | 无法可视化标注路由边 |

### 方案 B：RouteFn + RouteMap（条件边组）

Node 新增 `RouteFn func(*DAGContext) int` 和 `RouteMap map[int][]string`。路由节点执行后调用 RouteFn 获取索引，event loop 只激活 RouteMap[index] 中的下游节点，其余标记 Skipped。

| 优点 | 缺点 |
|------|------|
| 路由逻辑集中在一个函数，意图显式 | Node 新增两个字段 |
| API 风格与现有 ConditionFn 一致（functional option） | event loop 依赖传播增加路由判断分支 |
| Skipped 回流复用现有机制，merge pending 自然归零 | RouteMap 和 DependsOn 的关系需文档说明 |
| 不破坏现有 API（RouteFn 可选，不设置行为不变） | 不支持循环（DAG 无环约束，和现状一致） |
| 支持稀疏索引和多分支激活 | |

### 方案 C：SwitchFn + 位置数组

Node 新增 `SwitchFn func(*DAGContext) int` 和 `SwitchBranches []string`。返回值作为位置索引选择后继。

| 优点 | 缺点 |
|------|------|
| 路由逻辑集中 | 索引必须连续（0,1,2...），不支持稀疏 |
| 更贴近 C++ Taskflow 的位置索引模型 | 无法多分支激活（一个索引只能选一个后继） |
| | 索引和节点名的映射不直观，需数位置 |

### 方案 D：运行时动态边剪枝

节点完成时返回要激活的下游节点名列表，event loop 动态选择。

| 优点 | 缺点 |
|------|------|
| 最灵活，运行时决定任意后继组合 | 破坏静态 DAG 可验证性 |
| 支持完全动态的路由逻辑 | Validate 无法覆盖所有分支路径的环检测 |
| | API 不确定性强，调试困难 |
| | 与 DagBee 轻量静态 DAG 定位不符 |

### 方案 E：C++ Taskflow 强弱依赖模型

引入强依赖和弱依赖两种边类型，条件边为弱依赖不参与计数。

| 优点 | 缺点 |
|------|------|
| 对标业界最优实现 | 边模型从一组变两组，dag.go 结构大改 |
| 条件边零影响，不产生 Skipped | 破坏现有 pending 计数模型 |
| 支持循环回边 | DagBee 不支持循环（DAG 无环），强弱分离收益打折 |
| | 改动量大，与框架轻量定位不符 |

### 抉择

选择 **方案 B：RouteFn + RouteMap**。

方案 A 不解决根本问题。方案 C 是方案 B 的子集（位置数组 vs 显式 map），功能更弱。方案 D 破坏静态 DAG 可验证性。方案 E 引入强弱依赖是过度工程化——DagBee 的 Skipped 回流机制已等价覆盖"未选中后继不影响下游"的语义，不需要两种边类型。

方案 B 以最小侵入实现集中式路由决策，复用现有 Skipped 机制，不破坏 API 和 pending 模型。

---

## 核心设计

### 数据结构

**Node 扩展** (`node.go`)：

```go
type Node struct {
    // ...现有字段...
    RouteFn  func(*DAGContext) int  // 路由函数，返回选中的分支索引
    RouteMap map[int][]string       // 索引 → 下游节点名列表
}
```

- `RouteFn` 非 nil 时，该节点为路由节点
- `RouteMap` 声明各索引对应激活的下游节点
- `RouteFn` 和 `ConditionFn` 互斥（语义冲突：门控节点被跳过不应做路由决策）

**NodeResult 扩展** (`result.go`)：

```go
type NodeResult struct {
    // ...现有字段...
    RouteIndex int  // 路由节点选中的分支索引，-1 表示非路由节点
}
```

**DAG 扩展** (`dag.go`)：

```go
type DAG struct {
    // ...现有字段...
    routeEdges map[string]map[int][]string // nodeName → {index: [downstream]}
}
```

`AddNode` 时解析 `RouteMap`，填充 `routeEdges`。RouteMap 中的下游节点名必须在 `nodes` 中注册（Validate 校验）。

### 执行流程

```
1. Worker 执行路由节点的 Fn(ctx, dctx)
2. executeNode 调用 RouteFn(dctx) 获取 routeIndex
3. routeIndex 存入 NodeResult.RouteIndex
4. NodeResult 通过 doneCh 回流到 event loop
5. Event loop 依赖传播：
   a. 查 routeEdges[nodeName]，找到 RouteMap[routeIndex] = 选中分支
   b. 选中分支节点：pending 正常递减
   c. 未选中分支节点：标记 Skipped，通过 doneCh 回流
   d. Skipped 节点的下游 pending 正常递减（现有机制）
```

### Event loop 改动

现有依赖传播逻辑：

```go
for _, downstream := range d.edges[nr.NodeName] {
    newCount := atomic.AddInt32(pending[downstream], -1)
    if newCount == 0 {
        scheduler.Enqueue(d.nodes[downstream])
    }
}
```

改造后：

```go
if routeMap := d.routeEdges[nr.NodeName]; routeMap != nil && nr.RouteIndex >= 0 {
    // 路由节点：只激活选中分支，其余标记 Skipped
    selected := make(map[string]bool)
    for _, name := range routeMap[nr.RouteIndex] {
        selected[name] = true
    }
    for _, downstream := range d.edges[nr.NodeName] {
        if selected[downstream] {
            // 选中分支：pending 递减
            newCount := atomic.AddInt32(pending[downstream], -1)
            if newCount == 0 {
                scheduler.Enqueue(d.nodes[downstream])
            }
        } else {
            // 未选中分支：标记 Skipped 回流
            skipNR := acquireNodeResult()
            skipNR.NodeName = downstream
            skipNR.Status = StatusSkipped
            doneCh <- skipNR
        }
    }
} else {
    // 普通节点：全量递减（现有逻辑）
    for _, downstream := range d.edges[nr.NodeName] {
        newCount := atomic.AddInt32(pending[downstream], -1)
        if newCount == 0 {
            scheduler.Enqueue(d.nodes[downstream])
        }
    }
}
```

**示例推演**

DAG 结构：

```
classify ──► premium
         ──► standard
         ──► fallback

premium   ──► merge
standard  ──► merge
fallback  ──► merge
```

注册代码：

```go
d.AddNode("classify", classifyFn,
    NodeWithRoute(routeFn, map[int][]string{
        0: {"premium"},
        1: {"standard"},
        2: {"fallback"},
    }),
)
d.AddNode("premium",  processPremium,  NodeWithDependsOn("classify"))
d.AddNode("standard", processStandard, NodeWithDependsOn("classify"))
d.AddNode("fallback", handleFallback,  NodeWithDependsOn("classify"))
d.AddNode("merge", mergeFn, NodeWithDependsOn("premium", "standard", "fallback"))
```

注册后数据结构状态：

```
edges["classify"]      = ["premium", "standard", "fallback"]  // 来自三个 DependsOn
routeEdges["classify"] = {0: ["premium"], 1: ["standard"], 2: ["fallback"]}

pending["premium"]  = 1  (依赖 classify)
pending["standard"] = 1  (依赖 classify)
pending["fallback"] = 1  (依赖 classify)
pending["merge"]    = 3  (依赖 premium + standard + fallback)
```

classify 执行成功，RouteFn 返回 0（选中 premium）。event loop 进入路由分支：

```
1. 构建 selected = {"premium": true}

2. 遍历 edges["classify"]:
   ├── premium   → selected=true  → pending["premium"] 1→0 → 入队执行
   ├── standard  → selected=false → 标记 Skipped → doneCh <- skipNR
   └── fallback  → selected=false → 标记 Skipped → doneCh <- skipNR

3. doneCh 回流处理：
   ├── standard 的 Skipped NR 到达 → merge pending 3→2
   └── fallback 的 Skipped NR 到达 → merge pending 2→1

4. premium 执行完成 → doneCh <- successNR → merge pending 1→0 → 入队执行
```

最终结果：

```
classify:  Success, RouteIndex=0
premium:   Success
standard:  Skipped
fallback:  Skipped
merge:     Success  (pending 3→0, 三个回流: 2 Skipped + 1 Success)
```

对比普通节点（else 分支）：不做 selected 过滤，`edges["classify"]` 中的三个下游全部 pending 递减、全部入队执行。

### ConditionFn 与 RouteFn 的关系

| 维度 | ConditionFn | RouteFn |
|------|------------|---------|
| 决策时机 | 节点执行**前** | 节点执行**后** |
| 决策对象 | 自身（跑不跑） | 下游（哪些跑） |
| 返回值 | bool | int |
| 配套配置 | 无 | RouteMap |
| 适用场景 | 灰度开关、模型可用性检查 | 请求分流、A/B 实验分桶 |

两者互斥：`executeNode` 中先检查 `ConditionFn`，如果跳过则不调用 `RouteFn`。一个节点不应同时声明两者（Validate 校验报错）。

### Merge 节点的 pending 语义

```go
d.AddNode("classify", classifyFn,
    NodeWithRoute(routeFn, map[int][]string{
        0: {"premium"},
        1: {"standard"},
        2: {"fallback"},
    }),
)
d.AddNode("premium",  processPremium,  NodeWithDependsOn("classify"))
d.AddNode("standard", processStandard, NodeWithDependsOn("classify"))
d.AddNode("fallback", handleFallback,  NodeWithDependsOn("classify"))
d.AddNode("merge", mergeFn,
    NodeWithDependsOn("premium", "standard", "fallback"),
)
```

classify 返回 0 时：

```
classify 完成 → RouteFn 返回 0
  → premium: pending 递减 → 就绪 → 执行
  → standard: Skipped → doneCh 回流 → merge pending 递减
  → fallback: Skipped → doneCh 回流 → merge pending 递减
premium 完成 → doneCh 回流 → merge pending 递减 → 归零 → 就绪 → 执行
```

merge 的 pending 初始 = 3。三个 doneCh 回流（1 Success + 2 Skipped）使其归零。正确性由路由机制保证——RouteMap 中声明的所有分支都会被处理（激活或 Skipped），不存在"忘记配 ConditionFn 导致 pending 不归零"的问题。

### 多分支激活

RouteMap 的 value 是 `[]string`，一个索引可映射多个节点：

```go
NodeWithRoute(func(dctx *DAGContext) int {
    if abTest { return 0 }  // 同时走 A 和 B
    return 1                 // 只走 C
}, map[int][]string{
    0: {"branch_a", "branch_b"},
    1: {"branch_c"},
})
```

索引 0 同时激活 branch_a 和 branch_b，它们的 pending 都递减，并发执行。

---

## 改动文件清单

| 文件 | 改动 | 说明 |
|------|------|------|
| `node.go` | `RouteFn`、`RouteMap` 字段，`NodeWithRoute` 选项，互斥校验 | 类型扩展 |
| `dag.go` | `routeEdges` 存储，`AddNode` 解析 RouteMap，`Validate` 校验引用 | 边存储扩展 |
| `result.go` | `NodeResult.RouteIndex` 字段，`Reset` 清零 | 结果扩展 |
| `engine.go` | `executeNode` 路由分支调用 RouteFn；event loop 依赖传播增加路由判断 | 核心改动 |
| `route_test.go` | 新增测试文件 | 验证 |

---

## API 示例

### 基本 if/else 路由

```go
d := NewDAG("router")

d.AddNode("classify", func(_ context.Context, dctx *DAGContext) error {
    req := get RequestType()
    dctx.Set("type", req)
    return nil
}, NodeWithRoute(
    func(dctx *DAGContext) int {
        v, _ := GetTyped[string](dctx, "type")
        switch v {
        case "premium":  return 0
        case "standard": return 1
        default:         return 2
        }
    },
    map[int][]string{
        0: {"premium_pipeline"},
        1: {"standard_pipeline"},
        2: {"fallback_handler"},
    },
))

d.AddNode("premium_pipeline",  processPremium,  NodeWithDependsOn("classify"))
d.AddNode("standard_pipeline", processStandard, NodeWithDependsOn("classify"))
d.AddNode("fallback_handler",  handleFallback,  NodeWithDependsOn("classify"))
d.AddNode("output", outputFn,
    NodeWithDependsOn("premium_pipeline", "standard_pipeline", "fallback_handler"),
)
```

### 多分支同时激活

```go
d.AddNode("ab_gate", gateFn, NodeWithRoute(
    func(dctx *DAGContext) int {
        bucket, _ := GetTyped[int](dctx, "ab_bucket")
        if bucket == 0 { return 0 }  // 实验组：A + B 同时跑
        return 1                       // 对照组：只跑 C
    },
    map[int][]string{
        0: {"recall_a", "recall_b"},
        1: {"recall_c"},
    },
))
```

### ConditionFn + RouteFn 共存（不同节点）

```go
// gate 节点用 ConditionFn 决定自身是否执行
d.AddNode("experiment", runExperiment,
    NodeWithCondition(func(dctx *DAGContext) bool {
        return graySwitch.On
    }),
)

// classify 节点用 RouteFn 决定下游路由
d.AddNode("classify", classifyFn,
    NodeWithDependsOn("experiment"),
    NodeWithRoute(routeFn, routeMap),
)
```

---

## 风险评估

| 风险 | 级别 | 应对 |
|------|------|------|
| RouteMap 引用不存在的节点 | P1 | Validate 校验所有引用 |
| RouteFn 返回未在 RouteMap 中的索引 | P1 | executeNode 返回错误，标记节点 Failed |
| ConditionFn 和 RouteFn 同时设置 | P2 | Validate 报错 |
| RouteMap 和 DependsOn 交叉声明 | P2 | 文档约定：RouteMap 中的节点应同时出现在 DependsOn 中（通过 AddNode 自动建立 edges） |
| 路由节点自身被 Skipped（上游 ConditionFn 跳过） | 无风险 | RouteFn 不调用，RouteMap 不生效，所有下游按普通 Skipped 处理 |
| 未选中分支的 Skipped 回流增加 doneCh 流量 | 低 | doneCh buffer = total，不阻塞 |

---

## 实现顺序

1. `node.go` — RouteFn、RouteMap 字段、NodeWithRoute、互斥校验
2. `result.go` — RouteIndex 字段、Reset 清零
3. `dag.go` — routeEdges 存储、AddNode 解析、Validate 校验
4. `engine.go` — executeNode 调用 RouteFn、event loop 路由传播
5. `route_test.go` — 基本 if/else、多分支激活、merge pending、ConditionFn 互斥、未注册索引
6. 文档更新 — README、doc.go、issuesAndStrategy.md
