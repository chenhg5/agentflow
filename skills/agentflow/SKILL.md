---
name: agentflow
metadata:
  version: "3.2.0"
description: "使用 AgentFlow 设计、执行和检查由 JSON 数据交接与显式条件边组成的本地工作流。"
---

# AgentFlow workflow

先查 `agentflow schema --all --format json`、`schema workspace` 和 `schema workflow`。自定义程序查 `schema executors`，定时或 webhook 查 `schema trigger`。根据用户要求确定入口数据、每个节点的输出契约、边条件和完成标准，再创建工作流。

## 独立工作区

采用目录规范 v1。先用工具生成可运行的基础，再按任务调整：

```bash
agentflow workspace init my-workflow
cd my-workflow
agentflow executor init author --kind codex
agentflow executor init archive --kind command
```

`workspace init` 生成根目录图、注册表、README、忽略规则和一个可运行的 JSON 透传程序。替换示例节点与程序为实际任务，删除不用的示例注册和文件。`executor init --kind codex` 生成该 agent 的 `AGENTS.md`、`.agents/skills/workflow-contract/SKILL.md`、`.codex/config.toml` 与 `tools/` 输入工具；按角色改写常驻指令、增加所需 skills/工具。`AGENTS.md` 是 Codex 原生常驻指令，不是 API system role。配置加载取决于提供方的项目信任设置；不要修改全局 Codex 配置来让 demo 通过。

已有 agent 用 `executor add <name> --kind codex --workspace /existing/path` 只登记关联，也可登记工作区内的软链。不要对已有目录执行 init，不复制、覆盖或补写它的指令和工具。其他程序可用 `executor add <name> --kind command --workspace <existing-directory> --command '["node","main.js"]'` 登记，语言不限。先检查已有目录与 session 的真实信息，缺失时明确报告，不猜 ID。

优先建立独立的 workflow 工作区或仓库，由它调度用户的项目、仓库版本和现有 agent。不要默认在业务仓库内创建工作流目录。单条图直接放根目录 `workflow.json`；多条图时可按需使用 `workflows/`，引擎不要求固定目录。根目录还放 `executors.json`、可选 `triggers.json` 与生成的 `runs/`，从这个根目录运行 CLI。

推荐在 `executors/agents/<name>/` 聚合 agent 工作目录，在 `executors/programs/<name>/` 放普通程序。目录可以是软链，注册表的 `workspace` 也可以直接指向外部路径。用户已有 agent 时复用其目录、指令、skill 和工具，无需再建一份；修改前先确认是否属于当前任务范围。目录与 session 是两个独立关联：已有目录不代表已有 session，已有 session ID 不代表目录；用 `execution.session` 显式指定 new/fixed/inherit/fork。恢复持久会话并不等于接管正在运行的交互进程。

图不依赖目录分类。引擎从工作区根目录读取注册表，只通过 executor 名称启动程序。内置 CLI 适配器随 AgentFlow 安装，具体 agent/程序工作目录可本地存放或引用外部资源。仓库的 URL、ref、commit、worktree 等由 executor 的配置及输入输出承载，记录为运行结果或 artifacts；引擎不自动 checkout 仓库。

## 图结构与数据流

只使用 version `3`。`entry` 是起点，`edges` 是唯一控制流，节点数组顺序没有执行含义。入口节点得到 run input；之后每条被选中的边默认将源节点的完整 JSON 输出作为目标节点的输入。需要保留源节点输入或重组字段时，在边上写通用 `map`；节点没有 `input` 字段，也不要把业务指令伪装成上游数据。

循环默认必须有 `maxIterations` 限制。只有任务明确要求无限返工/持续运行时，才在 workflow 顶层设置 `allowUnboundedCycles: true`；这只是运行策略，不代表任何业务决策。

需要同图节点并发时，在分叉源节点写 `"dispatch":"all"`，从它连向各并行节点；在汇合节点写 `"join":{"from":["a","b"]}`，列出所有必须到达的直接前驱。每条边的 `map` 独立构造该分支或汇合输入。汇合节点收到以来源节点 ID 为键的 JSON 对象。不要为了并行而把两个节点塞进一个 shell 程序；参考 `examples/parallel.json`。普通条件分支继续使用默认的 `first`，不会同时执行多条边。

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

节点完成后按顺序评估出边，选第一条条件为真的边。无 `when` 的默认边必须在最后。条件用 Expr，支持比较、算术、逻辑、集合和正则等。条件可读源节点的 `input`、`output`，也可读运行 `inputs` 和历史 `nodes`。循环必须有 `maxIterations`。每次重新进入节点，输入来自刚穿越的边；历史访问另存于 run history。

迁移已有流程时，先确认源系统的条件归一化、默认边、未命中路由、结束和循环语义。不能为了让本地测试通过，就擅自补默认结束边、改变循环限制或把路由错误变成成功。原本无出边的终点可显式连 `$end`；有出边但没有条件命中是另一种情况。源语义不明时记录缺口，不猜业务意图。验证应覆盖普通路径以及未命中、大小写/空白等适用边界；不要无依据收紧源数据契约。

`config` 可引用当前节点输入 `{{ input.field }}`、运行输入 `{{ inputs.topic }}` 或历史节点结果 `{{ nodes.draft.result.summary }}`；执行器注册中的 command、workspace 和 env 可引用 `{{ input.field }}` 与 `{{ config.field }}`。边的 `when` 可写 `output.score > 80`，不加模板括号。边的 `map` 可写 `{"summary":"{{ input.summary }}","review":"{{ output.comments }}"}`；省略 `map` 会原样传递输出，`"map":{}` 会传空对象。精确模板保留 JSON 类型，拼接字符串时转换为字符串；边映射中缺失的引用变为空字符串。

