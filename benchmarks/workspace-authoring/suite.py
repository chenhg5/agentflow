#!/usr/bin/env python3
"""Layout-v1 cold authoring cases, golden builders and behavioral grading."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import time

ROOT = Path(__file__).resolve().parents[2]
HERE = Path(__file__).resolve().parent
SESSION = "fixture-existing-session"
TEXT = "directory-contract-42"


def write_json(path, value):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, ensure_ascii=False, indent=2)+"\n")


def digest_tree(directory):
    return {str(p.relative_to(directory)): hashlib.sha256(p.read_bytes()).hexdigest() for p in sorted(directory.rglob("*")) if p.is_file() and "__pycache__" not in p.parts}


def cli(root, *args, binary=None, allowed=(0,)):
    result = subprocess.run([str(binary or ROOT / "bin/agentflow"), *args, "--format", "json"], cwd=root, capture_output=True, text=True, timeout=60)
    assert result.returncode in allowed, f"{args}: {result.returncode}: {result.stdout} {result.stderr}"
    return json.loads(result.stdout)


def prepare(case, destination):
    root = Path(destination).resolve()
    root.mkdir(parents=True, exist_ok=False)
    (root / ".tool").mkdir()
    shutil.copy2(ROOT / "bin/agentflow", root / ".tool/agentflow")
    shutil.copytree(ROOT / "skills/agentflow", root / "skill")
    # Log discovery/scaffold use without exposing the private grader/golden.
    (root / "agentflow").write_text('''#!/usr/bin/env python3
import json,subprocess,sys
from pathlib import Path
root=Path(__file__).resolve().parent
r=subprocess.run([str(root/'.tool/agentflow'),*sys.argv[1:]],capture_output=True)
with (root/'cli-events.jsonl').open('a') as f: f.write(json.dumps({'argv':sys.argv[1:],'exit_code':r.returncode})+'\\n')
sys.stdout.buffer.write(r.stdout);sys.stderr.buffer.write(r.stderr);sys.exit(r.returncode)
''')
    (root / "agentflow").chmod(0o755)
    meta = {"case": case, "prepared_at": time.time(), "skill_sha256": hashlib.sha256((root / "skill/SKILL.md").read_bytes()).hexdigest(), "binary_sha256": hashlib.sha256((root / ".tool/agentflow").read_bytes()).hexdigest()}
    if case == "reuse":
        fixture = root.parent / (root.name + "-existing")
        fixture.mkdir(exist_ok=False)
        cli(fixture, "executor", "init", "existing", "--kind", "codex")
        external = fixture / "executors/agents/existing"
        (external / "AGENTS.md").write_text("Existing role: preserve incoming text and add ok:true. Use workflow_tools.read_input. Write result JSON to the execution-contract path. Do not change my files.\n")
        meta.update(existing=str(external), existing_hashes=digest_tree(external), session=SESSION)
        identity = f"复用已存在的 Codex agent，目录是 {external}，持久会话 ID 是 {SESSION}。通过 executor add 登记其路径或软链；必须在图中恢复这个固定 session。不要复制或更改该目录内容，不创建另一份 agent。"
    else:
        identity = "新建一个 Codex agent executor，放在 executors/agents/<name>/，有自己的 AGENTS.md、.agents/skills/、.codex/config.toml 和 tools/；使用本地 workflow_tools.read_input MCP 工具读取节点 JSON 输入。每次运行新起空白 session。常驻角色指令要体现本任务要求。"
    task = f"""在当前空目录创建一个独立 workflow 工作区，采用建议的目录规范 v1。先阅读 skill/SKILL.md，只使用本目录 ./agentflow 的 schema/help/初始化/校验能力和标准编程工具。不要访问产品源码、其他任务、历史答案或联网。允许只读检查下文给定的已有 agent 目录。不要修改全局 Codex 配置。

任务：{identity}
图是两个节点：agent 收到 run input {{"text":字符串}}，原样保留 text 并输出 {{"text":原值,"ok":true}}；之后普通程序收到 agent 的完整 output，在工作区根目录 output/result.json 保存该 JSON，输出 {{"artifact":"output/result.json","ok":true}}。程序放 executors/programs/<name>/，实际从 AGENTFLOW_INPUT_FILE 读输入、从 AGENTFLOW_ROOT 定位业务产物、写 AGENTFLOW_RESULT_FILE。不要把运行结果留在 executor 目录。
根目录 workflow.json、executors.json，并设置 triggers.json 每天北京时间9点触发本图，input text="{TEXT}"。无需启动持续服务。删除不用的 starter 节点、注册和程序。

