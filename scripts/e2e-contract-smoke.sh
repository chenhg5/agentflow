#!/usr/bin/env bash
set -euo pipefail

# Deterministic CLI-level end-to-end test for workflow contracts. It does not
# call a model executor; process invocations exercise runner, persistence, schema
# validation, explicit data binding, and external output submission.
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

cat > "$WORKSPACE/contracts.json" <<'JSON'
{
  "version": "2",
  "name": "e2e-contracts",
  "entry": "produce",
  "nodes": [
    {
      "id": "produce",
      "executor": "process",
      "input": {
        "command": [
          "sh",
          "-c",
          "printf '{\"summary\":\"fixture ok\",\"count\":2}' > \"$AGENTFLOW_RESULT_FILE\""
        ]
      },
      "outputSchema": {
        "type": "object",
        "properties": {
          "summary": {
            "type": "string"
          },
          "count": {
            "type": "integer"
          }
        },
        "required": [
          "summary",
          "count"
        ],
        "additionalProperties": false
      }
    },
    {
      "id": "verify",
      "executor": "process",
      "input": {
        "command": [
          "sh",
          "-c",
          "printf '{\"verified\":true}' > \"$AGENTFLOW_RESULT_FILE\""
        ],
        "summary": "{{ nodes.produce.result.summary }}",
        "count": "{{ nodes.produce.result.count }}"
      },
      "outputSchema": {
        "type": "object",
        "properties": {
          "verified": {
            "type": "boolean"
          }
        },
        "required": [
          "verified"
        ],
        "additionalProperties": false
      },
      "inputSchema": {
        "type": "object",
        "properties": {
          "command": {
            "type": "array"
          },
          "summary": {
            "type": "string"
          },
          "count": {
            "type": "integer"
          }
        },
        "required": [
          "command",
          "summary",
          "count"
        ],
        "additionalProperties": false
      }
    },
    {
      "id": "approval",
      "executor": "external",
      "input": {
        "verified": "{{ nodes.verify.result.verified }}"
      },
      "outputSchema": {
        "type": "object",
        "properties": {
          "choice": {
            "type": "string"
          },
          "note": {
            "type": "string"
          }
        },
        "required": [
          "choice",
          "note"
        ],
        "additionalProperties": false
      }
    },
    {
      "id": "finish",
      "executor": "process",
      "input": {
        "command": [
          "sh",
          "-c",
          "printf '{\"done\":true}' > \"$AGENTFLOW_RESULT_FILE\""
        ],
        "choice": "{{ nodes.approval.result.choice }}",
        "note": "{{ nodes.approval.result.note }}"
      }
    }
  ],
  "edges": [
    {
      "from": "produce",
      "to": "verify"
    },
    {
      "from": "verify",
      "to": "approval"
    },
    {
      "from": "approval",
      "to": "finish",
      "when": "nodes.approval.result.choice == 'happy'"
    },
    {
      "from": "approval",
      "to": "$end"
    },
    {
      "from": "finish",
      "to": "$end"
    }
  ]
}
JSON
cat > "$WORKSPACE/invalid.json" <<'JSON'
{
  "version": "2",
  "name": "e2e-invalid-contract",
  "entry": "bad",
  "nodes": [
    {
      "id": "bad",
      "executor": "process",
      "input": {
        "command": [
          "sh",
          "-c",
          "printf '{\"slug\":\"NOT-valid\"}' > \"$AGENTFLOW_RESULT_FILE\""
        ]
      },
      "outputSchema": {
        "type": "object",
        "properties": {
          "slug": {
            "type": "string",
            "pattern": "^[a-z]+$"
          }
        },
        "required": [
          "slug"
        ],
        "additionalProperties": false
      }
    }
  ],
  "edges": [
    {
      "from": "bad",
      "to": "$end"
    }
  ]
}
JSON

pushd "$WORKSPACE" >/dev/null
"$AGENTFLOW_BIN" workflow validate contracts.json --format json > validate.json
jq -e '.valid == true' validate.json >/dev/null

"$AGENTFLOW_BIN" run start contracts.json --format json > waiting.json
RUN_ID=$(jq -r '.run_id' waiting.json)
jq -e '.status == "waiting_input" and .nodes.produce.status == "succeeded" and .nodes.verify.status == "succeeded" and .nodes.approval.status == "waiting_input"' waiting.json >/dev/null

INPUT_ARTIFACT=".agentflow/runs/$RUN_ID/artifacts/verify.1.input.json"
jq -e '.summary == "fixture ok" and .count == 2' "$INPUT_ARTIFACT" >/dev/null
jq -e '.nodes.verify.artifacts.input == "artifacts/verify.1.input.json"' waiting.json >/dev/null

"$AGENTFLOW_BIN" run submit "$RUN_ID" approval --output '{"choice":"happy","note":"e2e approved"}' --format json > succeeded.json
jq -e '.status == "succeeded" and .nodes.finish.status == "succeeded" and .nodes.finish.result.done == true' succeeded.json >/dev/null
jq -e '.nodes.finish.input.choice == "happy" and .nodes.finish.input.note == "e2e approved"' succeeded.json >/dev/null

set +e
"$AGENTFLOW_BIN" run start invalid.json --format json > invalid-run.json
INVALID_STATUS=$?
set -e
[[ "$INVALID_STATUS" -ne 0 ]]
jq -e '.status == "failed" and (.nodes.bad.error | contains("output schema validation failed"))' invalid-run.json >/dev/null

DIGEST=$(jq -r '.workflow_digest' succeeded.json)
[[ "$DIGEST" =~ ^[0-9a-f]{12}$ ]]
"$AGENTFLOW_BIN" run latest e2e-contracts --status succeeded --format json > latest.json
jq -e --arg run "$RUN_ID" '.run_id == $run' latest.json >/dev/null
"$AGENTFLOW_BIN" run list --workflow e2e-invalid-contract --status failed --format json > failed-list.json
jq -e '(.runs | length) == 1 and .runs[0].status == "failed"' failed-list.json >/dev/null
"$AGENTFLOW_BIN" run stats e2e-contracts --by-version --format json > by-version.json
jq -e --arg digest "$DIGEST" '(.versions | length) == 1 and .versions[0].workflow_digest == $digest and .versions[0].duration_ms.count == 1 and .versions[0].waiting_ms.max_ms > 0' by-version.json >/dev/null
set +e
"$AGENTFLOW_BIN" run latest no-such-workflow --format json > /dev/null 2>&1
LATEST_STATUS=$?
set -e
[[ "$LATEST_STATUS" -eq 3 ]]

echo "e2e contract smoke passed: $RUN_ID"
popd >/dev/null
