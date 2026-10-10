# AgentFlow Installation and Usage Guide

> This guide is written for AI coding agents (Claude Code, Codex, Cursor, and similar tools) helping a user install and configure AgentFlow. Ask the user for choices that affect their files, sessions, credentials, or network exposure. Do not guess those values.

## What AgentFlow does

AgentFlow is a standalone local workflow runner. A workflow is a JSON file containing shell, agent, and human approval nodes. The runner starts eligible nodes, persists every state transition, validates structured results, resumes after approval or interruption, and stores run artifacts under `.agentflow/`.

```text
workflow.json  ->  agentflow run start  ->  .agentflow/runs/<run-id>/
                         |
                         +--> shell commands
                         +--> Claude Code / Codex sessions
                         +--> human approval
```

The workflow source belongs to the user or project. AgentFlow keeps a copy in each run directory as an immutable execution snapshot.

## Guidance for the installing agent

- **Ask before changing scope.** Confirm the user's OS, preferred install method, project directory, agent CLI, provider sessions, and whether the local web UI should be reachable outside localhost.
- **Keep secrets out of workflow files.** Do not put API keys, login cookies, or tokens in `workflow.json`, command arguments, chat messages, or commits. AgentFlow invokes the user's already-authenticated Claude Code or Codex CLI.
- **Use JSON output for automation.** Piped output is JSON by default. Prefer `--format json`; use `--format ndjson` for event streams and `--format table` for a human terminal.
- **Verify each step.** Run the check shown below before moving to the next step. If a command fails, inspect its machine-readable error and follow its suggestion.
- **Do not edit runtime state.** Agents write the structured result file and exit. The runner alone updates `state.json` and advances dependent nodes.

## Prerequisites

For a released binary, no Go toolchain is required. For source builds or `go install`, use Go 1.22 or newer.

```bash
go version       # only needed for source builds or go install
git --version
claude --version # needed for Claude Code agent nodes
codex --version  # needed for Codex agent nodes
```

Claude Code and Codex are optional. Install and authenticate at least one of them before running a workflow with an `agent` node:

```bash
claude            # complete the Claude login flow if needed
codex login       # complete the Codex login flow if needed
```

The web UI is served locally by the AgentFlow binary. Its current React Flow dependencies are loaded by the browser from public CDNs, so the browser needs network access to `esm.sh` and `unpkg.com` to render the graph.

## Install AgentFlow

Choose one method. Ask the user which method they prefer before installing.

### Option A: download a release archive

The current preview is `v0.0.1`. Pick the archive matching the user's OS and CPU:

```text
agentflow-v0.0.1-darwin-arm64.tar.gz
agentflow-v0.0.1-darwin-amd64.tar.gz
agentflow-v0.0.1-linux-arm64.tar.gz
agentflow-v0.0.1-linux-amd64.tar.gz
agentflow-v0.0.1-windows-arm64.zip
agentflow-v0.0.1-windows-amd64.zip
```

Release assets and `checksums.txt` are published at:

<https://github.com/chenhg5/agentflow/releases/tag/v0.0.1>

Example for Apple Silicon macOS:

```bash
mkdir -p "$HOME/.local/bin"
curl -fL -o /tmp/agentflow.tar.gz \
  https://github.com/chenhg5/agentflow/releases/download/v0.0.1/agentflow-v0.0.1-darwin-arm64.tar.gz
tar -xzf /tmp/agentflow.tar.gz -C /tmp
install -m 0755 /tmp/agentflow-v0.0.1-darwin-arm64/agentflow "$HOME/.local/bin/agentflow"
export PATH="$HOME/.local/bin:$PATH"
```

Verify the checksum before installing when the user requires supply-chain verification:

```bash
curl -fL -o /tmp/checksums.txt \
  https://github.com/chenhg5/agentflow/releases/download/v0.0.1/checksums.txt
cd /tmp
shasum -a 256 -c checksums.txt --ignore-missing
```

On Windows, extract the matching `.zip` and put `agentflow.exe` in a directory on `PATH`.

### Option B: install with Go

