# Observability and evaluation

AgentFlow records two kinds of evidence for every run:

1. **Runtime facts** are produced by the engine: wall-clock duration, active time, time waiting for input, selected executor, ordered candidate attempts, structured failures, per-attempt artifacts, bytes, node status, and append-only events.
2. **Evaluations** are measurements supplied after a run by a person, a program, or another agent. Their names and units are user-defined. AgentFlow does not assume that a field means quality, approval, cost, or any other business concept.

## Record an evaluation

Use `run evaluate` after the run finishes (or whenever an external evaluator has a result):

```bash
agentflow run evaluate RUN_ID --evaluation '{
  "source": "human-review",
  "measurements": {
    "quality": {"value": 0.90, "unit": "ratio", "direction": "higher"},
    "review_time": {"value": 18, "unit": "minutes", "direction": "lower"},
    "policy_fit": {"value": 1, "unit": "boolean", "direction": "neutral"}
  },
  "labels": {"cohort": "new-flow"},
  "notes": "Sampled by the operations team"
}' --format json
```

`direction` controls how a comparison is interpreted: `higher`, `lower`, or `neutral`. It is metadata, not a business rule. A neutral measurement is still stored and aggregated, but does not receive an improved/regressed flag.

The evaluation is saved as `evaluation.json` beside the run state and is also included by `run get`, `run metrics`, the web API, and later comparisons. Repeating the command replaces the evaluation for that run and records a new `run.evaluated` event.

## Compare runs

```bash
agentflow run compare CURRENT_RUN_ID BASELINE_RUN_ID --format json
```

If the baseline id is omitted, AgentFlow selects the latest earlier finished run of the same workflow:

```bash
agentflow run compare CURRENT_RUN_ID --format json
```

The result contains:

- `runtime`: duration, active time, input waiting time, and attempts. These use lower-is-better semantics because they describe execution cost or elapsed time.
- `measurements`: matching user-defined evaluation names from both runs. Each entry reports baseline, current, delta, percentage change, unit, direction, and (when direction is not neutral) `improved`.
- `same_workflow` and `same_version`: guards against comparing unrelated definitions.

`delta = current - baseline`; `change_percent = delta / abs(baseline) × 100`. When the baseline is zero, percentage change is omitted. A measurement is compared only when both runs use the same name, unit, and direction.

Runtime facts and external evaluations are intentionally separate. A run can be faster while its evaluated quality falls, or slower while quality improves. The comparison output makes that trade-off visible instead of collapsing it into one score.

For a fallback chain that eventually selects the built-in `external` executor, active time includes all earlier candidate work and waiting time begins at the external attempt's persisted `waiting_started_at`. Submitting output freezes that wait duration; bytes written by the submission are added to any result bytes already produced by failed candidates.

Run-level waiting time is the union of persisted waiting intervals, bounded by the run interval. Parallel branches that wait at the same time are therefore counted once rather than added together, and a finished or cancelled run keeps the same duration, active time, and waiting time on every later read. Cancellation freezes every started, unfinished node and attempt at the durable run finish time, including a continuation-pending node that recovery had not restarted yet, and node durations are capped at the run finish for a terminal run so snapshots do not keep growing with the read time. A terminal snapshot also caps any attempt evidence left unfinished by a parallel sibling branch, so per-attempt timings are stable even when another branch ended the run.

## Aggregate a cohort

```bash
agentflow run stats WORKFLOW_NAME --format json
agentflow run stats WORKFLOW_NAME --by-version --format json
```

Finished-run distributions include duration, active time, waiting time, and node metrics. Node metrics contain one row per unique visit, including visits archived by a manual retry or interrupted-attempt recovery; the latest visit's duplicate history record is counted only once. Attempts, bytes, duration, and waiting time therefore remain cumulative across a run's visits. When evaluations exist, `measurements` contains count, total, average, min, max, p50, and p95 for each measurement name, unit, and direction. Unevaluated and unfinished runs do not enter these measurement distributions. Use `--by-version` when a workflow definition changed and the versions should be reported separately.

## Reporting efficiency and quality

For a leadership report, define the cohort and baseline first, then report the observed deltas:

- efficiency: median or p95 active time, waiting time, attempts, and total duration;
- outcome: success/failure/cancellation rates and completed-run count;
- quality: one or more evaluator-owned measurements with an explicit unit and direction;
- sample size and evaluation coverage: how many finished runs have a measurement;
- guardrails: error rate, retries, and any quality metric that must not regress.

AgentFlow supplies the evidence and arithmetic. It does not invent a universal “productivity percentage”; that percentage depends on the chosen baseline, cohort, and evaluation definitions.
