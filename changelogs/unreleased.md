# Unreleased

Changes since [v0.5.0](v0.5.0.md).

- Hub: submit input to a step waiting for it from the run page. The form is built from the node's `outputSchema`. Runners that opt in (`"accept_commands": true` in `~/.agentflow/hub.json` or `AGENTFLOW_HUB_ACCEPT_COMMANDS=1`, never from a workspace file) pull the submission while reporting, check it against their own state (same wait, schema) and hand it to the run's executor like `run submit`, then report applied or rejected. Forms carry a CSRF token and cross-site posts are refused. See `docs/hub.md`.

