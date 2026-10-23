# Attempt notes

Worked only in `/tmp/agentflow-bench-baseline-numeric` (physical path `/private/tmp/agentflow-bench-baseline-numeric`). Used the supplied binary, skill, fixtures and standard local Python/shell. No network, model execution or real messaging.

## Errors and corrections

1. `./agentflow schema workflow --format json` exited 3: missing `workflow.schema.json`. Used the skill, CLI help, workflow validator and actual local executions to discover/check the contract.
2. `./agentflow schema executor --format json` exited 3: command schema `executor` not found. The skill's executor registration example and `doctor` supplied the needed registration checks.
3. Initial workflow validation exited 2: unknown top-level `inputSchema`. Changed it to `inputs: {"sample":{"type":"string","required":true}}`; retained node input/output JSON Schemas. Validation then passed.
4. First sample run failed before program execution because `{{ input.sample }}` resolved to null. Changed the run-input reference to `{{ inputs.sample }}`; subsequent sample run succeeded.
5. First `python3 validate.py` run failed parsing the expected missing-input error because this CLI writes usage error JSON to stderr. Changed the helper to parse `stdout or stderr`; rerun passed all 13 checks.
6. The commands without `sample` intentionally exited 2 with `missing_input`. The three invalid fixture-output tests intentionally exited 1 with output schema validation errors. These are successful negative checks.

## Exact discovery/validation commands run

All commands below ran with this directory as the absolute working directory. Repeated invocations reflect the corrections above.

```sh
./agentflow schema --all --format json
./agentflow schema workflow --format json
./agentflow --help
./agentflow schema executor --format json
./agentflow workflow --help
./agentflow config init --dry-run --format json
./agentflow workflow validate workflow.json --format json
./agentflow workflow validate workflow.json --format json
./agentflow doctor --format json
./agentflow run start workflow.json --format json
./agentflow run start workflow.json --input sample=sample-001 --format json
./agentflow workflow validate workflow.json --format json
./agentflow run start workflow.json --input sample=sample-001 --format json
python3 validate.py
python3 validate.py
```

Each `python3 validate.py` executes `./agentflow workflow validate workflow.json --format json` and `./agentflow run start workflow.json --format json`. The successful rerun then executes the command below once per case, setting `BENCH_MEASUREMENT` to the corresponding measurement object recorded in `validate.py`:

```sh
./agentflow run start workflow.json --input sample=above-80-clean --format json
./agentflow run start workflow.json --input sample=fraction-above-80-clean --format json
./agentflow run start workflow.json --input sample=at-80-clean --format json
./agentflow run start workflow.json --input sample=above-80-defect --format json
./agentflow run start workflow.json --input sample=at-50 --format json
./agentflow run start workflow.json --input sample=below-50 --format json
./agentflow run start workflow.json --input sample=below-50-defect --format json
./agentflow run start workflow.json --input sample=below-50-urgent --format json
./agentflow run start workflow.json --input sample=accept-priority-over-urgent --format json
./agentflow run start workflow.json --input sample=score-string --format json
./agentflow run start workflow.json --input sample=defect-item-number --format json
./agentflow run start workflow.json --input sample=urgent-string --format json
```

`validation/results.json` records all 13 passing assertions. Full JSON evidence for the nine successful routing runs is saved under `validation/`. All routing conditions are graph edge expressions. Runtime code remains the supplied fixtures.

Final deliverable check also passed using this exact command:

```sh
python3 - <<'PY'
import json
from pathlib import Path
root=Path('.')
submission=json.loads((root/'submission.json').read_text())
workflow=json.loads((root/submission['workflow']).read_text())
assert set(submission['roles']) == {'measure','accept','recheck'}
assert set(submission['roles'].values()) == {n['id'] for n in workflow['nodes']}
for name in ['RUNBOOK.md','ATTEMPT-NOTES.md','.agentflow/executors.json','validation/results.json']:
    assert (root/name).is_file(), name
print('Submission and deliverables present; role mapping matches workflow.')
PY
```
