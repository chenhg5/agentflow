#!/usr/bin/env python3
"""Private deterministic model transport; executes the submitted MCP server."""
import json
import os
from pathlib import Path
import subprocess
import sys
import tomllib

mode, session, expected = sys.argv[1:]
if expected:
    assert mode == "resume" and session == expected, "existing session was not resumed"
else:
    assert mode == "new" and not session, "owned agent must start a new session"
workspace = Path.cwd()
config = tomllib.loads((workspace / ".codex/config.toml").read_text())
servers = config["mcp_servers"]
assert "workflow_tools" in servers, "local workflow_tools server missing"
server = servers["workflow_tools"]
requests = [
    {"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {"protocolVersion": "2024-11-05", "capabilities": {}, "clientInfo": {"name": "benchmark", "version": "1"}}},
    {"jsonrpc": "2.0", "method": "notifications/initialized"},
    {"jsonrpc": "2.0", "id": 2, "method": "tools/list"},
    {"jsonrpc": "2.0", "id": 3, "method": "tools/call", "params": {"name": "read_input", "arguments": {}}},
]
environment = dict(os.environ)
environment.update(server.get("env", {}))
response = subprocess.run([server["command"], *server.get("args", [])], input="".join(json.dumps(r)+"\n" for r in requests), text=True, capture_output=True, env=environment, timeout=20)
assert response.returncode == 0, response.stderr
messages = {r["id"]: r for r in map(json.loads, response.stdout.splitlines())}
assert not any(r.get("error") for r in messages.values()), messages
listed = {r["name"]: r for r in messages[2]["result"]["tools"]}
assert listed["read_input"]["annotations"]["readOnlyHint"] is True
called = messages[3]["result"]
assert not called.get("isError"), called
incoming = json.loads(called["content"][0]["text"])
assert incoming == json.loads(Path(os.environ["AGENTFLOW_INPUT_FILE"]).read_text()), "tool input differs from engine input"
result = {"text": incoming["text"], "ok": True}
result_file = Path(os.environ["AGENTFLOW_RESULT_FILE"])
result_file.write_text(json.dumps(result))
result_file.with_suffix(".transport.json").write_text(json.dumps({"workspace": str(workspace), "root": os.environ["AGENTFLOW_ROOT"], "input_file": os.environ["AGENTFLOW_INPUT_FILE"], "result_file": str(result_file), "mode": mode, "session": session, "tool": "workflow_tools.read_input"}, indent=2))
Path(os.environ["AGENTFLOW_SESSION_FILE"]).write_text(session or "fixture-new-session")
print(json.dumps({"type": "thread.started", "thread_id": session or "fixture-new-session"}))
