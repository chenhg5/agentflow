# AgentFlow

AgentFlow is a standalone local workflow runner for Claude Code, Codex, and other command based agent sessions.

It uses a small JSON workflow definition, persists every run under `.agentflow/`, and supports resumable human approval steps.

The current release is `v0.0.1` (preview). Check the binary metadata with `agentflow version`; release steps are documented in [`docs/releasing.md`](docs/releasing.md).

For an agent-oriented installation and setup walkthrough, see [`INSTALL.md`](INSTALL.md).
The graph model and Web UI inspection behavior are documented in [`docs/graph-and-visualization.md`](docs/graph-and-visualization.md).

## Quick start

```bash
go run ./cmd/agentflow workflow validate examples/smoke.json --format json
go run ./cmd/agentflow run start examples/smoke.json --input name=Codex --format json
```

The command pauses at the `approve` node. Copy the run id and continue it:

```bash
go run ./cmd/agentflow run approve <run-id> approval --message "approved" --format json
go run ./cmd/agentflow run get <run-id> --format json
```

## Agent-friendly CLI contract

- Commands use a noun-verb tree such as `workflow validate` and `run get`.
- Piped output defaults to JSON; use `--format table` for humans and `--format ndjson` for event streams.
- stdout contains data. Diagnostics and errors go to stderr.
- Errors include a stable machine-readable code, a recovery suggestion, and `retryable`.
- Exit codes: `0` success, `1` execution failure, `2` invalid arguments, `3` not found, `4` permission denied, `5` conflict, `10` dry-run passed.
- `agentflow schema --all` exposes the command tree; `agentflow schema run start` returns one command contract; `agentflow schema workflow` returns the workflow JSON Schema.
- `--dry-run` is available for run start, approval, rejection, resume, cancellation, and provider initialization.
- `run start --idempotency-key <token>` safely replays an identical request without creating a duplicate run; a different request with the same key exits with code `5`.
- `run metrics <run-id>` exposes per-node duration, attempts, prompt/result/log byte sizes, workspace, and session; `run stats [workflow-name]` aggregates historical efficiency.
- `trigger list`, `trigger validate`, `trigger fire`, and `trigger serve` use the same dispatcher for cron and webhook events.

## Agent onboarding skill

给 Claude Code、Codex 或其他 agent 使用的编排手册位于 [`skills/agentflow/SKILL.md`](skills/agentflow/SKILL.md)。它覆盖和用户澄清需求、生成 workflow、provider/session 绑定、校验、dry-run、人工审批、恢复以及 metrics/stats 观测。

## Agent providers

Claude Code and Codex have built-in providers. Their binaries and provider-specific runtime options are configured outside workflow definitions in `.agentflow/providers.json` or `~/.agentflow/providers.json`. Start from [`providers.example.json`](providers.example.json). Custom agents can use `kind: "command"` with explicit command templates.

```json
{
  "providers": {
    "claude-code": { "kind": "claude-code", "binary": "claude" },
    "codex": { "kind": "codex", "binary": "codex", "model": "gpt-5.6-sol", "sandbox": "workspace-write" }
  }
}
```

Agent nodes can bind both a workspace and an existing session:

```json
{
  "id": "implement",
  "type": "agent",
  "provider": "claude-code",
  "workspace": "/Users/me/code/my-repo",
  "session": "{{ inputs.session_id }}",
  "prompt": "Implement the requested change and write the structured result."
}
```

`session` can be a literal ID or a template such as `{{ inputs.session_id }}` / `{{ nodes.previous.result.session_id }}`. The Runner executes with `cmd.Dir=workspace`, sets `AGENTFLOW_WORKSPACE` and `AGENTFLOW_SESSION_ID`, and persists the effective workspace/session under the node state. Command templates support `{{ prompt }}`, `{{ workspace }}`, `{{ session_id }}`, `{{ result_file }}`, `{{ run_id }}`, and `{{ node_id }}`. `promptMode: "stdin"` sends the prompt through stdin.

## Structured node results

Agent nodes must write a JSON object to the `AGENTFLOW_RESULT_FILE` environment variable before they exit successfully. The runner also includes the exact absolute result path and workspace in the execution contract appended to the prompt, which helps non-interactive sessions that cannot inspect process environment variables. The Runner stores that JSON under `node.result` and remains the only component that advances the workflow. stdout and stderr are retained only as log artifacts under `artifacts/logs/`.

For example, a command can write:

```json
{"decision":"pass","summary":"tests passed"}
```

