# AgentFlow

AgentFlow is a standalone local workflow runner for Claude Code, Codex, and other command based agent sessions.

It uses a small JSON workflow definition, persists every run under `.agentflow/`, and supports resumable human approval steps.

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

## Agent onboarding skill

给 Claude Code、Codex 或其他 agent 使用的编排手册位于 [`skills/agentflow/SKILL.md`](skills/agentflow/SKILL.md)。它覆盖和用户澄清需求、生成 workflow、provider/session 绑定、校验、dry-run、人工审批、恢复以及 metrics/stats 观测。

## Agent drivers

Drivers are configured outside workflow definitions in `.agentflow/providers.json` or `~/.agentflow/providers.json`. Start from `providers.example.json` and adjust the command syntax for the installed Claude Code or Codex CLI. `command` starts a new session; `resumeCommand` is selected when an agent node sets `session`.

Agent nodes can bind both a workspace and an existing session:

```json
{
  "id": "implement",
  "type": "agent",
  "driver": "claude-code",
  "workspace": "/Users/me/code/my-repo",
  "session": "{{ inputs.session_id }}",
  "prompt": "Implement the requested change and write the structured result."
}
```

`session` can be a literal ID or a template such as `{{ inputs.session_id }}` / `{{ nodes.previous.result.session_id }}`. The Runner executes with `cmd.Dir=workspace`, sets `AGENTFLOW_WORKSPACE` and `AGENTFLOW_SESSION_ID`, and persists the effective workspace/session under the node state. Command templates support `{{ prompt }}`, `{{ workspace }}`, `{{ session_id }}`, `{{ result_file }}`, `{{ run_id }}`, and `{{ node_id }}`. `promptMode: "stdin"` sends the prompt through stdin.

## Structured node results

Agent nodes must write a JSON object to the `AGENTFLOW_RESULT_FILE` environment variable before they exit successfully. The Runner stores that JSON under `node.result` and remains the only component that advances the workflow. stdout and stderr are retained only as log artifacts under `artifacts/logs/`.

For example, a command can write:

```json
{"decision":"pass","summary":"tests passed"}
```

Downstream nodes reference structured data with expressions such as `{{ nodes.review.result.decision }}`.

## Observability

Inspect one run with:

```bash
agentflow run metrics <run-id> --format json
agentflow run logs <run-id> --format ndjson
```

`run metrics` reports run and node status, duration, retry attempts, human waiting time, workspace/session binding, and byte counts. `input_bytes` is the rendered prompt sent to the node, `output_bytes` is the structured result file, and `stdout_bytes`/`stderr_bytes` are log sizes. Structured results and logs stay separate.

Aggregate historical runs to find slow or failure-prone steps:

```bash
agentflow run stats --format json
agentflow run stats release-review --format json
```

The aggregate includes run counts, average/P50/P95/max durations, human waiting time, retry totals, and input/output/log byte totals. Run data is persisted under `.agentflow/runs/<run-id>/` so it can be inspected or exported by another tool.

The Runner owns all state transitions. Agent processes receive `AGENTFLOW_RUN_ID`, `AGENTFLOW_NODE_ID`, `AGENTFLOW_NODE_TOKEN`, and `AGENTFLOW_RESULT_FILE`; they write the result file and exit, while the Runner validates the file and advances dependent nodes. An Agent never writes `state.json` or starts the next node directly.

The first release keeps drivers command based. This makes the workflow engine independent from a vendor SDK while preserving a path to richer session adapters later.

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
- human approve/reject/resume
- Claude Code and Codex command driver configuration, including workspace and existing-session resume
