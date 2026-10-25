# 首次配置：无需聊天上下文

先读本页，再读 README 中的恢复边界。图使用通用 external；飞书仅存在于此程序中。

## 1. 获取文件与工具

需要 macOS/Linux、Python 3.9+（标准库）、lark-cli 和支持 candidates、run submit --token 的 AgentFlow。
当前适配器在源码工作区，尚未随已发布版本交付。从源码根目录 `make build` 后用 bin/agentflow。
`agentflow schema --all --format json` 的 run submit flags 应包含 --token；不要只猜版本号。
安装脚本目前只安装二进制。单独安装 CLI/skill 不代表示例已安装；先定位匹配版本源码或完整发行包中的 examples/lark-review/。

在新建的独立工作区根目录操作，不覆盖已有文件：

```sh
mkdir -p executors/programs/lark
cp /path/to/agentflow/examples/lark-review/lark_adapter.py executors/programs/lark/
cp /path/to/agentflow/examples/lark-review/config.schema.json executors/programs/lark/
cp /path/to/agentflow/examples/lark-review/config.example.json executors/programs/lark/
cp /path/to/agentflow/examples/lark-review/workflow.json ./workflow.json
cp /path/to/agentflow/examples/lark-review/executors.json ./executors.json
```

完整示例为 Codex 起草 → 飞书确认 → Codex 整理行动。不要复制别人的数据库、日志或真实收件人。
把 runs/、*.sqlite3*、*.lock、*.log 加入工作区 .gitignore；为用户写 RUNBOOK，明确绝对根目录、启动命令、配置与结果位置。
已有 agent 按 AgentFlow skill 的 executor add 关联，不覆盖其文件。

## 2. 选机器人

profile 是 **lark-cli 本机配置名**，不是机器人显示名、app_id 或 open_id。

```sh
lark-cli config show --help
lark-cli config show
lark-cli auth status --profile my-bot --json --verify
lark-cli event status --json
```

复用用户指定的 profile。需要新应用时先读 lark-shared skill 和 `lark-cli config init --help`，用当前 CLI 的 `--new --name <profile>` 引导用户配置。
已有 app secret 用 --app-secret-stdin 输入，不能放进图、日志或命令参数。国内品牌为 feishu，海外为 lark。

机器人要有消息发送、消息读取、卡片更新能力；收件人要在应用可用范围内。
常见权限为 im:message:send_as_bot、im:message:readonly、im:message:update；具体以当前命令 reference、schema 和权限错误提示为准。
在应用后台 **事件与回调 → 回调配置** 开启 card.action.trigger，通过 WebSocket 长连接接收。
消费者 ready 不证明后台回调已开启。bot 缺权限去应用后台配置，不用 auth login 给 bot 授权。
同一机器人只允许一份 card.action.trigger consumer；已有服务先复用，不停止别人的消费者。

## 3. 选收件人

recipient 是同一个应用下的**用户 open_id**。当前仅支持一个用户私聊，不接受姓名、邮箱、群聊 oc_... 或机器人 ID。
已知同应用 open_id 可复用，否则按 lark-contact skill 查询：

```sh
lark-cli contact +search-user --query '用户姓名' --profile my-bot --as user
```

该查询需要 user 身份授权。多个同名结果请用户选择；权限不足时按提示处理或请用户提供同应用 ID，不能猜。
下面的 ou_REPLACE_ME 和 ou_REAL_ID 都是占位符，真实发送前必须替换。

## 4. 改内容、按钮与图

编辑 workflow.json 的 review.execution.candidates[0].config：

| 字段 | 意义 |
|---|---|
| profile | 机器人配置名，必须与服务 --profile 一致 |
| recipient | 用户 open_id；示例用 {{ inputs.reviewer }} 从运行输入获取 |
| title / subtitle | 卡片标题 / 副标题 |
| sections | 1–3 个内容区，各包含 title、content |
| hint | 按钮下方说明 |
| actions | 1–3 个按钮，必须有 label 和对象类型 output |
| actions[].style | default / primary_filled / danger / danger_filled / text |
| actions[].color | 结果颜色：blue / green / orange / red / grey / default |
| actions[].confirm | 点击前的确认文案，可选 |
| actions[].confirmation | 工作流接收结果后的文案，可选；不能声称下游已经完成 |

