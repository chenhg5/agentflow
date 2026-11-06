## Unreleased

Changes that are not part of a tagged release yet.

### Added

- `agentflow hub serve`: a Hub that collects runs from many machines into one web console (overview with daily runs chart, runs with filters, run detail with a step timeline, workflows with P50/P95, machines) and a read API under `/api/v1`. SQLite storage through a pure-Go driver, so the binary stays cgo-free. See `docs/hub.md`.
- Run reporting: with `AGENTFLOW_HUB_URL` (or `.agentflow/hub.json`, `~/.agentflow/hub.json`) set, `run start/approve/reject/resume/cancel` and `trigger fire/serve` report every state change through a local outbox. Reports are idempotent snapshots, secrets are masked, and a Hub outage never affects a run.
- `agentflow hub push [--all]` delivers pending reports and backfills existing runs; `agentflow hub status` shows the configuration, outbox size, and the last delivery.
- The reporting protocol (`internal/hubapi`, version 1) carries `orchestration: json|code`, so a future code-orchestrated runner reports to the same Hub.

