# AgentFlow Trigger Architecture

本文档定义外部触发能力的领域边界和第一版实现约束。当前 MVP 已提供 `trigger fire`、`trigger serve`、cron、webhook、幂等 delivery、持久化 dispatch 和 queued worker；新的事件源仍然不能直接实现成 engine 中的特殊分支。

`trigger serve` 的范围是当前独立 workflow 工作区，不是机器上的全局服务。一个 `triggers.json` 可以指向多个图文件；图路径显式配置，`workflows/` 是可选分类，单条图可直接使用根目录 `workflow.json`。

## 1. Bounded contexts

AgentFlow 分成四个相互独立的上下文：

```text
Workflow Definition  ->  Run Execution  <-  Triggering
                              |
                       Observability
```

- **Workflow Definition**：读取、校验 workflow JSON，提供 workflow 引用。
- **Run Execution**：创建 run、推进节点、持久化状态、处理重试和人工审批。
- **Triggering**：接收外部事件，将事件映射成一次 `StartRunCommand`。
- **Observability**：记录 run、trigger delivery、节点耗时和事件日志。

Triggering 只能调用 Run Execution 暴露的 application port。cron/webhook adapter 不能直接创建 run、修改 `state.json` 或调用 engine。

## 2. Domain model

### TriggerDefinition

`TriggerDefinition` 是触发器聚合根，描述一个外部入口如何启动一个 workflow：

```json
{
  "id": "nightly-review",
  "enabled": true,
  "source": {
    "kind": "cron",
    "expression": "0 2 * * *",
    "timezone": "Asia/Shanghai"
  },
  "target": {
    "workflow": "workflows/review.json"
  },
  "inputs": {
    "workspace": "/Users/me/code/repo",
    "mode": "nightly"
  },
  "policy": {
    "concurrency": "forbid",
    "misfire": "skip"
  }
}
```

触发器定义只描述业务规则，不保存运行中的 HTTP server、timer 或 goroutine。

### TriggerEvent

所有 source 都转换成统一的领域事件：

```go
type TriggerEvent struct {
    TriggerID    string
    SourceKind   string
    DeliveryID   string
    ReceivedAt   time.Time
    ScheduledAt  *time.Time
    Payload      json.RawMessage
    Headers      map[string]string
    Attributes   map[string]string
}
```

- `DeliveryID` 是 source 提供的稳定事件 ID，用于幂等。持久化 receipt 和运行 idempotency key 由 `(trigger ID, delivery ID)` 共同计算，放在当前工作区；不同 trigger 收到相同 delivery ID 仍各自投递。
- cron 使用 `triggerID + scheduledAt` 生成稳定的 `DeliveryID`。
- webhook 优先使用配置的 header，例如 `X-GitHub-Delivery`；没有时由 adapter 生成 ID。
- `Payload` 是原始事件数据，不直接拼接进 shell 命令。
- `Headers` 只在内存中用于鉴权和绑定；敏感 header 不写入 run state。
- `ScheduledAt` 表示计划触发时间，`ReceivedAt` 表示 AgentFlow 实际收到事件的时间。

### StartRunCommand

Triggering 最终只产生一个 application command：

```go
type StartRunCommand struct {
    WorkflowPath   string
    Inputs         map[string]string
    TriggerID      string
    TriggerKind    string
    DeliveryID     string
    ScheduledAt    *time.Time
    ReceivedAt     time.Time
    IdempotencyKey string
}
```

run 的业务输入和 trigger metadata 分开保存。workflow 只消费 `Inputs`；run state 额外记录 trigger 信息，便于追踪一条 run 是由哪个定时任务或 webhook 产生的。

第一版沿用当前 `map[string]string` 输入模型。Webhook 的 JSONPath binding 只允许映射到标量字段；完整 payload 作为受控 artifact 保存或按配置丢弃。后续需要传递对象时，再把 run input 升级为 JSON value，而不是让 trigger adapter 自己定义格式。

## 3. Ports

### Source port

不同外部来源都实现同一个 source 生命周期：

