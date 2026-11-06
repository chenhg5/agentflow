# Unreleased

Changes since [v0.6.0](v0.6.0.md).

- `trigger status` no longer reports finished runs as queued. Delivery receipts keep the run status from when the run was created, so every run a trigger had ever started showed `queued`. Deliveries now show `delivered` once a run exists, `run_status` / `run.status` with the run's current status (`missing` if its directory is gone), newest first, and a `summary` with counts by run status and `active_runs`, the real backlog.

- Performance: an idle queue worker no longer rereads every run the workspace ever had. Finished jobs move to `dispatch/settled/`, so a poll reads only live jobs (11 ms → 0.01 ms at 800 finished runs, 83 ms → 0.02 ms at 5,000; it held the queue lock all that time). Orphaned controls on finished or unqueued runs are recovered at a worker's first poll and every 30 seconds. `run queue` lists live jobs and counts settled ones under `terminal` and `settled`.
- Performance: trigger dispatch reads only the runs of that trigger's own deliveries, skips those whose queue job settled, and reads only their status, instead of loading every run in the workspace for the overlap check. The idempotency check reads each run's state once per process (workflow name and key never change). At 800 runs a scheduled dispatch did about 0.4 s of run loading; it now does about 15 ms after the first.
