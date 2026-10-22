# Execution semantics

How the runner decides what runs, when a run ends, and how other commands change a run that is executing.

## Scheduling

- Every pending node whose dependencies have finished is ready. Ready nodes run in parallel, up to the workflow's `maxParallel` (default `4`).
- Nodes start in definition order when slots are limited.
- `when` is evaluated after the dependency rule below; a false `when` skips the node with reason `when_false`.

## Dependency rule: `runIf`

| `runIf` | Runs when | Otherwise |
| --- | --- | --- |
| `success` (default) | every node in `needs` succeeded | skipped: `dependency_failed` or `dependency_skipped` |
| `failure` | any upstream node (direct or indirect) failed | skipped: `no_upstream_failure` |
| `always` | every node in `needs` finished, whatever the outcome | — |

A failed node only affects nodes downstream of it. Independent branches keep running. Typical uses:

```json
{ "id": "alert",   "type": "shell", "needs": ["publish"], "runIf": "failure", "command": ["lark-cli", "..."] },
{ "id": "receipt", "type": "shell", "needs": ["publish"], "runIf": "always",  "command": ["./write-receipt.sh"] }
```

## Human nodes

A human node waits without blocking other branches. The run reports `waiting_human` once nothing else can progress. `run approve` marks the node succeeded and continues the run. `run reject` marks it failed: dependents are skipped, and `runIf: failure` / `always` nodes run.

## Run status

When nothing is running:

- `waiting_human` if any node is waiting for a decision;
- `failed` if any node failed (even when a failure handler succeeded);
- `succeeded` otherwise.

A `cancelled` run schedules nothing new. Commands already running finish, but their results do not change the status.

## One executor per run

Only one process executes a run at a time. It holds `executor.lock` in the run directory, which records its PID. A lock left by a process that no longer exists is taken over, and nodes it left `running` go back to `pending` and run again (event `node.orphaned`). `run resume` uses this to recover from a crash.

`run approve`, `run reject`, `run cancel`, and `run retry` never write run state directly. They write a request under `control/`. The executor applies requests while it runs, checking about every 500 ms. If the run is idle, the command becomes the executor itself. When another process is executing, the command returns the current state with `"queued": true`.

## Retry

`run retry <run-id>` resets every failed node and everything downstream of it, keeps all other results, and continues the run. `--from a,b` restarts from chosen nodes instead. `--dry-run` lists the nodes it would reset. Nodes downstream of a reset node run again, including human approvals.
