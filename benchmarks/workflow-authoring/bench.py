#!/usr/bin/env python3
"""Prepare cold-context authoring attempts and grade actual submitted workflows."""

import argparse
import hashlib
import json
from pathlib import Path
import shutil
import os
import signal
import subprocess
import sys
import time

import fixtures

ROOT = Path(__file__).resolve().parents[2]
HERE = Path(__file__).resolve().parent


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def case_for(name):
    return next(
        case
        for case in json.loads((HERE / "cases.json").read_text())["cases"]
        if case["id"] == name
    )


def prepare(case_id, destination, skill=None):
    case = case_for(case_id)
    destination = Path(destination).resolve()
    destination.mkdir(parents=True, exist_ok=False)
    (destination / ".tool").mkdir()
    shutil.copy2(ROOT / "bin/agentflow", destination / ".tool/agentflow")
    wrapper = """#!/usr/bin/env python3
import json,sys,subprocess,time
from pathlib import Path
root=Path(__file__).resolve().parent
started=time.monotonic()
r=subprocess.run([str(root/'.tool/agentflow'),*sys.argv[1:]],capture_output=True)
with (root/'cli-events.jsonl').open('a') as out: out.write(json.dumps({'argv':sys.argv[1:],'exit_code':r.returncode,'elapsed_seconds':time.monotonic()-started,'stdout':r.stdout.decode(errors='replace'),'stderr':r.stderr.decode(errors='replace')})+'\\n')
sys.stdout.buffer.write(r.stdout);sys.stderr.buffer.write(r.stderr);sys.exit(r.returncode)
"""
    (destination / "agentflow").write_text(wrapper)
    (destination / "agentflow").chmod(0o755)
    shutil.copytree(Path(skill or ROOT / "skills/agentflow"), destination / "skill")
    (destination / "fixtures").mkdir()
    for name, content in {
        "channel.py": fixtures.CHANNEL,
        "measure.py": fixtures.MEASURE,
        "finish.py": fixtures.FINISH,
    }.items():
        (destination / "fixtures" / name).write_text(content)
    wrapper = f"""你在一个全新目录中完成下面的实际工作流搭建任务。只使用这个目录里的 ./agentflow 二进制、skill/SKILL.md、fixtures 和标准 Python/Node/shell 工具。不要读取其他目录、源码仓库、历史任务或 golden/reference 答案，不要联网、调用真实模型、发送真实消息。你可以使用工具的 schema/help/validate/dry-run 等能力发现接口。先阅读 skill/SKILL.md，然后完成任务。

业务需求：
{case["prompt"]}

交付要求：在这个目录生成 workflow JSON、自定义 executor 配置/程序（如需要）、RUNBOOK.md，以及 submission.json。submission.json 格式为 {{"workflow":"相对路径","roles":{{逻辑角色:实际节点ID}}}}，本题逻辑角色是 {json.dumps(case["roles"])}；角色映射只是供测试定位节点，你可以自由选择节点 ID。RUNBOOK.md 说明执行方法、推进方式和已验证/未验证内容。不要调用 Codex/Claude 模型执行交付的流程；模型步骤将由评测器用确定性程序替代。非模型程序可在本地测试。不需要向用户提问，需求已完整给出。只在当前工作目录工作。
"""
    (destination / "TASK.md").write_text(wrapper)
    meta = {
        "case": case_id,
        "split": case["split"],
        "binary_sha256": digest(destination / ".tool/agentflow"),
        "skill_sha256": digest(destination / "skill/SKILL.md"),
        "prepared_at": time.time(),
    }
    (destination / "attempt.json").write_text(json.dumps(meta, indent=2))
    return {
        "workspace": str(destination),
        "prompt": str(destination / "TASK.md"),
        **meta,
    }


