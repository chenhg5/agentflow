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
agentflow hub serve --host 0.0.0.0 --port 7075          # 数据默认在 ~/.agentflow/hub/hub.db
```

在每台跑 workflow 的机器上（或写进 `~/.agentflow/hub.json`、workflow 工作区里的 `hub.json`）：

```bash
export AGENTFLOW_HUB_URL=http://hub.example:7075
export AGENTFLOW_HUB_TOKEN=<同一个 token>
export AGENTFLOW_MACHINE=glenn-mbp                      # 可选，默认主机名
agentflow hub push --all                                # 一次性补报已有的运行
agentflow hub status --format json                      # 看配置、待发数量、上次发送结果
```

之后会改动运行的命令（`run start / enqueue / worker / submit / resume / retry / cancel / evaluate`、`trigger fire / serve`）会自动上报，运行刚创建（包括排队）时也会报一次，不需要改 workflow。

配置按字段覆盖，后面的赢：`~/.agentflow/hub.json` → `<工作区>/hub.json` → 环境变量 `AGENTFLOW_HUB_URL` / `AGENTFLOW_HUB_TOKEN` / `AGENTFLOW_MACHINE`。

项目里的 `hub.json` 跟着仓库走，不可信：**当 Hub 地址来自项目文件时，只会带上同一个文件里的 token**，你在 `~/.agentflow/hub.json` 或环境变量里的 token 不会发给它。这样克隆一个仓库不会把你的 token 引到别人的地址上。

```json
{ "url": "http://hub.example:7075", "token": "…", "machine": "build-01" }
```

## 2. 上报怎么做到「不拖累运行」

- **在引擎外面挂钩子。** `store.SaveState` 每次落盘后通知订阅者，telemetry 订阅它。引擎、审批、取消、trigger worker 不知道 Hub 的存在，以后换引擎（比如代码编排）只要还落状态就会被报上去。
- **先写本地发件箱，再发。** 每次保存把这次运行的快照写进 `<工作区>/hub-outbox/<run-id>.<seq>.json`（先写临时文件再改名，同一运行的旧快照随即删掉），不碰网络。发完只删 seq 不大于已发那份的文件，所以引擎在发送途中写下的新快照（比如最后的 succeeded）不会被误删；多个 agentflow 进程同时动同一个运行也一样。后台每 10 秒发一次，命令结束时再给 2 秒发一次。发不出去就留着，下次命令或 `hub push` 再发；Hub 宕机期间的运行一条不丢。
- **快照幂等。** 每份快照带 `seq`（`updatedAt` 的纳秒），Hub 只留 seq 最大的那份；重发、乱序、补报都安全。
- **只读命令不联网。** `run list / get / stats`、`web` 等不装钩子，不会因为 Hub 慢而变慢。
- **离开机器前脱敏。** 错误、`summary`、`reason`、输入里像令牌的串（`sk-…`、`ghp_…`、`glpat-…`、`sk_live_…`、`npm_…`、`AIza…`、AWS key、JWT、私钥、`Bearer …` / `Basic …`、`token=…`、URL 里的 `user:password@` 等）先打码；名字像密钥的输入整值打码；错误截 2000 字，summary/reason 截 500 字，输入最多 30 个、每个 300 字。stdout/stderr、结果文件全文、产物不上报，只报字节数。

## 3. 上报协议（v1）

`POST /api/v1/reports`，`Content-Type: application/json`，`Authorization: Bearer <token>`，一批最多 100 份：

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

**流程图 `graph`（可选）：** `entry`、`nodes`（`id`、候选执行器 `executors`、`parallel`、`join`）、`edges`（`source`、`target`、`condition`、`maxIterations`、`parallel`；`target` 为 `$end` 表示结束）。Hub 按 `workflow@workflowDigest` 只存一份，运行详情页用它画 React Flow 画布（React Flow 从 esm.sh 加载，离线时画布提示加载不到，步骤列表照常）。补报已有的运行时，就算快照本身是旧的，图也会补上。

**保留字段 `summary` / `reason`：** 节点结果 JSON 里的 `summary`（做了什么）和 `reason`（为什么这样判断）会原样（脱敏、截断后）显示在运行详情的步骤下面。想让 Hub 上一眼看懂一步做了什么，就在结果里写这两个字段。

**状态：** `pending`、`queued`、`running`、`waiting_input`（等人提交表单或外部输入；旧版本的 `waiting_human` 同样处理）、`waiting_resource`（等共享资源）、`succeeded`、`failed`、`cancelled`、`skipped`（步骤）。

**步骤的 `type`** 是这一步用的执行器（`codex`、`claude-code`、`process` 或 `executors.json` 里注册的名字）：选中候选后报选中的那个，还没开始时报第一个候选。

## 4. 代码编排

除了 JSON 工作流，也可以让脚本自己决定步骤：`agentflow code`。脚本用什么语言都行，调 CLI 就是 SDK。

```bash
agentflow code run nightly-triage --input repo=agentflow -- ./nightly.sh
```

`nightly.sh` 里：

```bash
count=$(agentflow code step fetch -- ./fetch.sh | jq .count)        # 跑一个命令当一步
for i in $(seq 1 "$count"); do
  agentflow code step triage --summary "第 $i 条" -- ./triage.sh "$i"  # 同一步再跑是新的一次
