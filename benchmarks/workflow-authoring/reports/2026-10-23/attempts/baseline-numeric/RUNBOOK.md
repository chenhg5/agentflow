# Factory quality workflow

Run commands from this directory so `.agentflow/executors.json`, relative workspace `.` and fixture paths resolve correctly. Python 3 and the supplied `./agentflow` binary are required.

## Deliverables

- `workflow.json`: version 2 graph with required string run input `sample`.
- `.agentflow/executors.json`: custom `quality-measure` and `quality-finish` executors, each with explicit command, workspace and `QUALITY_SITE=plant-7`.
- `submission.json`: maps the `measure`, `accept` and `recheck` logical roles to graph node IDs.
- `validate.py` and `validation/`: reproducible local checks and saved successful run evidence.

The executor programs are the supplied `fixtures/measure.py` and `fixtures/finish.py`; no additional runtime program is necessary. No node has or requires a prompt.

## Validate and run

```sh
./agentflow workflow validate workflow.json --format json
./agentflow doctor --format json
./agentflow run start workflow.json --input sample=sample-001 --format json
```

`sample` is required. The node receives it through `{{ inputs.sample }}` and validates it as a string. The fixture defaults to score 81, no defects and urgent false, so the example selects `accept`.

To simulate another measurement with the local fixture:

```sh
BENCH_MEASUREMENT='{"score":49,"defects":["scratch"],"urgent":true}' ./agentflow run start workflow.json --input sample=sample-002 --format json
```

This example selects `recheck`. The fixture environment override is for local testing; routing remains entirely in graph edges.

## Advancement and inspection

`run start` automatically executes the measurement program and evaluates its outgoing edges in order:

1. `score > 80 && len(defects) == 0` selects `accept`.
2. Otherwise, `urgent == true || score >= 50` selects `recheck`.
3. Otherwise, the default edge goes to `$end` and the run succeeds without executing a finish node.

Both finish nodes go directly to `$end`. Their input contains the literal branch name and exact templates for `score`, `defects` and `urgent`; these preserve JSON types. `finish.py` validates the environment and types and returns those values with the selected branch. There is no external node and no manual submit step.

Use the run ID returned by `run start`:

```sh
./agentflow run get <run-id> --format json
./agentflow run logs <run-id> --format ndjson
```

After correcting a program/configuration failure, `./agentflow run retry <run-id> --format json` retries failed work. For interrupted execution, inspect the run first and use `./agentflow run resume <run-id> --format json`. Recovery commands are documented but were not exercised in this attempt.

## Verified

```sh
python3 validate.py
```

All 13 checks passed: required sample rejection; nine numeric, array and boolean routing cases; and rejection of three invalid measurement output types. Cases include score 80 versus 80.5, score 50 versus 49.9, nonempty defects, urgency below 50 and accept priority when urgency is also true. Successful runs check visit paths, skipped nodes and exact finish input/output types. Results are in `validation/results.json`, with full successful run JSON beside it.

The CLI validator and doctor passed. `finish.py` successfully checked `QUALITY_SITE=plant-7`. Real model CLIs and external services were never invoked. Process interruption, retry/resume recovery and deployment to another machine were not tested.

`schema workflow --format json` is unavailable in this supplied binary because it cannot find `workflow.schema.json`; CLI validation and actual program runs provided the contract checks instead. See `ATTEMPT-NOTES.md` for encountered errors and corrections.
