# Execution semantics

Version `3` starts at `entry`. By default it executes one active node at a time; after success, outgoing edges are evaluated in definition order and the first matching target is activated. `$end` completes that path, and the run finishes when no other branch remains. A missing matching edge or invalid expression fails the active node. Cycles require a bounded edge by default; its `maxIterations` limits traversals per run. `allowUnboundedCycles: true` explicitly opts into unlimited loops.
When a node sets `dispatch: "all"`, all matching outgoing edges activate their targets and those nodes run concurrently. A `join.from` list waits for all named predecessor payloads before starting the join node. A run can therefore have multiple active nodes; the persisted active set and partial join inputs let a later `run submit` or `run resume` continue it. See [parallel nodes and joins](graph-and-visualization.md#parallel-nodes-and-joins).

## Executor contract

Each node references an executor registered in `executors.json` or a built-in executor. The registry defines the program entry point, working directory, and environment. Custom names have no graph meaning.

The invocation directory is the standalone workflow workspace root. Definitions, `executors.json`, optional `triggers.json`, and generated `runs/` live directly in that workspace. Executor directories may be elsewhere, including symlinks to existing agents or paths to external repositories. `AGENTFLOW_ROOT` identifies the workflow root; `AGENTFLOW_WORKSPACE` identifies the executor's working directory. Session policy is independent of directory selection. Resuming persisted session history does not attach to an already running interactive process.

The entry node receives the run input; each selected edge passes the source node output to its target. An edge may declare `map` to construct the target input from `{{ input.field }}`, `{{ output.field }}`, run inputs, and prior node results. An explicit empty `map` sends `{}`. A missing reference in an edge map yields an empty string. An empty run input is an empty JSON object, including after save/reload. Programs read a JSON object from `AGENTFLOW_INPUT_FILE`, write a JSON object to `AGENTFLOW_RESULT_FILE`, and exit with zero status. The runner validates `inputSchema` and `outputSchema` using JSON Schema draft 2020-12. stdout/stderr are logs. The runner alone advances the graph.

The built-in Codex/Claude Code adapters interpret `config.instruction` and optional `config.workspace`. The generic process adapter interprets `config.command`. The engine resolves `execution.session` independently of node input. These conventions live in adapters; the graph does not require prompt, decision, or any other business field.

An interaction program can send a message and wait for its callback before writing output. Its node remains active during the interaction; other branches may progress independently in a parallel graph. The demo binds its review node directly to such an executor. No workflow-name scanner is needed.

The built-in `external` executor instead persists `waiting_input` and releases the run lock. An independent application can supply output with:

```bash
agentflow run submit <run-id> <node-id> --output '{"score":8}'
```

Accepted output succeeds the node, then edge evaluation continues automatically. The fields and values are defined by the workflow's schema, without built-in business outcomes.

## Persistence and recovery

Every completed visit is appended to `history`. The latest state remains under `nodes.<id>` for expressions and display. Input/result/log artifacts have visit numbers. Executor registrations are environment configuration, resolved when executing; a later retry or resume uses current registrations.

`run retry` resets failed active nodes, preserving successful siblings in parallel runs. `run resume` resets a node left running by a crashed process; an interaction program may therefore be started again. Programs with external side effects should support idempotency. Controls are queued when the run is locked. Cancellation is processed at executor boundaries; stopping a currently running child immediately is not implemented.
