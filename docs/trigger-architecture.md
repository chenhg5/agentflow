# AgentFlow Trigger Architecture

本文档定义外部触发能力的领域边界和第一版实现约束。当前 AgentFlow 只有手动 `run start`；cron、webhook 和其他事件源属于后续能力，不能把它们直接实现成 engine 中的特殊分支。

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

- `DeliveryID` 是 source 提供的稳定事件 ID，用于幂等。
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
    Serve(context.Context, EventSink) error
}
```

- `CronSource` 在内部维护 timer，按计划产生 `TriggerEvent`。
- `WebhookSource` 提供 HTTP listener，鉴权、限流、读取 body 后产生 `TriggerEvent`。
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
- 必须显式指定 `timezone`；缺省使用项目配置的 timezone，不使用运行机器的隐式本地时区。
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
.agentflow/
├── triggers.json
├── trigger-deliveries/
│   └── <delivery-id>.json
├── dispatch/
│   └── <run-id>.json
└── runs/
    └── <run-id>/
```

`trigger-deliveries` 是幂等记录，`dispatch` 是可靠入队记录，`runs` 仍然是执行事实来源。

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

## 8. Implementation order

1. 提取 `RunStarter`，让现有 `run start` 先实现这个 port。
2. 增加 `TriggerDefinition`、`TriggerEvent`、delivery receipt 和 input binding 的纯 domain tests。
3. 实现 `agentflow trigger fire`，用手动事件验证幂等、并发和输入映射。
4. 实现 `CronSource` 和 `trigger serve`。
5. 实现带 HMAC、body limit、202/重复 delivery 语义的 `WebhookSource`。
6. 增加 queued run worker、恢复未完成 dispatch、优雅停机和 Web UI 观测。

每一步都通过 port 测试，不使用真实 Claude Code/Codex 作为 trigger 单元测试；真实 provider smoke test 继续单独保留。
