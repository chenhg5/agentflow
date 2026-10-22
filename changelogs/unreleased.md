## Unreleased

Changes that are not part of a tagged release yet.

### Added

- Independent ready nodes run in parallel, bounded by the workflow's `maxParallel` (default 4).
- `runIf` on nodes: `success` (default), `failure` (an upstream node failed), `always` (all needs finished).
- `run retry <run-id> [--from a,b] [--dry-run]` resets failed nodes, or chosen nodes, plus everything downstream, and keeps other results.
- `run resume` recovers nodes left `running` by a crashed executor.
- `docs/execution-semantics.md` and `scripts/e2e-execution-smoke.sh` (part of `make test-e2e`).

### Changed

- A failed node no longer stops the run: only its dependents are skipped, and independent branches finish. The run still ends `failed`.
- A human node waiting for a decision no longer blocks independent nodes.
- `run reject` marks the node failed and lets `runIf: failure | always` nodes run, instead of ending the run immediately. Its exit code is `1` with kind `workflow_failed`.
- One process executes a run at a time (`executor.lock`). `run approve`, `run reject`, and `run cancel` queue a request for the executor; if another process is executing, they return the current state with `"queued": true`. `run resume` on a busy run exits `5`.
