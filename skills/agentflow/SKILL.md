---
name: agentflow
version: 1.0.0
description: "使用 AgentFlow 与用户共同设计、校验、执行和观测 JSON workflow；当用户想把多个 Claude Code、Codex、shell 或人工审批步骤编排成可恢复流程时使用。"
---

# AgentFlow Workflow Orchestration

你负责把用户的目标落成一个可验证、可恢复、可观测的 AgentFlow workflow。先和用户确认流程边界，再生成或修改 workflow JSON；不要猜测 provider、workspace、session 或审批规则。

## 1. 先向用户确认

至少确认这些信息：

1. **目标**：流程最终要产出什么，成功标准是什么。
2. **输入**：运行时需要哪些值，哪些是必填；敏感值不要写入 workflow 文件。
3. **节点**：哪些步骤由 shell 执行，哪些步骤由 agent 执行，哪些步骤必须人工审批。
4. **依赖**：节点之间的先后关系；没有依赖的节点可以并行执行，但当前 MVP 的 Runner 会按定义顺序推进可执行节点。
5. **工作目录**：每个 agent 使用哪个绝对或相对 workspace。
6. **会话**：是启动新 session，还是继续已有 session；已有 session 的 ID 从哪里来。
7. **失败策略**：超时、重试次数、失败后是否跳过下游节点。
8. **输出**：每个节点要写入哪些结构化 JSON 字段，哪些文件需要作为 artifact 保留。

如果用户还没有提供 session ID，应明确让该值成为 workflow input，或先使用 provider 的新 session command；不要把“当前目录最近的会话”当成确定绑定。

## 2. 先查 CLI 契约

在编排前查询当前版本的命令和 workflow schema：

```bash
agentflow --help
agentflow run --help
agentflow schema --all --format json
agentflow schema workflow --format json
```

优先使用长命令和 JSON 输出。通过管道调用时 CLI 默认返回 JSON；stdout 是数据，stderr 是诊断。

## 3. Workflow 最小结构

```json
{
  "version": "1",
  "name": "release-review",
  "inputs": {
    "workspace": { "type": "path", "required": true },
    "session_id": { "type": "string", "required": false }
  },
  "nodes": [
    {
      "id": "inspect",
      "type": "agent",
      "driver": "claude-code",
      "workspace": "{{ inputs.workspace }}",
      "session": "{{ inputs.session_id }}",
      "prompt": "检查当前变更，并把结论写入 AGENTFLOW_RESULT_FILE。"
    },
    {
      "id": "approve",
      "type": "human",
      "needs": ["inspect"],
      "prompt": "请审批检查结论。"
    }
  ]
}
```

### 节点约定

- `shell`：使用 `command`，适合确定性的脚本和校验。
- `agent`：使用 `driver`、`prompt`、可选的 `workspace` 和 `session`。
- `human`：使用 `prompt`，运行后状态为 `waiting_human`，必须通过 CLI 决策才能继续。
- `needs`：声明依赖节点 ID。
- `when`：根据 `inputs` 或上游 `result` 做条件跳过。
- `retry.maxAttempts`：包含首次执行在内的最大尝试次数。
- `timeoutMs`：单次命令超时。

## 4. Provider、workspace 和 session

Provider 配置在 `.agentflow/providers.json` 或 `~/.agentflow/providers.json`，不放进 workflow：

```json
{
  "drivers": {
    "claude-code": {
      "command": ["claude", "--print", "{{ prompt }}"],
      "resumeCommand": ["claude", "--resume", "{{ session_id }}", "--print", "{{ prompt }}"]
    },
    "codex": {
      "command": ["codex", "exec", "{{ prompt }}"],
      "resumeCommand": ["codex", "exec", "resume", "{{ session_id }}", "{{ prompt }}"]
    }
  }
}
```

规则：

- 没有 `session` 时选择 `command`，表示启动新 session。
- 有 `session` 时选择 `resumeCommand`，表示向指定 session 发送下一轮任务。
- 有 session 但没有 `resumeCommand` 时，节点应失败并修复 provider 配置；不要静默启动新 session。
- Runner 使用节点的 workspace 作为进程工作目录，并设置 `AGENTFLOW_WORKSPACE`、`AGENTFLOW_SESSION_ID`。
- provider command 可以使用 `{{ prompt }}`、`{{ workspace }}`、`{{ session_id }}`、`{{ result_file }}`、`{{ run_id }}` 和 `{{ node_id }}`。
- Agent 也可以在结构化结果中返回 `session_id`，后续节点用 `{{ nodes.<id>.result.session_id }}` 继续该会话。

## 5. 结构化结果契约

Agent 成功退出前必须向 `AGENTFLOW_RESULT_FILE` 写入一个 JSON object：

```json
{
  "decision": "pass",
  "summary": "测试通过",
  "session_id": "optional-session-id"
}
```

