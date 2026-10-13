# Testing AgentFlow

AgentFlow has two test layers:

1. **Deterministic checks** run on every change. `make check` runs formatting, `go vet`, and all Go tests with fake drivers. It does not contact a model provider.
2. **Real agent smoke testing** is opt-in. It runs a small two-node workflow against the locally installed Claude Code and Codex CLIs and validates their structured result files.

3. **CLI end-to-end contract testing** runs without model quota. It exercises the complete JSON CLI path for field declarations, explicit and automatic handoff, persisted input artifacts, human approval, and a failing JSON Schema contract.

Run it with:

```bash
make test-e2e
```

The test uses shell nodes as deterministic stand-ins for agents. This verifies the runner and persistence boundary; the real provider smoke test below verifies Claude Code and Codex command construction separately.
Set `AGENTFLOW_KEEP_WORKSPACE=1` to retain the temporary `.agentflow` directory for inspection after the test.

## Run the real smoke test

Prerequisites:

- `claude` and `codex` are installed and already authenticated.
- `jq` is installed.
- The selected Codex model is available to the current account. The default is `gpt-5.6-sol`; override it when needed.

Run it from the repository:

```bash
make build
AGENTFLOW_REAL_CODEX_MODEL=gpt-5.6-sol ./scripts/real-agent-smoke.sh
```

The script creates a temporary Git workspace, writes a one-line `input.txt`, configures local providers, and starts [`examples/real-agent-pipeline.json`](../examples/real-agent-pipeline.json):

```text
claude_inspect (Claude Code) -> codex_verify (Codex)
```

The second node receives the first node's structured fields through `{{ nodes.claude_inspect.result.* }}`. Both agents must write a JSON object to the exact path in `AGENTFLOW_RESULT_FILE` (also printed in the execution contract). The script checks that the run succeeded, both nodes succeeded, and Codex returned `verified: true`. It prints the run metrics, including per-node duration and byte counts.

The Codex command uses `--dangerously-bypass-approvals-and-sandbox` because this is an unattended integration test. The fixture is disposable and the prompts prohibit source changes. Do not copy that flag into a production provider configuration without an isolated workspace policy.

To inspect the generated workspace after a run:

```bash
AGENTFLOW_KEEP_WORKSPACE=1 ./scripts/real-agent-smoke.sh
```

The default test removes the temporary workspace on exit. The model calls can take a few minutes and consume provider quota, so this script is intentionally excluded from `make check` and CI.

## What this catches

- Provider command expansion and non-interactive CLI invocation.
- Workspace binding and current-directory behavior.
- Passing structured results between dependent agent nodes.
- The result-file contract, including agents that cannot inspect process environment variables.
- Durable run state, log artifacts, and per-node observability metrics.

If a real run fails, preserve the workspace and inspect the exact run:

```bash
AGENTFLOW_KEEP_WORKSPACE=1 ./scripts/real-agent-smoke.sh
agentflow run get <run-id> --format json
agentflow run metrics <run-id> --format json
agentflow run logs <run-id> --format ndjson
```
