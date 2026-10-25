# AgentFlow setup

AgentFlow is a local binary. Each workflow defines explicit edges and references named executors. Programs can be written in any language and communicate through JSON files.

This workflow schema is in the current source checkout. Published binaries may precede it; build this checkout when testing these examples.

```bash
make build
bin/agentflow version --format json
bin/agentflow config init --format json
bin/agentflow doctor --format json
```

For released binaries, use the installation script or release archives:

```bash
curl -fsSL https://raw.githubusercontent.com/chenhg5/agentflow/main/scripts/install.sh | sh
```

Source builds need the Go version specified in `go.mod`. A registered program needs its own runtime installed. Codex and Claude Code executors use their already-authenticated CLIs.

## Executor configuration

For a new standalone workspace, prefer `agentflow workspace init <directory>`,
then run `agentflow executor init <name> --kind codex|command` from its root.
Use `executor add` to bind existing directories. See [layout v1](docs/workspace-layout.md).

`config init` creates `executors.json`. Custom programs need a command entry:

```json
{
  "executors": {
    "my-program": {
      "command": ["python3", "./executors/program.py"],
      "workspace": ".",
      "env": {"APP_PROFILE": "local"}
    },
    "codex": {
      "kind": "codex",
      "binary": "codex",
      "sandbox": "workspace-write"
    }
  }
}
```

The registry is read from the current working directory, then the home directory if no local file exists. Node `executor` values reference these names. A custom command can contain `{{ input.field }}` templates. An argument array runs directly; include a shell explicitly when a shell is required.

Generic programs read `AGENTFLOW_INPUT_FILE`, write JSON to `AGENTFLOW_RESULT_FILE`, and exit with code zero. Logs belong on stdout/stderr. AgentFlow supplies run, node, and invocation identifiers in environment variables; reserved contract variables cannot be overridden with executor `env`.

The built-in `process` executor reads an argument array from `config.command`. The built-in `external` executor waits for `run submit --output JSON`. Codex/Claude Code adapters read `config.instruction` and optional `config.workspace`. Session policy is declared in `execution.session`; business data flows from the run input or previous node output.

## First workflow

Run [examples/smoke.json](examples/smoke.json), which uses built-in executors and requires no custom registration:

```bash
agentflow workflow validate examples/smoke.json --format json
agentflow run start examples/smoke.json --input name=Codex --format json
agentflow run submit <run-id> approve --output '{"decision":"happy"}' --format json
agentflow run get <run-id> --format json
```

The sample's `decision` is an ordinary output property used by its edge expression. Other flows can choose any names and schema. The graph advances automatically after accepted output.

## Recovery and inspection

```bash
agentflow run logs <run-id> --format ndjson
agentflow run metrics <run-id> --format json
agentflow run retry <run-id> --format json
agentflow run resume <run-id> --format json
agentflow web --host 127.0.0.1 --port 7070
```

The UI displays the explicit edges, selected node states, and visit artifacts. It loads React Flow browser dependencies from public CDNs. A failed program stops the run until retry. A crashed running node is retried on resume; external side effects should be idempotent. Only one node is active in a run.

Use `triggers.json` for cron/webhook definitions and keep `agentflow trigger serve` running to deliver scheduled events. Run files live under `runs/`; delivery receipts and dispatch jobs live beside them. Persist the workflow, executor configuration, program sources, and trigger definitions to move a flow to another environment.

Run one trigger service per workspace. One config may target several graph files. Cron-only workspaces need no HTTP port; webhook workspaces may use distinct fixed ports or port `0` to pick an available port. Duplicate services for the same workspace are rejected, and delivery IDs are deduplicated separately for each trigger. A `workflows/` directory is optional; a single graph can be stored as `workflow.json` at the root.

See [README](README.md), [execution semantics](docs/execution-semantics.md), and [the agent skill](skills/agentflow/SKILL.md).