```go
type EventSink func(context.Context, TriggerEvent) error

type Source interface {
    ID() string
    Kind() string
}

type ServingSource interface {
    Source
    Serve(context.Context, EventSink) error
}
```

- `CronSource` 在内部维护 timer，按计划产生 `TriggerEvent`。
- `WebhookSource` 提供 HTTP handler，鉴权、body limit、读取 body 后产生 `TriggerEvent`；所有 webhook 共用一个 listener。
- 未来可以增加 `GitHubSource`、`QueueSource`、`FileSource`，它们不需要修改 engine。

Source 只负责“发现事件”。它不负责 workflow 查找、输入映射、幂等判断和启动 run。

### Trigger application port

```go
type Dispatcher interface {
    Dispatch(context.Context, TriggerEvent) (DispatchResult, error)
}
```

Dispatcher 的职责顺序固定为：

1. 加载 `TriggerDefinition`。
2. 检查 trigger 是否 enabled。
3. 校验 event 的 source kind 和 trigger ID。
4. 应用 input bindings。
5. 生成 deterministic idempotency key。
6. 执行并发策略。
7. 持久化 delivery receipt。
8. 调用 `RunStarter` 创建 queued run。

### Run execution port

```go
type RunStarter interface {
    Start(context.Context, StartRunCommand) (RunRef, error)
}
```

Dispatcher 依赖 `RunStarter`，不依赖 `engine.Execute`。这样 HTTP source 可以在 run 已经可靠入队后返回 202，长时间的 agent 执行由 run worker 处理。

## 4. Cron semantics

第一版 cron 规则：

- 使用标准五字段 cron：`minute hour day-of-month month day-of-week`。
- 建议显式指定 IANA `timezone`；当前省略时使用 UTC，不使用运行机器的隐式本地时区。
- 每次 occurrence 产生一个 event，即使上一次 run 仍在执行。
- 默认 `misfire: skip`：进程停止期间错过的 occurrence 不补跑。
- `misfire: fire-once` 可以作为后续策略，启动时最多补一个未处理 occurrence。
- 默认 `concurrency: forbid`：同一 trigger 有 active run 时，新 occurrence 记录为 skipped。
- 后续可以增加 `queue` 和 `replace`，但必须先定义队列上限和取消语义。

cron event 的输入：

```text
trigger.id
trigger.kind = cron
trigger.delivery_id
trigger.scheduled_at
trigger.received_at
```

workflow 的业务输入来自 `TriggerDefinition.inputs`。不把当前时间隐式塞进 workflow input，避免测试和重放时结果不稳定；需要时间的 workflow 显式绑定 `scheduled_at`。

## 5. Webhook semantics

Webhook endpoint 建议使用 trigger ID 作为路由：

```text
POST /triggers/{trigger-id}
```

定义示例：

```json
{
  "id": "github-pull-request",
  "enabled": true,
  "source": {
    "kind": "webhook",
    "path": "/triggers/github-pull-request",
    "method": "POST",
    "auth": {
      "kind": "hmac-sha256",
      "secretEnv": "AGENTFLOW_GITHUB_SECRET"
    },
    "deliveryIDHeader": "X-GitHub-Delivery",
    "maxBodyBytes": 1048576
  },
  "target": {
    "workflow": "workflows/pr-review.json"
  },
  "bindings": {
    "repo": { "from": "json", "path": "$.repository.full_name" },
    "action": { "from": "json", "path": "$.action" },
    "pr_number": { "from": "json", "path": "$.number" }
  },
  "policy": {
    "concurrency": "forbid"
  }
}
```

Webhook response rules：

- `401`：签名或 bearer token 无效。
- `413`：body 超过 `maxBodyBytes`。
- `404`：trigger 不存在或已禁用。
- `422`：payload 无法通过 binding 转换成 workflow 输入。
- `202`：delivery 已持久化并成功创建 queued run。
- 重复 `DeliveryID` 返回原 run 引用，不重复执行。
- `5xx` 只用于 delivery 未持久化、run 未入队等可重试失败。

Webhook adapter 在完成鉴权和 body 限制后才产生 `TriggerEvent`。它不把 secret 写入 event、run state 或普通日志。

## 6. Persistence and process model

