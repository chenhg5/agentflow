# 工厂质检纯程序工作流

交付入口是 `workflow.json`，节点角色映射见 `submission.json`。执行器注册在 `.agentflow/executors.json`，直接运行原有 `fixtures/measure.py` 和 `fixtures/finish.py`，无需新增业务程序。

## 执行

在本目录执行，依赖本地 `python3` 和 `./agentflow`：

```bash
./agentflow workflow validate workflow.json --check-executors --format json
./agentflow doctor --format json
./agentflow run start workflow.json --input 'sample=批次 A 01' --format json
```

`sample` 是必填字符串。两个自定义执行器都配置了 `command`、`workspace: "."` 和 `env.QUALITY_SITE: "plant-7"`。相对路径按当前调用目录解析，因此应从交付目录启动。程序读取 `AGENTFLOW_INPUT_FILE`，写入 `AGENTFLOW_RESULT_FILE`，正常退出后由引擎自动推进。所有节点都没有 `prompt`，也不调用模型。

## 推进与分支

入口为 `measure`，运行 `fixtures/measure.py`。它的结果通过 JSON Schema 校验为 `score: number`、`defects: string[]`、`urgent: boolean`。

`measure` 的出边按以下顺序匹配，条件全部在图中：

1. `score > 80 && len(defects) == 0`：进入 `accept`。
2. 否则 `urgent == true || score >= 50`：进入 `recheck`。
3. 否则进入 `$end`，正常成功结束。

`accept` 和 `recheck` 使用同一个自定义程序执行器运行 `fixtures/finish.py`。每个节点的输入通过精确模板引用测量结果，保留数字、数组和布尔类型；另传入该节点固定的 `branch`。输出契约要求对应的 `branch` 为 `accept` 或 `recheck`。完成后自动进入 `$end`，不需要 `run submit`。

高分、无缺陷且 `urgent=true` 时仍进入 `accept`，因为第一条匹配边优先。图中没有循环。

查询运行及日志：

```bash
./agentflow run get <run-id> --format json
./agentflow run logs <run-id> --format ndjson
```

若程序失败，先检查节点错误和日志，再修复输入、环境或程序。重试失败节点使用 `./agentflow run retry <run-id> --format json`；恢复中断运行使用 `./agentflow run resume <run-id> --format json`。执行器配置的更改会影响后续重试/恢复。不要用 `submit` 推进这些程序节点。

## 本地确定性验证

`fixtures/measure.py` 默认输出 `{"score":81,"defects":[],"urgent":false}`。可通过继承的 `BENCH_MEASUREMENT` 环境变量覆盖测量结果，以下命令实跑 `recheck`：

```bash
BENCH_MEASUREMENT='{"score":49,"defects":["scratch"],"urgent":true}' ./agentflow run start workflow.json --input 'sample=local-recheck' --format json
```

重现完整验证：

```bash
python3 verify.py
```

最后一次验证的 17 项检查全部通过，证据在 `validation/summary.json`，每条命令的 stdout/stderr 在同目录保存。运行状态、每次节点访问的输入、结果和日志保存在 `.agentflow/runs/`。

已验证：

- 工作流和注册执行器校验通过，本地 Python 执行器可用；dry-run 退出码为预期的 `10`。
- 10 个有效测量案例真实运行：accept、recheck、直接结束；80 和 50 的边界；80.1/49.9 小数；高分有缺陷；低分紧急；负数紧急；accept 优先于紧急条件。
- 后续节点收到并返回相同的数字、字符串数组、布尔值；Unicode 和含空格的 sample 正确传给 measure。
- `finish.py` 的 `QUALITY_SITE=plant-7` 检查通过。
- 缺少必填 sample 被拒绝；字符串 score、含数字的 defects 数组、字符串 urgent 均在 measure 输出契约校验处失败，未启动分支程序。

未验证：进程中断后的恢复、超时与执行器异常后的重试。没有调用真实模型、网络服务或发送消息。该图没有返工或次数限制，循环相关检查不适用。
