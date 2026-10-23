# 农场本地工作流

在本目录执行；需要 Python 3、Node.js 和已提供的 `./agentflow`。无需安装第三方包、联网或配置模型。

文件：`workflow.json` 定义 v2 图及节点输入/输出 JSON Schema；`.agentflow/executors.json` 注册 `farm-collect` 和 `farm-report`；`executors/collect.py`、`executors/report.js` 为可运行程序；`submission.json` 提供角色映射。

## 执行

```sh
./agentflow workflow validate workflow.json --check-executors --format json
./agentflow run start workflow.json --input batch=harvest-001 --format json
```

`batch` 为必填字符串。两个 executor 的工作目录均为 `.`（相对于 CLI 调用目录），配置注入 `FARM_SITE=orchard-2`；两个程序均检查该值。程序读取 `AGENTFLOW_INPUT_FILE`，将 JSON 写到 `AGENTFLOW_RESULT_FILE`；不通过 stdout 传输数据。

## 推进与检查

启动后自动执行 `collect → report → $end`，无需 submit 或人工推进。collect 返回 batch、readings=[3,7,11]、meta={unit:"kg",valid:true}。report 通过精确模板接收这些字段，保持数组、对象、布尔类型，计算求和，输出：

```json
{"batch":"harvest-001","total":21,"unit":"kg","valid":true}
```

使用 start 返回的 run_id 查询：

```sh
./agentflow run get <run-id> --format json
./agentflow run logs <run-id> --format ndjson
```

若运行中断，用 `run resume <run-id> --format json` 恢复。若节点失败，修复程序或配置后使用 `run retry <run-id> --format json` 重试失败节点；每节点自动尝试次数设置为 1。

## 验证记录

已通过工作流及 executor 注册项校验。已实际启动完整本地流程，运行 `run_20261023090150_33f5974f0e1aa3fc` 状态为 `succeeded`、outcome 为 `completed`，两个节点各执行一次，report 输出 total=21、unit="kg"、valid=true，batch 原样保留；report 运行输入确认 readings 为数组、meta 为对象、valid 为布尔值。

已验证缺少 batch 时 CLI 拒绝启动；两个程序分别以错误 FARM_SITE 直接调用，均非零退出且未生成结果文件。

未验证进程中断后的 resume/retry，以及其他操作系统或运行时版本。未调用任何模型、网络服务或消息接口。