不要把 stdout 或 stderr 当作节点输出。Runner 会把它们保存成日志 artifact，并只把结果文件解析为 `node.result`。下游节点通过 `{{ nodes.inspect.result.summary }}` 读取结果。

## 6. 校验和执行流程

### 6.1 校验

```bash
agentflow workflow validate workflow.json --format json
```

校验失败时，根据 JSON 错误里的 `code`、`message`、`suggestion` 修复后再试。不要跳过校验直接执行。

### 6.2 预演

```bash
agentflow run start workflow.json \
  --input workspace=/absolute/repo \
  --input session_id=<session-id> \
  --dry-run \
  --format json
```

退出码 `10` 表示 dry-run 成功；它不会创建或执行 run。检查预演中的节点顺序、workspace、session 和输入。

### 6.3 正式执行

为可重试的启动使用稳定的幂等 key：

```bash
agentflow run start workflow.json \
  --input workspace=/absolute/repo \
  --input session_id=<session-id> \
  --idempotency-key <stable-request-key> \
  --format json
```

同一 workflow、输入和幂等 key 会复用已有 run；同 key 对应不同请求时退出码为 `5`。

### 6.4 人工审批和恢复

当 run 返回 `waiting_human`：

```bash
agentflow run get <run-id> --format json
agentflow run approve <run-id> <node-id> --message "approved" --format json
# 或
agentflow run reject <run-id> <node-id> --message "需要修改" --format json
```

审批命令由 Runner 推进下游节点。不要让 agent 直接修改 `.agentflow/runs/<run-id>/state.json`，也不要让 agent 自己启动下一个节点。

如果进程被中断，先查看状态，再恢复：

```bash
agentflow run resume <run-id> --dry-run --format json
agentflow run resume <run-id> --format json
```

## 7. 观测单次执行和历史效率

### 单次 run

```bash
agentflow run metrics <run-id> --format json
```

返回：

- run 总耗时和状态；
- 每个节点的 `duration_ms`、`waiting_ms`、`attempts`；
- `input_bytes`：发送给节点的渲染后 prompt 字节数；
- `output_bytes`：结构化 result 文件字节数；
- `stdout_bytes` / `stderr_bytes`：日志字节数；
- agent 的 workspace 和 session ID。

事件时间线和原始日志：

```bash
agentflow run logs <run-id> --format ndjson
agentflow run get <run-id> --format json
```

运行数据默认保存在：

```text
.agentflow/runs/<run-id>/
├── workflow.json
├── state.json
├── events.jsonl
└── artifacts/
    ├── <node>.result.json
    └── logs/<node>.stdout.log
    └── logs/<node>.stderr.log
```

### 历史聚合

```bash
agentflow run stats --format json
agentflow run stats release-review --format json
```

聚合结果包括成功/失败/等待审批/取消数量，run 和节点耗时的平均值、P50、P95、最大值，以及输入、输出和日志字节总量。用它回答“哪个节点最慢”“审批等待占多少时间”“输出是否异常膨胀”“失败是否集中在某个节点”。

## 8. Web 可视化

需要和用户一起检查流程结构时，打开 workflow 图：

```bash
agentflow web workflow.json
```

需要检查一次实际执行时，打开 run 图：

```bash
agentflow web --run <run-id>
```

Web 页面会显示节点依赖、节点类型、状态、耗时、attempts 和输出大小；run 页面会每两秒刷新一次状态。CLI 会打印本地 URL。可以用 `--port 0` 选择可用端口，用 `--host 0.0.0.0` 暴露到局域网时必须确认网络访问范围。

## 9. 效率优化闭环

1. 先看 `run stats <workflow-name>`，定位 P95 高或失败率高的节点。
2. 再看对应 run 的 `run metrics`，区分 agent 执行时间、人工等待时间和重试时间。
3. 用 `run logs --format ndjson` 对照 `node.started`、`node.retrying`、`node.failed` 事件。
4. 根据字节数判断 prompt/result/log 是否过大；不要用 stdout 文本猜结构化输出。
5. 调整 workflow：拆分过大的 prompt、增加明确的 result schema、设置合理 timeout/retry、减少不必要的人审节点。
6. 重新 `workflow validate`、`run start --dry-run`，再用新的幂等 key 执行。
7. 保留前后两次 `run stats`，比较 P95、失败数、attempts 和字节量是否改善。

## 10. 与用户协作的交付格式

在提交 workflow 前，向用户说明：

- workflow 的目标、输入和最终输出；
- 每个节点做什么以及依赖关系；
- 哪些节点会调用真实 agent、使用哪个 workspace/session；
- 哪些节点需要人工审批；
- 失败、重试、超时和取消如何处理；
- 如何执行、如何审批、如何查看 metrics/stats；
- 哪些 provider 命令需要用户根据本机 CLI 版本调整。

执行后报告 run ID、最终状态、失败节点、耗时、attempts、输入/输出字节数，以及需要用户处理的审批节点。
