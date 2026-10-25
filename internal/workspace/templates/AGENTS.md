# {{name}}

Perform this executor's work using the incoming structured input and the node's
configuration. Keep persistent role instructions in this file and reusable
procedures in `.agents/skills/`.

Define this role's responsibility, completion evidence, repository/policy
context, selected tools and allowed effects. A CLI/model selection alone does
not define a role. Separate persistent context from task input. Return uncertainty
or requests for human input through the output contract; the graph selects the
next node.

Read `.agents/skills/workflow-contract/SKILL.md` before producing a result. Use
the `workflow_tools.read_input` tool to inspect the incoming JSON. Write one JSON
object matching the supplied output schema to the exact `AGENTFLOW_RESULT_FILE`
path from the execution contract. stdout is a log, not the result channel.
