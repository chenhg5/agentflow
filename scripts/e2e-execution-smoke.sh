#!/usr/bin/env bash
set -euo pipefail

# Deterministic CLI-level end-to-end test for execution semantics: parallel
# branches, failure isolation, runIf, non-blocking human nodes, retry, and
# cancel. Shell nodes only; no model provider is called.
ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
AGENTFLOW_BIN=${AGENTFLOW_BIN:-"$ROOT_DIR/bin/agentflow"}
KEEP_WORKSPACE=${AGENTFLOW_KEEP_WORKSPACE:-0}

if [[ ! -x "$AGENTFLOW_BIN" ]]; then
  echo "agentflow binary not found at $AGENTFLOW_BIN; run 'make build' or set AGENTFLOW_BIN" >&2
  exit 2
fi
command -v jq >/dev/null || { echo "jq is required" >&2; exit 2; }

WORKSPACE=$(mktemp -d "${TMPDIR:-/tmp}/agentflow-e2e.XXXXXX")
cleanup() {
  if [[ "$KEEP_WORKSPACE" == "1" ]]; then
    echo "workspace kept at $WORKSPACE"
  else
    rm -rf "$WORKSPACE"
  fi
}
trap cleanup EXIT

# A release-like window: two parallel builds, a human gate that does not block
# the smoke test, a publish step that fails until "fixed" exists, a failure
# alert, and a receipt that always runs.
cat > "$WORKSPACE/window.json" <<'JSON'
{
  "version": "1",
  "name": "e2e-window",
  "nodes": [
    { "id": "build_a", "type": "shell", "command": ["sh", "-c", "sleep 1; echo '{}' > \"$AGENTFLOW_RESULT_FILE\""] },
    { "id": "build_b", "type": "shell", "command": ["sh", "-c", "sleep 1; echo '{}' > \"$AGENTFLOW_RESULT_FILE\""] },
    { "id": "approval", "type": "human", "prompt": "Publish?" },
    { "id": "smoke", "type": "shell", "needs": ["build_a", "build_b"], "command": ["sh", "-c", "echo '{}' > \"$AGENTFLOW_RESULT_FILE\""] },
    { "id": "publish", "type": "shell", "needs": ["smoke", "approval"], "command": ["sh", "-c", "test -f fixed && echo '{}' > \"$AGENTFLOW_RESULT_FILE\""] },
    { "id": "alert", "type": "shell", "needs": ["publish"], "runIf": "failure", "command": ["sh", "-c", "echo '{\"alerted\":true}' > \"$AGENTFLOW_RESULT_FILE\""] },
    { "id": "receipt", "type": "shell", "needs": ["publish"], "runIf": "always", "command": ["sh", "-c", "echo '{\"receipt\":true}' > \"$AGENTFLOW_RESULT_FILE\""] }
  ]
}
JSON

pushd "$WORKSPACE" >/dev/null
"$AGENTFLOW_BIN" workflow validate window.json --format json | jq -e '.valid == true' >/dev/null

START=$(date +%s)
"$AGENTFLOW_BIN" run start window.json --format json > waiting.json
ELAPSED=$(( $(date +%s) - START ))
RUN_ID=$(jq -r '.run_id' waiting.json)
[[ "$ELAPSED" -lt 2 ]] || { echo "parallel builds took ${ELAPSED}s" >&2; exit 1; }
jq -e '.status == "waiting_human" and .nodes.smoke.status == "succeeded" and .nodes.approval.status == "waiting_human" and .nodes.publish.status == "pending"' waiting.json >/dev/null

set +e
"$AGENTFLOW_BIN" run approve "$RUN_ID" approval --message ok --format json > failed.json
APPROVE_STATUS=$?
set -e
[[ "$APPROVE_STATUS" -eq 1 ]]
jq -e '.status == "failed" and .nodes.publish.status == "failed" and .nodes.alert.status == "succeeded" and .nodes.receipt.status == "succeeded"' failed.json >/dev/null

"$AGENTFLOW_BIN" run retry "$RUN_ID" --dry-run --format json > plan.json || [[ $? -eq 10 ]]
jq -e '.reset_nodes == ["alert", "publish", "receipt"]' plan.json >/dev/null

touch fixed
"$AGENTFLOW_BIN" run retry "$RUN_ID" --format json > retried.json
jq -e '.status == "succeeded" and .nodes.publish.status == "succeeded" and .nodes.alert.status == "skipped" and .nodes.receipt.status == "succeeded" and .nodes.approval.status == "succeeded"' retried.json >/dev/null
jq -e '[.nodes.build_a.attempts, .nodes.build_b.attempts] == [1, 1]' retried.json >/dev/null

"$AGENTFLOW_BIN" run start window.json --format json > second.json
SECOND_ID=$(jq -r '.run_id' second.json)
"$AGENTFLOW_BIN" run cancel "$SECOND_ID" --format json | jq -e '.status == "cancelled"' >/dev/null
"$AGENTFLOW_BIN" run latest e2e-window --status cancelled --format json | jq -e --arg run "$SECOND_ID" '.run_id == $run' >/dev/null

echo "e2e execution smoke passed: $RUN_ID"
popd >/dev/null
