# Attempt notes

## Scope and implementation

Read only this directory's TASK.md, skill/SKILL.md, fixture programs, local tool schema/help, and artifacts created here. Used the supplied `./agentflow` and standard Python/shell tools. Did not inspect other attempts or parent source code and did not use external services or models.

Created a version 2 graph with three nodes and ordered conditional edges. Registered command executors with explicit workspace and QUALITY_SITE environment settings. Both finish nodes reuse the supplied finish.py, with branch and typed measurement fields declared in their inputs and schemas. The fixture programs were not modified; no branching logic was placed in executable business programs.

## Observed behavior and errors

- `schema --all`, `schema workflow`, `schema executors`, `workflow validate --check-executors`, `doctor`, and valid local starts succeeded.
- `run start --dry-run` returned exit code 10, as documented. It displayed downstream inputs as null because measure had not executed; actual runs resolved the values correctly.
- Missing sample returned exit code 2 and structured `missing_input` JSON on stderr. The verifier captures both stdout and stderr.
- Deliberately invalid measurement outputs returned exit code 1 and failed the measure node with precise schema errors: string score instead of number, number defects item instead of string, and string urgent instead of boolean. These are expected negative checks.
- The first verification script incorrectly assumed a failed visit would appear in `history`. The CLI returned `history: null` on output validation failure, so the script raised `TypeError: 'NoneType' object is not iterable`. The verification script was corrected to assert the `visits` count, measure failure/error, and pending downstream nodes. No workflow or executor change was needed. The final rerun passed.
- No unexpected CLI errors were observed.

## Validation and artifacts

`python3 verify.py` passed all 17 checks: workflow validation, executor discovery, dry-run, ten successful routing cases, required-input rejection, and three invalid-output rejections. `validation/summary.json` contains final statuses, exit codes, run IDs, and errors. The validation directory contains individual stdout/stderr captures; `.agentflow/runs/` contains durable engine state and artifacts, including the earlier development checks.

No resume, interruption, timeout, or retry tests were run. The deliverable workflow has no cycle or model executor. RUNBOOK.md documents execution, automatic progression, diagnostics, evidence and limitations.