## Session 策略

Session 是引擎的执行策略，不属于节点 input。每个节点可写：

- `{"execution":{"session":{"mode":"new"}}}`：每次访问新起空白 session；省略时也是此模式。
- `{"execution":{"session":{"mode":"fixed"}}}`：本次 run 第一次访问节点时创建 session，重访时继续使用同一个。可加 `"id":"existing-session-id"` 指定已有 session；`id` 可引用已声明的运行输入。
- `{"execution":{"session":{"mode":"inherit"}}}`：沿用进入本节点的上一节点 session；可用 `from` 明确指定来源节点。
- `{"execution":{"session":{"mode":"fork"}}}`：从上游 session 派生新 session；可用 `from` 指定来源节点。

`inherit`/`fork` 缺少来源 session 会失败，不能猜测。`fork` 必须报告新的 ID，避免后续错误地复用父 session。Codex 和 Claude Code 适配器从 CLI JSON 日志提取 session ID。自定义执行器在 `AGENTFLOW_SESSION_FILE` 写入新 ID；固定/沿用模式下引擎已经知道所选 ID。自定义执行器如需 session 操作，在注册项中提供 `command`、`resumeCommand` 和 `forkCommand`，按需使用 `{{session_id}}`。

## 注册与执行程序

`executors.json` 把任意名字关联到程序入口，语言和通信方式不限：

```json
{"executors":{"my-interaction":{"command":["python3","main.py"],"workspace":"executors/programs/my-interaction","env":{"APP_PROFILE":"local"}}}}
```

节点写 `"executor":"my-interaction"`。程序从 `AGENTFLOW_INPUT_FILE` 读取上游 JSON，从 `AGENTFLOW_CONFIG_FILE` 读取节点配置，完成工作后把 JSON 写到 `AGENTFLOW_RESULT_FILE` 并正常退出。stdout/stderr 是日志。执行器无需也不应修改 run state；引擎检查结果并自动推进边。飞书、钉钉、电话、终端等由执行器处理，图不认识这些产品。

`AGENTFLOW_ROOT` 是 workflow 工作区根目录，`AGENTFLOW_WORKSPACE` 是本次 executor 的执行目录；二者可以位于不同仓库或通过软链关联。使用引擎提供的绝对文件路径完成输入输出，不按执行目录猜测 runs 的位置。

引擎证据归 `runs/<run-id>/`：图快照、state、events、每次节点访问的 input/config/result 和 logs。业务产物位置由程序明确选择，例如以 `AGENTFLOW_ROOT` 为基准写 `output/`，并在 JSON 结果中返回路径。`output/` 不是引擎结果通道。不要把运行文件写到源码、agent 目录或被调度业务仓库。

内置执行器有 `codex`、`claude-code`、`process`、`external`。前两者读取 `config.instruction`、可选 `config.workspace`；`process` 读取 `config.command`；`external` 等待 `run submit`。缺少配置的程序入口须通过 `executors.json` 注册。

## 验证与运行

```bash
agentflow workspace validate --format json
agentflow workflow validate workflow.json --check-executors --format json
agentflow run start workflow.json --input topic=value --dry-run --format json
agentflow run start workflow.json --input topic=value --format json
agentflow run submit <run-id> <node-id> --output '{"score":8}' --format json
agentflow run get <run-id> --format json
agentflow run logs <run-id> --format ndjson
agentflow run retry <run-id> --format json
agentflow run resume <run-id> --format json
```

`workspace validate` 静态检查图、注册、目录/软链、可执行入口与触发配置，返回 `execution_verified:false`；它不验证模型认证、程序内部行为和运行时模板路径。交付前必须用代表输入实跑，检查 `run get` 的完成状态、每个节点 result/log 路径和业务产物内容。无法调用真实模型时可用临时注册表替身验证文件契约，保留原注册表并明确说明未验证模型行为；不要把 dry-run 写成实跑成功。附简短 RUNBOOK，写明根目录、启动/恢复方法、产物位置及验证范围。

验证过程也要遵守任务的写入范围。若只允许当前工作区，临时输入、schema 和结果文件放工作区内的 `scratch/` 并在结束后清理；`mktemp` 或 `tempfile` 要显式指定这个目录，不使用默认的系统 `/tmp`。

`submit` 用于 external 节点。定时/webhook 配置放 `triggers.json`，先 validate 和 fire，再持续运行 `trigger serve`。交付前用本地替身检验分支、返工、次数上限和无效输出。`run start --dry-run` 的成功退出码为 10。需要观察耗时用 `run metrics/stats`，业务质量数值由外部评估器写入 `run evaluate`，再用 `run compare` 对比。

`trigger serve` 按工作区运行，一个进程加载该工作区所有 trigger；一个工作区只启动一份服务。不同工作区使用独立的配置与运行记录。纯 cron 不占 HTTP 端口；启用 webhook 时各工作区使用不同固定端口或 `--port 0` 自动选择。当前 CLI 每个服务有一个 run worker，多条图会排队。trigger ID 和启用的 webhook path 在工作区内不得重复；delivery ID 的去重范围是该 trigger。默认同一 trigger 有活动 run 就跳过新 occurrence，显式 `policy.concurrency=allow` 才允许重叠入队；跨 trigger/工作区对同一仓库或 session 的访问没有自动资源互斥。
