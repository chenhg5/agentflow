# AgentFlow Hub

Hub 把多台机器上的 agentflow 运行汇到一个地方看：一个网页控制台（总览、运行、运行详情、Workflow、机器）和一组只读 JSON 接口。执行仍然完全在本地，Hub 只收运行的快照，不调度、不执行，挂了也不影响任何运行。

```text
机器 A: agentflow run / trigger serve ──┐  POST /api/v1/reports
机器 B: agentflow run / trigger serve ──┼──────────────────────────▶  agentflow hub serve  ──▶  网页 / 读接口
CI:     agentflow run start ────────────┘   (Bearer token, 批量, 幂等)       (SQLite)
```

## 1. 快速开始

在要放 Hub 的机器上：

```bash
export AGENTFLOW_HUB_TOKEN=$(openssl rand -hex 24)
agentflow hub serve --host 0.0.0.0 --port 8787          # 数据默认在 ~/.agentflow/hub/hub.db
```

在每台跑 workflow 的机器上（或写进 `~/.agentflow/hub.json`、项目的 `.agentflow/hub.json`）：

```bash
export AGENTFLOW_HUB_URL=http://hub.example:8787
export AGENTFLOW_HUB_TOKEN=<同一个 token>
export AGENTFLOW_MACHINE=glenn-mbp                      # 可选，默认主机名
agentflow hub push --all                                # 一次性补报已有的运行
agentflow hub status --format json                      # 看配置、待发数量、上次发送结果
```

之后 `run start / approve / reject / resume / cancel` 和 `trigger fire / serve` 会自动上报，不需要改 workflow。

配置按字段覆盖，后面的赢：`~/.agentflow/hub.json` → `<项目>/.agentflow/hub.json` → 环境变量 `AGENTFLOW_HUB_URL` / `AGENTFLOW_HUB_TOKEN` / `AGENTFLOW_MACHINE`。

```json
{ "url": "http://hub.example:8787", "token": "…", "machine": "build-01" }
```

## 2. 上报怎么做到「不拖累运行」

- **在引擎外面挂钩子。** `store.SaveState` 每次落盘后通知订阅者，telemetry 订阅它。引擎、审批、取消、trigger worker 不知道 Hub 的存在，以后换引擎（比如代码编排）只要还落状态就会被报上去。
- **先写本地发件箱，再发。** 每次保存把这次运行的最新快照写进 `.agentflow/outbox/<run-id>.json`（只留最新的一份），不碰网络。后台每 10 秒发一次，命令结束时再给 2 秒发一次。发不出去就留着，下次命令或 `hub push` 再发；Hub 宕机期间的运行一条不丢。
- **快照幂等。** 每份快照带 `seq`（`updatedAt` 的纳秒），Hub 只留 seq 最大的那份；重发、乱序、补报都安全。
- **只读命令不联网。** `run list / get / stats`、`web` 等不装钩子，不会因为 Hub 慢而变慢。
- **离开机器前脱敏。** 错误、`summary`、`reason`、输入里像令牌的串（`sk-…`、`ghp_…`、JWT、私钥、`Bearer …`、`token=…` 等）先打码；名字像密钥的输入整值打码；错误截 2000 字，summary/reason 截 500 字，输入最多 30 个、每个 300 字。stdout/stderr、结果文件全文、产物不上报，只报字节数。

## 3. 上报协议（v1）

`POST /api/v1/reports`，`Authorization: Bearer <token>`，一批最多 100 份：

```json
{
  "reports": [{
    "protocol": 1,
    "source": { "machine": "build-01", "hostname": "build-01.local", "project": "repo", "path": "/src/repo", "version": "v0.0.4" },
    "orchestration": "json",
    "runId": "run_20261106_…", "workflow": "nightly-report", "workflowDigest": "4b1d…",
    "status": "failed", "seq": 1793935958054406025,
    "createdAt": "…", "startedAt": "…", "finishedAt": "…", "updatedAt": "…",
    "durationMs": 207, "waitingMs": 0,
    "trigger": { "id": "nightly", "kind": "cron", "deliveryId": "…", "scheduledAt": "…" },
    "inputs": { "issue": "ABC-1" },
    "error": "probe: command exited with code 3",
    "steps": [{ "id": "probe", "type": "shell", "provider": "", "status": "failed", "attempts": 2,
                "startedAt": "…", "finishedAt": "…", "durationMs": 1, "waitingMs": 0,
                "error": "…", "summary": "…", "reason": "…", "outputBytes": 0, "logBytes": 42 }]
  }]
}
```

