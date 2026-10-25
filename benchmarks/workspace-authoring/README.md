# Workspace authoring benchmark

Two fresh-directory tasks check the v1 layout: create an owned Codex agent, or
bind an existing external agent and resume its known session without changing
its files. Both require a real archive program, a daily trigger, a runbook and
root-level evidence storage.

See [the local pilot results](RESULTS.md) for fresh-model attempts, native smoke
coverage and observed limitations.

```sh
make build
python3 benchmarks/workspace-authoring/suite.py selftest
python3 benchmarks/workspace-authoring/suite.py prepare owned /path/to/new-attempt
# Start an authoring agent in that directory with TASK.md as its only task.
python3 benchmarks/workspace-authoring/suite.py run-agent /path/to/new-attempt \
  --label codex-model-low \
  --agent-argv '["codex","exec","--json","--ignore-user-config","--ignore-rules","--skip-git-repo-check","-s","workspace-write","-m","MODEL","-c","model_reasoning_effort=\"low\"","-"]'
python3 benchmarks/workspace-authoring/suite.py grade /path/to/new-attempt --output report.json
```

Preparation exposes only the task, copied binary/skill, and (for reuse) a sibling
agent fixture. Do not expose golden builders, source or earlier attempts to the
authoring process. A new process receives the task on stdin and no resume ID;
the runner removes inherited session environment variables. This is context
separation, not a hostile-code filesystem sandbox. Inspect authoring logs for
resource-boundary violations when recording results.

The grader copies submissions to a temporary workspace. It checks static
bindings before replacing only model transport. The substitute invokes the
**submitted MCP server** and its read_input tool, then the engine runs the
**submitted archive program**. Assertions cover session selection, directory
bindings, exact upstream JSON, real business output, absolute contract paths,
run artifact locations and unchanged external files. Existing-agent fixtures
are copied for execution so grading does not write to the original directory.

Python 3.11+ is required for native TOML parsing. Codex must be discoverable for
the original registration's static validation; CI uses a version-only fixture
when no native binary is installed. Model authentication and quality are not
verified by the deterministic grader. Run a separate native smoke test to check
provider configuration/trust, instruction loading and actual model/tool use.

`make test-benchmark` includes positive golden cases and negative mutations for
missing executable scripts, wrong output location, broken workspace paths and
overwritten external instructions. Fresh model-authoring attempts are an opt-in
experiment; keep their reports, CLI events, native logs and binary/skill hashes.
