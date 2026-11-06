# Attempt notes

Worked only inside `/tmp/agentflow-bench-baseline-scheduled`, using the supplied binary, skill, fixtures and standard local Python/shell tools. No model CLI or external service was called.

## Encountered errors and corrections

- `./agentflow schema --all --format json` worked. `./agentflow schema workflow --format json` and `./agentflow schema trigger --format json` exited 3: missing `workflow.schema.json` / `trigger.schema.json`. `./agentflow schema executor --format json` also exited 3. Used CLI metadata, validation responses, model-free probes and JSON field names exposed by the supplied binary instead.
- A workflow-level `inputSchema` was rejected as an unknown field. Replaced it with `inputs` declarations with `type` and `required`; individual nodes retain `inputSchema` and `outputSchema`. A `default` input property was also rejected and is absent from the deliverable.
- A template probe showed `input.topic` resolves to null; corrected workflow references to `inputs.topic`. Missing prior review output resolves to null on the first draft. A self-loop reference to its own prior result also cleared to null; the delivered draft instead reads the different review node's result, verified across both reworks.
- Trigger probes initially used `type`, a flat workflow, and `source.cron`. Validator errors required `source.kind`, `target.workflow`, and `source.expression`. Normalized `trigger list` confirmed `enabled` must be true and fixed topic/reviewer values must be at the trigger's top-level `inputs`. Unsupported `input`/`inputs` fields under target were silently omitted. Experimental bindings had unresolved paths or a wrong type; the final config uses no bindings.
- A loop probe confirmed `maxIterations: 2` counts two return trips; selecting it a third time fails with outcome `max_iterations`. This is documented explicitly. No extra rework or finalize executes after exhaustion.
- External-model verification passed on the first complete graph run. The script was then extended to verify cron input values, UTC conversion, delivery idempotency, and cancellation of its waiting test run; that run also passed.

## Exact validation commands

Final direct checks were:

```sh
./agentflow workflow validate workflow.json --format json
./agentflow trigger validate --format json
./agentflow trigger list --format json
./agentflow run start workflow.json --input topic=onboarding --input reviewer=local-reviewer --dry-run --format json
python3 checks/verify.py
```

`run start --dry-run` succeeds with exit 10. The verification script was executed twice while it was being developed. The final execution's complete subprocess commands, including actual run IDs, submitted JSON, expected error cases, cron fire/re-fire and cleanup, are recorded verbatim as command argument arrays in `checks/validation-commands.ndjson`; resulting case outcomes are in `checks/validation-results.json`. The script can be rerun and generates a unique cron delivery ID.

Initial interface probes used:

```sh
./agentflow schema --all --format json
./agentflow schema workflow --format json
./agentflow schema trigger --format json
./agentflow schema executor --format json
./agentflow --help
./agentflow config init --dry-run --format json
./agentflow config init --format json
./agentflow workflow validate checks/template-probe.json --format json
./agentflow run start checks/template-probe.json --input topic=test --input reviewer=local-reviewer --format json
./agentflow workflow validate checks/loop-probe.json --format json
```

Additional Python-driven field/loop/trigger probes created only local external nodes. Their transient files remain under `checks/` and their diagnostic runs under `.agentflow/`. Two early waiting probe runs were cleaned up with:

```sh
./agentflow run cancel run_20261023085025_8f6df0182b3520cb --format json
./agentflow run cancel run_20261023084340_0d9c67114faaccae --format json
```

The final workflow and trigger files remain configured for Codex and the supplied local channel. No scheduler process was left running.