done
agentflow code step lint -- make lint &                              # 并行的步骤
agentflow code step test -- make test &
wait
agentflow code step decide --output '{"ship":true}' --summary "发布"  # 脚本自己算完的一步，直接记结果
```

- `code run` 开一次运行，把 `AGENTFLOW_RUN_ID` 交给脚本，脚本退出码决定运行成败（脚本自己调了 `code end` 就以它为准）。脚本中途退出时还在跑的步骤记为失败。
- `code step <id> -- <命令>` 和执行器的约定一样：命令可以往 `$AGENTFLOW_RESULT_FILE` 写一个 JSON 对象；stdout / stderr 记进日志，同时显示在脚本的 stderr；非零退出就记为失败并原样返回退出码；结果 JSON 打印到 stdout，方便 `$(...)` 拿。`--summary` / `--reason` 是 Hub 上显示的说明。
- 不带命令的 `code step <id> --output JSON [--status failed|skipped]` 记录脚本自己做完的一步。
- 不在 `code run` 里时，用 `code start <name>` 拿运行 ID，传 `--run` 或设 `AGENTFLOW_RUN_ID`，最后 `code end`。

运行和 JSON 工作流存在同一个地方（`runs/<id>/`，workflow 版本是 `code`），`run list / get / metrics` 和 Hub 都认。上报时 `orchestration` 是 `code`，页面上标「代码编排」。流程图按脚本实际走到的顺序长出来：一步接上「上次有步骤开始之后完成的那些步骤」，所以先后执行的是一条链，一起开始的共享前驱，之后的一步把它们汇合。每次改动都在运行目录的系统文件锁里做，脚本并行跑的步骤不会互相覆盖。`run resume / retry` 不会去执行代码运行。

## 5. Hub 内部结构

按 DDD 分层，依赖只朝里：

| 包 | 职责 | 依赖 |
| --- | --- | --- |
| `internal/hubapi` | 线上协议（runner 和 Hub 共用的唯一契约） | 无 |
| `internal/hub/domain` | 运行、步骤、机器、状态；校验和「谁新」的规则 | 无 |
| `internal/hub/app` | 用例：`Ingest`（存运行）、`Query`（总览、列表、详情、统计）；存储端口 `Store` | domain |
| `internal/hub/store/sqlite`、`memory` | 端口的实现；`storetest` 是每个实现都要过的契约测试 | app, domain |
| `internal/hub/api` | JSON 接口和鉴权；把协议翻译成领域对象（防腐层，`translate.go`） | app, domain, hubapi |
| `internal/hub/ui` | 服务端渲染的页面（`html/template` + 内嵌 CSS，样式取自 Loops 控制台，不需要前端构建） | app, domain, api |
| `internal/hub` | 组装：按 DSN 选存储、挂路由、安全头 | 以上 |
| `internal/telemetry` | runner 侧：配置、适配、脱敏、发件箱、发送 | hubapi, model, store, metrics, version |

协议只在 `api` 和 `telemetry` 两头出现；领域和用例不认识它，协议升版本时只改这两处的翻译。

换数据库：实现 `app.Store`、跑通 `storetest.Run`、在 `hub.OpenStore` 里加一个 DSN 前缀。仓储只做过滤和分页，聚合在 `app.Query` 里算，所以不依赖数据库特有的功能。

SQLite 用纯 Go 驱动（`modernc.org/sqlite`），二进制仍然不需要 cgo，所有发布平台照常交叉编译。

## 6. 访问控制

| Hub 监听 | 上报 | 看页面 / 读接口 |
| --- | --- | --- |
| 本机（默认 `127.0.0.1`） | 设了 `AGENTFLOW_HUB_TOKEN` 就要 token，没设就不要 | 不要 |
| 其它地址 | 必须设 `AGENTFLOW_HUB_TOKEN`，否则拒绝启动 | 要：浏览器弹登录框，用户名随意，密码填 `AGENTFLOW_HUB_VIEW_TOKEN`（没设就用上报 token）；接口用 `Authorization: Bearer` |

`/healthz` 不鉴权。上报接口只收 `Content-Type: application/json`：网页不经 CORS 预检发不出这种请求，而 Hub 从不放行预检，所以你浏览的网页没法往本机的 Hub 塞假运行。Hub 没有自带 TLS，跨网络使用时放在反向代理或内网后面。

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
