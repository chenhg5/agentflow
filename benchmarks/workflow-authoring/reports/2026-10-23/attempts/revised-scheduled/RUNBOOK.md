# 产品研究团队的工作日提案审核

交付入口是 `workflow.json`，角色映射在 `submission.json`。所有命令从本目录执行：

```sh
cd /tmp/agentflow-bench-revised-scheduled
./agentflow workflow validate workflow.json --check-executors --format json
./agentflow trigger validate --format json
./agentflow doctor --format json
```

## 流程和契约

运行输入 `topic`、`reviewer` 都是必填字符串。节点 schema 进一步要求它们非空。

| 节点 | 执行器 | 输入 | 输出 |
| --- | --- | --- | --- |
| `draft` | `codex` | `prompt`、`topic`；`feedback` 首轮为 null，返工时为最新审核 note | `summary`、`check`，均为非空字符串 |
| `review` | `local-review` | `recipient` 为运行输入 reviewer；`content` 为最新 `{summary, check}`；`actions` 为三个 `{label, output}` 对象 | `signal` 为 happy/again/sad 之一；`note` 为字符串 |
| `finalize` | `codex` | `prompt`、`topic`、最新完整 `proposal`、最新完整 `review` | 非空字符串 `next_step`、`verification` |

每个输入输出对象均有 JSON Schema，禁止额外字段。审核 actions 的 output 是 `{signal, note}` 对象，标签说明通过、返工、终止。模拟渠道从环境指定的审核序列中返回审核 JSON。

控制流为 `draft → review`。happy 进入 finalize，随后结束；sad 直接正常结束；again 把 note 传给下一次 draft。返工边 `maxIterations: 2`，因此最多起草三轮、审核三轮。第三次审核仍为 again 时引擎以 `failed / max_iterations` 停止，不执行 finalize。需求仅指定 sad 正常结束，因此保留引擎对次数耗尽的明确失败语义。

finalize 的 proposal 和 review 分别精确引用当前 `nodes.draft.result`、`nodes.review.result`，并同时出现在 prompt 中。不会复用首轮提案；首轮 draft 的可空 feedback 契约允许尚无审核结果。

## 执行器与自动推进

`.agentflow/executors.json` 明确注册 Codex 入口及 `local-review`：

- Codex 使用 `codex` 二进制、当前目录 workspace，并允许目录无 Git 仓库。
- local-review 直接执行 `python3 ./fixtures/channel.py`，无需额外包装程序或扫描 workflow 的服务。

AgentFlow 把渲染后的 JSON 写到 `AGENTFLOW_INPUT_FILE`。channel.py 读取其中的 recipient/content/actions，再把审核 JSON 写到 `AGENTFLOW_RESULT_FILE`。正常退出且通过 outputSchema 后，图自动选择出边并推进。此渠道节点不是 external，正常操作不使用 `run submit`。

手动运行以下命令会实际调用交付配置中的 Codex：

```sh
./agentflow run start workflow.json --input 'topic=改进首次使用体验' --input 'reviewer=research-reviewer-local' --format json
```

本地渠道默认返回 `{"signal":"happy","note":"local test"}`。若要模拟返工，可在启动前设置：

```sh
export BENCH_RESPONSES='[{"signal":"again","note":"补充用户验证指标"},{"signal":"happy","note":"请先做小范围验证"}]'
export BENCH_CHANNEL_COUNTER="$PWD/channel-count.txt"
```

每组新的模拟实验应换一个 counter 文件名，或仅删除该组已知的 counter 文件，以便从序列首项开始。channel.py 每次执行会增加计数；实际重复审核时需考虑此副作用。模拟序列、计数仅属于本地 fixture，不会联网。

## 工作日 09:15 定时器

`.agentflow/triggers.json` 已配置启用的 `weekday-research-0915`：

- cron：`15 9 * * 1-5`，时区：`Asia/Shanghai`。
- target：`workflow.json`。
- 固定运行输入示例：topic 为“产品研究：改进首次使用体验”，reviewer 为 `research-reviewer-local`。调整这两个值即可更改定时运行的主题与审核人；手动运行仍必须传两个输入。
- `concurrency: forbid` 避免同一触发器重叠运行；`misfire: skip` 跳过错过的时间点。

校验配置后，在当前目录启动并持续保留该前台进程：

```sh
./agentflow trigger serve --host 127.0.0.1 --port 0
```

这就是定时器和运行 worker，负责创建 run 和执行图；无需另起渠道扫描服务。终端退出或停止进程后定时器停止。这里的工作日指周一至周五，不包含法定节假日调休规则。

以下命令可手动发射一次触发事件；在交付配置下也会调用 Codex。相同 delivery ID 用于防止同一次事件重复创建运行：

```sh
./agentflow trigger fire weekday-research-0915 --scheduled-at '2026-10-26T09:15:00+08:00' --delivery-id manual-20261026-0915 --format json
```

## 查看与恢复

```sh
./agentflow run list --format json
./agentflow run get RUN_ID --format json
./agentflow run logs RUN_ID --format ndjson
./agentflow run metrics RUN_ID --format json
```

每轮 input/result/log 都保存在 `.agentflow/runs/RUN_ID/artifacts/`。程序失败或 schema 不符时先看 get/logs，修复原因后对失败节点运行 `./agentflow run retry RUN_ID --format json`；中断的运行用 `run resume RUN_ID`。不要把次数耗尽当成需要自动重新起草的程序失败，它已经达到本次运行的返工上限。

## 本地验证与边界

验证脚本仅把 Codex 注册项临时替换为 `tests/model_stub.py`，通过 finally 恢复原配置；渠道仍执行给定的 fixture。请在定时器停止时运行测试，避免临时注册影响其他运行。

```sh
python3 tests/validate_local.py
python3 tests/validate_trigger.py
```

已验证：workflow 与 executor 校验、trigger 校验、doctor、dry-run（成功退出码 10）、两项必填输入缺失的拒绝；happy、sad、一次返工后通过、两次返工后通过、第三次返工请求导致 max_iterations、非法审核 signal 导致 schema 失败。替身断言了首轮 null、每次最新 note、每轮渠道的收件人和提案、最终步骤收到最新提案与审核意见。

已验证触发器通过 fire 创建成功运行，短暂启动 serve 完成运行测试，相同 delivery ID 复用同一 run；测试 worker 已停止。报告与逐次 CLI 输出位于 `tests/evidence/`，总表为 `report.json`、`preflight.report.json`、`trigger.report.json`。

未验证：真实 Codex 调用与认证、模型输出质量，以及持续运行到真实工作日 09:15 的墙钟触发。cron 表达式和时区已经配置校验；手动 fire 验证了触发分发，不代表已经等到真实定时时刻。没有调用真实模型、联网或发送真实消息，交付的模型节点保留 Codex 执行器。