交付 RUNBOOK.md 和 submission.json；后者格式 {{"workflow":"workflow.json","roles":{{"agent":"实际节点ID","archive":"实际节点ID"}}}}。请做静态校验和 dry-run，若使用临时替身验证，应恢复正式注册并注明范围。不要调用模型执行交付图、不要发送外部消息；评测器会替换模型传输，真实执行你交付的 MCP 工具和普通程序。不要向用户提问，本题需求完整。所有写操作限定当前目录，给定已有 agent 保持原样。
"""
    (root / "TASK.md").write_text(task)
    write_json(root / "attempt.json", meta)
    return {"workspace": str(root), **meta}


PROGRAM = '''import json,os
from pathlib import Path
incoming=json.loads(Path(os.environ['AGENTFLOW_INPUT_FILE']).read_text())
report=Path(os.environ['AGENTFLOW_ROOT'])/'output/result.json'
report.parent.mkdir(parents=True,exist_ok=True)
report.write_text(json.dumps(incoming))
Path(os.environ['AGENTFLOW_RESULT_FILE']).write_text(json.dumps({'artifact':'output/result.json','ok':True}))
'''


def golden(root):
    root = Path(root)
    meta = json.loads((root / "attempt.json").read_text())
    cli(root, "workspace", "init", ".")
    if meta["case"] == "owned":
        cli(root, "executor", "init", "transform", "--kind", "codex")
        session = {"mode": "new"}
    else:
        cli(root, "executor", "add", "transform", "--kind", "codex", "--workspace", meta["existing"])
        session = {"mode": "fixed", "id": meta["session"]}
    cli(root, "executor", "init", "archive", "--kind", "command")
    registry = json.loads((root / "executors.json").read_text())
    del registry["executors"]["step"]
    write_json(root / "executors.json", registry)
    shutil.rmtree(root / "executors/programs/step")
    (root / "executors/programs/archive/main.py").write_text(PROGRAM)
    shape = lambda props: {"type": "object", "required": list(props), "properties": {key: {"type": kind} for key, kind in props.items()}, "additionalProperties": False}
    data = shape({"text": "string", "ok": "boolean"})
    flow = {"version": "3", "name": "layout-v1", "entry": "transform", "inputs": {"text": {"type": "string", "required": True}}, "nodes": [{"id": "transform", "execution": {"candidates": [{"executor": "transform", "session": session, "config": {"instruction": "Use workflow_tools.read_input; preserve text and add ok:true. Write the required result JSON."}}]}, "inputSchema": shape({"text": "string"}), "outputSchema": data}, {"id": "archive", "execution": {"candidates": [{"executor": "archive"}]}, "inputSchema": data, "outputSchema": shape({"artifact": "string", "ok": "boolean"})}], "edges": [{"from": "transform", "to": "archive"}, {"from": "archive", "to": "$end"}]}
    write_json(root / "workflow.json", flow)
    write_json(root / "triggers.json", {"triggers": [{"id": "daily", "enabled": True, "source": {"kind": "cron", "expression": "0 9 * * *", "timezone": "Asia/Shanghai"}, "target": {"workflow": "workflow.json"}, "inputs": {"text": TEXT}}]})
    write_json(root / "submission.json", {"workflow": "workflow.json", "roles": {"agent": "transform", "archive": "archive"}})
    (root / "RUNBOOK.md").write_text("Run from this root: agentflow workspace validate; agentflow run start workflow.json --input text=example. Evidence is in runs/, report in output/result.json. Model behavior not verified by deterministic transport.\n")


def grade(source):
    source = Path(source).resolve()
    checks = []
    def check(name, condition, detail=""):
        checks.append({"name": name, "passed": bool(condition), "detail": str(detail)})
        assert condition, name + ": " + str(detail)
    try:
        meta = json.loads((source / "attempt.json").read_text())
        submission = json.loads((source / "submission.json").read_text())
        check("single graph at root", submission["workflow"] == "workflow.json" and not (source / ".agentflow").exists() and not (source / "workflows").exists())
        check("runbook", (source / "RUNBOOK.md").is_file())
        flow = json.loads((source / "workflow.json").read_text())
        registry = json.loads((source / "executors.json").read_text())["executors"]
        nodes = {node["id"]: node for node in flow["nodes"]}
        agent = nodes[submission["roles"]["agent"]]
        archive = nodes[submission["roles"]["archive"]]
        agent_candidate = agent["execution"]["candidates"][0]
        archive_candidate = archive["execution"]["candidates"][0]
        a, p = registry[agent_candidate["executor"]], registry[archive_candidate["executor"]]
        check("only task executors", len(registry) == 2 and len(nodes) == 2)
        check("agent implementation", a.get("kind") == "codex")
        workspace = Path(a["workspace"])
        original_agent = (source / workspace).resolve()
        check("program grouped", Path(p["workspace"]).parts[:2] == ("executors", "programs"))
        check("agent files", all((original_agent / name).is_file() for name in ["AGENTS.md", ".codex/config.toml"]) and any((original_agent / ".agents/skills").glob("*/SKILL.md")) and (original_agent / "tools").is_dir())
        if meta["case"] == "owned":
            check("owned agent grouped", workspace.parts[:2] == ("executors", "agents") and not (source / workspace).is_symlink())
            check("new session", agent_candidate.get("session", {}).get("mode", "new") == "new")
        else:
            check("external directory retained", original_agent == Path(meta["existing"]).resolve())
            check("external files unchanged", digest_tree(original_agent) == meta["existing_hashes"])
            check("fixed existing session", agent_candidate["session"] == {"mode": "fixed", "id": meta["session"]})
            local = source / "executors/agents"
            check("no duplicated agent", not local.exists() or all(f.is_symlink() for f in local.iterdir()))
        report = cli(source, "workspace", "validate", binary=source / ".tool/agentflow")
        check("static validation", report["valid"] and report["execution_verified"] is False, report["issues"])
        triggers = json.loads((source / "triggers.json").read_text())["triggers"]
        check("daily schedule bound", any(t.get("enabled", True) and t["source"].get("kind") == "cron" and t["source"].get("expression") == "0 9 * * *" and t["source"].get("timezone") == "Asia/Shanghai" and t["target"]["workflow"] == "workflow.json" and t.get("inputs") == {"text": TEXT} for t in triggers))
        with tempfile.TemporaryDirectory(prefix="agentflow-layout-grade-") as temporary:
            root = Path(temporary).resolve() / "workspace"
            shutil.copytree(source, root, symlinks=True, ignore=shutil.ignore_patterns("runs", "output", "__pycache__", "agent-stdout.log", "agent-stderr.log"))
            if meta["case"] == "reuse":
                copied_agent = Path(temporary) / "existing"
                shutil.copytree(original_agent, copied_agent)
                a["workspace"] = str(copied_agent)
            # Replace only model transport; preserve directory, MCP and real archive.
            a["command"] = [sys.executable, str(HERE / "transport.py"), "new", "{{session_id}}", meta.get("session", "")]
            a["resumeCommand"] = [sys.executable, str(HERE / "transport.py"), "resume", "{{session_id}}", meta.get("session", "")]
            a["env"] = {**a.get("env", {}), "PYTHONDONTWRITEBYTECODE": "1"}
            write_json(root / "executors.json", {"executors": registry})
            start = cli(root, "run", "start", "workflow.json", "--input", "text="+TEXT, binary=source / ".tool/agentflow")
            state = start.get("run", start)
            check("run succeeded", state["status"] == "succeeded", state["status"])
            run_dir = root / "runs" / state["run_id"]
            check("run store", run_dir.is_dir() and not (root / p["workspace"] / "runs").exists())
            paths = []
            for node in state["nodes"].values():
                for path in node["artifacts"].values():
                    artifact = (run_dir / path).resolve()
                    check("node artifact", artifact.is_file() and artifact.is_relative_to(run_dir), path)
                    paths.append(path)
            check("agent output transferred", state["nodes"][archive["id"]]["input"] == {"text": TEXT, "ok": True})
            check("business output", json.loads((root / "output/result.json").read_text()) == {"text": TEXT, "ok": True})
            check("archive result", state["nodes"][archive["id"]]["result"] == {"artifact": "output/result.json", "ok": True})
            proof = json.loads(next(run_dir.glob("artifacts/*.transport.json")).read_text())
            check("tool invocation and cwd", proof["tool"] == "workflow_tools.read_input" and Path(proof["workspace"]).resolve() == Path(state["nodes"][agent["id"]]["workspace"]).resolve())
            check("absolute contract paths", proof["root"] == str(root) and all(Path(proof[k]).is_relative_to(run_dir) for k in ["input_file", "result_file"]))
            if meta["case"] == "reuse":
                check("resume transport", proof["mode"] == "resume" and proof["session"] == meta["session"])
                check("copied external unchanged", digest_tree(copied_agent) == meta["existing_hashes"])
    except Exception as error:
        if not checks or checks[-1]["passed"]:
            checks.append({"name": "evaluation", "passed": False, "detail": str(error)})
    return {"attempt": str(source), "passed": all(c["passed"] for c in checks), "scope": "static bindings + real submitted MCP/program execution; model transport substituted, no model-quality claim", "checks_passed": sum(c["passed"] for c in checks), "checks_total": len(checks), "checks": checks}


def selftest():
    with tempfile.TemporaryDirectory(prefix="agentflow-layout-selftest-") as tmp:
        root = Path(tmp)
        if not shutil.which("codex"):
            stub = root / "bin/codex"
            stub.parent.mkdir()
            stub.write_text("#!/bin/sh\necho 'benchmark availability fixture; must not execute a model' >&2\nexit 99\n")
            stub.chmod(0o755)
            os.environ["PATH"] = str(stub.parent) + os.pathsep + os.environ.get("PATH", "")
        for case in ["owned", "reuse"]:
            attempt = root / case
            prepare(case, attempt)
            golden(attempt)
            report = grade(attempt)
            assert report["passed"], report
            print(f"PASS golden {case}: {report['checks_passed']} checks")
            # Valid JSON is insufficient: bad entrypoints and storage must fail.
            program = attempt / "executors/programs/archive/main.py"
            before = program.read_text()
            program.unlink()
            assert not grade(attempt)["passed"]
            program.write_text(before.replace("Path(os.environ['AGENTFLOW_ROOT'])", "Path.cwd()"))
            assert not grade(attempt)["passed"]
            program.write_text(before)
            registry = json.loads((attempt / "executors.json").read_text())
            original_registry = json.loads(json.dumps(registry))
            registry["executors"]["archive"]["workspace"] = "missing"
            write_json(attempt / "executors.json", registry)
            assert not grade(attempt)["passed"]
            write_json(attempt / "executors.json", original_registry)
            if case == "reuse":
                flow = json.loads((attempt / "workflow.json").read_text())
                original_flow = json.loads(json.dumps(flow))
                flow["nodes"][0]["execution"]["candidates"][0]["session"] = {"mode": "new"}
                write_json(attempt / "workflow.json", flow)
                assert not grade(attempt)["passed"]
                write_json(attempt / "workflow.json", original_flow)
                (Path(json.loads((attempt / "attempt.json").read_text())["existing"]) / "AGENTS.md").write_text("overwritten")
                assert not grade(attempt)["passed"]
            print(f"PASS negative mutations {case}")


def main():
    parser = argparse.ArgumentParser()
    sub = parser.add_subparsers(dest="command", required=True)
    p = sub.add_parser("prepare"); p.add_argument("case", choices=["owned", "reuse"]); p.add_argument("directory")
    p = sub.add_parser("golden"); p.add_argument("directory")
    p = sub.add_parser("grade"); p.add_argument("directory"); p.add_argument("--output")
    sub.add_parser("selftest")
    p = sub.add_parser("run-agent"); p.add_argument("directory"); p.add_argument("--agent-argv", required=True); p.add_argument("--label", required=True); p.add_argument("--timeout", type=int, default=420)
    args = parser.parse_args()
    if args.command == "prepare":
        print(json.dumps(prepare(args.case, args.directory), ensure_ascii=False, indent=2))
    elif args.command == "golden":
        golden(args.directory)
    elif args.command == "grade":
        report = grade(args.directory)
        if args.output:
            write_json(Path(args.output), report)
        summary = {key: report[key] for key in ["attempt", "passed", "checks_passed", "checks_total"]}
        summary["failures"] = [check for check in report["checks"] if not check["passed"]]
        print(json.dumps(summary, ensure_ascii=False, indent=2))
        sys.exit(0 if report["passed"] else 1)
    elif args.command == "selftest":
        selftest()
    else:
        sys.path.insert(0, str(ROOT / "benchmarks/workflow-authoring"))
        from bench import run_agent
        result = run_agent(args.directory, json.loads(args.agent_argv), args.label, args.timeout)
        print(json.dumps(result, indent=2))
        sys.exit(0 if result["agent_status"] == "completed" else 2)


if __name__ == "__main__":
    main()
