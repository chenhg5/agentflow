#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
AGENTFLOW_BIN=${AGENTFLOW_BIN:-"$ROOT_DIR/bin/agentflow"}
KEEP_WORKSPACE=${AGENTFLOW_KEEP_WORKSPACE:-0}
PORT=${AGENTFLOW_E2E_PORT:-18761}

if [[ ! -x "$AGENTFLOW_BIN" ]]; then
  echo "agentflow binary not found at $AGENTFLOW_BIN; run 'make build' or set AGENTFLOW_BIN" >&2
  exit 2
fi
command -v jq >/dev/null || { echo "jq is required" >&2; exit 2; }
command -v curl >/dev/null || { echo "curl is required" >&2; exit 2; }

WORKSPACE=$(mktemp -d "${TMPDIR:-/tmp}/agentflow-trigger-e2e.XXXXXX")
SERVER_PID=""
cleanup() {
  if [[ -n "$SERVER_PID" ]]; then
    kill "$SERVER_PID" 2>/dev/null || true
    wait "$SERVER_PID" 2>/dev/null || true
  fi
  if [[ "$KEEP_WORKSPACE" == "1" ]]; then
    echo "workspace kept at $WORKSPACE"
  else
    rm -rf "$WORKSPACE"
  fi
}
trap cleanup EXIT

mkdir -p "$WORKSPACE/.agentflow"
cat > "$WORKSPACE/webhook-workflow.json" <<'JSON'
{"version": "2", "name": "webhook-e2e", "entry": "work", "nodes": [{"id": "work", "executor": "process", "input": {"command": ["sh", "-c", "printf '{\"done\":true}' > \"$AGENTFLOW_RESULT_FILE\""], "name": "{{ inputs.name }}"}}], "edges": [{"from": "work", "to": "$end"}], "inputs": {"name": {"type": "string", "required": true}}}
JSON
cat > "$WORKSPACE/cron-workflow.json" <<'JSON'
{"version": "2", "name": "cron-e2e", "entry": "work", "nodes": [{"id": "work", "executor": "process", "input": {"command": ["sh", "-c", "printf '{\"done\":true}' > \"$AGENTFLOW_RESULT_FILE\""]}}], "edges": [{"from": "work", "to": "$end"}]}
JSON
cat > "$WORKSPACE/.agentflow/triggers.json" <<'JSON'
{"triggers":[{"id":"webhook-e2e","enabled":true,"source":{"kind":"webhook","path":"/hooks/e2e","method":"POST","deliveryIDHeader":"X-Delivery-ID"},"target":{"workflow":"webhook-workflow.json"},"bindings":{"name":{"from":"json","path":"$.name"}}},{"id":"cron-e2e","enabled":true,"source":{"kind":"cron","expression":"* * * * *"},"target":{"workflow":"cron-workflow.json"},"inputs":{"name":"cron"}}]}
JSON

pushd "$WORKSPACE" >/dev/null
"$AGENTFLOW_BIN" trigger validate --format json > trigger-validate.json
jq -e '.valid == true and .count == 2' trigger-validate.json >/dev/null

"$AGENTFLOW_BIN" trigger fire cron-e2e --delivery-id cron-fire-e2e --format json > cron-fire.json
jq -e '.status == "succeeded" and .trigger.delivery_id == "cron-fire-e2e"' cron-fire.json >/dev/null

"$AGENTFLOW_BIN" trigger serve --host 127.0.0.1 --port "$PORT" > trigger-serve.log 2>&1 &
SERVER_PID=$!
for _ in $(seq 1 50); do
  code=$(curl -sS -o /dev/null -w '%{http_code}' "http://127.0.0.1:$PORT/hooks/e2e" || true)
  if [[ "$code" == "405" ]]; then
    break
  fi
  sleep 0.1
done

curl -fsS -X POST "http://127.0.0.1:$PORT/hooks/e2e" \
  -H 'Content-Type: application/json' \
  -H 'X-Delivery-ID: webhook-e2e-check' \
  -d '{"name":"webhook"}' > webhook.json
jq -e '.status == "queued" and .delivery_id == "webhook-e2e-check"' webhook.json >/dev/null
WEBHOOK_RUN_ID=$(jq -r '.run.run_id' webhook.json)
sleep 0.5
"$AGENTFLOW_BIN" run get "$WEBHOOK_RUN_ID" --format json > webhook-run.json
jq -e '.status == "succeeded" and .inputs.name == "webhook" and .nodes.work.input.name == "webhook"' webhook-run.json >/dev/null

curl -fsS -X POST "http://127.0.0.1:$PORT/hooks/e2e" \
  -H 'Content-Type: application/json' \
  -H 'X-Delivery-ID: webhook-e2e-check' \
  -d '{"name":"webhook"}' > webhook-duplicate.json
jq -e '.duplicate == true and .delivery_id == "webhook-e2e-check"' webhook-duplicate.json >/dev/null

for _ in $(seq 1 70); do
  count=$(find "$WORKSPACE/.agentflow/runs" -type f -name state.json -print0 2>/dev/null | xargs -0 -r grep -l '"workflowName": "cron-e2e"' | wc -l | tr -d ' ')
  if [[ "$count" -ge 2 ]]; then
    break
  fi
  sleep 1
done
[[ "$count" -ge 2 ]]

echo "e2e trigger smoke passed: webhook_run=$WEBHOOK_RUN_ID cron_runs=$count"
popd >/dev/null
