# Testing AgentFlow

`make test-benchmark` checks both graph authoring and
[workspace authoring](../benchmarks/workspace-authoring/README.md). The latter
executes submitted MCP tools and program executors, checks file locations and
external-agent preservation, and substitutes model transport. Fresh-context
model-authoring experiments and native provider smoke runs remain opt-in.

AgentFlow has three test layers:

1. **Deterministic checks** run on every change. `make check` runs formatting, `go vet`, and all Go tests with fake drivers. It does not contact a model executor.
2. **Real agent smoke testing** is opt-in. It runs a small two-node workflow against the locally installed Claude Code and Codex CLIs and validates their structured result files.

3. **CLI end-to-end testing** runs without model quota. It exercises the complete JSON CLI path for named executor registrations, explicit input binding, persisted input artifacts, external output submission, failing JSON Schema contracts, webhook intake, cron dispatch, the persistent cron scheduler, and delivery idempotency.

Run it with:

```bash
make test-e2e
```

The test uses process executor invocations as deterministic stand-ins for agents. This verifies the runner and persistence boundary; the real provider smoke test below verifies Claude Code and Codex command construction separately.
Set `AGENTFLOW_KEEP_WORKSPACE=1` to retain the temporary workflow workspace for inspection after the test.

To run only the trigger coverage:

```bash
make test-e2e-triggers
```

## Web UI checks

For the Web node inspector, run `go test ./internal/web` and
`node scripts/test-web-ui.cjs` (Node.js required). These cover visit-specific
input/output and logs, empty/falsy JSON values, artifact containment, browser
locale matching, saved language priority, and component rendering. Browser
verification should also select a running node and a completed node, switch
visits where available, and reload after changing the language.

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

The script creates a temporary Git workspace, writes a one-line `input.txt`, configures local executors, and starts [`examples/real-agent-pipeline.json`](../examples/real-agent-pipeline.json):

```text
claude_inspect (Claude Code) -> codex_verify (Codex)
```

The second node receives the first node's structured fields through `{{ nodes.claude_inspect.result.* }}`. Both agents must write a JSON object to the exact path in `AGENTFLOW_RESULT_FILE` (also printed in the execution contract). The script checks that the run succeeded, both nodes succeeded, and Codex returned `verified: true`. It prints the run metrics, including per-node duration and byte counts.

The Codex command uses `--dangerously-bypass-approvals-and-sandbox` because this is an unattended integration test. The fixture is disposable and the prompts prohibit source changes. Do not copy that flag into a production executor configuration without an isolated workspace policy.

To inspect the generated workspace after a run:

```bash
AGENTFLOW_KEEP_WORKSPACE=1 ./scripts/real-agent-smoke.sh
```

The default test removes the temporary workspace on exit. The model calls can take a few minutes and consume provider quota, so this script is intentionally excluded from `make check` and CI.

## What this catches

- Executor command expansion and non-interactive CLI invocation.
- Workspace binding and current-directory behavior.
- Passing structured results between dependent agent nodes.
- The result-file contract, including agents that cannot inspect process environment variables.
- Durable run state, log artifacts, and per-node observability metrics.

## Measuring a change in a flow

Run the same workflow against a defined baseline cohort, then attach evaluator-owned measurements to finished runs:

```bash
agentflow run evaluate <run-id> --evaluation '{"measurements":{"quality":{"value":0.9,"direction":"higher"},"cost":{"value":12,"unit":"USD","direction":"lower"}}}' --format json
agentflow run compare <run-id> <baseline-run-id> --format json
agentflow run stats <workflow-name> --by-version --format json
```

Use the comparison for a run-level explanation and `stats` for a cohort report. Runtime timing is collected automatically; quality and other business outcomes need an explicit evaluator and should include sample size and coverage in the report.

If a real run fails, preserve the workspace and inspect the exact run:

```bash
AGENTFLOW_KEEP_WORKSPACE=1 ./scripts/real-agent-smoke.sh
agentflow run get <run-id> --format json
agentflow run metrics <run-id> --format json
agentflow run logs <run-id> --format ndjson
```

## Cold-context workflow authoring

See [the authoring benchmark](../benchmarks/workflow-authoring/README.md). `make test-benchmark` runs only deterministic golden/reference checks. It also tests that alternate node names/order are accepted and defective workflows are rejected. A separate fresh agent process can author a solution from the binary and skill; its output is evaluated against behavior rather than an exact JSON answer. Live agent calls are not part of CI.

## Durable queue contract

`make test-e2e` also runs `scripts/test-durable-queue.py` against a temporary workspace. It starts actual CLI, worker, trigger and Web processes with deterministic Python/external fixtures; the proof JSON distinguishes model/IM coverage. Use `AGENTFLOW_QUEUE_WORKSPACE` and `AGENTFLOW_QUEUE_REPORT` to retain evidence at specific paths. See [queue contracts](durable-queue.md).
