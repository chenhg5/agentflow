# Attempt notes

Work was limited to `/tmp/agentflow-bench-revised-scheduled`, its local `./agentflow`, skill and fixtures, and standard Python/shell tools. No other attempts, project source, external services, real models or real message channels were used.

Read TASK.md and skill/SKILL.md, then discovered interfaces using schema --all, schema workflow, schema executors, schema trigger, schema run start, schema trigger serve, and config init --dry-run. The direct fixture registration was sufficient; no custom production wrapper was needed.

Deliverables: workflow.json, submission.json, .agentflow/executors.json, .agentflow/triggers.json, RUNBOOK.md. Test substitutes and replay scripts are under tests/; CLI evidence is under tests/evidence/. Original Codex registration was restored after every local substitution test. No scheduler process remains active.

Observed CLI behavior and errors:

- workflow validate --check-executors, trigger validate, doctor all passed (exit 0). Doctor only confirms local executable availability, not authenticated model execution.
- run start --dry-run succeeded with exit 10. The first draft feedback rendered as JSON null; as expected the dry-run preview has null downstream results because no upstream step executed.
- Missing topic and missing reviewer each returned exit 2 with `missing_input` JSON on stderr. These expected errors were explicitly checked; stdout alone is insufficient for errors.
- Invalid review signal `unknown` returned a failed run and exit 1. Node error: `output schema validation failed: /signal: at '/signal': value must be one of 'happy', 'again', 'sad'`. Finalize never ran.
- Third consecutive again returned exit 1, run status failed and outcome max_iterations. There were exactly three drafts and three reviews, with no finalize and no fourth draft.
- `run get` returned exit 0 even for a failed run, so validation asserted the JSON status/outcome instead of interpreting its exit alone.
- trigger fire returned the created run ID and a top-level succeeded result while its nested dispatch record retained queued status. Validation fetched the run to confirm success, checked all trigger input values and latest proposal, then fired the identical delivery ID again to confirm run reuse.
- No unexpected tool errors required a workaround.

The six deterministic graph scenarios passed, including approval, rejection, one/two reworks, exhaustion, and invalid output. Test stub assertions checked the latest review note on rework, channel request contracts, and final input freshness. Static production checks and missing-input tests passed. Trigger dispatch/idempotency with a short-lived serve process passed. Evidence summaries: tests/evidence/report.json, tests/evidence/preflight.report.json, tests/evidence/trigger.report.json.

True Codex execution and a real-time wait for cron 09:15 were intentionally not performed. The configured cron and timezone were validated; manual fire and local worker dispatch were executed using deterministic substitutes. Review default is a local simulated happy result; RUNBOOK.md explains configuring a response sequence and its counter.
