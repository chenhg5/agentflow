# Recoverable Feishu interaction adapter

**首次使用请从 [五步配置指引](SETUP.md) 开始。** 配套文件：workflow.json、executors.json、config.example.json、config.schema.json、lark_adapter.py、supervisor.example.conf。

## Lifecycle and boundaries

1. AgentFlow persists `waiting_input` and releases its run lock. Waiting counts as
   waiting time, not agent compute time. No per-node process waits for a human.
2. The service discovers waiting invocations by executor name, reading the local
   run state and rendered config artifact. It starts the event consumer and waits
   for its documented ready marker before sending cards.
3. An invocation has one journal record and stable send idempotency key. The card
   callback carries its invocation token and action index. Message ID, recipient,
   token, current run state and action index must all match.
4. The listener commits events to a SQLite inbox; the first valid choice is
   committed before submitting. Later conflicting clicks are ignored. One run
   worker calls `run submit --token ... --output ...`. It does not block ingestion
   of other cards while downstream agents work.
5. The engine checks the invocation token again when applying queued controls,
   validates the output schema and traverses ordinary edges. Stale queued results
   cannot affect a later visit of the same node.
6. Restart reconciliation uses persisted node/history tokens and output evidence.
   It retries recorded submissions and pending card updates, and resumes a run
   interrupted after acceptance. Card patch failure never undoes a submission.
   A cancellation or superseded invocation removes buttons and marks the card stale.

Cards distinguish **waiting**, **choice recorded locally**, **result accepted by
workflow**, and **expired**. Acceptance does not claim the whole workflow succeeded.
Colors and confirmation copy come from action configuration, including negative
outcomes. Logs expose API failures; workers log into the journal directory.

This is a small local adapter, not an exactly-once messaging service. Feishu's
WebSocket stream is not a durable inbox: clicks made while the adapter/connection
is offline, or lost before the local inbox commit, may need to be clicked again.
The original card remains usable after reconnect. A crash between a successful
send and saving its message ID relies on the upstream idempotency window; preserve
the journal and avoid deleting/rebuilding it. Cards older than the message patch
API's 14-day limit need operator intervention. Invalid output contracts are
reported in worker logs; fix config and create a new request. Preserve the journal
across restarts. Drain pending recorded submissions before upgrading an adapter;
the journal is not a portable interchange format across different implementations.

## Dynamic fields and explicit deadline

The example now collects a free-form comment. This is adapter configuration,
inside `candidate.config`, and has no special meaning in the graph engine:

```json
{
  "form":{"fields":[{"name":"comments","type":"multiline","label":"修改意见","maxLength":1000}]},
  "actions":[{"label":"要求修改","output":{"decision":"request_changes","case":"case-1","sha":"sha-one"},
              "formOutput":{"comments":"comments"},"requiredFormFields":["comments"]}],
  "deadline":{"afterSeconds":3600,"output":{"decision":"expired","case":"case-1","sha":"sha-one","comments":"审核超时"}}
}
```

Merge this fragment with recipient/title/sections. Output names and values remain
arbitrary; align the entire output with `node.outputSchema` and ordinary edges.
`formOutput` maps **destination output key → source field name**; it cannot overlap
any fixed output key. A callback cannot supply additional output keys or override
the displayed case/version. All unknown fields, non-text values, overlong text and
action-specific required fields containing only whitespace are rejected without
advancing. The original accepted text, including whitespace, is preserved in the
journal and output. Dynamic values are not engine templates: write `"comments"`,
not `"{{ comments }}"`. Only actual filled values undergo merged output dry-run;
no fabricated placeholder is submitted to preflight.

Fields support `text` and `multiline`. Each has a unique name, label and explicit
maxLength of 1–1000, matching the [Card 2.0 input limit](https://open.feishu.cn/document/feishu-cards/card-json-v2-components/interactive-components/input).
Names beginning `af_` are reserved for invocation-bound form buttons. The adapter
reads `form_value` and `action_name` according to `lark-cli event schema
card.action.trigger`; form buttons use `form_action_type: submit`, as documented
for the [form container](https://open.feishu.cn/document/feishu-cards/card-json-v2-components/containers/form-container).
Requiredness is checked per action on the server; marking a field globally
required in the card would incorrectly block actions that allow an empty comment.

`deadline` requires **either** an absolute timezone-qualified `at` timestamp **or**
positive `afterSeconds`, plus an explicit schema-valid timeout `output`. Relative
time starts at the persisted external candidate attempt's start, including when
the service first discovers it late. The computed `deadline_at` is durable and
never extended by restarting. Omit `deadline` for no automatic expiry.
`node.timeoutMs` is not an external-input timer.

The first locally committed valid choice wins; the deadline check and selection
share one SQLite transaction. After expiry, even a callback claiming an earlier
client timestamp cannot approve: it selects only the configured timeout output.
The service processes overdue requests after restart. The timeout path uses the
same lease-token-scoped submission as a human and ordinary graph edges can route
it to `$end` or a timeout handler. There is no implicit approval and no run-wide
cancel, which could otherwise target a later invocation. Deadline means the end
of waiting for a human choice, not a guarantee that all downstream work finishes
by that time. A saved choice blocked by validation remains blocked for inspection;
it is not silently replaced with another choice.

## Version checks and acceptance evidence

Put report links and immutable version fingerprints in `sections`, and carry
their identifiers in fixed button output. An optional generic pre-submit hook is:

```json
{"preSubmit":{"command":["python3","executors/programs/review/check_version.py"],"timeoutSeconds":10}}
```

The hook runs with workspace cwd, receives JSON on stdin containing run_id,
node_id, token, selection, source (`human`/`timeout`), the already persisted
output/form_values/selected_at and config snapshot. It must exit zero and emit
`{"ok":true}`; another result, invalid JSON, launch failure or timeout blocks
submission with a retained error. Hooks must be read-only/idempotent because a
restart may rerun them. No shell evaluates argv and callbacks cannot change it.
The adapter does not interpret business versions or perform business actions.

A pre-submit hook alone cannot atomically prevent a version change after its
check. Where applying approval requires exact freshness, immediately follow the
review node with a deterministic business gate that checks and applies the
decision under the business system's concurrency controls. RSI owns that gate;
adapter acceptance is not authorization to skip it.

The journal keeps stable rejection reason codes and the actual merged selection
before invoking the CLI. Cards distinguish saved choice, observed engine
acceptance, and separately observed successful run completion; a downstream
failure/cancel is also shown separately. Failed card patching retries without
reversing engine acceptance. A blocked or superseded request must be inspected
and replaced rather than editing the recorded output. Single designated user
private chat remains the supported channel scope.

## Checks and visual review

```sh
AGENTFLOW_BIN=/path/to/agentflow python3 -m unittest discover -s examples/lark-review -v
```

Tests use the real engine and a fake Feishu transport; no real messages are sent.
AF-06 tests inject a clock and simulate forms, old cards, identity mismatch,
schema errors, restart windows, stale-version hooks, and deadline/click races.
These do not certify a real client's rendering or a human callback; that requires
a separately authorized real user entering text and clicking the card.
`preview.html` renders the same card payloads approximately in a browser, with
mobile widths and state switches. It is a design preview, not Feishu's renderer.
`preview-cards.json` contains the actual Card 2.0 payloads for inspection.
