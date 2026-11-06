# Unreleased

Changes since [v0.5.0](v0.5.0.md).

- Code orchestration: `agentflow code run <name> -- <script>` records a script as a run whose steps it drives; inside it, `agentflow code step <id> [-- command]` records a step (run like an executor: result file, logs, exit code; or work the script did, with `--output`/`--summary`), and `code end` / the script's exit code ends it. Code runs live in `runs/` like workflow runs (workflow version `code`), show in `run list/get`, report to the Hub as `orchestration: code`, and draw their graph in the order the script reached steps, parallel steps included.


- Hub: finished runs older than `--retain` (default 90 days, `AGENTFLOW_HUB_RETAIN`, `0` keeps them forever) are removed at start and hourly, with the graphs no remaining run uses. Runs still going or waiting for input are always kept.


- Hub: submit input to a step waiting for it from the run page. The form is built from the node's `outputSchema`. Runners that opt in (`"accept_commands": true` in `~/.agentflow/hub.json` or `AGENTFLOW_HUB_ACCEPT_COMMANDS=1`, never from a workspace file) pull the submission while reporting, check it against their own state (same wait, schema) and hand it to the run's executor like `run submit`, then report applied or rejected. Forms carry a CSRF token and cross-site posts are refused. See `docs/hub.md`.
- Hub: each step's latest attempt reports the end of its stdout/stderr, a preview of its result JSON, and its files by name and size, masked and within a 32 KB budget per report (failed and running steps first). The run page shows them under each step; failed steps open by default.

