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

## Agent drivers

Drivers are configured outside workflow definitions in `.agentflow/providers.json` or `~/.agentflow/providers.json`. Start from `providers.example.json` and adjust the command syntax for the installed Claude Code or Codex CLI. `{{ prompt }}` is expanded as one argument, `{{ result_file }}` expands to the structured result path, and `promptMode: "stdin"` sends the prompt through stdin.

## Structured node results

Agent nodes must write a JSON object to the `AGENTFLOW_RESULT_FILE` environment variable before they exit successfully. The Runner stores that JSON under `node.result` and remains the only component that advances the workflow. stdout and stderr are retained only as log artifacts under `artifacts/logs/`.

For example, a command can write:

```json
{"decision":"pass","summary":"tests passed"}
```

Downstream nodes reference structured data with expressions such as `{{ nodes.review.result.decision }}`.

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
- Claude Code and Codex command driver configuration
