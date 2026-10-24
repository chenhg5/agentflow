#!/usr/bin/env bash
set -euo pipefail
ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
AGENTFLOW_BIN=${AGENTFLOW_BIN:-"$ROOT_DIR/bin/agentflow"}
WORKSPACE=$(mktemp -d "${TMPDIR:-/tmp}/agentflow-execution-e2e.XXXXXX")
trap 'if [[ "${AGENTFLOW_KEEP_WORKSPACE:-0}" != "1" ]]; then rm -rf "$WORKSPACE"; fi' EXIT
cat > "$WORKSPACE/window.json" <<'JSON'
{
  "version":"3", "name":"e2e-window", "entry":"build",
  "nodes":[
    {"id":"build","executor":"process","config":{"command":["sh","-c","printf '{}' > \"$AGENTFLOW_RESULT_FILE\""]}},
    {"id":"review","executor":"external","outputSchema":{"type":"object","required":["score"],"properties":{"score":{"type":"number"}}}},
    {"id":"publish","executor":"process","config":{"command":["sh","-c","test -f fixed && printf '{}' > \"$AGENTFLOW_RESULT_FILE\""]}}
  ],
  "edges":[
    {"from":"build","to":"review"},
    {"from":"review","to":"build","when":"nodes.review.result.score < 5","maxIterations":1},
    {"from":"review","to":"publish","when":"nodes.review.result.score >= 5"},
    {"from":"publish","to":"$end"}
  ]
}
JSON
pushd "$WORKSPACE" >/dev/null
"$AGENTFLOW_BIN" workflow validate window.json --format json | jq -e '.valid' >/dev/null
"$AGENTFLOW_BIN" run start window.json --format json > waiting.json
RUN_ID=$(jq -r '.run_id' waiting.json)
jq -e '.status == "waiting_input"' waiting.json >/dev/null
set +e
"$AGENTFLOW_BIN" run submit "$RUN_ID" review --output '{"score":"bad"}' --format json > invalid.json 2>/dev/null
INVALID_STATUS=$?
set -e
[[ "$INVALID_STATUS" -eq 2 ]]
"$AGENTFLOW_BIN" run submit "$RUN_ID" review --output '{"score":3}' --format json > rework.json
jq -e '.status == "waiting_input" and .visits.build == 2 and .visits.review == 2' rework.json >/dev/null
set +e
"$AGENTFLOW_BIN" run submit "$RUN_ID" review --output '{"score":8}' --format json > failed.json
FAILED_STATUS=$?
set -e
[[ "$FAILED_STATUS" -eq 1 ]]
jq -e '.status == "failed" and .nodes.publish.status == "failed"' failed.json >/dev/null
"$AGENTFLOW_BIN" run retry "$RUN_ID" --dry-run --format json > plan.json || [[ $? -eq 10 ]]
jq -e '.reset_nodes == ["publish"]' plan.json >/dev/null
touch fixed
"$AGENTFLOW_BIN" run retry "$RUN_ID" --format json > retried.json
jq -e '.status == "succeeded" and .nodes.publish.status == "succeeded" and .visits.build == 2' retried.json >/dev/null
"$AGENTFLOW_BIN" run start window.json --format json > second.json
SECOND_ID=$(jq -r '.run_id' second.json)
"$AGENTFLOW_BIN" run cancel "$SECOND_ID" --format json | jq -e '.status == "cancelled"' >/dev/null
# Node type/provider aliases must be rejected, never silently ignored.
jq '.nodes[0].type = "program"' window.json > obsolete.json
set +e
"$AGENTFLOW_BIN" workflow validate obsolete.json --format json >/dev/null 2>&1
OBSOLETE_STATUS=$?
set -e
[[ "$OBSOLETE_STATUS" -eq 2 ]]
echo "e2e execution smoke passed: $RUN_ID"
popd >/dev/null
