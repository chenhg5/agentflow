---
name: workflow-contract
description: Read this executor's incoming workflow JSON and produce a result matching its output contract.
---

Use `workflow_tools.read_input` to read the structured input. Perform the task
described by your role instructions and this execution's configuration. Preserve
JSON types and write the result to the supplied absolute result-file path.
`AGENTFLOW_ROOT` is the orchestration directory; `AGENTFLOW_WORKSPACE` is your
working directory. Do not edit the engine's run state.
