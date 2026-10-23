# Workflow graph and visualization

Workflow version `2` uses `entry`, `nodes`, and `edges`. The edge list is the sole source of control flow. A node's position in the JSON array does not determine execution order.

```json
{
  "version": "2",
  "name": "review-demo",
  "entry": "draft",
  "nodes": [
    { "id": "draft", "executor": "codex", "input": { "prompt": "Draft a proposal" } },
    { "id": "review", "executor": "external", "input": { "text": "Review the proposal" } }
  ],
  "edges": [
    { "from": "draft", "to": "review" },
    { "from": "review", "to": "draft", "when": "nodes.review.result.decision == 'again'", "maxIterations": 2 },
    { "from": "review", "to": "$end" }
  ]
}
```

After a node succeeds, the runner evaluates its outgoing edges in JSON order and follows the first whose `when` is true. An edge without `when` is the default and must be last. `$end` completes the run. Conditions use [Expr](https://expr-lang.org/docs/v1.15/language-definition): comparisons, arithmetic, logical operators, membership, string and regex operations, and collection functions. They read `inputs` and `nodes.<id>.result/status/input`. A condition must return a boolean. Validation checks syntax; runtime errors fail the active node.

Cycles must contain a bounded edge. `maxIterations` limits how many times that edge can be traversed in one run; every visit is recorded in `run.state.history`, and its input, result, and logs have visit-numbered artifacts.

The Web API exposes the same edges in `graph.edges`, including `condition` and `maxIterations`. The Web UI draws these edges, including the return path and `$end`. The graph view does not add routing rules of its own.

Nodes reference a named executor. Registration lives in `.agentflow/executors.json`; command programs may be written in any language. The graph only validates JSON input/output and follows its edges. Removed workflow fields are rejected.