def run_agent(workspace, argv, label, timeout):
    workspace = Path(workspace).resolve()
    argv = [
        part.replace("{workspace}", str(workspace)).replace(
            "{task}", str(workspace / "TASK.md")
        )
        for part in argv
    ]
    if not argv or not all(isinstance(part, str) for part in argv):
        raise ValueError("agent argv must be a non-empty string array")
    meta = json.loads((workspace / "attempt.json").read_text())
    meta.update(
        agent_label=label,
        agent_argv=argv,
        context_mode="new process; task passed on stdin; no resume",
    )
    environment = {
        key: value
        for key, value in os.environ.items()
        if key not in ["CODEX_THREAD_ID", "CODEX_SESSION_ID", "CLAUDE_SESSION_ID"]
    }
    started = time.monotonic()
    process = subprocess.Popen(
        argv,
        cwd=workspace,
        env=environment,
        stdin=subprocess.PIPE,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
        start_new_session=os.name != "nt",
    )
    status = "completed"
    try:
        stdout, stderr = process.communicate(
            (workspace / "TASK.md").read_text(), timeout=timeout
        )
        if process.returncode:
            status = "process_error"
    except subprocess.TimeoutExpired:
        status = "timeout"
        if os.name != "nt":
            os.killpg(process.pid, signal.SIGTERM)
        else:
            process.terminate()
        try:
            stdout, stderr = process.communicate(timeout=5)
        except subprocess.TimeoutExpired:
            if os.name != "nt":
                os.killpg(process.pid, signal.SIGKILL)
            else:
                process.kill()
            stdout, stderr = process.communicate()
    (workspace / "agent-stdout.log").write_text(stdout)
    (workspace / "agent-stderr.log").write_text(stderr)
    meta.update(
        agent_status=status,
        agent_exit_code=process.returncode,
        agent_elapsed_seconds=time.monotonic() - started,
    )
    (workspace / "attempt.json").write_text(
        json.dumps(meta, ensure_ascii=False, indent=2) + "\n"
    )
    return {
        "workspace": str(workspace),
        "agent_status": status,
        "agent_exit_code": process.returncode,
        "elapsed_seconds": meta["agent_elapsed_seconds"],
    }


def main():
    parser = argparse.ArgumentParser()
    commands = parser.add_subparsers(dest="command", required=True)
    p = commands.add_parser("prepare")
    p.add_argument("case")
    p.add_argument("destination")
    p.add_argument("--skill")
    p = commands.add_parser("grade")
    p.add_argument("workspace")
    p.add_argument("--output")
    p.add_argument(
        "--binary", help="Replay an unchanged submission using another tool binary"
    )
    p = commands.add_parser("references")
    p.add_argument("destination")
    p = commands.add_parser("run-agent")
    p.add_argument("workspace")
    p.add_argument("--agent-argv", required=True)
    p.add_argument("--agent-label", required=True)
    p.add_argument("--timeout", type=int, default=420)
    args = parser.parse_args()
    if args.command == "prepare":
        print(
            json.dumps(
                prepare(args.case, args.destination, args.skill),
                ensure_ascii=False,
                indent=2,
            )
        )
    elif args.command == "grade":
        from grader import Grade

        workspace = Path(args.workspace)
        meta = json.loads((workspace / "attempt.json").read_text())
        report = Grade(workspace, case_for(meta["case"]), args.binary).evaluate()
        if args.output:
            Path(args.output).write_text(
                json.dumps(report, ensure_ascii=False, indent=2) + "\n"
            )
        print(
            json.dumps(
                {
                    key: report[key]
                    for key in ["attempt", "passed", "checks_passed", "checks_total"]
                }
                | {
                    "failures": [
                        item for item in report["checks"] if not item["passed"]
                    ]
                },
                ensure_ascii=False,
                indent=2,
            )
        )
        sys.exit(0 if report["passed"] else 1)
    elif args.command == "run-agent":
        result = run_agent(
            args.workspace, json.loads(args.agent_argv), args.agent_label, args.timeout
        )
        print(json.dumps(result, ensure_ascii=False, indent=2))
        sys.exit(0 if result["agent_status"] == "completed" else 2)
    elif args.command == "references":
        from goldens import generate

        generate(args.destination)


if __name__ == "__main__":
    main()