config.schema.json 约束的是适配器配置，不能代替业务 outputSchema。
config.example.json 是独立参考，**不会自动被 workflow 引用**：真正运行读的是 candidate.config。
可把实际 candidate.config 导出到该文件，再用 --check-config 校验。
模板如 {{ input.summary }}、{{ inputs.topic }} 先由引擎渲染。

按钮 output 是任意 JSON 对象。示例 happy/90 走 output.score > 80 到 finalize，sad/40 走 output.score > 0 回 draft（最多两次），sad/0 走最后默认边到 $end。
`maxIterations: 2` 限制该边最多穿越两次：第三次选择返工时，运行变为 failed，outcome 为 max_iterations；不会继续尝试默认边，也不会自动通过。若业务要求超限转交其他节点，需要显式设计对应路由。
自定义字段后同步改 outputSchema、边条件及下游 inputSchema/指令。没有内置 approve/reject 语义。

注册表 lark-review 是 {"kind":"external"}。如果改成 my-confirm，注册表名字、candidate.executor、服务 --executor my-confirm **三处一致**。
external 注册本身不启动适配器，也不要给它配置 command。

## 5. 检查、启动与验收

```sh
# 不联网、不发消息、不写 journal；先导出实际 candidate.config 到此文件。
python3 executors/programs/lark/lark_adapter.py --profile my-bot --check-config executors/programs/lark/config.example.json
agentflow workflow validate workflow.json --check-executors --format json
agentflow run start workflow.json --input topic='一个小改进' --input reviewer=ou_REPLACE_ME --dry-run --format json

# 终端 A。workspace 是图、注册表、runs 所在根目录，不是适配器目录。
python3 executors/programs/lark/lark_adapter.py --workspace . --profile my-bot --executor lark-review --agentflow /absolute/path/to/agentflow

# 终端 B。用户已授权机器人、收件人与内容，且已替换真实 ID 后执行。
agentflow run start workflow.json --input topic='一个小改进' --input reviewer=ou_REAL_ID --format json
```

AgentFlow dry-run 成功退出码 10；适配器 --check-config 成功 0，错误 2。
本地检查只验证配置字段和 profile，不检查联网权限、模板引用或业务 outputSchema。
运行时每个按钮在发送前都会经过 run submit --dry-run --token ... 的输出契约验证。

验收：draft 成功 → review waiting_input → 卡片送达 → **用户真实点击** → review 输出正确 → 下一节点自动执行 → 卡片结果更新。
不能手工 submit 后声称飞书验证成功。无模型/网络可用隔离副本的替身验证，但必须明确范围。
定时另配 triggers.json、持续运行 trigger serve；适配器仍需单独运行。守护进程示例见 supervisor.example.conf。

## 排障和恢复

| 现象 | 检查 |
|---|---|
| 等待但没卡片 | 服务是否启动；workspace 是否正确；executor 三处同名；profile 是否匹配 |
| profile mismatch | config.profile 与 --profile 不同；修正后建新 run，历史 run 读的是图快照 |
| 有卡片但点击无反应 | 回调是否开通；consumer 是否 ready；操作者是否为指定用户；是否为旧卡片 |
| 已记录但未继续 | journal 旁 run 日志；输出 schema；--token 支持 |
| 已接收但没更新卡片 | 服务 card_error 日志、更新权限；服务会重试 |
| 重启/搬迁 | 保留同一 journal；使用同一 workspace、profile、executor |

默认 journal 在 executors/programs/lark/state.sqlite3。一份服务可处理工作区内多个图/节点。
同机器人集中到一个服务；不同机器人服务必须指定不同 --state 文件，不能共享默认 journal。
已持久化的选择可以恢复；离线期间未送达本地的点击可能要重试。详细边界见 README。
