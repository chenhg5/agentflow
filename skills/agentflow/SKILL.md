---
name: agentflow
metadata:
  version: "3.7.0"
description: "使用 AgentFlow 设计、执行和检查由 JSON 数据交接与显式条件边组成的本地工作流。"
---

# AgentFlow workflow

先查 `agentflow schema --all --format json`、`schema workspace` 和 `schema workflow`。自定义程序查 `schema executors`，定时或 webhook 查 `schema trigger`。根据用户要求确定入口数据、每个节点的输出契约、边条件和完成标准，再创建工作流。

## 先设计职责与数据

搭建或迁移时先在 `DESIGN.md` 或 RUNBOOK 中说明：节点职责与输入输出契约；每个 agent 的角色、上下文来源、skills、工具与权限、工作目录和 session 策略；所有结果的后继节点、数据映射、并行汇合与返工；人工介入的时机、展示材料、允许输出、回传关联和恢复；完成标准与实际验证范围。按任务选 agent、程序或外部交互，不按节点数量凑角色。涉及 agent、人工审核或旧项目迁移时，先读 [设计与验收指引](references/design.md)。

内置 Codex/Claude 是启动适配器，不会自动提供专业角色。新角色用 initializer 建自己的常驻资源；复用外部 agent 时指出实际指令/skill/tool 来源，不能仅因项目目录存在就称它是已有专业 agent。工具或 skills 无需额外配置时说明依据。角色职责、政策等稳定上下文与每次任务的输入分开组织。图只管理通用契约与边，业务角色和通信渠道不进入引擎规则。

## 独立工作区

采用目录规范 v1。先用工具生成可运行的基础，再按任务调整：

```bash
agentflow workspace init my-workflow
cd my-workflow
agentflow executor init author --kind codex
agentflow executor init archive --kind command
```

`workspace init` 生成根目录图、注册表、README、忽略规则和一个可运行的 JSON 透传程序。替换示例节点与程序为实际任务，删除不用的示例注册和文件。`executor init --kind codex` 生成该 agent 的 `AGENTS.md`、`.agents/skills/workflow-contract/SKILL.md`、`.codex/config.toml` 与 `tools/` 输入工具；按角色改写常驻指令、增加所需 skills/工具。`AGENTS.md` 是 Codex 原生常驻指令，不是 API system role。配置加载取决于提供方的项目信任设置；不要修改全局 Codex 配置来让 demo 通过。

`executor init <name> --kind claude-code` 生成 `CLAUDE.md`、`.claude/skills/`、`.mcp.json` 和相同输入工具。Claude 的项目 MCP 配置仍受本地信任与授权设置控制，生成文件不代表已经加载。`workspace init` 的 `DESIGN.md` 是待按任务填写的设计起点，不是交付完成证据。

已有 agent 用 `executor add <name> --kind codex --workspace /existing/path` 只登记关联，也可登记工作区内的软链。不要对已有目录执行 init，不复制、覆盖或补写它的指令和工具。其他程序可用 `executor add <name> --kind command --workspace <existing-directory> --command '["node","main.js"]'` 登记，语言不限。先检查已有目录与 session 的真实信息，缺失时明确报告，不猜 ID。

优先建立独立的 workflow 工作区或仓库，由它调度用户的项目、仓库版本和现有 agent。不要默认在业务仓库内创建工作流目录。单图工作区用根目录 `workflow.json`；多图工作区将所有图统一放入 `workflows/<name>.json`，按用途命名，不在根目录额外保留“主图”或副本。这是推荐的搭建约定，引擎仍支持显式文件路径；目录层级不代表图之间的调用关系。根目录保留共享的 `executors.json`、可选 `triggers.json` 与生成的 `runs/`，从这个根目录运行 CLI。

从单图扩展到多图时，将原图移入 `workflows/`，同步修改 trigger、程序入口、测试和文档的路径，不留兼容副本或软链。`workspace validate` 默认检查根目录图及启用的 trigger 目标；没有 trigger 的图要分别用 `--workflow workflows/<name>.json` 校验，不会自动扫描目录。运行中的 trigger 服务启动时读取配置，修改后需确认无执行中的任务再重新加载服务。

推荐在 `executors/agents/<name>/` 聚合 agent 工作目录，在 `executors/programs/<name>/` 放普通程序。目录可以是软链，注册表的 `workspace` 也可以直接指向外部路径。用户已有 agent 时复用其目录、指令、skill 和工具，无需再建一份；修改前先确认是否属于当前任务范围。目录与 session 是两个独立关联：已有目录不代表已有 session，已有 session ID 不代表目录；在每个 `execution.candidates[]` 中用 `session` 显式指定 new/fixed/inherit/fork。恢复持久会话并不等于接管正在运行的交互进程。

