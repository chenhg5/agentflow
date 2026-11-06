# Unreleased

Changes since [v0.5.0](v0.5.0.md).

- Hub: finished runs older than `--retain` (default 90 days, `AGENTFLOW_HUB_RETAIN`, `0` keeps them forever) are removed at start and hourly, with the graphs no remaining run uses. Runs still going or waiting for input are always kept.

