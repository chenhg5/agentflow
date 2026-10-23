#!/usr/bin/env bash
set -euo pipefail

# This test intentionally invokes real model CLIs and may consume provider quota.
# Keep it opt-in; the default `make check` suite uses deterministic fake drivers.

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
AGENTFLOW_BIN=${AGENTFLOW_BIN:-"$ROOT_DIR/bin/agentflow"}
MODEL=${AGENTFLOW_REAL_CODEX_MODEL:-gpt-5.6-sol}
KEEP_WORKSPACE=${AGENTFLOW_KEEP_WORKSPACE:-0}

if [[ ! -x "$AGENTFLOW_BIN" ]]; then
  echo "agentflow binary not found at $AGENTFLOW_BIN; run 'make build' or set AGENTFLOW_BIN" >&2
  exit 2
fi
command -v claude >/dev/null || { echo "claude CLI is required" >&2; exit 2; }
command -v codex >/dev/null || { echo "codex CLI is required" >&2; exit 2; }
command -v jq >/dev/null || { echo "jq is required" >&2; exit 2; }

WORKSPACE=$(mktemp -d "${TMPDIR:-/tmp}/agentflow-real-smoke.XXXXXX")
cleanup() {
  if [[ "$KEEP_WORKSPACE" == "1" ]]; then
    echo "workspace kept at $WORKSPACE"
  else
    rm -rf "$WORKSPACE"
  fi
}
trap cleanup EXIT

printf 'AgentFlow real integration fixture\n' > "$WORKSPACE/input.txt"
mkdir -p "$WORKSPACE/.agentflow"
cp "$ROOT_DIR/examples/real-agent-pipeline.json" "$WORKSPACE/workflow.json"
cat > "$WORKSPACE/.agentflow/executors.json" <<JSON
{
  "executors": {
    "claude-code": {
      "kind": "claude-code",
      "binary": "claude",
      "extraArgs": ["--permission-mode", "acceptEdits", "--permission-prompts", "none"]
    },
    "codex": {
      "kind": "codex",
      "binary": "codex",
      "model": "$MODEL",
      "dangerouslyBypassApprovalsAndSandbox": true,
      "skipGitRepoCheck": true,
      "ephemeral": true
    }
  }
}
JSON

pushd "$WORKSPACE" >/dev/null
set +e
"$AGENTFLOW_BIN" run start workflow.json --input workspace=. --format json > run.json
STATUS=$?
set -e
cat run.json
RUN_ID=$(jq -r '.run_id // empty' run.json)
if [[ "$STATUS" -ne 0 || -z "$RUN_ID" ]]; then
  echo "real-agent workflow failed to start (exit=$STATUS)" >&2
  if [[ "$STATUS" -eq 0 ]]; then
    exit 1
  fi
  exit "$STATUS"
fi

"$AGENTFLOW_BIN" run metrics "$RUN_ID" --format json > metrics.json
jq '{status, duration_ms, totals, nodes: [.nodes[] | {id, status, duration_ms, input_bytes, output_bytes, stdout_bytes, stderr_bytes}]}' metrics.json

if ! jq -e '.status == "succeeded" and .nodes.claude_inspect.status == "succeeded" and .nodes.codex_verify.status == "succeeded" and .nodes.codex_verify.result.verified == true' run.json >/dev/null; then
  echo "real-agent workflow did not produce the expected structured results" >&2
  "$AGENTFLOW_BIN" run logs "$RUN_ID" --format ndjson >&2 || true
  exit 1
fi

echo "real-agent smoke passed: $RUN_ID"
popd >/dev/null
