# 可持久暂停的文案工作流

交付入口为 `workflow.json`，角色映射为 `submission.json`。`draft` 和 `finalize` 使用内置 `codex`；`review` 使用内置 `external`，到达后保存 `waiting_input` 并退出 CLI。外部审核由另一个终端提交 JSON，无需通知程序、监听服务或常驻进程。

`revision` 是内置 `process` 节点，执行 `executors/revision.py`。只需标准 Python 3，无需自定义 executor 注册配置。它从上一轮 review 的已持久化输入计算返工次数，不自行读写 run state。

## 路由和契约

`topic` 是必填、非空的字符串运行输入，在入口节点的 inputSchema 验证；缺失或空字符串会在模型步骤之前失败。运行输入模板使用 `{{ inputs.topic }}`。

起草输出是严格对象 `{"summary":"文案","check":"自查"}`；整理输出是严格对象 `{"next_step":"行动","verification":"核验"}`。二者的字段均必填且为字符串，拒绝额外字段。

审核的 outputSchema 嵌在 `review` 节点中，也单独保存在 `validation/review-output.schema.json`：

```json
{
  "type": "object",
  "additionalProperties": false,
  "required": ["mood", "feedback", "confidence"],
  "properties": {
    "mood": {"type": "string", "enum": ["go", "back", "stop"]},
    "feedback": {"type": "string"},
    "confidence": {"type": "number", "minimum": 0, "maximum": 1}
  }
}
```

`mood=go` 且 `confidence>=0.8` 才进入整理；`back` 不论置信度、以及低置信度的 `go` 都带 feedback 和上一轮文案返回起草。最多返回两次，即最多三轮起草/审核。两次返工后再次请求返工则正常结束，不进入整理。`stop` 在任意轮次正常结束，状态为 `succeeded`，不会当成执行异常。

循环边同时设置 `maxIterations: 2` 作为保护。显式返工计数条件先限制次数，避免将正常的次数用尽变成引擎的 `max_iterations` 失败。JSON 的业务字段保持为 `mood`。

## 执行

在此目录操作，保留整个 `.agentflow/runs/` 目录，它保存工作流快照、状态、事件和每次访问的输入/结果 artifacts。

```sh
cd /tmp/agentflow-bench-baseline-external
./agentflow workflow validate workflow.json --format json
./agentflow run start workflow.json --input 'topic=给初次养猫的人写一段产品文案' --format json
```

上面的正式 start 会使用本机配置的 Codex。记录返回的 `run_id`。完成起草后返回值应为 `status=waiting_input`、`active_node=review`。首次起草的 feedback 和 previous_summary 为 null，提示中的对应文本为空；返工时使用上次审核和文案。

## 从独立终端推进

以下 `RUN_ID` 改为实际 start 返回值。原来的启动进程可以已经退出。审核员可先 get 查看 `nodes.review.input.summary`、`check` 和 `reworks`。

```sh
cd /tmp/agentflow-bench-baseline-external
RUN_ID='run_实际返回的ID'
./agentflow run get "$RUN_ID" --format json
./agentflow run submit "$RUN_ID" review --output '{"mood":"go","feedback":"可以整理。","confidence":0.8}' --format json
```

也可由独立 Python 程序读取任意 JSON 文件并提交，schema 由引擎统一校验：

```sh
python3 submit_review.py "$RUN_ID" review-back.json
```

示例文件另有 `review-go.json`、`review-stop.json`。每次只提交当前等待中的审核一次：返工提交后会自动执行起草并再次暂停；高置信度 go 后会自动执行整理；stop 后直接成功结束。无效 JSON 契约输出会被拒绝，run 仍在等待，不会消耗返工次数。不要给已经结束的 run 重复提交。

重新启动终端后，使用同一个目录和 run ID 即可读取并提交。`resume` 可恢复中断的执行；在外部审核等待处调用它仍会等待审核，不会伪造审核结果：

```sh
./agentflow run get "$RUN_ID" --format json
./agentflow run resume "$RUN_ID" --format json
./agentflow run submit "$RUN_ID" review --output '{"mood":"stop","feedback":"终止本次内容制作。","confidence":0}' --format json
```

## 已验证与边界

运行 `python3 validation/verify.py` 可重建测试副本并复验。测试副本仅将两个模型节点替换为本地确定性 Python 程序，审核、计数、条件边、schema 校验和 run 持久化仍使用真实二进制。正式 workflow 保留 `codex` executor。

已验证 10 项案例、54 次 CLI 调用：必填/空 topic 拒绝；start 进程退出后由新进程 get/resume/submit 成功推进；0.8 门槛；stop 正常结束；低置信度和 back 返工及上下文传递；最多两次返工；最后一轮仍可批准；11 种错误审核输出被拒绝且继续等待。另已实测 `submit_review.py` 从文件提交 stop。结果及逐条命令见 `validation/results.json`、`validation/commands.json`、`validation/helper-check.json`。

未调用真实 Codex/Claude，未验证模型内容质量、模型 CLI 安装和认证，也未模拟正在执行的模型进程崩溃。未联网、调用外部服务或发送真实消息。