图不依赖目录分类。引擎从工作区根目录读取注册表，只通过 executor 名称启动程序。内置 CLI 适配器随 AgentFlow 安装，具体 agent/程序工作目录可本地存放或引用外部资源。仓库的 URL、ref、commit、worktree 等由 executor 的配置及输入输出承载，记录为运行结果或 artifacts；引擎不自动 checkout 仓库。

## 图结构与数据流

只使用 version `3`。`entry` 是起点，`edges` 是唯一控制流，节点数组顺序没有执行含义。入口节点得到 run input；之后每条被选中的边默认将源节点的完整 JSON 输出作为目标节点的输入。需要保留源节点输入或重组字段时，在边上写通用 `map`；节点没有 `input` 字段，也不要把业务指令伪装成上游数据。

循环默认必须有 `maxIterations` 限制。只有任务明确要求无限返工/持续运行时，才在 workflow 顶层设置 `allowUnboundedCycles: true`；这只是运行策略，不代表任何业务决策。
`maxIterations` 限制该边的穿越次数；命中已达上限的边时，运行失败（outcome: max_iterations），不会再尝试后面的默认边。向用户解释超限后果；业务需要超限转交时显式建模并验证。

需要同图节点并发时，在分叉源节点写 `"dispatch":"all"`，从它连向各并行节点；在汇合节点写 `"join":{"from":["a","b"]}`，列出所有必须到达的直接前驱。每条边的 `map` 独立构造该分支或汇合输入。汇合节点收到以来源节点 ID 为键的 JSON 对象。不要为了并行而把两个节点塞进一个 shell 程序；参考 `examples/parallel.json`。普通条件分支继续使用默认的 `first`，不会同时执行多条边。

```json
{
  "version": "3", "name": "example", "entry": "draft",
  "inputs": {"topic": {"type": "string", "required": true}},
  "nodes": [
    {"id": "draft", "execution": {"candidates": [{"executor": "author", "config": {"instruction": "根据收到的主题起草方案"}}]}, "outputSchema": {"type": "object"}},
    {"id": "review", "execution": {"candidates": [{"executor": "external"}]}, "outputSchema": {"type": "object"}}
  ],
  "edges": [
    {"from": "draft", "to": "review"},
    {"from": "review", "to": "draft", "when": "nodes.review.result.score < 5", "maxIterations": 2},
    {"from": "review", "to": "$end"}
  ]
}
```

节点的结构字段包括 `id`、`execution.candidates`、可选 `execution.fallback`、`inputSchema`、`outputSchema`、`timeoutMs`、`retry.maxAttempts`，以及并行控制的 `dispatch` 和 `join`。候选按优先级排列，每个候选包含 `executor`、可选的不透明 `config` 与独立 `session`；即使只有一个 executor 也必须写 candidates。不要使用已移除的节点顶层 `executor`、`config` 或 `execution.session`。`inputSchema` 校验到达逻辑节点的数据，`outputSchema` 校验成功候选的结果。结果必须是 JSON 对象。业务字段可任意命名，`happy`、`reject`、`decision`、`score` 等没有引擎特权。

候选失败不会穿越业务边。`retry.maxAttempts` 是每个候选各自的命令尝试上限，耗尽后才判断是否选择下一候选。默认只在进程未启动的 `failure.kind == 'start_failed'` 时回退；进程已经启动后的非零退出、超时、缺失/无效结果或 output schema 失败默认直接使节点失败。确需其他策略时写通用 Expr，例如 `"fallback":{"when":"failure.kind == 'exit_code' && failure.code == 75"}`。不要根据业务错误文本、产品名或模型名写引擎回退条件。

节点完成后按顺序评估出边，选第一条条件为真的边。无 `when` 的默认边必须在最后。条件用 Expr，支持比较、算术、逻辑、集合和正则等。条件可读源节点的 `input`、`output`，也可读运行 `inputs` 和历史 `nodes`。循环必须有 `maxIterations`。每次重新进入节点，输入来自刚穿越的边；历史访问另存于 run history。

迁移已有流程时，先确认源系统的条件归一化、默认边、未命中路由、结束和循环语义。不能为了让本地测试通过，就擅自补默认结束边、改变循环限制或把路由错误变成成功。原本无出边的终点可显式连 `$end`；有出边但没有条件命中是另一种情况。源语义不明时记录缺口，不猜业务意图。验证应覆盖普通路径以及未命中、大小写/空白等适用边界；不要无依据收紧源数据契约。