返回 `{"accepted": n, "stale": n, "rejected": ["run-id: 原因"]}`。`stale` 是比已存的旧的快照，`rejected` 是不合法的（缺 machine / runId / workflow、状态不认识、协议版本不对）；一份坏的不影响同批其它的。

**保留字段 `summary` / `reason`：** 节点结果 JSON 里的 `summary`（做了什么）和 `reason`（为什么这样判断）会原样（脱敏、截断后）显示在运行详情的步骤下面。想让 Hub 上一眼看懂一步做了什么，就在结果里写这两个字段。

**状态：** `pending`、`queued`、`running`、`waiting_human`、`succeeded`、`failed`、`cancelled`、`skipped`（步骤）。

## 4. 兼容以后的代码编排

协议和 Hub 都不认识 JSON workflow 本身，只认「一次运行 + 有序的步骤」：

- `orchestration` 现在是 `json`，代码编排的 runner 报 `code`；页面上会标出来，其余完全一样。
- 步骤是一个有序列表，`id` 是步骤名。JSON 编排按 workflow 里的节点顺序报；代码编排按步骤第一次出现的顺序报，循环、foreach、子流程里的步骤用带下标的 id（如 `review[2]`、`deploy/notify`）。
- `workflowDigest` 对 JSON 是定义的哈希，对代码可以是源码或构建的哈希，用来按版本比较。
- 代码编排的 runner 只要实现「状态变化时产出一份 RunReport」，复用同一个发件箱和发送器（`internal/telemetry` 里与引擎有关的只有 `adapter.go`）。

## 5. Hub 内部结构

按 DDD 分层，依赖只朝里：

| 包 | 职责 | 依赖 |
| --- | --- | --- |
| `internal/hubapi` | 线上协议（runner 和 Hub 共用的唯一契约） | 无 |
| `internal/hub/domain` | 运行、步骤、机器、状态；校验和「谁新」的规则 | 无 |
| `internal/hub/app` | 用例：`Ingest`（收报告）、`Query`（总览、列表、详情、统计）；存储端口 `Store` | domain |
| `internal/hub/store/sqlite`、`memory` | 端口的实现；`storetest` 是每个实现都要过的契约测试 | app, domain |
| `internal/hub/api` | JSON 接口和鉴权 | app |
| `internal/hub/ui` | 服务端渲染的页面（`html/template` + 内嵌 CSS，样式取自 Loops 控制台，不需要前端构建） | app, api |
| `internal/hub` | 组装：按 DSN 选存储、挂路由、安全头 | 以上 |
| `internal/telemetry` | runner 侧：配置、适配、脱敏、发件箱、发送 | hubapi, model, store |

换数据库：实现 `app.Store`、跑通 `storetest.Run`、在 `hub.OpenStore` 里加一个 DSN 前缀。仓储只做过滤和分页，聚合在 `app.Query` 里算，所以不依赖数据库特有的功能。

SQLite 用纯 Go 驱动（`modernc.org/sqlite`），二进制仍然不需要 cgo，所有发布平台照常交叉编译。

## 6. 访问控制

| Hub 监听 | 上报 | 看页面 / 读接口 |
| --- | --- | --- |
| 本机（默认 `127.0.0.1`） | 设了 `AGENTFLOW_HUB_TOKEN` 就要 token，没设就不要 | 不要 |
| 其它地址 | 必须设 `AGENTFLOW_HUB_TOKEN`，否则拒绝启动 | 要：浏览器弹登录框，用户名随意，密码填 `AGENTFLOW_HUB_VIEW_TOKEN`（没设就用上报 token）；接口用 `Authorization: Bearer` |

`/healthz` 不鉴权。Hub 没有自带 TLS，跨网络使用时放在反向代理或内网后面。

## 7. 读接口

| 路径 | 说明 |
| --- | --- |
| `GET /api/v1/overview?days=14` | 总数、成功率、每天的运行、等人处理、最近失败、各 workflow 统计、机器数 |
| `GET /api/v1/runs?workflow=&machine=&project=&status=a,b&since=&until=&limit=&offset=` | 运行列表，新的在前，带 `total` |
| `GET /api/v1/runs/{machine}/{runId}` | 一次运行和它的步骤 |
| `GET /api/v1/workflows?days=30` | 各 workflow 的次数、成功率、P50/P95、最近结果 |
| `GET /api/v1/machines` | 上报过的机器 |

运行 id 只在一台机器上唯一，所以运行的主键是 `machine + runId`。

## 8. 还没做

- 日志、产物上传；跳回运行所在机器的 `agentflow web --run`。
- 从 Hub 审批 human 节点（需要把决定送回 runner，留到有双向通道时）。
- 数据保留策略和 Postgres 存储。
