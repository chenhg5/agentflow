# Attempt notes

All work stayed in `/tmp/agentflow-bench-baseline-external`. Read TASK.md and the supplied skill first. No other attempts, project source, external services, real messages, or model CLIs were used.

Encountered and corrected:

- `./agentflow schema workflow --format json` exited 3: `workflow schema not found: workflow.schema.json`. `./agentflow schema executor --format json` also exited 3. Used the supplied skill, command metadata/help, validate and local runtime probes instead.
- A workflow-level `inputSchema` was rejected with exit 2 (`unknown field`). Moved required nonempty topic validation to the entry node's inputSchema, before any model step.
- Initial dry-run with `{{ input.topic }}` silently rendered null. Correct workflow namespace is `{{ inputs.topic }}`. Expression-style optional fallback inside templates also rendered null; used documented simple result paths and explicitly nullable first-visit fields instead.
- Referencing `nodes.draft.result.summary` during a draft revisit produced null because the active node resets. Previous text now comes from persisted `nodes.review.input.summary`; feedback comes from `nodes.review.result.feedback`.
- The exploratory loop with only maxIterations=2 failed with outcome `max_iterations` on a third back. Added deterministic revision counter, a guarded return edge and normal end fallback. Stop always routes to normal end.
- The first helper verification expected an unvisited finalize node to remain `pending`; the engine correctly marks it `skipped` at normal end. Corrected that assertion and the helper test passed.
- First full verification script failed while parsing an expected schema rejection: CLI errors were on stderr. Fixed capture parsing to use stderr when stdout is empty; reran successfully.

Validation commands actually run from the assigned directory:

```sh
./agentflow schema --all --format json
./agentflow schema workflow --format json
./agentflow schema executor --format json
./agentflow --help
./agentflow workflow validate --help
./agentflow run start --help
./agentflow workflow validate workflow.json --format json
./agentflow run start workflow.json --input topic=本地验证 --dry-run --format json
./agentflow run start validation/workflow.test.json --input topic=本地验证 --format json
./agentflow run submit run_20261023084521_403331f78f2b131f review --output '{"mood":"back","feedback":"more concrete","confidence":0.9}' --format json
./agentflow run get run_20261023084521_403331f78f2b131f --format json
./agentflow run start validation/workflow.test.json --input topic=final-local-validation --format json
python3 validation/verify.py
```

Two more exploratory submissions used the same first run with `{"mood":"back","feedback":"revision 2","confidence":0.9}` and `{"mood":"back","feedback":"revision 3","confidence":0.9}`. They confirmed two returns and the bare limit failure. These exploratory failures are not failures of the delivered workflow.

The successful final `python3 validation/verify.py` performed 54 exact CLI invocations, including each schema rejection, stored with exit codes in `validation/commands.json`. Its 10 case results are in `validation/results.json`. The separate JSON-file submit helper test's exact start and submit commands are in `validation/helper-check.json`; both exited 0. All executable verification used deterministic substitutes for model steps.
