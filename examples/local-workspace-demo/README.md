# 独立 workflow 工作区（目录规范 v1）

这个目录本身就是 workflow 工作区，可以复制成一个独立 Git 仓库。它调度外部项目、不同 worktree 和已有 agent，运行记录也归它所有。CLI 从本目录启动，直接读取根目录文件并写入 `runs/`。单条图直接用 `workflow.json`；多条图时可按需使用 `workflows/` 分类，引擎不要求目录名。

```text
my-workflow/
├── workflow.json             # 图：节点、边、会话策略、JSON 契约
├── executors.json                  # 名称 → 执行方式与工作目录
├── triggers.json                   # 本例：每天 09:00（Asia/Shanghai）
├── runs/                           # 生成：图快照、状态、输入输出、事件和日志
├── executors/
│   ├── agents/
│   │   ├── author/                 # 本 workflow 自带的 Codex agent
│   │   │   ├── AGENTS.md
│   │   │   ├── .agents/skills/
│   │   │   ├── .codex/config.toml
│   │   │   └── tools/
│   │   └── verifier/               # 另一个独立 agent
│   └── programs/report_writer/
│       └── main.py
├── lib/mcp_bridge/server.py         # 共享工具适配代码
├── data/source.md                  # 示例数据
└── output/                         # 生成的报告
```

`workflow.json`、`executors.json`、`triggers.json`、executor 代码和本地 agent 指令进入版本控制；`runs/` 与 `output/` 是运行产物。内置的 Codex/Claude/command 适配器随 AgentFlow 安装，工作区不用复制适配器实现。`agents/` 和 `programs/` 只是推荐分类，图通过注册表中的名称引用 executor。

## 复用已有 agent

agent 工作目录可以直接使用软链，不需要在这里重建一套 agent。例如已有 `/path/to/my-agent`：

```sh
mkdir -p executors/agents
ln -s /path/to/my-agent executors/agents/existing
```

在根目录 `executors.json` 注册：

```json
{
  "executors": {
    "existing": {
      "kind": "codex",
      "workspace": "executors/agents/existing"
    }
  }
}
```

`workspace` 也可以直接写外部绝对路径，无需建软链。复用目录时沿用其中的指令、skills 和工具配置，不自动覆盖。目录与会话分别关联；如果要继续已有会话，在图节点中显式配置：

```json
{
  "id": "continue-work",
  "execution": {"candidates": [{
    "executor": "existing",
    "session": {"mode": "fixed", "id": "existing-session-id"}
  }]}
}
```

省略 session 策略会创建新会话。`fixed` 加 `id` 会恢复该提供方保存的会话历史；这是会话恢复能力，尚不提供对正在运行的交互进程的接管或跨流程并发协调。已有会话的目录、账户及读写权限需要由其 executor 启动配置满足，不修改全局 Codex 配置。

## 工作区与项目仓库

workflow 工作区可以独立于所有业务仓库存在。具体 executor 的工作目录可以指向仓库、worktree 或 agent 目录。仓库 URL、分支、ref、commit、worktree 路径由 executor 的 config 或输入输出承载；执行器可以把实际使用的版本写入结果或 artifacts，随 run 保存。图引擎不自动 checkout 或捕获 Git 版本。

程序通过 `AGENTFLOW_ROOT` 获取 workflow 工作区，通过 `AGENTFLOW_WORKSPACE` 获取当前 executor 目录。输入输出使用引擎提供的绝对文件路径，因此 executor 不必与运行记录放在同一个仓库。

## 运行这个 demo

```sh
agentflow workflow validate workflow.json --check-executors
agentflow run start workflow.json --input topic=field-note
agentflow run list
```

定时运行需在本目录执行 `agentflow trigger serve` 并保持进程运行。它只监听本工作区 `triggers.json` 中的计划；配置文件本身不会启动服务。本例每天北京时间 09:00 启动一次，错过的时间不补跑，上次 run 尚活动时跳过。纯 cron 不占 HTTP 端口，同一工作区重复启动服务会被拒绝。多条图可以共用这个工作区的触发服务，也可以分工作区各自运行。

图是 `draft → verify → report → $end`。前两个节点从 `executors/agents/` 启动独立的 Codex 会话，加载自己的 `AGENTS.md`、skill 和 MCP 工具；最后一个节点从 `executors/programs/report_writer` 启动 Python 程序并写 `output/report.md`。

`AGENTS.md` 承载 Codex 的常驻项目指令，不等同于 API 的 system 角色消息。项目 `.codex/config.toml` 由 Codex 按其项目信任规则加载；用户级配置与全局工具仍可能叠加。本 demo 不修改全局 Codex 配置。

本机验证（2026-10-25）：run `run_20261024233905_16ef69466fafb67a` 的三个节点成功，两个 MCP 工具调用成功；状态、节点输入输出和日志直接位于 `runs/<run-id>/`。独立临时工作区的 CLI 测试还验证了通过软链进入已有 agent 目录并恢复固定 session（使用确定性程序替代模型传输）。
