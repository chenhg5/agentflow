# Unreleased

Changes since [v0.6.0](v0.6.0.md).

- `trigger status` no longer reports finished runs as queued. Delivery receipts keep the run status from when the run was created, so every run a trigger had ever started showed `queued`. Deliveries now show `delivered` once a run exists, `run_status` / `run.status` with the run's current status (`missing` if its directory is gone), newest first, and a `summary` with counts by run status and `active_runs`, the real backlog.
