# Cold-context workflow authoring benchmark

This benchmark evaluates **an agent + the shipped CLI + the shipped skill**. A participant receives a new directory containing the binary, skill, task, and local fixture programs. It does not receive project source, conversation history, reference answers, or prior attempts. It authors real files, which an independent deterministic evaluator executes.

## Cases and acceptance

| Case | Split | What is checked |
| --- | --- | --- |
| scheduled-review | development | Weekday cron/timezone/inputs, Codex entry, explicit interaction program registration, arbitrary signal names, all review routes, bounded rework and latest feedback |
| numeric-routing | development | `>` versus `>=`, AND/OR, array length, branch priority, number/array/boolean handoff, registered program environment |
| external-loop | development | Durable pause/reload/submit, arbitrary field names, confidence bounds, return paths, invalid output rejection, bounded loops |
| mixed-language | holdout | Authored Python and Node executors, program entry points, environment, nested JSON handoff and computed result |

`cases.json` contains business tasks and scenario contracts. `goldens/` contains private reference solutions. `grader.py` tests runtime behavior and required interfaces, rather than requiring an identical JSON file, node naming, array ordering, or one exact implementation. Reference configs are named `executors.json` / `triggers.json`; materialize them under `.agentflow/` when running a reference.

An exhausted rework loop may either end normally or fail with the documented `max_iterations`, because the task only limits the number of returns. Extra technical nodes are allowed unless the task explicitly requires its first action to be Codex. Quality findings, such as undeclared required run inputs, are recorded separately from functional task success. Mock model steps produce deterministic contract outputs: this measures orchestration, not model writing quality.

The grader checks supplied executor bindings and runs local programs. It replaces registered model adapters with deterministic programs and does not call models while grading. Participant code is executed; these task workspaces are resource separation, not an operating-system security boundary. The evaluator provides a limited process environment and fresh runtime state for every scenario.

## Deterministic regression checks

Prerequisites: Go, Python 3.10+, Node.js, and a Unix-style shell.

```bash
make test-benchmark
```

No model call is made. The self-test runs all four positive goldens, an equivalent solution with reordered/renamed nodes, and seven deliberately broken variants. It must reject wrong numeric boundaries, missing executor bindings, lost JSON types, missing feedback, incorrect stop routing, unbounded cycles and missing program environment. This gate also runs in CI.

The references themselves caught an empty-object persistence bug during initial testing. A benchmark evaluator must pass its own positive and mutation tests before its scores are trusted.

## Run a fresh authoring attempt

Build the binary and prepare a new destination that does not exist:

```bash
make build
python3 benchmarks/workflow-authoring/bench.py prepare scheduled-review /tmp/my-authoring-attempt
```

Give a fresh agent only `/tmp/my-authoring-attempt/TASK.md`, with that directory as its working directory. No prior conversation or reference solution should be supplied. The task specifies a `submission.json` manifest mapping logical roles to the participant's chosen node IDs; this is an evaluation artifact, not a production workflow field.

For an external CLI, the runner accepts an argument array and supplies the task through stdin. For example, with a Codex version supporting these flags:

```bash
python3 benchmarks/workflow-authoring/bench.py run-agent /tmp/my-authoring-attempt \
  --agent-label codex-default-fresh \
  --agent-argv '["codex","exec","--ephemeral","--ignore-user-config","--ignore-rules","--skip-git-repo-check","-s","workspace-write","--json","-C","{workspace}","-"]' \
  --timeout 420
```

Other CLIs can be substituted with their own fresh-session arguments. `{workspace}` and `{task}` are replaced inside individual argv elements; no shell string is evaluated. The runner records argv, exit status, elapsed time and raw stdout/stderr. Session-ID environment hints are removed. Model authentication is the caller's existing setup. Live authoring calls use model resources and are opt-in; they are not part of CI.

Evaluate the files after the agent finishes:

```bash
python3 benchmarks/workflow-authoring/bench.py grade /tmp/my-authoring-attempt \
  --output /tmp/my-authoring-report.json
```

To replay the unchanged artifact against a newer binary, add `--binary /absolute/path/to/agentflow`. Authoring and evaluation binary hashes are both preserved. A process timeout/authentication failure should be reported as infrastructure failure, separately from a completed agent's incorrect workflow.

## Reproducibility and scoring

Each attempt records task split, binary SHA-256, skill SHA-256 and context mode. The CLI wrapper records actual commands, exit codes and outputs. Failed negative tests are expected; do not count every nonzero exit as authoring failure. Baseline notes without wrapper traces are labeled as participant-reported observations.

A task passes only if every applicable functional check passes. Do not turn raw assertion counts into a cross-task score: different tasks have different check counts, and a malformed submission may fail before all checks run. Report task success, quality findings, unexpected discovery/validation failures and retained artifacts separately. Never claim first-draft correctness from final success after self-repair, and never claim pass@k without k independent attempts.

Freeze skill and tool snapshots before holdout authoring. Changing the evaluator to accept an alternative valid design is a rubric correction, not an agent improvement. Preserve old reports and distinguish authoring snapshots from later tool-only replays. This initial small suite is a starting point; a result for a few sessions does not establish reliability for every model or capability level.

See the [initial observed report](reports/2026-10-23/REPORT.md).
