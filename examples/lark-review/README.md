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
reported in worker logs; fix config and create a new request. There is currently
no automatic expiration timer or collection of free-form comments.

## Checks and visual review

```sh
AGENTFLOW_BIN=/path/to/agentflow python3 -m unittest discover -s examples/lark-review -v
```

Tests use the real engine and a fake Feishu transport; no real messages are sent.
`preview.html` renders the same card payloads approximately in a browser, with
mobile widths and state switches. It is a design preview, not Feishu's renderer.
`preview-cards.json` contains the actual Card 2.0 payloads for inspection.