```bash
go install github.com/chenhg5/agentflow/cmd/agentflow@v0.0.1
```

If the command is not found afterwards, add Go's binary directory to `PATH`:

```bash
export PATH="$(go env GOPATH)/bin:$PATH"
```

### Option C: build from source

```bash
git clone https://github.com/chenhg5/agentflow.git
cd agentflow
make build
./bin/agentflow version --format json
```

The release build command is documented in [`docs/releasing.md`](docs/releasing.md).

## Verify the installation

Run these commands from the user's project directory:

```bash
agentflow version --format json
agentflow --help
agentflow schema --all --format json
agentflow doctor --format json
```

Expected version output contains `version`, `commit`, `built`, `go`, and `platform`. `doctor` reports the current directory, configured drivers, and the local run store path.

## Configure agent providers

Provider configuration is separate from workflow definitions. Initialize a project-local file:

```bash
agentflow config init
```

This creates `.agentflow/providers.json`. If a project-local file is absent, AgentFlow also checks `~/.agentflow/providers.json`. Edit the file for the installed CLI versions:

```json
{
  "drivers": {
    "claude-code": {
      "command": ["claude", "--print", "{{ prompt }}"],
      "resumeCommand": ["claude", "--resume", "{{ session_id }}", "--print", "{{ prompt }}"]
    },
    "codex": {
      "command": ["codex", "exec", "{{ prompt }}"],
      "resumeCommand": ["codex", "exec", "resume", "{{ session_id }}", "{{ prompt }}"]
    }
  }
}
```

Rules:

- `command` starts a new agent session.
- `resumeCommand` is required when an agent node specifies `session`.
- `workspace` becomes the child process working directory.
- AgentFlow sets `AGENTFLOW_WORKSPACE`, `AGENTFLOW_SESSION_ID`, `AGENTFLOW_RUN_ID`, `AGENTFLOW_NODE_ID`, and `AGENTFLOW_RESULT_FILE` for the process.
- Command templates support `{{ prompt }}`, `{{ workspace }}`, `{{ session_id }}`, `{{ result_file }}`, `{{ run_id }}`, and `{{ node_id }}`.

Never silently replace a missing `resumeCommand` with a new session. If a workflow names a session, the run should fail clearly until the provider is configured to resume it.

## Create and execute a workflow

Start with the bundled example:

```bash
agentflow workflow validate examples/smoke.json --format json
agentflow run start examples/smoke.json --input name=AgentFlow --format json
```

The example pauses at a human node. Approve it and inspect the final run:

```bash
agentflow run approve <run-id> approve --message "approved" --format json
agentflow run get <run-id> --format json
```

A minimal agent workflow looks like this:

```json
{
  "version": "1",
  "name": "review-and-approve",
  "inputs": {
    "workspace": { "type": "path", "required": true },
    "session_id": { "type": "string", "required": false }
  },
  "nodes": [
    {
      "id": "review",
      "type": "agent",
      "driver": "claude-code",
      "workspace": "{{ inputs.workspace }}",
      "session": "{{ inputs.session_id }}",
      "prompt": "Review the current changes. Write a JSON object with summary and decision to AGENTFLOW_RESULT_FILE."
    },
    {
      "id": "approve",
      "type": "human",
      "needs": ["review"],
      "prompt": "Approve the review result before continuing."
    }
  ]
}
```

Validate before executing:

```bash
agentflow workflow validate review-and-approve.json --format json
agentflow run start review-and-approve.json \
  --input workspace=/absolute/path/to/repo \
  --input session_id=<optional-session-id> \
  --dry-run --format json
```

Dry-run exits with code `10` and does not create a run. If the plan is correct, execute it with a stable idempotency key when the request may be retried:

```bash
agentflow run start review-and-approve.json \
  --input workspace=/absolute/path/to/repo \
  --idempotency-key review-2026-10-10-001 \
  --format json
```

The same workflow, inputs, and idempotency key reuse the existing run. A different request using that key returns a conflict instead of creating a duplicate.

## Structured results and artifacts

Agent nodes must write one JSON object to `AGENTFLOW_RESULT_FILE` before exiting with code `0`:

