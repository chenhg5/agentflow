# Stable schedule occurrences

The scheduler separates **planned time** from **receipt time**. Bind trusted
event facts into workflow inputs explicitly:

```json
{"triggers":[{
  "id":"daily",
  "enabled":true,
  "source":{"kind":"cron","expression":"15 2 * * *","timezone":"Asia/Shanghai"},
  "target":{"workflow":"workflow.json"},
  "bindings":{
    "planned":{"from":"event","path":"scheduled_at"},
    "received":{"from":"event","path":"received_at"},
    "zone":{"from":"event","path":"schedule_timezone"},
    "occurrence":{"from":"event","path":"delivery_id"}
  },
  "policy":{"concurrency":"forbid","misfire":"fire-once"}
}]}
```

Other readonly paths are `trigger_id` and `catch_up`. Facts cannot be overridden
by event attributes. Bindings resolve to strings under the existing workflow
input contract, including `"true"`/`"false"` for catch_up. Missing facts fail
resolution (for example, a webhook has no scheduled time). Use `source.timezone`;
inline TZ/CRON_TZ prefixes in expressions are rejected to keep one authority.

For Shanghai 2026-10-26 02:15, scheduled_at is `2026-10-25T18:15:00Z`.
A normal application that converts this instant into Asia/Shanghai and subtracts
one calendar day gets report date **2026-10-25**, even if execution waits until
the 27th. The scheduler never computes business dates. Use a normal executor for
that conversion, not its current execution date. Receipt time, timezone and
planned time remain fixed in the run's inputs and trigger metadata.

## First enable, restart and edits

- The first service observation establishes a baseline at that instant. It does
  not invent history before the workspace's first cursor.
- Default `skip` records occurrences missed during downtime without running
  them. `fire-once` selects only the **latest** missed occurrence; earlier ones
  have a skipped count plus first/last planned instants. It is not catch-up-all.
- A live minute timer tolerates polling within the scheduled minute. On startup,
  or when more than a minute late, the configured misfire policy applies.
- Definition changes establish a new baseline at the next service start. This
  includes expression/timezone/input/target/policy changes; pure JSON formatting
  does not change the fingerprint. The prior through-time and the observed new
  boundary are retained. Disabled/re-enabled definitions also get boundaries.
- A previously persisted plan is recovered under its frozen definition **before**
  establishing an edit boundary. Already accepted work is recovered separately
  from the at-most-one newly selected catch-up occurrence. Removing a definition
  stops its scheduling; an outstanding plan stays visible and resumes if the
  definition is restored. Unobserved off/on edits cannot be inferred as history.
- Configuration is loaded at service start. Preserve the cursor/receipt/run
  records and restart to apply edits; deleting them loses deduplication history.

Automatic occurrence identity is trigger ID plus planned UTC instant, never the
restart or callback time. The catch-up flag and first receipt time are persisted;
they do not create a different identity. `trigger fire` is an explicit ad-hoc
delivery: its custom delivery ID intentionally belongs to the manual stream.
Do not run a host timer and the persistent scheduler for the same business job
without defining a single authority.

## Recoverable handoff

The same workspace uses `scheduler.lock` for the service, a short
`cron-state.lock` for cursor transactions, and `trigger-dispatch.lock` for receipt
transactions. A durable plan precedes dispatch. A pending receipt freezes the
prepared graph, inputs and event facts before AF-01 atomic run creation. A complete
receipt precedes cursor advancement. Recovery reuses that request and run ID even
if interrupted after run creation or after a graph file edit. It never checks
forbid against its own already accepted creation intent.

Other pending creation intents count as active under forbid. A genuinely active
run still results in a persisted `skipped / active_run` decision; catch-up does
not bypass concurrency. A delivery skipped by policy is handled, not silently
retried later. There is no external side-effects exactly-once guarantee.

Inspect all persisted evidence with:

```sh
agentflow trigger validate --format json
agentflow trigger status --format json
```

`trigger-cursors/` contains baselines, outstanding plans, missed ranges and the
last decision. `trigger-deliveries/` contains pending/completed/skipped receipts
and prepared request snapshots. `runs/` remains the execution truth. These are
runtime state and are ignored by scaffolds; keep them in the durable store rather
than committing local observations as workflow definitions.

Tests inject Shanghai clocks, run a deterministic normalization process, execute
two competing runners, and abruptly exit test processes at five plan/receipt/run/
watermark boundaries. They require no waiting for real days or provider account.
