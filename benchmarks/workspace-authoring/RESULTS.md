# Local pilot: 2026-10-25

Source checkout based on `3166f31` with layout-v1 scaffolding/validation changes.
Native Codex CLI 0.153.4. Each authoring attempt was a new process, no resume,
with only its task, copied AgentFlow binary/skill and optional external-agent
fixture. User config/rules and automatic project instructions were disabled for
authoring. Shell-command logs were inspected for source/history/golden access;
none was observed. This is context separation, not a hostile filesystem sandbox.
The runner retained binary and skill SHA-256 hashes per attempt.

| Authoring model (low effort) | Case | Artifact/run checks | Scope audit |
| --- | --- | --- | --- |
| gpt-5.6-sol | Owned agent | 28/28 | No violation observed |
| gpt-5.6-sol | Existing agent | 32/32 | No violation observed |
| gpt-5.6-luna | Owned agent | 28/28 | Used system temporary directory |
| gpt-5.6-luna | Existing agent | 32/32 | Wrote schema scratch file to `/tmp` |
| gpt-5.6-luna, revised skill | Owned agent | 28/28 | Scratch remained within workspace and was removed |
| gpt-5.6-luna, revised skill | Existing agent | 32/32 | Scratch remained within workspace; one root scratch file remained |

All six submissions passed the **artifact and execution contract**. The two
initial Luna attempts violated the task's write boundary during authoring. The
skill was clarified to keep validation scratch files inside the permitted
workspace. Fresh retests fixed the boundary violation; one left a scratch file
at the root instead of cleaning a `scratch/` directory. File cleanup discipline
is a remaining authoring limitation, separate from the verified runtime paths.

The deterministic grader replaced only model transport. It executed each
submitted MCP input tool and ordinary archive program, verified exact JSON
handoff, session selection, business output, run artifacts and unchanged
external-agent files. Positive goldens plus eight independent negative mutations
are included in `make test-benchmark`.

A separately retained native demo used the **fresh Sol-owned submission** with
no manual graph, role, skill, tool or program changes. Only its model was pinned
to the locally available `gpt-5.6-sol`. Native MCP read_input succeeded; the agent
and archive nodes completed, and `output/result.json` contained the exact input
text plus `ok:true`. A separate scaffold test also created a real session and
successfully resumed that same session with another input.

`make check`, all three CLI E2E scripts, both deterministic benchmark suites and
skill validation passed locally. These two simple tasks and two models are a
pilot, not proof that every agent can author every workflow. Native project
configuration still depends on provider trust/settings; existing live
interactive processes and cross-workflow shared-session coordination are not
covered.
