# Workflow workspace layout v1

The workspace is a standalone orchestration directory. It may coordinate several
business repositories and existing agents. Invoke AgentFlow from its root.

```text
my-workflow/
├── workflow.json                    # single graph
├── executors.json                   # named executor registrations
├── DESIGN.md                        # contracts, roles/context/tools, routing, human feedback
├── triggers.json                    # optional schedules/webhooks
├── executors/
│   ├── agents/
│   │   ├── author/
│   │   │   ├── AGENTS.md
│   │   │   ├── .agents/skills/<skill>/SKILL.md
│   │   │   ├── .codex/config.toml
│   │   │   └── tools/               # agent-local tools
│   │   └── existing -> /existing/agent
│   └── programs/archive/main.py      # arbitrary language
├── lib/                             # optional shared code
├── output/                          # optional business artifacts
├── runs/<run-id>/                    # generated engine evidence
├── dispatch/                        # generated shared durable run queue
└── trigger-deliveries/               # generated delivery records
```

For a workspace with multiple graphs, group **all** graph definitions under
`workflows/` and name each by its purpose:

```text
my-workflows/
├── workflows/
│   ├── release.json
│   ├── qa.json
│   └── watchdog.json
├── executors.json
├── executors/
├── triggers.json
└── runs/
```

These graphs share the workspace registry, trigger configuration and run store.
Directory nesting does not define parent/child graphs or connect their execution.
Use explicit graph targets such as `workflows/release.json` in triggers and CLI
commands, invoked from the workspace root.

When expanding a single graph workspace, move `workflow.json` into `workflows/`
and update triggers, operator programs, tests and documentation. Keep one source
per graph; remove the root file instead of leaving a copy or compatibility link.
The trigger service reads configuration at startup; reload it after changing
targets when no work is executing. Stored run snapshots keep their own graph
definitions and are not source files to move.

There is no `.agentflow/` container. Single versus multiple graph placement is a
recommended authoring convention; the engine accepts explicit file paths and
does not require these directory names. Directory categories are authoring conventions; the graph
engine resolves executor names through the registry and does not interpret them
as business types. Built-in adapters ship with AgentFlow and need no local copy.
The v1 layout version describes these conventions; graph format remains v3.

## Create or bind

```sh
agentflow workspace init my-workflow
cd my-workflow
agentflow executor init author --kind codex
agentflow executor init archive --kind command
agentflow executor add existing --kind codex --workspace /existing/agent
agentflow schema workspace --format json
```

Initialization creates a runnable pass-through graph, root registry, README and
ignore rules. Adapt or remove the starter `step` executor when building the real
graph. Executor initialization creates owned files and registers them. Existing
files, directories and symlinks cause a conflict; they are never overwritten.
`--dry-run` reports planned files without creating them (exit 10).

`executor add` only edits the root registry. An external path or symlink can
reference an existing workspace without copying or modifying its contents.
Use `--kind command --command '["node","main.js"]'` for arbitrary programs.
Only Codex and command have bundled scaffolds; other implementations can be
registered directly. The scaffold's Python program is a starter, not a required
language or a graph node type.

Codex scaffolding includes persistent role instructions, a local workflow-contract
skill, a stdio MCP configuration and a read-only input tool. Adapt these for the
agent's job. `AGENTS.md` is the provider's persistent instruction mechanism, not
an API system role. Native project config loading requires appropriate provider
trust settings; global settings may still apply. AgentFlow does not modify global
Codex configuration or promise strict per-agent isolation.

Workspace and session are independent. Bind the directory in the registry;
choose `new`, `fixed`, `inherit` or `fork` on each graph execution candidate.
Resuming a persisted provider session does not attach to a live interactive
process. Existing agent access and sandbox permissions remain its owner's
configuration; registration does not silently widen them.

## Where files go

| File/data | Location / owner |
| --- | --- |
| Graph definitions | Root `workflow.json` for one graph; `workflows/<name>.json` for multiple graphs, authored |
| Executor registry, optional triggers | Workspace root, authored |
| Agent instructions, skills and tools | Executor workspace, authored or externally owned |
| Graph snapshot, run state and events | `runs/<run-id>/`, engine |
| Candidate-attempt input/config/result/session | `runs/<run-id>/artifacts/<node-id>.<visit>.candidate-<n>.attempt-<n>.*`, engine |
| Candidate-attempt stdout/stderr | `runs/<run-id>/artifacts/logs/`, engine |
| Dispatch queue and delivery evidence | `dispatch/`, `queue.json`, `worker.json`, `trigger-deliveries/`, shared worker and trigger service |
| Business reports/files | Program-selected, e.g. `output/`; return paths in result |
| Provider session history | Provider-managed store; run metadata records session ID |

