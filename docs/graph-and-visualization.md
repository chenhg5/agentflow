# Workflow graph and visualization

AgentFlow keeps workflow JSON compact, then derives a richer graph view for
validation and visualization. This lets the runner remain deterministic while
the UI can explain how a node is connected.

## Edge channels

The Web API exposes a `graph.edges` array from `/api/state`:

```json
{
  "id": "inspect->review",
  "source": "inspect",
  "target": "review",
  "channels": ["condition", "control", "data"],
  "condition": "nodes.inspect.result.ready == true",
  "mapping": {
    "summary": "$node.inspect.output.summary"
  }
}
```

- `control` comes from `needs` and determines execution order.
- `data` comes from structured `inputMapping` (and is shown as a handoff in the UI).
- `condition` is present when the target node has a `when` expression.

The normalized graph is a view of the workflow. It does not create a second
execution engine or allow an agent process to advance a run.

## Validation rules

`agentflow workflow validate` now also checks graph-specific invariants:

- duplicate and self dependencies;
- dependency cycles;
- `$node.<id>.output.<field>` references to missing nodes;
- data references whose source node is absent from `needs`.

The last rule is deliberate: the runner only waits for declared dependencies.
An undeclared data dependency would make execution order accidental and would
make a run difficult to reproduce.

## Web inspection

Run:

```bash
agentflow web workflow.json
agentflow web --run <run-id>
```

The graph view uses different colors for control, data, and conditional edges.
Clicking a node opens its input/output contract, input mapping, resolved input,
structured result, artifacts, and runtime status. A run view refreshes every two
seconds and keeps the existing metrics and log links available through the CLI.

