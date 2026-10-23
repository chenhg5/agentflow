---
name: agentflow
metadata:
  version: "2.1.0"
description: "使用 AgentFlow 设计、执行和检查由 JSON 输入输出与显式条件边组成的本地工作流。"
---

# AgentFlow workflow

先查 `agentflow schema --all --format json` 和 `agentflow schema workflow --format json`。需要自定义程序时查 `schema executors`，需要定时/webhook 时查 `schema trigger`；这些 schema 随二进制提供，在空目录也可读取。根据用户已有信息确定输入、输出和完成标准，在已有授权内推进。

## 图结构

只使用 version `2`，通过 `entry`、`nodes`、`edges` 定义图。节点字段为 `id`、`executor`、`input`、`inputSchema`、`outputSchema`、`timeoutMs`、`retry.maxAttempts`。节点数组顺序不决定执行顺序。

```json
{
  "version": "2", "name": "example", "entry": "step",
  "nodes": [{"id": "step", "executor": "codex", "input": {"prompt": "完成用户指定任务并输出 JSON。"}}],
  "edges": [{"from": "step", "to": "$end"}]
}
```

工作流运行输入声明在顶层 `inputs`；顶层没有 `inputSchema`。节点的输入输出约束声明在各节点 `inputSchema` / `outputSchema`。需要结构化交接时，用 schema 明确字段和 JSON 类型。

三个位置的引用上下文不同：

| 位置 | 写法 | 用途 |
| --- | --- | --- |
| 节点 input 内 | `{{ inputs.sample }}` | 运行时 `--input sample=...` |
| 节点 input 内 | `{{ nodes.inspect.result.score }}` | 指定节点的 JSON 输出 |
| executor 配置 command/env 内 | `{{ input.sample }}` | 当前节点渲染后的输入 |
| 边 when | `nodes.inspect.result.score > 80` | Expr 表达式，不加模板大括号 |

精确模板会保留数字、数组、对象和布尔类型；把它拼进句子后会变成字符串。循环第一次进入时，后面节点还没有结果：精确引用会得到 null，要使用允许 null 的 schema，或将可选反馈嵌入带前缀的提示文字；不支持在模板里写 JS/Expr 的 `??` 等运算。

节点完成后按定义顺序计算出边，选择第一条为真的边。没有 `when` 的边是默认边，必须放最后。条件使用 Expr，支持大小比较、算术、逻辑组合、集合、字符串、正则等操作。循环必须包含设置 `maxIterations` 的边。当前每个 run 同时只有一个 active node。节点重新进入时，其本轮状态会重置；上一轮的正文若仍需使用，应由其他节点的 input/result 显式携带，不能依赖正在重新执行节点的旧 result。

`maxIterations` 限制的是这条边的穿越次数，超过限制会以 `max_iterations` 结束为失败。优先用图的边界限制完成用户要求；若用户另有“次数耗尽必须正常结束”的要求，才设计对应计数和终止条件，不要把技术计数程序无理由放到 Agent 起草之前。

业务字段只能位于 `input`/结果及其 schema 中。不要创建特殊审核节点或为 approve/reject/happy 等值赋予引擎含义。

## 注册程序入口

`.agentflow/executors.json` 关联名字与程序入口：

```json
{"executors":{"my-interaction":{"command":["python3","./executors/interaction.py"],"workspace":".","env":{"APP_PROFILE":"local"}}}}
```

节点写 `"executor":"my-interaction"`，运行到此节点就会自动启动入口。名字任意，语言任意。程序从 `AGENTFLOW_INPUT_FILE` 读取 JSON，对用户交互完成后把 JSON 写进 `AGENTFLOW_RESULT_FILE` 并正常退出。stdout/stderr 仅是日志，程序不修改 run state。`command` 与 `env` 支持 `{{ input.field }}`；相对 workspace 按调用目录解析，相对命令路径在该 workspace 下解析。

内置执行器：`codex`、`claude-code`、`process`、`external`。前两个适配器读取 `input.prompt`、可选 `input.workspace` 和 `input.session`；`process` 读取 `input.command` 数组；`external` 等待任意符合 outputSchema 的 JSON 提交。自定义程序与这些执行器使用相同的节点引用方式。

飞书、钉钉、终端或其他交互由自定义执行器负责。图无需识别这些产品。将收件人、卡片内容和操作返回值写进该程序的 input，明确 executor 注册项，避免程序扫描特定 workflow 名称或节点 ID。

## 执行和恢复

```bash
agentflow workflow validate workflow.json --check-executors --format json
agentflow run start workflow.json --input key=value --format json
agentflow run submit <run-id> <node-id> --output '{"customField":8}' --format json
agentflow run get <run-id> --format json
agentflow run logs <run-id> --format ndjson
agentflow run retry <run-id> --format json
agentflow run resume <run-id> --format json
```

submit 用于 external 执行器；启动的交互程序通常直接写结果文件，退出后自动推进。输入输出使用 JSON Schema draft 2020-12 校验；精确模板 `{{ nodes.step.result.value }}` 保留 JSON 类型，不要自动猜测同名字段传递。每次节点访问都有编号 input/result/log artifacts。

交付前校验实际工作流和注册项，存在触发器时也运行 `trigger validate`。`run start --dry-run` 的成功退出码是 10。CLI 错误 JSON 可能在 stderr，不能只看 stdout。不要把 dry-run 或 schema 通过描述为整条流程已经实跑。

涉及分支/返工时，用本地替身验证各条路：进入后续节点、直接结束、回到前面节点、次数耗尽及无效输出。不要为验证而调用真实模型或给真实收件人发消息，除非已有相应授权。模型步骤可在测试副本中替换成遵循相同输出契约的程序，交付配置仍保留用户选择的执行器。

使用 `doctor` 检查执行器，`web` 查看图，`run metrics/stats` 查看观测数据。失败后只重试当前失败节点；resume 恢复中断执行。执行器当前配置会影响后续 retry/resume。外部副作用应支持幂等。

定时与 webhook 由 `.agentflow/triggers.json` 定义。先 validate，再 fire 验证，最后持续运行 `trigger serve`。这些触发器负责创建 run，图负责 run 内节点推进。