`AGENTFLOW_ROOT` identifies the orchestration root. `AGENTFLOW_WORKSPACE` is the
executor's working directory. Always read/write the absolute
`AGENTFLOW_INPUT_FILE`, `AGENTFLOW_CONFIG_FILE` and `AGENTFLOW_RESULT_FILE` paths.
stdout is a log. A report under `output/` does not replace the required result
JSON. Relative command arguments resolve from the executor workspace.

## Verify a deliverable

The first design step is to identify node responsibilities and JSON contracts,
then each agent's persistent instructions, relevant context, selected skills,
tools, permissions and session policy. Record routing, joins, rework and human
feedback in `DESIGN.md` or an existing runbook. A provider adapter is a launch
mechanism; the author still supplies the specialist role. An external agent can
be reused in place when its actual context/resources have been identified.

`executor init <name> --kind claude-code` now creates the corresponding native
`CLAUDE.md`, `.claude/skills/workflow-contract/SKILL.md`, `.mcp.json` and local
input tools. The generated MCP entry uses project scope; actual availability
still depends on provider configuration and workspace trust. See the official
[Claude skills](https://code.claude.com/docs/en/skills) and
[project MCP configuration](https://code.claude.com/docs/en/mcp) documentation.

```sh
agentflow workspace validate --input agent_dir=/existing/agent --format json
```

`agent_resources` inventories each native agent candidate's local instruction,
skill and tool configuration files with hashes, resolved directory, session
policy and missing/unresolved sources. Supplying representative inputs resolves
paths based on run input; future node outputs remain unknown. The report does
not expose configuration contents or inspect user-wide settings.
`effective_context_verified:false` distinguishes discovery from actual loading
and role behavior. Missing local resources are advisory: inline instructions,
provider tools, external skills and program-only workflows remain supported.

1. Run `workspace validate` to check the default graph and enabled trigger
   targets, registrations, directory/symlink bindings and executable availability.
   A multi graph workspace does not need a root `workflow.json`. Graph directories
   are not scanned automatically: validate each graph without an enabled trigger
   explicitly with `--workflow workflows/<name>.json`.
2. Validate the graph and dry-run representative input. Exit 10 means only that
   the dry-run passed.
3. Execute a nonempty representative run. Inspect whether required nodes actually
   ran, their results/logs and business artifacts. Test custom programs for real;
   separately identify model stubs. Test human feedback routes with preserved
   task identity and comments. A skipped reviewer or successful degraded candidate
   does not establish that review work completed.
4. Include a RUNBOOK with root directory, launch/resume commands, output paths
   and verified/unverified behavior.

Static validation reports `execution_verified:false`. Reachability checks model
the failure the runtime fallback policy would observe for a statically
unavailable candidate, including its 1-based global attempt position after
retries and the retry budgets consumed by earlier candidates (available or
not); when a policy depends on the runtime driver message, validation reports
reachability as inconclusive instead of claiming a later candidate is
unreachable. That dependency is read from the parsed expression's
`failure.message` / `failure['message']` selector, so a string literal or an
unrelated identifier containing "message" is not mistaken for it; if the policy
matches arbitrary driver text, the result is always inconclusive and synthetic
validator text never proves reachability. Command
configuration follows runtime precedence: deterministic session/command errors
are reported before workspace or executable availability, and this session
preflight applies to every executor kind, including external and agent
candidates. Native registrations follow `BuildCommand` exactly: a configured
base command does not satisfy `inherit`, `fork`, or a fixed explicit ID without
`resumeCommand`/`forkCommand`, and Codex with `ephemeral: true` rejects any
existing session. An entry candidate cannot inherit a session, a missing or
sessionless source is reported, and a statically known incompatible session
protocol is rejected; sources whose runtime protocol is indeterminate remain
conservatively usable. An explicit `session.from` must also be able to complete
before the target visit: a source reachable from the entry only through an
acyclic target is rejected, while a target on a cycle stays inconclusive because
an earlier iteration may have completed the source. Availability is checked for
the command the candidate's session mode actually launches, and a missing
resume or fork command is reported as a configuration error rather than a
startup failure. The external adapter supports only `session.mode: new`, since it
waits for another application and has no provider session lifecycle. Static
validation cannot verify model credentials, every program
argument, unresolved runtime workspace templates or business behavior. [Workspace
authoring benchmark](../benchmarks/workspace-authoring/README.md)
checks fresh agent submissions using real programs and a deterministic model
transport; native model smoke tests are recorded separately.
