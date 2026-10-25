# Workflow design

Describe the actual task and its acceptance criteria before replacing the starter.
This document is an authoring aid, not an engine schema or execution evidence.

## Nodes and data

For each node, record its responsibility, executor, input/output schema, data
sources and completion evidence. Pass task data through JSON edges; for large
artifacts pass an explicit path and identity/version rather than guessing a
shared filename. Separate task input from persistent role context and adapter
configuration.

## Agent roles

For each cognitive responsibility, identify the instruction source, selected
skills and tools, relevant repository/policy context, working directory,
permissions and session policy. Explain any deliberate empty skill/tool list.
For reused agents record their actual resource locations; an existing project
directory alone does not establish an existing specialist role. Program-only
workflows can state that no agent role is required.

## Transitions and human participation

Record the conditions, input mapping and destinations for every outcome,
parallel join and rework loop. Decide what happens when no condition matches or
the iteration limit is reached. For human steps specify what the person sees,
which outputs they may submit, how identity/call freshness is checked, which
edge each output takes, and how waits, duplicate submissions and recovery work.
Declare any timeout/escalation policy and implement it in the configured adapter
or workflow; documentation does not add runtime capabilities.

## Verification

List representative inputs and evidence for meaningful agent work, each required
route and human-feedback loop. Record skipped/unverified nodes and degraded
results explicitly. Static validation or a successful empty run does not establish
task completion. Describe retry/recovery and how external effects avoid duplication.
