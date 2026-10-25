#!/usr/bin/env python3
"""Tiny stdio MCP adapter for per-agent local programs in this demo."""

import json
import subprocess
import sys
from pathlib import Path


manifest = json.loads(Path(sys.argv[1]).read_text(encoding="utf-8"))
tools = {item["name"]: item for item in manifest["tools"]}


def reply(request_id, result=None, error=None):
    message = {"jsonrpc": "2.0", "id": request_id}
    if error is None:
        message["result"] = result
    else:
        message["error"] = error
    sys.stdout.write(json.dumps(message, ensure_ascii=False) + "\n")
    sys.stdout.flush()


for line in sys.stdin:
    request = None
    try:
        request = json.loads(line)
        method = request.get("method")
        if "id" not in request:
            continue
        request_id = request["id"]
        if method == "initialize":
            reply(request_id, {
                "protocolVersion": request.get("params", {}).get("protocolVersion", "2024-11-05"),
                "capabilities": {"tools": {}},
                "serverInfo": {"name": manifest["name"], "version": "0.1.0"},
            })
        elif method == "ping":
            reply(request_id, {})
        elif method == "tools/list":
            reply(request_id, {"tools": [
                {
                    "name": item["name"],
                    "description": item["description"],
                    "inputSchema": item["inputSchema"],
                    "annotations": item.get("annotations", {}),
                }
                for item in manifest["tools"]
            ]})
        elif method == "tools/call":
            params = request.get("params", {})
            tool = tools.get(params.get("name"))
            if tool is None:
                reply(request_id, error={"code": -32602, "message": "unknown tool"})
                continue
            completed = subprocess.run(
                tool["command"],
                input=json.dumps(params.get("arguments", {})),
                text=True,
                capture_output=True,
                timeout=15,
                check=False,
            )
            reply(request_id, {
                "content": [{"type": "text", "text": completed.stdout if completed.returncode == 0 else completed.stderr}],
                "isError": completed.returncode != 0,
            })
        else:
            reply(request_id, error={"code": -32601, "message": "method not found"})
    except Exception as error:
        if isinstance(request, dict) and "id" in request:
            reply(request["id"], error={"code": -32603, "message": str(error)})
        else:
            print(error, file=sys.stderr)
