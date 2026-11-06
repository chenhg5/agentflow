# 本地产品研究工作流

交付入口是 `workflow.json`；角色映射在 `submission.json`。所有命令应在本目录执行。

## 文件和契约

- `workflow.json`：AgentFlow version 2 图，入口 `draft`。
- `.agentflow/executors.json`：`codex` 使用内置 Codex 适配器；`local-review-channel` 直接启动 `python3 ./fixtures/channel.py`，workspace 为当前目录。
- `.agentflow/triggers.json`：已启用的 `weekday-research-review`，`15 9 * * 1-5`，时区 `Asia/Shanghai`，即周一至周五 09:15。
- `checks/verify.py`：不调用模型的可重复验证程序；`checks/validation-results.json` 保存结果，`checks/validation-commands.ndjson` 保存逐条实际 CLI 命令与退出码。

运行输入 `topic`、`reviewer` 都是必填字符串，节点输入契约还要求非空。定时运行由 trigger 顶层 `inputs` 提供这两个值；修改其中的示例主题和审核人即可配置团队日常任务。

| 节点 | 输入 | 输出 |
| --- | --- | --- |
| `draft` / Codex | `topic`、`reviewer`、`review_note`、`prompt`。首次 note 为 null，返工时为最新审核 note；prompt 同时包含该意见。 | `{ "summary": "非空摘要", "check": "非空自查" }` |
| `review` / 本地渠道 | `recipient` 为运行输入 reviewer；`content` 包含 topic 和最新 summary/check；`actions` 是三个 `{label, output}` 对象，output 包含 signal/note。 | `{ "signal": "happy 或 again 或 sad", "note": "字符串，可空" }` |
| `finalize` / Codex | `topic`、最新完整 `proposal`、最新完整 `review`、包含摘要、自查、审核 signal/note 的 `prompt`。 | `{ "next_step": "非空行动", "verification": "非空验证方法" }` |

各节点都配置了 JSON Schema 输入和输出契约、300000ms 超时和一次执行尝试。Codex 适配器从 `prompt` 读取任务，因此业务上下文也写进 prompt。

## 推进和返工

`draft → review`。审核 happy 自动执行 finalize；sad 正常完成 run，finalize 不运行；again 把最新 note 带回 draft。返工边 `maxIterations: 2`，最多产生三份提案。第三次审核仍为 again 时，引擎以 `failed / max_iterations` 停止，不运行 finalize。这个上限是业务返工次数，与执行器失败重试不同。

渠道程序直接读取 `AGENTFLOW_INPUT_FILE`，把审核 JSON 写到 `AGENTFLOW_RESULT_FILE` 后退出。AgentFlow 随即校验结果并自动推进；不需要手工 `run submit` 或另起扫描 workflow 的服务。`happy/again/sad` 只是契约字段，由显式条件边解释。

`fixtures/channel.py` 是本地模拟程序，默认返回 happy 和 `local test`。可通过 `BENCH_RESPONSES` 设置 JSON 响应数组，通过 `BENCH_CHANNEL_COUNTER` 指定计数文件；响应依次使用，超出数组后重复最后一项。这些变量应设置在启动 run/定时器的进程环境中。

## 执行和启动定时器

先检查配置：

```sh
./agentflow workflow validate workflow.json --format json
./agentflow trigger validate --format json
./agentflow trigger list --format json
```

手动启动实际流程（会调用已安装并授权的 Codex）：

```sh
./agentflow run start workflow.json \
  --input 'topic=改进新用户引导' \
  --input 'reviewer=local-product-reviewer' \
  --format json
```

编辑 `.agentflow/triggers.json` 的顶层 `inputs.topic` 和 `inputs.reviewer` 后，在本目录持续运行定时器：

```sh
./agentflow trigger serve --host 127.0.0.1 --port 0
```

保持该进程运行，并让机器在计划时间处于唤醒状态。它负责 cron 调度和 run worker，工作流自己负责审核后的推进。停止前台进程可用 Ctrl-C。无需另一个渠道扫描服务。

需要人工触发一次计划事件时：

```sh
./agentflow trigger fire weekday-research-review \
  --scheduled-at 2026-10-26T09:15:00+08:00 \
  --delivery-id manual-check-20261026 \
  --format json
```

相同 delivery ID 用于同一事件的安全重发；新事件使用新的 ID。该命令也会执行实际模型流程。

## 查看和恢复

```sh
./agentflow run get <run-id> --format json
./agentflow run logs <run-id> --format ndjson
./agentflow run metrics <run-id> --format json
```

中断的运行可用 `./agentflow run resume <run-id> --format json` 恢复。先查看错误和日志，再对可重试失败使用 `./agentflow run retry <run-id> --dry-run --format json` 检查影响范围，随后去掉 `--dry-run` 重试。达到返工上限后应评估是否发起新的 run。渠道模拟器会在执行时推进计数文件。

## 已验证和未验证

执行以下命令可重复本地验证：

```sh
python3 checks/verify.py
```

验证程序只把 workflow 副本中的两个 Codex 节点改为 external，并以确定性 JSON 提交代替模型；审核仍运行真实的本地 fixture。已验证 workflow/trigger 校验、缺失必填输入被拒绝、happy、sad、一次返工后 happy、两次返工后 happy、第三次 again 达到上限、非法审核 signal 被拒绝，以及最新意见传回起草、最新提案和意见传给 finalize。还验证了同一 cron 配置的手工 fire、必填值注入、09:15 上海时间被记录为 01:15 UTC、重复 delivery ID 不创建新 run。

验证时临时将 trigger 目标改成 external 副本，结束后已恢复原配置；产生的等待测试 run 已取消。没有调用真实 Codex/Claude、外部服务或发送真实消息。未验证真实模型输出质量、真实渠道和机器跨日唤醒/长期运行；没有等待到实际 09:15，也没有留下常驻定时器。
