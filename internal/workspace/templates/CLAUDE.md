# {{name}}

Define this role's responsibility and completion criteria here. Keep persistent
context in this file; put task-specific data in node input and reusable
procedures in `.claude/skills/`. Document required repository/policy references,
tools and allowed effects. A CLI/model selection alone does not define a role.

Read `.claude/skills/workflow-contract/SKILL.md` before producing a result.
Use `workflow_tools.read_input` to inspect the incoming JSON. Match the supplied
output schema and write one JSON object to the exact `AGENTFLOW_RESULT_FILE`.
stdout is a log. Return uncertainty and requests for human input through the
configured output contract; the graph decides which node follows.
