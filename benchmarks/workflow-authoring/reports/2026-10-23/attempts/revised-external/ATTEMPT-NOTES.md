# Attempt notes

工作严格限于 `/tmp/agentflow-bench-revised-external`。先阅读 TASK.md 和 skill/SKILL.md，再查询本地 `schema --all`、`schema workflow`、`schema executors`，并检查 doctor。未读取其他尝试、源码仓库或参考答案，未联网、调用真实模型或发送真实消息。

采用内置 external，避免启动外部通知或交互程序。review 的 mood/feedback/confidence 保留原名，并通过严格 JSON Schema 约束。正式图不需要自定义执行器；测试注册项仅把测试副本模型节点替换成本地确定性程序。

## 观察到的错误和修正

- 首次本地验证脚本误把 stop 后未执行的 finalize 预期为 `pending`。实际 run 为 `succeeded` / `completed`，finalize 是 `skipped` / `not_selected`。查看真实状态后修正测试断言为 skipped；工作流无需修改。
- 缺 topic 的负面验证返回退出码 2、`missing_input`，错误 JSON 位于 stderr。预期行为。
- 13 个不合契约的提交均返回退出码 2、`invalid_output`：空对象、未知 mood、decision 代替 mood、缺 confidence、缺 feedback、feedback 非字符串、confidence 字符串、confidence 布尔值、低于 0、高于 1、额外字段、数组、null。重新 get 证明 run 仍停在 review，访问次数不变。预期行为。
- 第三次请求返工返回退出码 1，持久状态为 `failed` / `max_iterations`，draft 总访问数仍为 3。预期的两次返工上限；这不是 stop 分支。
- 成功的 run start --dry-run 返回退出码 10。未把 dry-run 当作完整实跑。
- 没有遇到未知 schema、接口不兼容或执行器注册失败。

## 验证证据

`python3 tests/verify.py` 完整通过，51 次独立 CLI 调用，所有业务分支与契约负面案例均有断言。`validation/summary.json` 为摘要，`validation/commands.json` 保存完整命令、退出码、stdout/stderr，`.agentflow/runs/` 保存各次 run 和 artifacts。另用 `submit_review.py` 在新的进程中读取本地 JSON 文件并完成 stop 提交，记录 `validation/helper.json`。

正式 workflow.json 的 draft/finalize 保留 codex；只对它执行 validate 与 dry-run。模型执行与模型中断恢复未实测，未进行真实模型调用。持久等待、独立进程重新加载和后续推进均通过本地替身实跑。
