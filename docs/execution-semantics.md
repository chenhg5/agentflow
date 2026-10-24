# Execution semantics

Version `2` executes one active node at a time, starting at `entry`. After success, outgoing edges are evaluated in definition order and the first matching target is activated. `$end` completes the run. A missing matching edge or invalid expression fails the active node. Cycles require a bounded edge; its `maxIterations` limits traversals per run.

## Executor contract

Each node references an executor registered in `.agentflow/executors.json` or a built-in executor. The registry defines the program entry point, working directory, and environment. Custom names have no graph meaning.

The entry node receives the run input; each selected edge passes the source node output to its target. An empty run input is an empty JSON object, including after save/reload. Programs read a JSON object from `AGENTFLOW_INPUT_FILE`, write a JSON object to `AGENTFLOW_RESULT_FILE`, and exit with zero status. The runner validates `inputSchema` and `outputSchema` using JSON Schema draft 2020-12. stdout/stderr are logs. The runner alone advances the graph.

The built-in Codex/Claude Code adapters interpret `config.instruction` and optional `config.workspace`. The generic process adapter interprets `config.command`. The engine resolves `execution.session` independently of node input. These conventions live in adapters; the graph does not require prompt, decision, or any other business field.

An interaction program can send a message and wait for its callback before writing output. Its process remains running during the interaction and occupies the run's single active node. The demo binds its review node directly to such an executor. No workflow-name scanner is needed.

The built-in `external` executor instead persists `waiting_input` and releases the run lock. An independent application can supply output with:

```bash
agentflow run submit <run-id> <node-id> --output '{"score":8}'
```

Accepted output succeeds the node, then edge evaluation continues automatically. The fields and values are defined by the workflow's schema, without built-in business outcomes.

## Persistence and recovery

Every completed visit is appended to `history`. The latest state remains under `nodes.<id>` for expressions and display. Input/result/log artifacts have visit numbers. Executor registrations are environment configuration, resolved when executing; a later retry or resume uses current registrations.

`run retry` resets the active failed node. `run resume` resets a node left running by a crashed process; an interaction program may therefore be started again. Programs with external side effects should support idempotency. Controls are queued when the run is locked. Cancellation is processed at executor boundaries; stopping a currently running child immediately is not implemented.
