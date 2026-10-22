## Unreleased

Changes that are not part of a tagged release yet.

### Added

- Every run records `workflow_digest`, a content hash of its workflow definition. Older runs derive it from their workflow snapshot.
- `run stats --by-version` returns one aggregate per workflow version; `run stats --digest <hash>` filters to one version.
- `run latest <workflow-name> [--status ...]` returns the newest matching run and exits `3` when there is none.
- `run list --workflow <name> --status <a,b>` filters runs; results are ordered newest first and include `workflow_digest` and `finished_at`.
- `run metrics` reports `active_ms` and `waiting_ms` beside `duration_ms`.
- `scripts/install.sh` installs the newest release (including previews) with checksum verification.

### Changed

- `run stats` run-level distributions (`duration_ms`, new `active_ms`, `waiting_ms`) only include finished runs, so runs still waiting for a human no longer inflate averages.
