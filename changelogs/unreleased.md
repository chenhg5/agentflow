# Unreleased

- Stable planned/received event bindings and latest-only fire-once catch-up, durable cron plans/cursors and prepared creation receipts, explicit enable/edit boundaries, skipped-range and delivery diagnostics via trigger status.

- Independent Feishu adapter: named text/multiline forms, immutable fixed-output mapping, actual merged-output validation, persisted selection/deadline arbitration, explicit timeout outputs, optional read-only pre-submit hook, rejection audit and separate acceptance/completion feedback. Deterministic transport/clock tests cover restart and stale-card boundaries; real user callbacks remain separate channel acceptance.

Changes since [v0.4.1](v0.4.1.md).

- Add same-store resource capacities and atomic per-attempt requests across runs and graph parallelism, resource waiting/readmission, orphan quarantine and explicit verified cleanup, CLI diagnostics and contract tests.