建议的数据布局：

```text
workflow/
├── triggers.json
├── trigger-deliveries/
│   └── <delivery-id>.json
├── dispatch/
│   └── <run-id>.json
└── runs/
    └── <run-id>/
```

`trigger-deliveries` 是幂等记录，`dispatch` 是可靠入队记录，`runs` 仍然是执行事实来源。

同一工作区由 `scheduler.lock` 的操作系统文件锁限制为一个长期服务进程；进程退出后锁自动释放。不同工作区可以各自运行服务。纯 cron 工作区不创建 HTTP listener；有启用的 webhook 时才监听 HTTP，所有 webhook 共享该工作区的一个 listener。默认端口 `0` 自动选择并输出实际地址，也可显式指定不同端口。端口占用会在启动 timer 和恢复 queued jobs 前报错，不会由启动失败的服务执行任务。启用的 webhook path（含尾部 `/` 归一化）不得重复。

同一工作区的 CLI `trigger fire` 与服务投递使用 `trigger-dispatch.lock` 做跨进程事务串行化，保护 receipt 去重、重叠策略检查和 run 创建。默认 `concurrency=forbid` 按 trigger ID 检查 pending/queued/running/waiting_input；`allow` 允许同一 trigger 重叠入队。该策略没有跨 trigger 或跨工作区的仓库/session 资源互斥语义。

当前 CLI 启动一个 run worker，因此同一服务的不同 triggered run 顺序执行；单个 run 内的 `dispatch: all` 节点并行仍独立生效。外部等待节点退出本次执行后释放 worker，后续 run 可以继续。跨 run 增加 worker 数和全局多工作区 supervisor 是独立能力，当前没有自动全局发现服务。

第一版长期运行命令：

```bash
agentflow trigger serve
```

它负责加载 trigger definitions、启动所有 source、接收 event、持久化 delivery、投递 run。CLI 的 `run start` 仍然走同一个 `RunStarter`，但使用同步执行模式；trigger 使用 queued/worker 模式。

如果用户希望使用系统级 cron，而不是让 AgentFlow 常驻，可以提供：

```bash
agentflow trigger fire <trigger-id> --scheduled-at 2026-10-13T02:00:00+08:00
```

这个命令复用同一个 Dispatcher，不绕过幂等和并发策略。系统 cron、launchd、Kubernetes CronJob 都只负责唤醒它。

## 7. Package boundaries

建议的 Go 包结构：

```text
internal/trigger/
├── domain.go       # TriggerDefinition, TriggerEvent, policies
├── ports.go        # Source, Dispatcher, RunStarter, repositories
├── application.go  # Dispatch use case and input binding
├── cron.go         # CronSource adapter
├── webhook.go      # WebhookSource adapter
└── store.go        # trigger definition and delivery persistence
```

依赖方向：

```text
cli / http / cron adapters
            ↓
   trigger application
            ↓
 trigger domain + ports
            ↓
  workflow/run application
```

`internal/engine` 不导入 `net/http`、cron library 或 webhook package。`internal/trigger` 也不直接调用 `store.SaveState`，所有状态变化经过 application service。

## 8. MVP implementation status

1. `RunStarter`、`Dispatcher`、delivery receipt 和 input binding 已在 `internal/trigger` 中实现。
2. `agentflow trigger fire` 复用同一个 dispatcher，支持稳定 delivery ID 和并发策略。
3. `agentflow trigger serve` 启动五字段 cron、按 timezone 调度、webhook HTTP listener 和 queued worker。
4. webhook 支持 body limit、HMAC SHA-256、bearer token、标量 JSONPath binding 和重复 delivery 去重。
5. `dispatch/<run-id>.json` 在服务重启时恢复 queued job；run 和 trigger metadata 继续写入现有观测接口。

后续可以增加 `queue`/`replace` 并发语义、misfire 补偿策略、外部队列 source 和 UI 中的 trigger delivery 视图，但这些扩展不需要修改 engine。

Trigger 的单元测试使用 fake `RunStarter` 和本地 shell workflow，不调用真实 Claude Code/Codex；真实 provider smoke test 继续单独保留。
