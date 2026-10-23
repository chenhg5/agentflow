# 持久暂停的文案工作流

交付入口是 `workflow.json`；`submission.json` 将 draft/review/finalize 映射到同名节点。draft 与 finalize 保留内置 `codex` 执行器，review 使用内置 `external`。不需要通知程序，也不需要为正式流程注册自定义执行器。`.agentflow/executors.json` 中两个 `local-*` 注册项仅供测试副本使用。

## 正式运行与独立终端提交

在本目录执行：

```sh
cd /tmp/agentflow-bench-revised-external
./agentflow workflow validate workflow.json --check-executors --format json
./agentflow run start workflow.json --input 'topic=便携咖啡杯' --format json > run-start.json
python3 -c 'import json; print(json.load(open("run-start.json"))["run_id"])'
```

`topic` 必填。实际启动会调用 Codex 起草。起草成功后，启动命令退出，run 以 `waiting_input` 状态持久保存，`active_node` 为 `review`。记下输出中的真实 run ID。稿件和自检可用 `run get` 查看，位于 `nodes.review.input.summary` 与 `nodes.review.input.check`。

另开终端，回到同一目录，将 `run_id` 设为刚才的实际值：

```sh
cd /tmp/agentflow-bench-revised-external
run_id='替换为实际的 run ID'
./agentflow run get "$run_id" --format json
./agentflow run submit "$run_id" review --output '{"mood":"go","feedback":"审核通过","confidence":0.9}' --format json
```

也可以由本地独立程序读取任意 JSON 文件并提交，程序不修改业务字段：

```sh
python3 submit_review.py "$run_id" examples/review-go.json
```

将文件替换成 `examples/review-back.json` 或 `examples/review-stop.json` 可分别返工或正常结束。每次审核只提交一份结果；有效 submit 会立即按图继续执行，可能调用 Codex 整理，或者返工后再次暂停。无需另外运行通知程序、后台 worker 或 trigger 服务。

## 严格输出契约与路由

审核输出必须是 JSON 对象，且仅有以下三个必填属性：

| 属性 | 契约 |
| --- | --- |
| `mood` | 字符串，枚举 `go`、`back`、`stop` |
| `feedback` | 字符串，可为空 |
| `confidence` | JSON 数字，闭区间 `[0, 1]`；字符串或布尔值无效 |

`outputSchema` 使用 draft 2020-12，设置 `required` 和 `additionalProperties: false`。未知 mood、缺字段、额外字段、错误类型和越界值会被拒绝，run 继续等待；修正后再次 submit 即可。

| 审核结果 | 后续动作 |
| --- | --- |
| `go` 且 `confidence >= 0.8` | Codex finalize，输出字符串 `next_step` / `verification`，随后成功结束 |
| `back`，任意合法 confidence | 带 feedback 返回 draft |
| `go` 且 `confidence < 0.8` | 带 feedback 返回 draft |
| `stop`，任意合法 confidence | 直接成功结束，finalize 标为 skipped |

draft 输出严格对象 `{ "summary": "完整文案", "check": "自检说明" }`。每次返工的 feedback 同时放入 draft 的结构化 input 和 Codex prompt；第一次反馈为 null，inputSchema 明确允许 null。每轮 review 都收到本轮最新文案。finalize 收到本轮文案、自检及完整审核对象。

返工边设置 `maxIterations: 2`，两个返工分支共用这条边，所以总共最多两次返回（初稿加两轮修改，最多三次 draft）。用完两次后仍要求返工，run 以 `failed` / `max_iterations` 结束，不会执行第四次 draft；第三轮审核仍可 go 进入整理或 stop 成功结束。该边界行为与 stop 的正常结束互相独立。

## 持久化与重新加载

run、历史及逐次访问的 input/result/log artifacts 保存在本目录 `.agentflow/runs/`。保留该目录并在同一调用目录使用本地工具，即可关闭原进程和终端后重新读取、提交：

```sh
./agentflow run get "$run_id" --format json
./agentflow run resume "$run_id" --format json
./agentflow run submit "$run_id" review --output '{"mood":"stop","feedback":"结束本次工作","confidence":0}' --format json
./agentflow run logs "$run_id" --format ndjson
```

等待 external 时不必 resume；直接 submit 会重新加载并推进。对 waiting_input 的 resume 保留当前等待，不重复起草。若执行过程意外中断，可用 resume 恢复；实际模型节点执行失败后，用 `run get` 检查，再按需 `run retry` 重试失败节点。

## 本地验证范围

运行无模型验证：

```sh
python3 tests/verify.py
```

测试副本 `tests/workflow.local.json` 与交付图的业务输入、契约、边相同，仅将 Codex 节点换成 `tests/model_stub.py` 的确定性执行器。验证脚本在每条 CLI 命令后等待进程完全退出，下一条命令启动全新进程，覆盖退出、get 重新加载、resume 保持暂停以及随后 submit 推进。

已验证：正式图和测试图校验；dry-run 退出码 10；topic 必填；0.8 边界放行；低 confidence 返工；back 在 confidence=1 时仍返工；反馈传递；最新文案交接；两次返工后通过；第三次返工以 max_iterations 限制；stop 正常成功；13 种无效审核结果拒绝且不改变暂停状态。`validation/summary.json` 和 `validation/commands.json` 记录结果与 stdout/stderr/退出码。独立文件提交程序另外通过本地 stop 提交验证，记录在 `validation/helper.json`。

未验证：真实 Codex 调用、文案内容质量、真实模型执行中的强制终止恢复。本次未调用任何真实模型或外部服务；正式流程仅校验和 dry-run，实际分支执行全部使用本地替身。
