# 开源 DAG 框架深度调研报告

> 调研时间：2026-03-10
>
> 本文档对当前主流开源 DAG（Directed Acyclic Graph，有向无环图）框架进行全面梳理，涵盖 Python、Go、Java、Rust、TypeScript、C++ 等多种语言生态，从架构设计、核心特性、适用场景等维度进行对比分析。

---

## 目录

- [1. 概述](#1-概述)
- [2. 框架总览对比表](#2-框架总览对比表)
- [3. Python 生态](#3-python-生态)
  - [3.1 Apache Airflow](#31-apache-airflow)
  - [3.2 Dagster](#32-dagster)
  - [3.3 Prefect](#33-prefect)
  - [3.4 Luigi (Spotify)](#34-luigi-spotify)
  - [3.5 Flyte](#35-flyte)
- [4. Go 生态](#4-go-生态)
  - [4.1 Temporal](#41-temporal)
  - [4.2 Dagu](#42-dagu)
  - [4.3 goflow](#43-goflow)
  - [4.4 go-dag](#44-go-dag)
  - [4.5 ppacer](#45-ppacer)
- [5. Java 生态](#5-java-生态)
  - [5.1 Apache DolphinScheduler](#51-apache-dolphinscheduler)
  - [5.2 Netflix Maestro](#52-netflix-maestro)
  - [5.3 Azkaban (LinkedIn)](#53-azkaban-linkedin)
  - [5.4 Kestra](#54-kestra)
  - [5.5 Cadence (Uber)](#55-cadence-uber)
- [6. Kubernetes 原生](#6-kubernetes-原生)
  - [6.1 Argo Workflows](#61-argo-workflows)
- [7. Rust 生态](#7-rust-生态)
  - [7.1 dagrs](#71-dagrs)
  - [7.2 dagx](#72-dagx)
  - [7.3 Floxide](#73-floxide)
  - [7.4 taskflow-rs](#74-taskflow-rs)
- [8. TypeScript 生态](#8-typescript-生态)
  - [8.1 Flowcraft](#81-flowcraft)
  - [8.2 dagflowjs](#82-dagflowjs)
  - [8.3 OpenWorkflow](#83-openworkflow)
- [9. C++ 生态](#9-c-生态)
  - [9.1 Taskflow](#91-taskflow)
- [10. 框架选型指南](#10-框架选型指南)
- [11. 总结与趋势](#11-总结与趋势)

---

## 1. 概述

DAG 框架的核心目标是对任务依赖关系进行建模和调度，确保任务按正确的拓扑顺序执行。根据定位和复杂度，可将现有框架分为三个层级：

| 层级 | 定位 | 代表项目 |
|------|------|----------|
| **平台级编排器** | 完整的工作流管理平台，含 Web UI、调度器、监控、多租户 | Airflow, DolphinScheduler, Kestra, Argo Workflows |
| **应用级框架** | 面向开发者的编程框架，可嵌入业务系统 | Temporal, Dagster, Prefect, Flyte, Maestro |
| **轻量级库** | 提供 DAG 数据结构与执行引擎的最小化库 | Dagu, goflow, dagrs, dagx, Taskflow(C++), Flowcraft |

---

## 2. 框架总览对比表

| 框架 | 语言 | GitHub Stars | 许可证 | 定位 | 核心特点 |
|------|------|-------------|--------|------|----------|
| **Apache Airflow** | Python | ~38k | Apache-2.0 | 平台级 | 行业标准，生态最丰富 |
| **Dagster** | Python | ~15k | Apache-2.0 | 应用级 | 资产优先，数据血缘 |
| **Prefect** | Python | ~22k | Apache-2.0 | 应用级 | Python 原生，动态工作流 |
| **Luigi** | Python | ~18k | Apache-2.0 | 应用级 | Spotify 出品，批处理管道 |
| **Flyte** | Python/Go | ~5.1k | Apache-2.0 | 应用级 | K8s 原生，ML 流水线 |
| **Temporal** | Go | ~19k | MIT | 应用级 | 持久化执行，强一致性 |
| **Dagu** | Go | ~3.1k | GPL-3.0 | 轻量级 | 单二进制，YAML 定义，零依赖 |
| **goflow** | Go | ~293 | MIT | 轻量级 | 简单 DAG 调度器+面板 |
| **go-dag** | Go | - | Apache-2.0 | 轻量级 | DAG 执行引擎库 |
| **ppacer** | Go | - | Apache-2.0 | 轻量级 | 高性能，低资源开销 |
| **DolphinScheduler** | Java | ~14k | Apache-2.0 | 平台级 | 分布式，拖拽式 UI，30+ 任务类型 |
| **Netflix Maestro** | Java | ~3.7k | Apache-2.0 | 平台级 | 超大规模，支持循环图 |
| **Azkaban** | Java | ~4.5k | Apache-2.0 | 平台级 | LinkedIn 出品，已趋于稳定 |
| **Kestra** | Java | ~13k | Apache-2.0 | 平台级 | 声明式 YAML，1200+ 插件 |
| **Cadence** | Go | ~9.2k | MIT | 应用级 | Uber 出品，Temporal 前身 |
| **Argo Workflows** | Go | ~14.5k | Apache-2.0 | 平台级 | CNCF 毕业项目，K8s CRD |
| **dagrs** | Rust | ~391 | Apache-2.0/MIT | 轻量级 | Rust DAG 引擎 |
| **dagx** | Rust | - | MIT | 轻量级 | 编译期环检测，极致性能 |
| **Floxide** | Rust | - | - | 轻量级 | 分布式并行工作流框架 |
| **taskflow-rs** | Rust | - | - | 轻量级 | 异步任务处理框架 |
| **Flowcraft** | TypeScript | - | MIT | 轻量级 | 零依赖，声明式 DAG |
| **dagflowjs** | TypeScript | - | MIT | 轻量级 | 类型安全 DAG 执行引擎 |
| **OpenWorkflow** | TypeScript | - | MIT | 轻量级 | 持久化可恢复工作流 |
| **Taskflow** | C++ | ~10k+ | MIT | 轻量级 | 任务并行编程系统，CPU/GPU |

---

## 3. Python 生态

### 3.1 Apache Airflow

- **GitHub**: [apache/airflow](https://github.com/apache/airflow) (~38k stars)
- **创建者**: Airbnb (2014)，现为 Apache 基金会顶级项目
- **许可证**: Apache-2.0
- **最新版本**: 3.0 (2025 年 4 月)

#### 架构设计

```
┌─────────────┐    ┌──────────────┐    ┌──────────────┐
│  Web Server  │    │   Scheduler  │    │   Triggerer   │
│  (Flask UI)  │    │  (调度核心)   │    │ (异步事件循环) │
└──────┬───────┘    └──────┬───────┘    └──────┬───────┘
       │                   │                   │
       └───────────┬───────┘───────────────────┘
                   │
            ┌──────▼───────┐
            │  Metadata DB  │
            │ (PostgreSQL)  │
            └──────┬───────┘
                   │
            ┌──────▼───────┐
            │   Executor    │──► Workers
            │ (执行引擎)    │
            └──────────────┘
```

- **Scheduler**：触发已调度的工作流，将任务提交给 Executor
- **Executor**：执行引擎，生产环境通常推送到独立 Worker 节点
- **Web Server**：提供 Web UI 用于检查和调试 DAG
- **Triggerer**：异步事件循环组件（Airflow 2.2+ 引入）
- **Metadata DB**：存储所有调度器、执行器、Web 服务器的状态

#### 核心特性

- Python 代码定义 DAG，支持 Operators、Sensors、TaskFlow 装饰器
- Airflow 3.0 新增：解耦的任务执行接口（Task SDK）、事件驱动调度、DAG 版本控制、资产感知
- 丰富的社区集成生态（数百种 Provider Packages）
- 支持 RBAC、审计日志、多租户
- 月下载量超 3000 万，约 80,000 家组织在使用

#### 适用场景

- ETL/ELT 批处理管道
- 企业级复杂数据工作流
- 需要广泛集成的场景（各类云服务、数据库、大数据组件）
- 已有 Airflow 运维经验的团队

#### 局限性

- 学习曲线陡峭
- 本地开发和测试体验较弱（3.0 有所改善）
- 不适合低延迟实时任务
- 部署和运维复杂度高（需要数据库、消息队列等基础设施）

---

### 3.2 Dagster

- **GitHub**: [dagster-io/dagster](https://github.com/dagster-io/dagster) (~15k stars)
- **创建者**: Elementl (2018)
- **许可证**: Apache-2.0
- **语言**: Python (66%) + TypeScript (32.5%)

#### 架构设计

Dagster 采用**资产优先（Asset-First）**的架构理念，围绕数据资产（表、数据集、ML 模型、报告等）而非任务来建模。

核心概念：
- **Software-Defined Assets (SDA)**：将数据资产定义为代码
- **Dagster Instance**：核心运行时，管理运行历史和元数据
- **Dagit (Web UI)**：提供数据血缘图、运行监控、资产目录
- **Grpc Server**：用于代码仓库与 Dagster Instance 的通信

#### 核心特性

- 资产感知的编排，端到端数据血缘追踪
- 原生 dbt 集成
- 软件工程实践：分支部署、单元测试、类型检查
- 优秀的本地开发体验
- 声明式编程模型

#### 适用场景

- 数据平台建设（资产管理和血缘追踪）
- 需要将管道视为可测试软件的团队
- dbt 深度用户
- 数据资产目录和可观测性需求

#### 局限性

- 相对较新的生态，集成数量不如 Airflow
- 资产优先的范式需要一定的思维转换
- 企业级功能需要商业版本

---

### 3.3 Prefect

- **GitHub**: [PrefectHQ/prefect](https://github.com/PrefectHQ/prefect) (~22k stars)
- **创建者**: Prefect Technologies (2018)
- **许可证**: Apache-2.0

#### 架构设计

Prefect 采用**解耦式架构**，编排逻辑与执行环境分离：

- 使用 `@flow` 和 `@task` 装饰器将 Python 函数转化为工作流
- 支持任意执行环境（本地、CI/CD、VM、Kubernetes）
- 分为自托管 Server 和 Prefect Cloud 两种部署模式

#### 核心特性

- Python 原生体验，将任何 Python 函数变为生产级工作流
- 动态/事件驱动工作流支持
- 内建重试（指数退避）、缓存、依赖管理
- Cron 调度与事件驱动自动化
- 自托管 / Cloud 可选

#### 适用场景

- 需要最大灵活性的动态工作流
- Python 优先的团队
- 事件驱动型管道
- 快速原型开发和迭代

#### 局限性

- 高级功能（如 RBAC、审计）需要 Prefect Cloud
- 社区集成不如 Airflow 丰富
- 从 Prefect 1.x 到 2.x 的迁移存在较大变化

---

### 3.4 Luigi (Spotify)

- **GitHub**: [spotify/luigi](https://github.com/spotify/luigi) (~18.3k stars)
- **创建者**: Spotify
- **许可证**: Apache-2.0
- **最新版本**: 3.6.0 (2024-12)

#### 核心特性

- 通过 Python 类继承定义任务和依赖
- 基于目标（Target）的依赖解析，检查输出是否存在来判断任务是否完成
- 内建 Hadoop/HDFS 支持
- 简单的可视化界面
- 轻量级，上手简单

#### 适用场景

- 批处理管道
- Hadoop 生态数据处理
- 简单到中等复杂度的工作流

#### 局限性

- 功能相对基础，缺乏现代编排器的高级特性（事件驱动、资产管理等）
- 调度能力弱，通常依赖外部 cron
- 社区活跃度下降，新项目建议选择更现代的替代方案
- 不支持分布式执行

---

### 3.5 Flyte

- **GitHub**: [flyteorg/flyte](https://github.com/flyteorg/flyte) (~5.1k stars)
- **创建者**: Lyft (后独立为 Union.ai)
- **许可证**: Apache-2.0
- **SDK 语言**: Python、Java、Scala

#### 架构设计

Flyte 采用 Kubernetes 原生架构，核心组件包括：
- **FlyteAdmin**：控制平面，管理工作流注册和执行
- **FlytePropeller**：K8s Operator，负责工作流执行
- **FlyteConsole**：Web UI
- **DataCatalog**：数据缓存和共享

#### 核心特性

- 强类型系统：FlyteFile, FlyteDirectory, StructuredDataset 等
- 可复现的 ML 管道：任务版本化、数据快照
- Kubernetes 原生调度，支持 GPU 资源
- 多语言 SDK (Python, Java, Scala)
- 内建模型服务能力

#### 适用场景

- ML 模型训练和推理管道
- 数据处理和分析管道
- 需要强可复现性的场景
- Kubernetes 环境下的工作流编排

#### 局限性

- 依赖 Kubernetes，部署复杂度较高
- 生态和社区相对较小
- 非 ML 场景下的优势不明显

---

## 4. Go 生态

### 4.1 Temporal

- **GitHub**: [temporalio/temporal](https://github.com/temporalio/temporal) (~19k stars)
- **创建者**: Temporal Technologies（原 Cadence 核心团队）
- **许可证**: MIT
- **SDK 语言**: Go, Java, TypeScript, Python, .NET

#### 架构设计

Temporal 是一个**持久化执行系统（Durable Execution System）**：

```
┌──────────────┐     ┌──────────────┐
│  Temporal     │     │   Worker      │
│  Server       │◄───►│  (Go/Java/   │
│  (Go)         │     │   TS/Python)  │
├──────────────┤     └──────────────┘
│ Frontend Svc  │
│ History Svc   │
│ Matching Svc  │
│ Worker Svc    │
└──────┬───────┘
       │
┌──────▼───────┐
│ Persistence   │
│ (Cassandra/   │
│  PostgreSQL/  │
│  MySQL)       │
└──────────────┘
```

- **事件溯源（Event Sourcing）**：所有工作流状态通过事件历史完整记录
- **确定性重放（Deterministic Replay）**：故障恢复时重放事件历史
- **Activity / Workflow 分离**：Workflow 代码确定性执行，Activity 处理副作用

#### 核心特性

- 持久化执行：工作流可以跨越数秒到数年
- Exactly-once 执行语义
- 多语言 SDK（Go, Java, TypeScript, Python, .NET）
- 自动重试、超时、心跳检测
- Saga 模式原生支持
- 工作流版本化与向后兼容

#### 适用场景

- 微服务编排
- 长时间运行的业务流程（订单处理、支付、审批流）
- 需要强一致性保证的关键任务
- 分布式事务（Saga 模式）

#### 局限性

- 需要部署和运维 Temporal Server 集群
- 学习曲线较陡（事件溯源、确定性约束）
- 不太适合纯数据管道/ETL 场景
- 资源开销相对较大

---

### 4.2 Dagu

- **GitHub**: [dagu-org/dagu](https://github.com/dagu-org/dagu) (~3.1k stars)
- **许可证**: GPL-3.0
- **最新版本**: v2.1.3

#### 架构设计

Dagu 追求极简主义：
- **单二进制部署**，内存占用 < 128MB
- **无外部依赖**：不需要数据库、消息队列
- **文件系统存储**：基于文件的状态管理
- **YAML 声明式**：工作流通过 YAML 文件定义

#### 核心特性

- 19+ 执行器类型：Shell, Docker, SSH, HTTP, JQ, Mail 等
- 分布式执行：基于标签的 Worker 路由、自动服务发现
- Cron 调度 + 时区支持
- 内建 JWT 认证与 RBAC（admin, manager, operator, viewer）
- 嵌套工作流与执行血缘追踪
- 可观测性：实时日志、甘特图、Prometheus 指标、OpenTelemetry
- 内建 LLM Agent：支持自然语言创建和调试工作流
- 支持离线（Air-gapped）环境

#### 适用场景

- 本地优先的轻量级任务调度
- CI/CD 管道编排
- 不想引入复杂基础设施的场景
- cron 的现代化替代方案

#### 局限性

- GPL-3.0 许可证可能限制商业使用
- 社区和生态相对较小
- 不适合超大规模分布式场景

---

### 4.3 goflow

- **GitHub**: [fieldryand/goflow](https://github.com/fieldryand/goflow) (~293 stars)
- **许可证**: MIT

#### 核心特性

- 简单的 DAG 调度器，附带 Web Dashboard
- Go 原生实现
- 轻量，易于嵌入

#### 适用场景

- 需要嵌入 Go 应用的简单 DAG 调度
- 学习 DAG 调度器原理

---

### 4.4 go-dag

- **GitHub**: [rhosocial/go-dag](https://github.com/rhosocial/go-dag)
- **许可证**: Apache-2.0

#### 核心特性

- 专注于 DAG 工作流执行管理
- 纯 Go 库，可嵌入业务系统
- 轻量级 API

---

### 4.5 ppacer

- **GitHub**: [ppacer/core](https://github.com/ppacer/core)
- **许可证**: Apache-2.0
- **官网**: [ppacer.org](https://ppacer.org)

#### 核心特性

- 高可靠性、高性能的 DAG 调度器
- 自包含二进制，极简部署
- 核心库仅依赖 SQLite 驱动
- 支持端到端测试
- 外部告警/通知集成

#### 局限性

- 处于 pre-MVP 阶段，尚未生产就绪

---

## 5. Java 生态

### 5.1 Apache DolphinScheduler

- **GitHub**: [apache/dolphinscheduler](https://github.com/apache/dolphinscheduler) (~14.1k stars)
- **创建者**: 易观（后捐赠给 Apache）
- **许可证**: Apache-2.0
- **贡献者**: 609+

#### 架构设计

DolphinScheduler 采用**分布式去中心化**架构：

```
┌──────────────┐    ┌──────────────┐
│ Master Server │    │ Master Server │  (可多实例)
│ ┌──────────┐ │    │ ┌──────────┐ │
│ │ Quartz   │ │    │ │ Quartz   │ │
│ │ Scheduler│ │    │ │ Scheduler│ │
│ └──────────┘ │    │ └──────────┘ │
└──────┬───────┘    └──────┬───────┘
       │                   │
       └────────┬──────────┘
                │
         ┌──────▼──────┐
         │  ZooKeeper   │ (集群管理/容错)
         └──────┬──────┘
                │
       ┌────────┼────────┐
┌──────▼──┐  ┌──▼──────┐  ┌──▼──────┐
│ Worker 1 │  │ Worker 2 │  │ Worker N │
└──────────┘  └─────────┘  └─────────┘
```

- **MasterServer**：DAG 拆分、任务提交与监控、健康检测
- **WorkerServer**：执行任务，提供日志服务
- **ZooKeeper**：集群管理与容错
- **AlertServer**：告警服务（插件化）
- **API Layer**：RESTful 接口 + Web UI

#### 核心特性

- 拖拽式 Web UI 创建工作流，也支持 Python / YAML / Open API
- 30+ 任务类型（Spark, Flink, Hive, Shell, Python, SQL 等）
- 跨项目、跨工作流的任务依赖
- 数据回填与工作流版本控制
- 多租户、高可用
- 超过 8,000 家企业生产使用

#### 适用场景

- 大数据平台的统一调度
- 多团队共享的工作流平台
- 需要拖拽式 UI 的非开发人员
- 国内大数据生态（对国产大数据组件支持好）

#### 局限性

- 依赖 ZooKeeper，部署复杂度较高
- 代码库较重，二次开发成本高
- 国际化文档和社区相对弱

---

### 5.2 Netflix Maestro

- **GitHub**: [Netflix/maestro](https://github.com/Netflix/maestro) (~3.7k stars)
- **创建者**: Netflix
- **许可证**: Apache-2.0
- **语言**: Java (100%)

#### 架构设计

Maestro 设计用于超大规模场景，近期从 Conductor 2.x 的无状态 Worker 模型迁移到**有状态 Actor 模型**（基于 Java 21 虚拟线程），实现了 100 倍性能提升：
- 步骤启动开销从 ~5 秒降低到 50 毫秒
- 基于 Generation ID 的所有权管理
- 内存中状态管理
- Flow Group 分区实现水平扩展

#### 核心特性

- **同时支持有环和无环工作流**（不局限于 DAG）
- 可复用模式：foreach 循环、子工作流、条件分支
- 多种执行格式：Docker 镜像、Notebook、Bash、SQL、Python
- 日均执行约 50 万个 Job，峰值 200 万
- 水平无限扩展

#### 适用场景

- 超大规模数据/ML 工作流
- Netflix 级别的吞吐量需求
- 需要循环工作流支持的场景

#### 局限性

- 开源版本功能可能不如 Netflix 内部版本完整
- 社区较小，文档相对不足
- 学习和部署门槛较高

---

### 5.3 Azkaban (LinkedIn)

- **GitHub**: [azkaban/azkaban](https://github.com/azkaban/azkaban) (~4.5k stars)
- **创建者**: LinkedIn
- **许可证**: Apache-2.0

#### 核心特性

- 基于项目的任务管理
- 简洁的 Web UI
- DAG 调度支持

#### 局限性（不推荐新项目使用）

- 无自动任务失败重试
- 权限控制粒度粗（仅项目级）
- 无版本管理和回滚
- 扩展性差，需修改源码
- 社区更新缓慢

---

### 5.4 Kestra

- **GitHub**: [kestra-io/kestra](https://github.com/kestra-io/kestra) (~13k stars)
- **创建者**: Kestra
- **许可证**: Apache-2.0
- **最新版本**: 1.3 (LTS)

#### 核心特性

- **YAML 声明式**工作流定义，Everything-as-Code
- 1200+ 插件，覆盖主流第三方服务
- 多语言脚本支持，无需代码重写
- 内建代码编辑器 + Git / Terraform 集成
- 毫秒级实时执行
- Schedule / Webhook / API / 事件驱动触发
- 自定义可观测性面板
- 超过 120,000 次部署，执行超 10 亿个工作流

#### 适用场景

- 数据管道、dbt 工作流
- 微服务编排
- 基础设施自动化
- 审批流
- 需要低代码体验的团队

#### 局限性

- 高级特性需要企业版
- 相对较新，生态仍在发展

---

### 5.5 Cadence (Uber)

- **GitHub**: [cadence-workflow/cadence](https://github.com/cadence-workflow/cadence) (~9.2k stars)
- **创建者**: Uber
- **许可证**: MIT
- **语言**: Go (但归类在 Java 生态因为它主要与 Java SDK 配合使用)

#### 核心特性

- Temporal 的前身，核心设计理念相同
- 分布式工作流编排引擎
- 事件溯源，容错执行
- Go 和 Java SDK

#### 现状

- Temporal 由同一团队的核心人员创建，是 Cadence 的精神继承者
- Temporal 在语言支持、性能优化、安全性、社区活跃度等方面全面超越 Cadence
- 新项目建议直接选择 Temporal

---

## 6. Kubernetes 原生

### 6.1 Argo Workflows

- **GitHub**: [argoproj/argo-workflows](https://github.com/argoproj/argo-workflows) (~14.5k stars)
- **创建者**: Argo Project (Intuit)
- **许可证**: Apache-2.0
- **状态**: CNCF 毕业项目

#### 架构设计

Argo Workflows 以 Kubernetes CRD（Custom Resource Definition）方式实现：
- 工作流定义为 K8s 资源（YAML/JSON）
- 每个任务步骤运行在独立的 Pod/Container 中
- 利用 K8s 原生能力进行调度、资源管理和故障恢复

#### 核心特性

- 容器原生：每个任务步骤都是一个容器
- DAG 和 Steps 两种工作流编排模式
- 支持 Artifacts 传递（S3, GCS, HDFS 等）
- Multiplex 日志查看器、嵌入式小部件
- Webhook 触发、SSO (OAuth2/OIDC)
- Prometheus 指标
- Java, Go, Python SDK
- 200+ 组织官方使用

#### 适用场景

- CI/CD 管道（Kubernetes 环境）
- ML 管道（与 Kubeflow Pipelines 集成）
- 数据和批处理
- 基础设施自动化

#### 局限性

- 强依赖 Kubernetes
- 每个任务启动一个 Pod，冷启动延迟较高
- 对于小规模/非 K8s 环境不适用
- 工作流定义较为冗长（YAML）

---

## 7. Rust 生态

### 7.1 dagrs

- **GitHub**: [dagrs-dev/dagrs](https://github.com/dagrs-dev/dagrs) (~391 stars)
- **许可证**: Apache-2.0 / MIT 双许可
- **官网**: dagrs.com

#### 核心特性

- Rust 实现的 DAG 执行引擎
- 基于 Tokio 异步运行时
- 类型安全的任务定义
- 社区相对活跃

---

### 7.2 dagx

- **许可证**: MIT
- **发布日期**: 2025-10

#### 核心特性

- 极致性能：比 dagrs 快 1.04-129 倍
- **编译期环检测**：利用 Rust 类型系统在编译时防止循环依赖
- 最小化设计：per-task 开销 ~100ns 构建，~790ns 内联执行
- 真正的并行执行

#### 适用场景

- 对性能要求极高的嵌入式 DAG 执行
- 需要编译期安全保证的场景

---

### 7.3 Floxide

- **Crates.io**: [floxide](https://crates.io/crates/floxide)
- **版本**: 3.2.2

#### 核心特性

- 专业的分布式并行工作流框架
- 跨多个 Worker 的分布式执行
- 类型安全的节点定义
- 可插拔后端（队列和检查点）
- 事件驱动

---

### 7.4 taskflow-rs

- **GitHub**: [lispking/taskflow-rs](https://github.com/lispking/taskflow-rs)

#### 核心特性

- 异步任务处理框架
- 多任务类型：HTTP、Shell 命令、自定义处理器
- 依赖管理
- 指数退避重试
- 实时监控
- 分布式 Worker 支持
- 可插拔存储后端

---

## 8. TypeScript 生态

### 8.1 Flowcraft

- **GitHub**: [gorango/flowcraft](https://github.com/gorango/flowcraft)
- **官网**: flowcraft.js.org
- **许可证**: MIT

#### 核心特性

- 零依赖，轻量级
- 声明式 DAG 工作流定义
- 重试、降级、超时、并行执行、批处理、循环、子流
- 静态分析工具（环检测、图生成）
- 从内存脚本扩展到分布式系统

---

### 8.2 dagflowjs

- **GitHub**: [abdullah2993/dagflowjs](https://github.com/abdullah2993/dagflowjs)
- **许可证**: MIT

#### 核心特性

- 类型安全的 DAG 执行引擎
- 依赖管理与并行执行
- 指数退避重试
- 超时控制
- 全面的指标收集
- 校验钩子
- 错误策略

---

### 8.3 OpenWorkflow

- **GitHub**: [openworkflowdev/openworkflow](https://github.com/openworkflowdev/openworkflow)
- **官网**: openworkflow.dev
- **运行时**: Node.js / Bun

#### 核心特性

- 持久化、可恢复的工作流（可在崩溃/重启后恢复）
- 使用现有数据库（PostgreSQL / SQLite），无需独立编排服务
- 类型安全的输入/输出
- 支持可暂停的工作流

---

## 9. C++ 生态

### 9.1 Taskflow

- **GitHub**: [taskflow/taskflow](https://github.com/taskflow/taskflow) (~10k+ stars)
- **许可证**: MIT
- **官网**: taskflow.github.io

#### 架构设计

Taskflow 是一个**通用任务并行编程系统**，基于现代 C++17：

```cpp
#include <taskflow/taskflow.hpp>

tf::Taskflow taskflow;
auto [A, B, C, D] = taskflow.emplace(
  []() { std::cout << "Task A\n"; },
  []() { std::cout << "Task B\n"; },
  []() { std::cout << "Task C\n"; },
  []() { std::cout << "Task D\n"; }
);
A.precede(B, C);  // A 在 B 和 C 之前
D.succeed(B, C);  // D 在 B 和 C 之后

tf::Executor executor;
executor.run(taskflow).wait();
```

#### 核心特性

- Header-only 库，零外部依赖
- 同时支持 CPU 和 GPU（CUDA）工作负载
- 可扩展到百万级任务
- 支持条件任务、循环、组合、动态任务图
- 跨平台：Linux, Windows, macOS
- 已应用于 CAD 软件、量子电路模拟、机器学习

#### 适用场景

- 高性能计算（HPC）
- 科学计算与仿真
- CAD/EDA 工具
- 需要 CPU/GPU 混合并行的场景
- 嵌入式系统中的任务编排

#### 局限性

- C++ 专用，无跨语言支持
- 面向单机任务并行，非分布式编排
- 不提供持久化、调度、Web UI 等编排平台功能

---

## 10. 框架选型指南

### 按场景推荐

| 场景 | 推荐框架 | 理由 |
|------|----------|------|
| **企业级数据管道 (ETL)** | Airflow / DolphinScheduler | 成熟生态，广泛集成 |
| **数据资产管理与血缘** | Dagster | Asset-first 设计 |
| **ML/AI 工作流** | Flyte / Argo Workflows | K8s 原生，GPU 支持，可复现 |
| **微服务编排** | Temporal | 持久化执行，Saga 模式 |
| **轻量本地调度 (cron 替代)** | Dagu | 单二进制，零依赖 |
| **超大规模工作流** | Netflix Maestro | 经 Netflix 验证的超大规模 |
| **声明式低代码编排** | Kestra | YAML 定义，1200+ 插件 |
| **Kubernetes 环境 CI/CD** | Argo Workflows | CNCF 毕业项目，K8s 原生 |
| **高性能计算 (C++)** | Taskflow | CPU/GPU 并行，百万任务级 |
| **嵌入式 Go 应用 DAG** | goflow / go-dag | 轻量库，易嵌入 |
| **嵌入式 Rust DAG** | dagx / dagrs | 编译期安全，极致性能 |
| **前端/Node.js 工作流** | Flowcraft / OpenWorkflow | TS 原生，零/少依赖 |

### 按团队技术栈推荐

| 技术栈 | 首选 | 备选 |
|--------|------|------|
| **Python 团队** | Airflow → Dagster → Prefect | Luigi (遗留项目) |
| **Go 团队** | Temporal → Dagu | goflow, go-dag |
| **Java 团队** | DolphinScheduler → Kestra | Maestro, Cadence |
| **K8s 基础设施** | Argo Workflows → Flyte | Airflow on K8s |
| **Rust 团队** | dagrs → dagx | Floxide, taskflow-rs |
| **TypeScript 团队** | Flowcraft → OpenWorkflow | dagflowjs |
| **C++ 团队** | Taskflow | — |

---

## 11. 总结与趋势

### 当前市场格局

1. **Python 生态最为成熟**：Airflow 仍然是行业标准，但 Dagster 和 Prefect 正在快速追赶，分别在资产管理和开发者体验上做出差异化。
2. **Go 生态呈现两极分化**：Temporal 占据应用级框架的高端市场（微服务编排），Dagu 则提供了极简的本地优先方案。
3. **Java 生态聚焦大数据**：DolphinScheduler 和 Kestra 分别满足大数据调度和声明式编排的需求。
4. **Rust/TS/C++ 多为轻量级库**，面向嵌入式或特定领域场景。

### 发展趋势

1. **资产优先（Asset-First）**：从 "执行什么任务" 转向 "管理什么数据资产"（Dagster, Airflow 3.0）
2. **事件驱动**：从纯定时调度转向事件触发（Prefect, Kestra, Airflow 3.0）
3. **声明式定义**：YAML/声明式工作流定义越来越流行（Kestra, Dagu, Argo）
4. **AI/LLM 集成**：新一代框架开始集成 AI 能力（Dagu 内建 LLM Agent, Kestra 支持 AI 管道）
5. **轻量化与去中心化**：减少外部依赖，单二进制部署趋势明显（Dagu, ppacer）
6. **多语言 SDK**：核心引擎用 Go/Java 实现，但提供多语言客户端 SDK（Temporal, Flyte, Argo）
7. **Kubernetes 原生**：与 K8s 深度集成成为标配而非选项