`config` 可引用当前节点输入 `{{ input.field }}`、运行输入 `{{ inputs.topic }}` 或历史节点结果 `{{ nodes.draft.result.summary }}`；执行器注册中的 command、workspace 和 env 可引用 `{{ input.field }}` 与 `{{ config.field }}`。边的 `when` 可写 `output.score > 80`，不加模板括号。边的 `map` 可写 `{"summary":"{{ input.summary }}","review":"{{ output.comments }}"}`；省略 `map` 会原样传递输出，`"map":{}` 会传空对象。精确模板保留 JSON 类型，拼接字符串时转换为字符串；边映射中缺失的引用变为空字符串。

## Session 策略

Session 是引擎的执行策略，不属于节点 input。每个 execution candidate 可写：

- `{"session":{"mode":"new"}}`：每次访问新起空白 session；省略时也是此模式。
- `{"session":{"mode":"fixed"}}`：本次 run 第一次选择该节点的这个 executor 时创建 session，重访或重试该候选时继续使用。可加 `"id":"existing-session-id"` 指定已有 session；`id` 可引用已声明的运行输入。
- `{"session":{"mode":"inherit"}}`：沿用进入本节点的上一节点兼容 session；可用 `from` 明确指定来源节点。
- `{"session":{"mode":"fork"}}`：从兼容的上游 session 派生新 session；可用 `from` 指定来源节点。

内置 `external` 等待器只支持默认/`new`，会明确拒绝 `fixed`、`inherit`、`fork`。需要持久会话时，使用实现对应 session 契约的 executor。

`inherit`/`fork` 缺少来源 session 会失败，不能猜测。`from` 指定的来源必须在本节点执行前已完成；并行时用边和 `join` 明确保证先后关系，不要引用尚未执行的未来节点。原生 provider 只有同类 provider 的注册别名兼容；自定义 command 必须是同一个注册身份，不能把失败候选的 session 传给另一 provider。`fork` 必须报告新的 ID，避免后续错误地复用父 session。Codex 和 Claude Code 适配器从 CLI JSON 日志提取 session ID。自定义执行器在 `AGENTFLOW_SESSION_FILE` 写入新 ID；固定/沿用模式下引擎已经知道所选 ID。自定义执行器如需 session 操作，在注册项中提供 `command`、`resumeCommand` 和 `forkCommand`，按需使用 `{{session_id}}`。

## 注册与执行程序

`executors.json` 把任意名字关联到程序入口，语言和通信方式不限：

```json
{"executors":{"my-interaction":{"command":["python3","main.py"],"workspace":"executors/programs/my-interaction","env":{"APP_PROFILE":"local"}}}}
```

节点候选写 `{"executor":"my-interaction"}`。程序从 `AGENTFLOW_INPUT_FILE` 读取上游 JSON，从 `AGENTFLOW_CONFIG_FILE` 读取当前候选配置，完成工作后把 JSON 写到 `AGENTFLOW_RESULT_FILE` 并正常退出。stdout/stderr 是日志。执行器无需也不应修改 run state；引擎检查结果并自动推进边。飞书、钉钉、电话、终端等由执行器处理，图不认识这些产品。

`AGENTFLOW_ROOT` 是 workflow 工作区根目录，`AGENTFLOW_WORKSPACE` 是本次 executor 的执行目录；二者可以位于不同仓库或通过软链关联。使用引擎提供的绝对文件路径完成输入输出，不按执行目录猜测 runs 的位置。

引擎证据归 `runs/<run-id>/`：图快照、state、events，以及每个节点访问/候选/尝试独立的 input/config/result/session/stdout/stderr。检查 `selectedExecutor`、`attemptSequence`、结构化 `failure` 和每次尝试的 artifacts；不要把中间候选失败误当成业务节点失败。业务产物位置由程序明确选择，例如以 `AGENTFLOW_ROOT` 为基准写 `output/`，并在 JSON 结果中返回路径。`output/` 不是引擎结果通道。不要把运行文件写到源码、agent 目录或被调度业务仓库。

内置执行器有 `codex`、`claude-code`、`process`、`external`。前两者读取 `config.instruction`、可选 `config.workspace`；`process` 读取 `config.command`；`external` 等待 `run submit`。缺少配置的程序入口须通过 `executors.json` 注册。

