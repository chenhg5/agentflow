# Unreleased

Changes since [v0.5.0](v0.5.0.md).

- Code orchestration: `agentflow code run <name> -- <script>` records a script as a run whose steps it drives; inside it, `agentflow code step <id> [-- command]` records a step (run like an executor: result file, logs, exit code; or work the script did, with `--output`/`--summary`), and `code end` / the script's exit code ends it. Code runs live in `runs/` like workflow runs (workflow version `code`), show in `run list/get`, report to the Hub as `orchestration: code`, and draw their graph in the order the script reached steps, parallel steps included.