Downstream nodes reference structured data with expressions such as `{{ nodes.review.result.decision }}`.

Nodes can make this boundary explicit with `input`, `inputSchema`, and `outputSchema`. Exact placeholders preserve JSON types, and contract failures stop the node before the command starts or after the structured result is read:

```json
{
  "input": { "count": "{{ nodes.inspect.result.line_count }}" },
  "inputSchema": { "type": "object", "required": ["count"], "properties": { "count": { "type": "integer" } } },
  "outputSchema": { "type": "object", "required": ["verified"], "properties": { "verified": { "type": "boolean" } } }
}
```

Contracts use JSON Schema draft 2020-12, so constraints such as `pattern`, `oneOf`, `const`, nested arrays, and strict `additionalProperties` are enforced both when loading a workflow and when a node runs. For field-oriented handoff, add `inputFields` and `outputFields`; `outputFields` derive a strict object schema when `outputSchema` is omitted. Downstream input is built by explicit `inputMapping` first, then by matching field names from direct dependencies, with an explicit `input` template taking precedence. The rendered input is saved as `artifacts/<node>.input.json`. stdout and stderr remain logs; only `AGENTFLOW_RESULT_FILE` is business output.

## Cron and webhook triggers

Store trigger definitions in `.agentflow/triggers.json` and start the long-lived runtime:

```bash
agentflow trigger validate --format json
agentflow trigger serve --host 127.0.0.1 --port 7071
```

The service runs cron scheduling, webhook HTTP intake, durable delivery receipts, a dispatch queue, and workers. A webhook is acknowledged with `202` after the delivery and queued run are persisted. For an external scheduler, use `agentflow trigger fire <trigger-id> --scheduled-at ... --delivery-id ...`; repeated delivery IDs are idempotent. See [`examples/triggers.json`](examples/triggers.json) and [`docs/trigger-architecture.md`](docs/trigger-architecture.md).

## Observability

Inspect one run with:

```bash
agentflow run metrics <run-id> --format json
agentflow run logs <run-id> --format ndjson
```

`run metrics` reports run and node status, trigger metadata, duration, retry attempts, human waiting time, workspace/session binding, and byte counts. `input_bytes` is the rendered prompt plus structured node input, `output_bytes` is the structured result file, and `stdout_bytes`/`stderr_bytes` are log sizes. Structured results and logs stay separate.

Aggregate historical runs to find slow or failure-prone steps:

```bash
agentflow run stats --format json
agentflow run stats release-review --format json
```

The aggregate includes run counts, average/P50/P95/max durations, human waiting time, retry totals, and input/output/log byte totals. Run data is persisted under `.agentflow/runs/<run-id>/` so it can be inspected or exported by another tool.

## Workflow visualization

Open a workflow definition in a local React Flow based UI:

```bash
agentflow web workflow.json
```

Open the graph for an existing run, including live node status and metrics:

```bash
agentflow web --run <run-id>
```

The server prints a local URL and refreshes run data every two seconds. Use `--port 0` to let the operating system choose an available port, or `--host 0.0.0.0` when the UI needs to be reached from another machine on a trusted network.

The UI distinguishes control, structured-data, and conditional edges. Select a node to inspect its contracts, resolved input, structured result, artifacts, and runtime state.

The Runner owns all state transitions. Agent processes receive `AGENTFLOW_RUN_ID`, `AGENTFLOW_NODE_ID`, `AGENTFLOW_NODE_TOKEN`, and `AGENTFLOW_RESULT_FILE`; they write the result file and exit, while the Runner validates the file and advances dependent nodes. An Agent never writes `state.json` or starts the next node directly.

Provider adapters keep vendor-specific CLI and session behavior out of the workflow engine while preserving a generic command escape hatch. Session discovery and history are separate capabilities that can be added without changing workflow definitions.

For reproducible validation, [`docs/testing.md`](docs/testing.md) describes the deterministic test suite and an opt-in smoke workflow that invokes real Claude Code and Codex sessions.

Build a standalone binary with:

```bash
go build -o agentflow ./cmd/agentflow
```

## MVP scope

- JSON workflow validation
- shell, agent, and human nodes
- dependency ordering and conditional `when` expressions
- retries, timeouts, and persisted run state
- structured JSON node results with separate stdout/stderr log artifacts
- explicit node input/output schemas with runtime validation
- human approve/reject/resume
- built-in Claude Code and Codex providers, including workspace and existing-session resume
- cron/webhook triggers with durable dispatch and idempotent deliveries