环境继承属于 executor 注册配置。根据实际 `agentflow schema executors` 能力设置 `environment`：省略/`{"mode":"inherit"}` 继承宿主变量；`{"mode":"allowlist","inherit":["PATH","LANG"]}` 只继承指定变量；`{"mode":"empty"}` 不继承宿主变量。随后加入显式 env 与引擎保留通道；不能配置或继承 `AGENTFLOW_*` 来覆盖当前调用。原生 CLI 需要的 HOME/认证须显式安排。环境白名单不是文件系统 sandbox，获配秘密的程序也不能主动把值写入日志。workspace validate 和启动事件只报告模式/变量名，不报告值。

同一 worker 运行模型角色和持有测试凭据的 runner 时，为它们分别选环境白名单；`env:{}` 不会清空宿主环境。秘密通过受控运行时/wrapper 获取，不放进 graph、input 或 config artifact。

## 验证与运行

对可能重复派发的创建请求使用 `--idempotency-key`。工作区 + workflow name + key 对应同一图内容、inputs 和不可变 parent/correlation 元数据；并发复用只创建一条 run，变更请求返回 conflict/exit 5。CLI 和 trigger 共用创建事务。保留 runs/requests 创建日志及执行 outbox；创建中断后复用同 key 或重启 worker 恢复，不要删除 state/journal 来“重新开始”。这不保证任意程序外部副作用 exactly-once。

```bash
agentflow workspace validate --format json
agentflow workspace validate --input topic=value --format json
agentflow workflow validate workflow.json --check-executors --format json
agentflow run start workflow.json --input topic=value --dry-run --format json
agentflow run start workflow.json --input topic=value --format json
agentflow run submit <run-id> <node-id> --token <leaseToken> --output '{"score":8}' --format json
agentflow run get <run-id> --format json
agentflow run logs <run-id> --format ndjson
agentflow run retry <run-id> --format json
agentflow run resume <run-id> --format json
```

`workspace validate` 静态检查图、注册、目录/软链、可执行入口与触发配置，返回 `execution_verified:false`。`--input key=value` 可解析依赖 run input 的路径；依赖尚未执行的上游输出时仍为 unresolved。`agent_resources` 列出每个原生 agent 候选的工作目录、session、常驻指令/skill/tool 配置文件与摘要及未确认项，只表示发现资源，不证明 provider 实际加载、认证成功或角色质量。

交付前用代表输入实跑，逐一核对关键节点是否实际执行、输出内容与验收标准是否匹配。零任务、`review_limit=0`、跳过关键节点、空结果 smoke 或降级成功都不能代替真实任务验收。用至少一个非空样本验证 agent 的上下文和工具确实起作用，并分别检验人工通过、返工、拒绝及适用的无效/重复回传；值名和路线以用户定义为准。无法调用真实模型时可用临时注册表替身验证数据契约，保留原注册表并明确列出模型/角色行为未验证。附简短 RUNBOOK，写明启动/恢复方法、产物位置、运行 ID 和逐节点验证范围。

验证过程也要遵守任务的写入范围。若只允许当前工作区，临时输入、schema 和结果文件放工作区内的 `scratch/` 并在结束后清理；`mktemp` 或 `tempfile` 要显式指定这个目录，不使用默认的系统 `/tmp`。

`submit` 用于 external 节点。定时/webhook 配置放 `triggers.json`，先 validate 和 fire，再持续运行 `trigger serve`。交付前用本地替身检验分支、返工、次数上限和无效输出。`run start --dry-run` 的成功退出码为 10。需要观察耗时用 `run metrics/stats`，业务质量数值由外部评估器写入 `run evaluate`，再用 `run compare` 对比。

`trigger serve` 按工作区运行，一个进程加载该工作区所有 trigger；一个工作区只启动一份服务。不同工作区使用独立的配置与运行记录。纯 cron 不占 HTTP 端口；启用 webhook 时各工作区使用不同固定端口或 `--port 0` 自动选择。worker 默认一个活动 run，可用 `run worker --concurrency N` 或 `trigger serve --concurrency N` 配置；两者共享所有者，已有 worker 时 trigger 只入队，不另开额度。trigger ID 和启用的 webhook path 在工作区内不得重复；delivery ID 的去重范围是该 trigger。默认同一 trigger 有活动 run 就跳过新 occurrence，显式 `policy.concurrency=allow` 才允许重叠入队；跨 trigger/工作区对同一仓库或 session 的访问没有自动资源互斥。

