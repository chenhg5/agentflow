---
name: agentflow
metadata:
  version: "3.0.0"
description: "使用 AgentFlow 设计、执行和检查由 JSON 数据交接与显式条件边组成的本地工作流。"
---

# AgentFlow workflow

先查 `agentflow schema --all --format json` 和 `agentflow schema workflow --format json`。自定义程序查 `schema executors`，定时或 webhook 查 `schema trigger`。根据用户要求确定入口数据、每个节点的输出契约、边条件和完成标准，再创建工作流。

## 图结构与数据流

只使用 version `3`。`entry` 是起点，`edges` 是唯一控制流，节点数组顺序没有执行含义。入口节点得到 run input；之后每条被选中的边将源节点的完整 JSON 输出作为目标节点的输入。节点没有 `input` 字段，也不要把业务指令伪装成上游数据。

```json
{
  "version": "3", "name": "example", "entry": "draft",
  "inputs": {"topic": {"type": "string", "required": true}},
  "nodes": [
    {"id": "draft", "executor": "codex", "config": {"instruction": "根据收到的主题起草方案"}, "outputSchema": {"type": "object"}},
    {"id": "review", "executor": "external", "outputSchema": {"type": "object"}}
  ],
  "edges": [
    {"from": "draft", "to": "review"},
    {"from": "review", "to": "draft", "when": "nodes.review.result.score < 5", "maxIterations": 2},
    {"from": "review", "to": "$end"}
  ]
}
```

节点只定义 `id`、`executor`、可选 `config`、`inputSchema`、`outputSchema`、`execution.session`、`timeoutMs` 和 `retry.maxAttempts`。`config` 是执行器的不透明配置；图引擎不解释其业务键。`inputSchema` 校验到达节点的运行数据，`outputSchema` 校验结果。结果必须是 JSON 对象。业务字段可任意命名，`happy`、`reject`、`decision`、`score` 等没有引擎特权。

节点完成后按顺序评估出边，选第一条条件为真的边。无 `when` 的默认边必须在最后。条件用 Expr，支持比较、算术、逻辑、集合和正则等。循环必须有 `maxIterations`。每次重新进入节点，输入来自刚穿越的边；历史访问另存于 run history。

`config` 可引用当前节点输入 `{{ input.field }}`、运行输入 `{{ inputs.topic }}` 或历史节点结果 `{{ nodes.draft.result.summary }}`；执行器注册中的 command、workspace 和 env 可引用 `{{ input.field }}` 与 `{{ config.field }}`。边的 `when` 写 `nodes.draft.result.score > 80`，不加模板括号。精确模板保留 JSON 类型，拼接字符串时转换为字符串。

## Session 策略

Session 是引擎的执行策略，不属于节点 input。每个节点可写：

- `{"execution":{"session":{"mode":"new"}}}`：每次访问新起空白 session；省略时也是此模式。
- `{"execution":{"session":{"mode":"fixed"}}}`：本次 run 第一次访问节点时创建 session，重访时继续使用同一个。可加 `"id":"existing-session-id"` 指定已有 session；`id` 可引用已声明的运行输入。
- `{"execution":{"session":{"mode":"inherit"}}}`：沿用进入本节点的上一节点 session；可用 `from` 明确指定来源节点。
- `{"execution":{"session":{"mode":"fork"}}}`：从上游 session 派生新 session；可用 `from` 指定来源节点。

`inherit`/`fork` 缺少来源 session 会失败，不能猜测。`fork` 必须报告新的 ID，避免后续错误地复用父 session。Codex 和 Claude Code 适配器从 CLI JSON 日志提取 session ID。自定义执行器在 `AGENTFLOW_SESSION_FILE` 写入新 ID；固定/沿用模式下引擎已经知道所选 ID。自定义执行器如需 session 操作，在注册项中提供 `command`、`resumeCommand` 和 `forkCommand`，按需使用 `{{session_id}}`。

## 注册与执行程序

`.agentflow/executors.json` 把任意名字关联到程序入口，语言和通信方式不限：

```json
{"executors":{"my-interaction":{"command":["python3","./executors/interaction.py"],"workspace":".","env":{"APP_PROFILE":"local"}}}}
```

节点写 `"executor":"my-interaction"`。程序从 `AGENTFLOW_INPUT_FILE` 读取上游 JSON，从 `AGENTFLOW_CONFIG_FILE` 读取节点配置，完成工作后把 JSON 写到 `AGENTFLOW_RESULT_FILE` 并正常退出。stdout/stderr 是日志。执行器无需也不应修改 run state；引擎检查结果并自动推进边。飞书、钉钉、电话、终端等由执行器处理，图不认识这些产品。

内置执行器有 `codex`、`claude-code`、`process`、`external`。前两者读取 `config.instruction`、可选 `config.workspace`；`process` 读取 `config.command`；`external` 等待 `run submit`。缺少配置的程序入口须通过 `.agentflow/executors.json` 注册。

## 验证与运行

```bash
agentflow workflow validate workflow.json --check-executors --format json
agentflow run start workflow.json --input topic=value --format json
agentflow run submit <run-id> <node-id> --output '{"score":8}' --format json
agentflow run get <run-id> --format json
agentflow run logs <run-id> --format ndjson
agentflow run retry <run-id> --format json
agentflow run resume <run-id> --format json
```

`submit` 用于 external 节点。定时/webhook 配置放 `.agentflow/triggers.json`，先 validate 和 fire，再持续运行 `trigger serve`。交付前用本地替身检验分支、返工、次数上限和无效输出。`run start --dry-run` 的成功退出码为 10；schema 通过或 dry run 通过不等于实跑完成。需要观察耗时用 `run metrics/stats`，业务质量数值由外部评估器写入 `run evaluate`，再用 `run compare` 对比。
