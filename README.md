# AgentFlow

AgentFlow runs local workflows described by `entry`, `nodes`, and explicit `edges`. Each node calls a named executor with JSON input and produces JSON output. Executors can run Codex, Claude Code, an interactive program, or a program you write in any language.

The runner persists state under `.agentflow/`, validates input/output schemas, evaluates conditions, and advances the graph automatically. Workflow version `2` is the supported format.

## Install and try

```bash
curl -fsSL https://raw.githubusercontent.com/chenhg5/agentflow/main/scripts/install.sh | sh
```

This executor schema is currently in the source checkout. Build the checkout to try it:

```bash
make build
bin/agentflow workflow validate examples/smoke.json --check-executors --format json
bin/agentflow run start examples/smoke.json --input name=Codex --format json
bin/agentflow run submit <run-id> approve --output '{"decision":"happy"}' --format json
```

See [INSTALL.md](INSTALL.md) for setup and [skills/agentflow/SKILL.md](skills/agentflow/SKILL.md) for agent instructions.

## Workflow

```json
{
  "version": "2",
  "name": "example",
  "entry": "draft",
  "nodes": [
    {"id": "draft", "executor": "codex", "input": {"prompt": "Write a short proposal."}},
    {"id": "review", "executor": "my-review", "input": {"content": "{{ nodes.draft.result.summary }}"}}
  ],
  "edges": [
    {"from": "draft", "to": "review"},
    {"from": "review", "to": "draft", "when": "nodes.review.result.score < 5", "maxIterations": 2},
    {"from": "review", "to": "$end"}
  ]
}
```

A node has `id`, `executor`, optional `input`, `inputSchema`, `outputSchema`, `timeoutMs`, and `retry.maxAttempts`. The array position does not determine execution order. The first matching outgoing edge selects the next node. An edge without `when` is the final default. Conditions support comparisons, arithmetic, logical combinations, membership, regex operations, and collection functions. Cycles need a bounded edge. See [graph semantics](docs/graph-and-visualization.md).

`prompt`, `score`, `decision`, recipient IDs, and similar values are executor input/output data. The graph assigns no meaning to them.

## Register your executor

Create `.agentflow/executors.json` in the directory where you run AgentFlow. If absent, the runner checks `~/.agentflow/executors.json`. Start with [executors.example.json](executors.example.json) or `agentflow config init`.

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

Executor names are arbitrary. `command` is an argument array, `workspace` is the process working directory, and `env` adds environment variables. Configured commands and environment values can reference `{{ input.field }}`. A relative workspace resolves from the AgentFlow invocation directory; relative command paths resolve inside that workspace.

Built-in executors are `codex`, `claude-code`, `process`, and `external`. Codex and Claude Code adapters accept `input.prompt`, optional `input.workspace`, and optional `input.session`. Their CLI options belong in executor configuration. `process` accepts `input.command` as an argument array. `external` persists `waiting_input` until `run submit` supplies JSON output. These are ready-made executors; nodes use the same reference contract for custom executors.

## Program contract

The runner starts the configured program whenever the graph reaches its node. The program reads a JSON object from `AGENTFLOW_INPUT_FILE`, writes one JSON object to `AGENTFLOW_RESULT_FILE`, and exits successfully. stdout and stderr are logs. The runner validates output and selects the next edge.

```python
import json, os
from pathlib import Path
request = json.loads(Path(os.environ["AGENTFLOW_INPUT_FILE"]).read_text())
Path(os.environ["AGENTFLOW_RESULT_FILE"]).write_text(json.dumps({"score": 8}))
```

The language and communication channel are unrestricted. An interaction program can send a card or display a terminal prompt, wait for a response, then write output. A person clicking a button does not select graph edges; the returned JSON is evaluated by the graph's conditions. Programs do not edit run state.

Input and output contracts use JSON Schema draft 2020-12. Exact templates such as `{{ nodes.draft.result.count }}` preserve JSON types. Every visit has separate input, result, and log artifacts. See [execution semantics](docs/execution-semantics.md).

## Operate and inspect

```bash
agentflow doctor --format json
agentflow schema --all --format json
agentflow run get <run-id> --format json
agentflow run logs <run-id> --format ndjson
agentflow run metrics <run-id> --format json
agentflow run stats <workflow-name> --by-version --format json
agentflow run retry <run-id> --format json
agentflow run resume <run-id> --format json
agentflow run cancel <run-id> --format json
agentflow web --host 127.0.0.1 --port 7070
```

One active node runs at a time. Retry restarts the active failed node. Each run saves its workflow snapshot; executor registrations are resolved from the current environment. Changing executor configuration therefore affects a later resume or retry. Old workflow formats and removed node fields are rejected.

Cron and webhook configuration lives in `.agentflow/triggers.json`. `trigger validate`, `trigger fire`, and `trigger serve` use the same dispatcher; the persistent scheduler requires `trigger serve` to stay running. See [trigger architecture](docs/trigger-architecture.md).

CLI stdout contains data; diagnostics go to stderr. Piped output defaults to JSON. Exit codes are `0` success, `1` execution failure, `2` invalid input, `3` not found, `4` permission denied, `5` conflict, and `10` successful dry run. Start requests support idempotency keys. Controls are queued when another process owns the run lock.

Run `make check` and `make test-e2e` for deterministic verification. Real model testing is opt-in: [testing](docs/testing.md). Release instructions: [releasing](docs/releasing.md).

## Workflow authoring benchmark

[Cold-context authoring benchmark](benchmarks/workflow-authoring/README.md) includes business tasks, private golden workflows, deterministic execution checks, arbitrary agent-command runners, and retained authoring artifacts. `make test-benchmark` verifies the goldens and detects deliberately broken routes, bindings, type handoffs, feedback and loop limits without calling a model. Live authoring runs are opt-in. The [initial report](benchmarks/workflow-authoring/reports/2026-10-23/REPORT.md) distinguishes final task success from errors repaired during authoring.