人工交互可能持续很久或需要重启恢复时，优先把执行器注册为 `kind: external`，由独立适配器服务接收请求和提交结果。图只保存不透明 config；通信渠道、卡片和按钮输出归适配器/工作流配置。适配器必须持久化收到的选择，校验操作人、消息 ID 和本次调用 token；提交时传 waiting node 的 `leaseToken`，防止旧回调命中新一轮。发送前用 `run submit --dry-run --token ...` 校验每个按钮的输出。卡片更新应可重试；明确区分结果已接收和整个流程已完成。参考 `examples/lark-review/README.md`，保留连接离线时点击可能需重试的边界。


## 持久队列与父子运行

批量派发使用 `run enqueue <graph> --input key=value --idempotency-key <key>`，然后运行 `run worker --concurrency N`；producer 入队后即可退出。不能用异步 shell/Popen 代替持久队列。enqueue 或 --async 会持久启用托管队列；之后 start/trigger fire/submit/retry/resume/cancel 都返回持久接收状态，实际执行由同一 worker 准入，省略 --async 也不能绕过。worker 离线时请求保留在队列，须启动服务；用 `run queue` 查看 active/queued/waiting/terminal、owner 和 heartbeat，用 run get 查询最终结果。

并发数约束活动 **run**；图内并行节点可能启动多个进程，不能声称限制了全局 executor 数。waiting_input 释放 run 槽位；同时还有运行节点则继续占位。中断恢复不重跑已确认完成的节点，但未确认完成的 attempt 可能重放，外部副作用应幂等。硬杀 worker 不保证杀死子进程；cancel 在执行边界处理。

子运行在创建时用 `--parent <run> --parent-node <node>`、可重复 `--correlation key=value` 关联。程序可从 AGENTFLOW_RUN_ID 和 AGENTFLOW_NODE_ID 取父标识，cwd 使用 AGENTFLOW_ROOT。仅支持同 store 的已存在父/节点，关系和字符串标签不可变且参与创建幂等校验。`run list --parent <id> --correlation key=value` 查询子集，run get 父运行显示子状态计数，Web 双向跳转。父成功表示其派发工作完成；子状态独立，取消不级联。相同业务对象跨批次复用由业务 ledger 决定，图不理解标签业务含义。

## 复用飞书交互程序

用户要求飞书人工确认时，先定位同版本源码/完整发行包的 `examples/lark-review/SETUP.md`，按五步指引操作。仅安装二进制不保证附带该目录；找不到时获取匹配的源码/示例，不猜开发者机器路径。配套文件包含完整 workflow.json、executors.json、config.example.json、config.schema.json 和 lark_adapter.py。

确认卡片内容、按钮任意 JSON 输出、机器人 profile 和同应用用户 open_id；同步调整输出契约与边。profile 与服务参数一致；executor 名称在注册表/节点/服务三处一致。用 --check-config 本地检查，再验证图和 dry-run；external 注册不会自动启动服务。保持单消费者，由用户真实点击验证自动推进，不自行 submit 伪装飞书成功。身份/权限参考 lark-shared，查人参考 lark-contact，发送与卡片遵循 lark-im。

## 跨 run 资源额度

涉及共享写入、固定 session 或全局 executor 上限时，在工作区 `resources.json` 统一声明 key 的 capacity，在需要启动进程的节点写 `resources: [{"key":"writer","units":1}]`。key 仅标识通用资源，图不理解仓库/审批含义；同路径或同 session 不会自动加锁。未知 key、重复 key、零/超额 units 会在创建/执行前拒绝。先查 `schema resources`。

worker concurrency 限制活动 run，resources 限制实际占资源 attempt，二者独立；要限制全部进程，给所有对应节点声明同一 quota。多项资源原子申请；不足进入 waiting_resource，不消耗 attempt/retry/fallback。仅剩资源等待时会让出 run 名额；额度释放后 worker 自动唤醒。external 不占主动资源。fallback/retry 为新 attempt 重新申请，正常退出后释放；程序须在返回前结束自己派生的写入任务，不得后台脱离 claim 生命周期。

`resource list`、`run queue` 可查额度、持有者和 orphaned claim。owner 消失不表示进程/后代已退出，额度不会按 TTL 被抢占。先确认整个遗留执行范围停止，再用 `resource resolve <claim> --confirm-stopped --reason <已观察到的清理证据>`；仍存活的已记录 PID 会被拒绝。PID 已退出也不能代替后代进程核对。首版只覆盖同机同 store，不能当作跨机器分布式锁。
