# AgentFlow

AgentFlow runs local workflows described by `entry`, `nodes`, and explicit `edges`. Each node calls a named executor with JSON input and produces JSON output. Executors can run Codex, Claude Code, an interactive program, or a program you write in any language.

Use a standalone workflow workspace to coordinate your repositories, agents, and programs. Run AgentFlow from that workspace: `workflow.json` defines a graph, `executors.json` registers execution entry points, and `runs/` holds state, snapshots, and logs. The runner validates input/output schemas, evaluates conditions, and advances the graph automatically. Workflow version `3` is the supported format.

## Install and try

```bash
curl -fsSL https://raw.githubusercontent.com/chenhg5/agentflow/main/scripts/install.sh | sh
```

The current preview is [v0.3.0](https://github.com/chenhg5/agentflow/releases/tag/v0.3.0). It changes the workflow execution schema: use `execution.candidates` for every node, including nodes with a single executor. See the [migration notes and known limitations](changelogs/v0.3.0.md) before upgrading.

To build and try the source checkout:

```bash
make build
bin/agentflow workflow validate examples/smoke.json --check-executors --format json
bin/agentflow run start examples/smoke.json --input name=Codex --format json
bin/agentflow run submit <run-id> approve --output '{"decision":"happy"}' --format json
```

See [INSTALL.md](INSTALL.md) for setup and [skills/agentflow/SKILL.md](skills/agentflow/SKILL.md) for agent instructions.

## Standalone workspace

Keep orchestration in its own directory or Git repository. It can coordinate several project repositories and worktrees; placing it inside one project is optional. A recommended layout is:

```text
my-workflow/
├── workflow.json
├── executors.json
├── triggers.json                 # optional
├── executors/
│   ├── agents/                   # owned directories or symlinks to existing agents
│   └── programs/
└── runs/                         # generated state, snapshots, and logs
```

Create the v1 layout using the CLI:

```sh
agentflow workspace init my-workflow
cd my-workflow
agentflow executor init author --kind codex
agentflow executor init archive --kind command
agentflow workspace validate --format json
```

The starter graph runs immediately; adapt it for your task. Fill in `DESIGN.md` with node contracts, agent responsibilities/context/skills/tools, routing and human feedback. `executor init --kind codex` and `--kind claude-code` create native role resources and a local MCP input tool. `executor add <name> --kind codex --workspace /existing/agent` binds an existing directory without changing its files. `workspace validate --input key=value` resolves representative input-based paths and inventories local agent resources; discovery does not prove actual loading or task completion. See [layout v1](docs/workspace-layout.md) for the full contract and [authoring guidance](skills/agentflow/references/design.md) for design and acceptance.

Run CLI commands from `my-workflow/`. Register external agent or repository paths through `workspace`; reuse existing session history through a candidate's `session`. Repository URLs, refs, and commit IDs are ordinary executor config or result data; repository checkout and version capture are performed by the executor.

Use root `workflow.json` for a single graph. For multiple graphs, group all definitions under `workflows/<name>.json` without a separate root graph. These are layout conventions: CLI commands and trigger targets accept explicit JSON paths, and folder nesting does not connect graphs. See [workspace layout](docs/workspace-layout.md) for both structures and moving from one to several graphs.

## Workflow

```json
{
  "version": "3",
  "name": "example",
  "entry": "draft",
  "nodes": [
    {"id": "draft", "execution": {"candidates": [{"executor": "codex", "config": {"instruction": "Write a short proposal."}}]}},
    {"id": "review", "execution": {"candidates": [{"executor": "my-review"}]}}
  ],
  "edges": [
    {"from": "draft", "to": "review"},
    {"from": "review", "to": "draft", "when": "nodes.review.result.score < 5", "maxIterations": 2},
    {"from": "review", "to": "$end"}
  ]
}
```

A node has `id`, `execution.candidates`, optional `execution.fallback`, `inputSchema`, `outputSchema`, `timeoutMs`, and `retry.maxAttempts`. Each ordered candidate owns its executor, opaque config, and session policy. The entry node receives the run input; each later node receives the previous node's JSON output by default. An edge may declare `map` to build the next input from source `input`, source `output`, run `inputs`, and prior `nodes`; for example, `"map":{"review":"{{ output.comments }}","original":"{{ input.summary }}"}`. The array position does not determine execution order. The first matching outgoing edge selects the next node. An edge without `when` is the final default. Conditions support comparisons, arithmetic, logical combinations, membership, regex operations, and collection functions. Cycles need a bounded edge by default; `allowUnboundedCycles: true` explicitly permits unlimited loops. See [graph semantics](docs/graph-and-visualization.md).

Set `"dispatch":"all"` on a node to run every matching outgoing branch concurrently. A target with `"join":{"from":["left","right"]}` starts only after both predecessors have delivered input; it receives `{"left": <payload>, "right": <payload>}`. The default dispatch remains first matching edge. See [the runnable parallel example](examples/parallel.json) and [parallel execution semantics](docs/graph-and-visualization.md#parallel-nodes-and-joins).

`prompt`, `score`, `decision`, recipient IDs, and similar values are executor configuration or input/output data. The graph assigns no meaning to them.

## Register your executor

Create `executors.json` at the workflow workspace root. If absent, only built-in executors are available. Start with [executors.example.json](executors.example.json) or `agentflow config init`.

```json
{
  "executors": {
    "my-review": {
      "command": ["python3", "./executors/review.py"],
      "workspace": ".",
      "env": {"REVIEW_PROFILE": "local"}
    }
  }
}
```

Executor names are arbitrary. `command` is an argument array, `workspace` is the process working directory, and `env` adds environment variables. Configured commands and environment values can reference `{{ config.field }}` and `{{ input.field }}`. A relative workspace resolves from the AgentFlow invocation directory; relative command paths resolve inside that workspace.

Built-in executors are `codex`, `claude-code`, `process`, and `external`. Codex and Claude Code adapters accept candidate `config.instruction` and optional `config.workspace`. Candidate session behavior belongs in `execution.candidates[].session`: `new` starts fresh on every visit, `fixed` creates and reuses one session for that node/candidate in the run (or uses a supplied ID), `inherit` resumes a compatible upstream session, and `fork` branches from one. Their CLI options belong in executor registration. `process` accepts candidate `config.command` as an argument array. `external` persists `waiting_input` until `run submit` supplies JSON output.

Candidate order is priority order. Retries are per candidate. By default the next candidate is tried only when the OS process could not start; a launched nonzero exit, timeout, missing/invalid result, or output-contract failure stops the node. Set `execution.fallback.when` to an Expr over structured `failure` fields to opt into another policy, for example `failure.kind == 'exit_code' && failure.code == 75`. See the [runnable fallback example](examples/executor-fallback.json).

Executor registrations can set `"environment":{"mode":"allowlist","inherit":["PATH","LANG"]}` to select host variables, or `"environment":{"mode":"empty"}` to omit them. The default is `inherit`. Explicit `env` entries and fresh engine file/session channels are added afterward. See [environment and creation guarantees](docs/execution-semantics.md#executor-environment) for boundaries and recovery behavior.

## Program contract

The runner starts the configured program whenever the graph reaches its node. The program reads incoming JSON from `AGENTFLOW_INPUT_FILE`, optional executor configuration from `AGENTFLOW_CONFIG_FILE`, writes one JSON object to `AGENTFLOW_RESULT_FILE`, and exits successfully. It may write a newly created session ID to `AGENTFLOW_SESSION_FILE`. stdout and stderr are logs. The runner validates output and selects the next edge.

`AGENTFLOW_ROOT` points to the workflow workspace; `AGENTFLOW_WORKSPACE` points to this executor's working directory. An executor workspace may be a local directory, a symlink, or an external project path. A directory binding does not select a session: use the selected candidate's `session` to start, resume, or fork one. Existing agent directories keep their own instructions and tools. See [the standalone workspace demo](examples/local-workspace-demo/README.md).

```python
import json, os
from pathlib import Path
request = json.loads(Path(os.environ["AGENTFLOW_INPUT_FILE"]).read_text())
Path(os.environ["AGENTFLOW_RESULT_FILE"]).write_text(json.dumps({"score": 8}))
```

The language and communication channel are unrestricted. An interaction program can send a card or display a terminal prompt, wait for a response, then write output. A person clicking a button does not select graph edges; the returned JSON is evaluated by the graph's conditions. Programs do not edit run state.

Input and output contracts use JSON Schema draft 2020-12. Exact templates in candidate `config`, such as `{{ nodes.draft.result.count }}`, preserve JSON types. Every candidate attempt has separate input, config, result, session, stdout, and stderr paths. Run state records the selected executor, structured failure, and complete attempt sequence. See [execution semantics](docs/execution-semantics.md).

## Operate and inspect

```bash
agentflow doctor --format json
agentflow schema --all --format json
agentflow run get <run-id> --format json
agentflow run logs <run-id> --format ndjson
agentflow run metrics <run-id> --format json
agentflow run stats <workflow-name> --by-version --format json
agentflow run evaluate <run-id> --evaluation '{"measurements":{"quality":{"value":0.9,"direction":"higher"}}}' --format json
agentflow run compare <run-id> --format json
agentflow run retry <run-id> --format json
agentflow run resume <run-id> --format json
agentflow run cancel <run-id> --format json
agentflow web --host 127.0.0.1 --port 7070
```

Serial graphs run one active node at a time; `dispatch: "all"` can run siblings concurrently. Retry restarts failed active nodes while preserving successful siblings. Each run saves its workflow snapshot; executor registrations are resolved from the current environment. Changing executor configuration therefore affects a later resume or retry. Old workflow formats and removed node fields are rejected.

Cron and webhook configuration lives in `triggers.json`. `trigger validate`, `trigger fire`, and `trigger serve` use the same dispatcher; the persistent scheduler requires `trigger serve` to stay running. See [trigger architecture](docs/trigger-architecture.md) and [stable occurrences / catch-up](docs/cron-occurrences.md). `event` bindings expose the original planned time separately from receipt time; opt into `misfire: "fire-once"` for latest-only catch-up. `trigger status` shows baselines, missed ranges and delivery decisions.

Each workspace runs its own trigger service. One service can schedule several graph files from its `triggers.json`; its shared run worker defaults to one active run. Set `run worker --concurrency N` or `trigger serve --concurrency N` to change admission; an existing worker owns the budget. Services in different workspaces have separate receipts and run stores. Cron-only services do not bind HTTP ports. Webhooks share one listener per workspace: use distinct explicit ports across workspaces, or port `0` for an automatically assigned port (reported on stderr). A second service for the same workspace is rejected. Delivery deduplication and the default `forbid` overlap policy are scoped to each trigger, not to the repositories or sessions its executors use.

CLI stdout contains data; diagnostics go to stderr. Piped output defaults to JSON. Exit codes are `0` success, `1` execution failure, `2` invalid input, `3` not found, `4` permission denied, `5` conflict, and `10` successful dry run. Start requests support idempotency keys. Controls are queued when another process owns the run lock.

Runtime metrics and user-defined evaluations can be compared across runs. See [observability and evaluation](docs/observability-and-evaluation.md) for the JSON contract, cohort statistics, and a reporting method that keeps efficiency evidence separate from outcome quality.

Run `make check` and `make test-e2e` for deterministic verification. Real model testing is opt-in: [testing](docs/testing.md). Release instructions: [releasing](docs/releasing.md).

## Workflow authoring benchmark

[Cold-context authoring benchmark](benchmarks/workflow-authoring/README.md) includes business tasks, private golden workflows, deterministic execution checks, arbitrary agent-command runners, and retained authoring artifacts. `make test-benchmark` verifies the goldens and detects deliberately broken routes, bindings, type handoffs, feedback and loop limits without calling a model. Live authoring runs are opt-in. The [initial report](benchmarks/workflow-authoring/reports/2026-10-23/REPORT.md) distinguishes final task success from errors repaired during authoring.

## Feishu human review example

Follow the [five-step Lark adapter setup guide](examples/lark-review/SETUP.md) for configurable content, buttons, bots and reviewers. It includes a complete workflow, registry, config schema and local checker. The graph uses the generic external executor contract.

### Durable batch dispatch

Use `run enqueue` to persist requests and `run worker --concurrency N` to execute them. Managed start/submit/retry/resume/cancel requests share admission. Parent/child links and opaque labels are creation metadata; CLI and Web expose child status counts and navigation. See [queue usage and recovery boundaries](docs/durable-queue.md).

### Resource quotas

Declare workspace capacities in `resources.json` and per-node `resources` requests to limit actual executor attempts across runs and parallel branches. Resource waits release run admission; abandoned claims remain quarantined until verified cleanup. See [resource contracts](docs/resources.md).
