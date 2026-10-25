# Workflow workspace layout v1

The workspace is a standalone orchestration directory. It may coordinate several
business repositories and existing agents. Invoke AgentFlow from its root.

```text
my-workflow/
├── workflow.json                    # single graph
├── executors.json                   # named executor registrations
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
├── dispatch/                        # generated trigger queue
└── trigger-deliveries/               # generated delivery records
```

There is no `.agentflow/` container. `workflows/` is optional when organizing
multiple graphs. Directory categories are authoring conventions; the graph
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
choose `new`, `fixed`, `inherit` or `fork` in the graph's `execution.session`.
Resuming a persisted provider session does not attach to a live interactive
process. Existing agent access and sandbox permissions remain its owner's
configuration; registration does not silently widen them.

## Where files go

| File/data | Location / owner |
| --- | --- |
| Graph, executor registry, optional triggers | Workspace root, authored |
| Agent instructions, skills and tools | Executor workspace, authored or externally owned |
| Graph snapshot, run state and events | `runs/<run-id>/`, engine |
| Node input/config/result | `runs/<run-id>/artifacts/<node-id>.<visit>.*.json`, engine |
| Node stdout/stderr | `runs/<run-id>/artifacts/logs/`, engine |
| Dispatch queue and delivery evidence | `dispatch/`, `trigger-deliveries/`, trigger service |
| Business reports/files | Program-selected, e.g. `output/`; return paths in result |
| Provider session history | Provider-managed store; run metadata records session ID |

`AGENTFLOW_ROOT` identifies the orchestration root. `AGENTFLOW_WORKSPACE` is the
executor's working directory. Always read/write the absolute
`AGENTFLOW_INPUT_FILE`, `AGENTFLOW_CONFIG_FILE` and `AGENTFLOW_RESULT_FILE` paths.
stdout is a log. A report under `output/` does not replace the required result
JSON. Relative command arguments resolve from the executor workspace.

## Verify a deliverable

1. Run `workspace validate` to check the default graph and enabled trigger
   targets, registrations, directory/symlink bindings and executable availability.
   A trigger-only workspace need not have `workflow.json`. Select other graphs
   explicitly with `--workflow <path>`.
2. Validate the graph and dry-run representative input. Exit 10 means only that
   the dry-run passed.
3. Execute a smoke run. Inspect completion, actual node results/logs and business
   artifacts. Test custom programs for real; separately identify model stubs.
4. Include a RUNBOOK with root directory, launch/resume commands, output paths
   and verified/unverified behavior.

Static validation reports `execution_verified:false`. It cannot verify model
credentials, every program argument, runtime workspace templates or business
behavior. [Workspace authoring benchmark](../benchmarks/workspace-authoring/README.md)
checks fresh agent submissions using real programs and a deterministic model
transport; native model smoke tests are recorded separately.
