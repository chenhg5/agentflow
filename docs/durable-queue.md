# Durable queue and run relationships

All CLI start requests and trigger deliveries publish a graph snapshot, inputs,
run state, creation journal and execution outbox before any executor starts.
Programs can enqueue child runs and exit without keeping a parent process alive.
The graph schema remains unchanged.

## Start a service

Run commands from the workflow workspace root:

```sh
agentflow run enqueue workflows/item.json --input item=one --idempotency-key batch1-one
agentflow run worker --concurrency 4
```

`enqueue` returns a run ID immediately. It opts the workspace into managed
admission by persisting `queue.json`. That setting survives worker exit. Start
one foreground worker (or supervise that command with your process manager).
No detached executor is needed in the producer. Worker default concurrency is 1;
set the count for the workload. Stop and restart the owner to change the count.

`trigger serve --concurrency 4` uses the same queue and worker ownership lock.
When a worker already exists, the trigger service publishes to it; its own
concurrency flag does not replace the owner's budget. A second `run worker`
is rejected with exit 5. Each workspace has its own owner and run store.

In a managed workspace, `start`, `trigger fire`, `submit`, `retry`, `resume`, and
`cancel` return durable acceptance without waiting for execution. Omitting
`--async` cannot bypass admission. `--async` also opts a previously unmanaged
workspace into managed admission. If the worker is offline, work stays queued.
The response includes the current run state and `dispatch` generation/status;
`queued: true` means work is outstanding, not that it has completed.

Without `queue.json`, `run start` and controls retain synchronous behavior using
a temporary owner of this same queue, with one active run. Concurrent commands
coordinate through the owner lock. There is no alternate direct engine path in
the CLI. To dispatch children from inside a run, use **enqueue**, not synchronous
`start`, which could wait for its parent's occupied admission slot.

```sh
agentflow run submit <run> <node> --token <leaseToken> --output '{"score":8}' --async
agentflow run retry <failed-run> --async
agentflow run resume <run> --async
agentflow run queue --format json
agentflow run get <run> --format json
```

Concurrency limits **active runs**, not individual executor processes. A run
with `dispatch: all` can launch several executors within its slot. Explicit [workspace resource requests](resources.md) provide cross-run
executor quotas and generic locks independently of this active-run limit. A run releases its slot when only external input is
pending. A run with both executing and waiting nodes still occupies a slot.

## Persistence and recovery

- `queue.json`: durable managed mode and configured concurrency.
- `dispatch/<run>.json`: generation, acknowledged generation, claim, owner,
  status, reason, timestamps and last error.
- `worker.lock`: a persistent inode with an OS lock, held for the owner's whole
  lifetime, including graceful draining. A process crash releases the lock.
- `worker.json`: owner ID, concurrency and most recent heartbeat. Use the live
  lock check in `run queue` to distinguish an offline worker from stale metadata.
- `runs/requests/` and each run's immutable `dispatch.json`: creation journal and
  initial outbox, allowing interrupted publication to be repaired.
- `runs/<run>/control/`: durable submission, retry and cancellation inbox.

The worker recovers queued jobs and claims belonging to a dead owner. Completion
acknowledges only the claimed generation; a concurrent wake remains outstanding.
A control published before a producer dies, before the wake is written, is also
recovered by scanning the durable inbox. Requests are never acknowledged solely
because they were read. Queue errors are returned or retained in the job;
resuming explicitly can request another attempt after configuration is fixed.

Completed nodes and checkpointed attempts are retained on restart. An attempt
still recorded as running can execute again after interruption. Arbitrary
external effects are **at least once** across that uncertainty boundary: make
program effects idempotent. A hard-killed worker may leave its external child
process alive; process-tree termination is not provided by this version.
Graceful shutdown stops admission and waits for active executors. Cancellation
is applied at executor boundaries; cancelling a queued run does not launch its
executor. Parent cancellation does not cascade to children.

## Parent and correlation metadata

```sh
agentflow run enqueue workflows/item.json --input item=one \
  --idempotency-key batch1-one \
  --parent <parent-run-id> --parent-node dispatch \
  --correlation batch=batch1 --correlation item=one
agentflow run list --parent <parent-run-id> --correlation batch=batch1
agentflow run get <parent-run-id>
agentflow web --run <parent-run-id>
```

Parent run ID, optional parent node ID, and arbitrary string correlation labels
are immutable creation metadata. The parent must already exist in the same run
store, and the named node must exist in its snapshot. Paths, missing parents and
cyclic ancestry are rejected. This version does not link across workspaces.
Idempotency compares graph, inputs **and** relationship/correlation metadata;
changing any of these under the same key conflicts. Labels have no scheduling or
business meaning; repeated `--correlation key=value` list filters all must match.

A dispatcher program receives `AGENTFLOW_ROOT`, `AGENTFLOW_RUN_ID` and
`AGENTFLOW_NODE_ID`. It can invoke enqueue with `cwd=AGENTFLOW_ROOT` and use those
IDs as parent metadata. Provider sessions remain a separate execution policy.
Parent success means its dispatch completed; child completion/failure never
changes the parent. `run get` shows direct children and state counts. Web views
link in both directions. Children continue independently after parent exit.

## Verification

`make test-e2e` includes `scripts/test-durable-queue.py`. It uses real CLI and
worker processes, ordinary Python executors and external-input fixtures. It
checks ten nonempty children, 4 active / 6 queued, human slot release, ten
concurrent submissions, duplicate owner rejection, shared trigger ownership,
queued cancellation, failed dispatcher retry, abrupt and graceful restarts,
completed-node retention, immutable identity, and CLI/Web parent tracing.
Store unit tests exercise a wake concurrent with completion, an inbox write
without a wake, and a stale owner attempting to acknowledge a new claim. These
verify engine contracts; they do not exercise real model providers or IM APIs.
