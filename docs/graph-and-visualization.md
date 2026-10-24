# Workflow graph and visualization

Workflow version `3` uses `entry`, `nodes`, and `edges`. The edge list is the sole source of control flow. A node's position in the JSON array does not determine execution order.

```json
{
  "version": "3",
  "name": "review-demo",
  "entry": "draft",
  "nodes": [
    { "id": "draft", "executor": "codex", "config": { "instruction": "Draft a proposal" } },
    { "id": "review", "executor": "external" }
  ],
  "edges": [
    { "from": "draft", "to": "review" },
    { "from": "review", "to": "draft", "when": "nodes.review.result.decision == 'again'", "maxIterations": 2 },
    { "from": "review", "to": "$end" }
  ]
}
```

After a node succeeds, the runner evaluates its outgoing edges in JSON order and, by default, follows the first whose `when` is true. In this default mode, an edge without `when` is the fallback and must be last. `$end` completes the path; the run finishes when every active branch has ended. Conditions use [Expr](https://expr-lang.org/docs/v1.15/language-definition): comparisons, arithmetic, logical operators, membership, string and regex operations, and collection functions. They read source `input` and `output`, run `inputs`, and `nodes.<id>.result/status/input`. A condition must return a boolean. Validation checks syntax; runtime errors fail the active node.

Cycles must contain a bounded edge by default. `maxIterations` limits how many times that edge can be traversed in one run. Set top-level `allowUnboundedCycles: true` only when an unlimited loop is intended; a workflow with that setting can continue until a condition exits the loop or the run is cancelled. Every visit is recorded in `run.state.history`, and its input, result, and logs have visit-numbered artifacts.

The Web API exposes the same edges in `graph.edges`, including `condition`, `map`, and `maxIterations`. The Web UI draws these edges, including the return path and `$end`. The graph view does not add routing rules of its own.

Nodes reference a named executor. Registration lives in `.agentflow/executors.json`; command programs may be written in any language. The graph only validates JSON input/output and follows its edges. Removed workflow fields are rejected.

The entry node receives the run input. Every selected edge carries its source node JSON result into the target node as input by default. An optional edge `map` can project fields from the source input, output, run inputs, and prior node results; `{}` sends an empty object. Nodes do not declare an input template. Executor-specific settings live in opaque `config`; `execution.session` selects new, fixed, inherited, or forked agent sessions.

## Parallel nodes and joins

The default `dispatch` is `first`: evaluate outgoing edges in order and follow the first match. With `"dispatch":"all"`, every outgoing edge whose `when` is true starts its target. Edges without `when` always match in this mode. The selected target nodes run concurrently in one AgentFlow run, and each receives its own edge payload. Use different targets for the outgoing edges.

A join node declares immediate predecessors with `"join":{"from":["left","right"]}`. It waits for one edge payload from each listed predecessor, then starts with a JSON object keyed by predecessor ID. An incoming edge's `map` controls that predecessor's payload. All incoming edges must come from listed predecessors. A human/external branch may wait while another branch finishes; completed payloads survive process restarts. If the run has no active node and a join still lacks inputs, it fails with `join_incomplete` rather than appearing to finish.

`$end` finishes one active branch; the run succeeds after every active branch has finished and no join is waiting. A failed branch fails the run after already-started sibling commands finish. `run retry` can restart the failed branch without rerunning successful siblings. The run state and CLI expose `activeNodes`/`active_nodes`; `activeNode`/`active_node` is populated only when exactly one node is active. A join using `execution.session.mode` `inherit` or `fork` must set `execution.session.from` to choose a source session.

```sh
agentflow workflow validate examples/parallel.json
agentflow run start examples/parallel.json --format json
```