```json
{
  "decision": "pass",
  "summary": "All checks passed",
  "session_id": "optional-session-id"
}
```

The runner stores this object as the node result. stdout and stderr are logs only; they are never treated as structured node output. Downstream nodes can read fields with expressions such as `{{ nodes.review.result.summary }}`.

Do not write `.agentflow/runs/<run-id>/state.json` from an agent. Do not start the next node from an agent. Exit after writing the result file and let the runner advance the workflow.

## Human approval and recovery

When a run returns `waiting_human`, inspect the pending node and decide explicitly:

```bash
agentflow run get <run-id> --format json
agentflow run approve <run-id> <node-id> --message "approved" --format json
# or
agentflow run reject <run-id> <node-id> --message "needs changes" --format json
```

Approval resumes dependent nodes. Rejection marks the run failed. If the process was interrupted, inspect the run first, then resume it:

```bash
agentflow run resume <run-id> --dry-run --format json
agentflow run resume <run-id> --format json
```

Cancel a run when the user explicitly asks for cancellation:

```bash
agentflow run cancel <run-id> --format json
```

## Observe a run and the workflow history

For a single run:

```bash
agentflow run metrics <run-id> --format json
agentflow run logs <run-id> --format ndjson
agentflow run get <run-id> --format json
```

Metrics include run and node duration, human waiting time, attempts, rendered prompt bytes, structured result bytes, stdout/stderr bytes, workspace, and session ID. Historical aggregation is available with:

```bash
agentflow run stats --format json
agentflow run stats review-and-approve --format json
```

Open the visual graph in a browser:

```bash
agentflow web review-and-approve.json
agentflow web --run <run-id>
```

The command prints a local URL. The run page refreshes state and metrics every two seconds. Use `--port 0` for an available port. Only use `--host 0.0.0.0` when the user understands that the local UI will be reachable from the network.

## Runtime data and cleanup

AgentFlow keeps runtime data in the current project directory:

```text
.agentflow/
├── providers.json
└── runs/
    └── <run-id>/
        ├── workflow.json
        ├── state.json
        ├── events.jsonl
        └── artifacts/
            ├── *.result.json
            └── logs/*.stdout.log
            └── logs/*.stderr.log
```

The original workflow file can live anywhere. The copy under a run directory is the definition snapshot used by that run. `.agentflow/` is ignored by the repository's `.gitignore` and may contain prompts, inputs, session IDs, and work results; do not commit it unless the user explicitly wants to archive it.

To remove local runtime data after confirming it is no longer needed:

```bash
rm -rf .agentflow
```

## Exit codes and troubleshooting

| Code | Meaning | What to do |
|---:|---|---|
| 0 | Success | Continue or inspect the returned JSON. |
| 1 | Execution failure | Run `run get` and `run logs`; check the failed node's error and artifacts. |
| 2 | Invalid arguments or workflow | Run `--help`, `schema workflow`, and `workflow validate`. |
| 3 | Resource not found | Check the workflow path or run ID. |
| 4 | Permission error | Check workspace and artifact directory permissions. |
| 5 | Conflict | Check idempotency keys or an already-decided human node. |
| 10 | Dry-run passed | This is a successful preview, not an executed run. |

Common fixes:

- **Driver is not configured:** run `agentflow config init`, edit `.agentflow/providers.json`, and verify the provider command directly in the target workspace.
- **Existing session cannot resume:** add a provider `resumeCommand` and provide the exact session ID. Do not remove `session` unless starting a new session is intentional.
- **Structured result is missing or invalid:** make the agent write a JSON object to `AGENTFLOW_RESULT_FILE`; stdout text does not count as the node result.
- **Workflow validation fails:** fix the reported node ID, dependency, type, required field, or cycle before running again.
- **Web UI is blank:** check browser access to `esm.sh` and `unpkg.com`, or use `run get`/`run metrics` as the CLI fallback.

## Uninstall

If installed with Go, remove the binary from `$(go env GOPATH)/bin`. If installed from an archive, remove the copied `agentflow` binary. Runtime data is project-local under `.agentflow/` and can be removed separately after review.
