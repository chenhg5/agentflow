# Workspace resource admission

`resources.json` is the workspace authority for generic named capacities:

```json
{"resources":{"writer":{"capacity":1},"executor-pool":{"capacity":2}}}
```

A process node explicitly requests units. The graph stores identities and units,
without knowing whether a key represents a repository, session or machine budget:

```json
{"id":"work","resources":[{"key":"writer","units":1},{"key":"executor-pool","units":1}],"execution":{"candidates":[{"executor":"author"}]}}
```

Discover the definition using `agentflow schema resources`; validate a complete
workspace with `workspace validate`. Unknown keys, duplicate requests, nonpositive
or excessive units are rejected before execution. Capacity is not redeclared in
graphs. Stop admission and drain holders before editing capacities; existing
claims are never revoked by a configuration change. Configured paths and session IDs do not automatically acquire locks.

## Attempts, runs and waiting

All requested capacities are reserved in one short transaction before an attempt
can start. A shortage produces `waiting_resource`, without a process launch,
attempt increment, retry or fallback. The candidate cursor is retained. A run
waiting only for resources releases its active run slot; the common worker polls
readiness and readmits it after capacity becomes available. Mixed executing and
waiting nodes retain their run slot. Submit/retry/resume use the same admission.

`worker --concurrency N` limits active runs; resources limit explicitly claimed
attempt units, including graph-level parallel branches. To constrain every
executor, every applicable process node must request the shared pool. No request
means no quota. An external candidate registers human input without claiming
active resources, even if its node declares resources for process candidates.

Every retry and fallback reserves a new claim and invocation token. Release occurs
after the process returns, including failure or timeout. A program must finish its
owned writing subprocesses before returning; detaching them escapes its execution
contract. This implementation does not supervise arbitrary detached descendants.

## Claims and crash recovery

`resource-state/claims/<id>.json` records owner PID, launched PID, run, node,
attempt, token, requested units, status and timestamps. A per-claim OS lock fences
owner lifetime. Acquisition/release/start/orphan/resolve events are appended to
the owning run; no PID timeout or heartbeat expiration steals quota.

A dead owner is marked `orphaned` and its units remain occupied, since its child
may still be writing. Recovered attempts wait behind that claim. Inspect:

```sh
agentflow resource list --format json
agentflow run queue --format json
```

After verifying that the complete old execution scope, including descendants, has
stopped, explicitly release the orphan:

```sh
agentflow resource resolve <claim-id> --confirm-stopped --reason 'PID and descendants stopped; writing ceased'
```

This command rejects a live recorded PID and requires a cleanup reason. PID death
alone does not prove descendants stopped; the confirmation acknowledges that
external check. Active claims cannot be resolved. Unknown launch state is also
quarantined; inspect and stop the entire associated execution scope before cleanup.
Only an orphan may be explicitly resolved. No command automatically kills another
process, bypasses capacity or force-clears an active owner. The claim ID is also
available to programs as `AGENTFLOW_RESOURCE_CLAIM`.

Resources apply only to one host and one run store. Cooperating executors must use
this store; external programs that ignore admission are outside its guarantees.
This is not a distributed lock or an exactly-once external-effects protocol.

## Verification

`make test-e2e` runs `scripts/test-resource-admission.py` with actual CLI, worker
and deterministic Python processes. The seven AF-05 scenarios cover capacity one,
capacity two over ten runs including graph parallelism, opposing A/B request
orders, simultaneous submit/retry/resume, cancellation and fallback, worker kill
with a still-writing child, explicit verified orphan cleanup, and external input
releasing capacity. Unit tests cover no attempt/fallback consumption while
waiting, atomic allocation, invalid configuration and durable quarantine.
No provider account or real notification is used by these tests.
