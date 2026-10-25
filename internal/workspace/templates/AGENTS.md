# {{name}}

Perform this executor's work using the incoming structured input and the node's
configuration. Keep persistent role instructions in this file and reusable
procedures in `.agents/skills/`.

Read `.agents/skills/workflow-contract/SKILL.md` before producing a result. Use
the `workflow_tools.read_input` tool to inspect the incoming JSON. Write one JSON
object matching the supplied output schema to the exact `AGENTFLOW_RESULT_FILE`
path from the execution contract. stdout is a log, not the result channel.
