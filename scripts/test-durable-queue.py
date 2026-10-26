#!/usr/bin/env python3
"""Real CLI/service recovery test using deterministic program and external executors.
No model accounts, global configuration, IM messages, or real human approvals.
"""
import concurrent.futures
import hashlib
import json
import os
from pathlib import Path
import signal
import socket
import subprocess
import sys
import tempfile
import time
import urllib.request

BIN = str(Path(os.environ.get("AGENTFLOW_BIN", "bin/agentflow")).resolve())
ROOT = Path(os.environ.get("AGENTFLOW_QUEUE_WORKSPACE") or tempfile.mkdtemp(prefix="af-queue-e2e-"))
ROOT.mkdir(parents=True, exist_ok=True)
processes = []
logs = []
proof = {"workspace": str(ROOT), "executor_kind": "deterministic Python programs + external input", "checks": []}


def check(value, message):
    if not value:
        raise AssertionError(message)
    proof["checks"].append(message)


def cli(*args, code=0):
    result = subprocess.run([BIN, *args, "--format", "json"], cwd=ROOT, text=True, capture_output=True, timeout=20)
    if result.returncode != code:
        raise AssertionError(f"{args}: exit {result.returncode}, expected {code}\n{result.stdout}\n{result.stderr}")
    return json.loads(result.stdout or result.stderr)


def service(*args):
    output = open(ROOT / ("service-" + str(len(processes)) + ".log"), "w")
    logs.append(output)
    process = subprocess.Popen([BIN, *args], cwd=ROOT, stdout=output, stderr=output)
    processes.append(process)
    return process


def wait(predicate, message, seconds=30):
    end = time.monotonic() + seconds
    while time.monotonic() < end:
        value = predicate()
        if value:
            return value
        time.sleep(.04)
    raise AssertionError("timed out: " + message)


def state(run):
    return json.loads((ROOT / "runs" / run / "state.json").read_text())


def stable(run, status):
    try:
        job = json.loads((ROOT / "dispatch" / (run + ".json")).read_text())
        return state(run)["status"] == status and job["generation"] == job["completed"] and not job.get("owner")
    except FileNotFoundError:
        return False


def snapshot():
    return cli("run", "queue")


def stop(process, kill=False):
    if process.poll() is None:
        process.kill() if kill else process.terminate()
        process.wait(timeout=20)


def flow(name, nodes):
    return {"version": "3", "name": name, "entry": nodes[0]["id"], "inputs": {"id": {"type": "string", "required": True}}, "nodes": nodes, "edges": [{"from": n["id"], "to": nodes[i+1]["id"] if i+1 < len(nodes) else "$end"} for i, n in enumerate(nodes)]}


def node(name, mode=None):
    return {"id": name, "execution": {"candidates": [{"executor": "external" if mode is None else "fixture", "config": {} if mode is None else {"mode": mode}}]}}


try:
    proof["binary_sha256"]=hashlib.sha256(Path(BIN).read_bytes()).hexdigest()
    proof["version"]=cli("version")
    helper = ROOT / "executor.py"
    helper.write_text('''import json, os, pathlib, subprocess, sys, time
root=pathlib.Path(os.environ["AGENTFLOW_WORKSPACE"])
input=json.loads(pathlib.Path(os.environ["AGENTFLOW_INPUT_FILE"]).read_text())
config=json.loads(pathlib.Path(os.environ["AGENTFLOW_CONFIG_FILE"]).read_text())
mode=config["mode"];run=os.environ["AGENTFLOW_RUN_ID"]
if mode=="dispatch":
 for i in range(int(input.get("count",10))):
  subprocess.run([os.environ["AF_TEST_BIN"],"run","enqueue","child.json","--input",f"id={i}","--idempotency-key",f"{run}/{i}","--parent",run,"--parent-node",os.environ["AGENTFLOW_NODE_ID"],"--correlation","batch=demo","--correlation",f"item={i}"],cwd=root,check=True,stdout=subprocess.DEVNULL)
 if input.get("fail_once")=="yes" and not (root/("dispatched-"+run)).exists():
  (root/("dispatched-"+run)).touch();sys.exit(73)
else:
 with (root / (mode + "-starts")).open("a") as f: f.write(run+"\\n")
 if mode in ("phase1","phase2"):
  pid=root/(mode+"-"+run+".pid");pid.write_text(str(os.getpid()))
  while not (root/("gate-"+mode)).exists() and not (root/("gate-"+mode+"-"+run)).exists():time.sleep(.02)
  pid.unlink(missing_ok=True)
 if mode=="phase2" and input["id"]=="3":sys.exit(71)
pathlib.Path(os.environ["AGENTFLOW_RESULT_FILE"]).write_text(json.dumps(input))
''')
    (ROOT / "executors.json").write_text(json.dumps({"executors": {"fixture": {"kind": "command", "workspace": str(ROOT), "command": [sys.executable, str(helper)], "env": {"AF_TEST_BIN": BIN}}}}))
    (ROOT / "parent.json").write_text(json.dumps(flow("dispatcher", [node("dispatch", "dispatch")])))
    child_flow=flow("child", [node("seed", "seed"), node("phase1", "phase1"), node("human"), node("phase2", "phase2"), node("final")])
    child_flow["edges"][-2:] = [{"from":"phase2","to":"final","when":"nodes.phase2.result.id in ['0', '1']"}, {"from":"phase2","to":"$end"}, {"from":"final","to":"$end"}]
    (ROOT / "child.json").write_text(json.dumps(child_flow))
    parent = cli("run", "enqueue", "parent.json", "--input", "id=batch", "--idempotency-key", "parent")["run_id"]
    check(state(parent)["status"] == "queued", "enqueue persists and returns before executor starts")
    check(not (ROOT / "seed-starts").exists(), "queue survives producer exit without detached subprocess")
    worker = service("run", "worker", "--concurrency", "4")
    wait(lambda: stable(parent, "succeeded"), "parent dispatch complete")
    children = cli("run", "list", "--parent", parent)["runs"]
    ids = {state(r["run_id"])["inputs"]["id"]: r["run_id"] for r in children}
    check(len(ids) == 10, "one succeeded dispatcher has ten persistent nonempty child requests")
    first = wait(lambda: (s if (s := snapshot())["counts"]["active"] == 4 and s["counts"]["queued"] == 6 else None), "4 active / 6 queued")
    proof["initial_counts"] = first["counts"]
    cli("run", "worker", "--concurrency", "9", code=5)
    check(snapshot()["concurrency"] == 4, "duplicate worker rejected without changing admission budget")
    # Trigger server attaches to the same owner and cannot create a second budget.
    (ROOT / "triggers.json").write_text(json.dumps({"triggers": [{"id": "once", "enabled": True, "source": {"kind": "cron", "expression": "0 0 1 1 *"}, "target": {"workflow": "child.json"}, "inputs": {"id": "trigger"}}]}))
    scheduler = service("trigger", "serve", "--concurrency", "9")
    wait(lambda: (ROOT / "scheduler.lock").exists(), "scheduler starts")
    time.sleep(.15)
    check(scheduler.poll() is None and snapshot()["concurrency"] == 4, "trigger service shares the existing worker; no second concurrency budget")
    stop(scheduler)
    # Cancel a queued run before any executor was admitted.
    queued_id = cli("run", "start", "child.json", "--input", "id=cancel")["run_id"]
    resumed=cli("run","resume",queued_id)
    check(resumed["queued"] and state(queued_id)["status"]=="queued","managed start and resume cannot bypass the active budget")
    cli("run", "cancel", queued_id, "--async")
    active_child=next(ROOT.glob("phase1-*.pid")).name[len("phase1-"):-len(".pid")]
    (ROOT/("gate-phase1-"+active_child)).touch()
    wait(lambda: (s:=snapshot())["counts"]["waiting"]==1 and s["counts"]["active"]==4 and s["counts"]["queued"]==6,"one human wait admits next queued child")
    check(True,"one waiting child releases a slot while three siblings still execute")
    (ROOT / "gate-phase1").touch()
    wait(lambda: stable(queued_id, "cancelled"), "queued cancellation")
    check(queued_id not in (ROOT / "seed-starts").read_text().splitlines(), "cancelled queued run launches no executor")
    remaining = list(ids.values())
    wait(lambda: all(stable(rid, "waiting_input") for rid in remaining), "human waits release all active slots")
    check(snapshot()["counts"]["active"] == 0, "waiting_input releases run slots")
    check(state(parent)["status"] == "succeeded", "parent success stays independent of unfinished children")
    # Restart with all children waiting; waiting leases must survive unchanged.
    tokens = {rid: state(rid)["nodes"]["human"]["leaseToken"] for rid in ids.values()}
    stop(worker)
    worker = service("run", "worker", "--concurrency", "4")
    wait(lambda: snapshot()["worker_online"], "waiting restart")
    check(all(state(rid)["nodes"]["human"]["leaseToken"] == token for rid, token in tokens.items()), "restart preserves human leases and waiting attempts")
    # Ten simultaneous submits, without --async: omitting flag cannot bypass.
    with concurrent.futures.ThreadPoolExecutor(max_workers=10) as pool:
        futures = [pool.submit(cli, "run", "submit", rid, "human", "--token", tokens[rid], "--output", json.dumps({"id": key})) for key, rid in ids.items()]
        for future in futures:
            future.result()
    peak = wait(lambda: (s if (s := snapshot())["counts"]["active"] == 4 and s["counts"]["queued"] == 6 else None), "10 submissions still limited to 4")
    proof["after_submit_counts"] = peak["counts"]
    check(len(list(ROOT.glob("phase2-*.pid"))) <= 4, "ten concurrent human submissions obey the same four-run limit")
    wait(lambda:len(list(ROOT.glob("phase2-*.pid")))==4,"four executor processes started before crash")
    # Kill the owner during phase2, stop its synthetic orphan executors, restart.
    seed_counts = (ROOT / "seed-starts").read_text().splitlines()
    stop(worker, kill=True)
    for path in ROOT.glob("phase2-*.pid"):
        try:
            os.kill(int(path.read_text()), signal.SIGTERM)
        except ProcessLookupError:
            pass
        path.unlink(missing_ok=True)
    check(not snapshot()["worker_online"], "OS service ownership is released by abrupt worker exit")
    worker = service("run", "worker", "--concurrency", "4")
    wait(lambda: snapshot()["counts"]["active"] == 4, "orphaned running requests re-admitted")
    check((ROOT / "seed-starts").read_text().splitlines() == seed_counts, "restart does not replay completed upstream nodes")
    (ROOT / "gate-phase2").touch()
    wait(lambda: all(stable(rid, "failed" if key == "3" else ("waiting_input" if key in ("0","1") else "succeeded")) for key, rid in ids.items()), "all re-admitted children finish")
    check(state(parent)["status"] == "succeeded", "child failure never changes the succeeded parent")
    # Re-run a failed dispatcher after it already published its children.
    retry_parent=cli("run","enqueue","parent.json","--input","id=retry","--input","count=2","--input","fail_once=yes","--idempotency-key","retry-parent")["run_id"]
    wait(lambda:stable(retry_parent,"failed"),"dispatcher failure after enqueue")
    prior={r["run_id"] for r in cli("run","list","--parent",retry_parent)["runs"]}
    cli("run","retry",retry_parent,"--from","dispatch","--async")
    wait(lambda:stable(retry_parent,"succeeded"),"dispatcher retry")
    again={r["run_id"] for r in cli("run","list","--parent",retry_parent)["runs"]}
    check(len(prior)==2 and again==prior,"failed dispatcher retry creates no duplicate children")
    check({r["run_id"] for r in cli("run","list","--parent",parent)["runs"]}==set(ids.values()),"original parent still has exactly ten children")
    cli("run", "enqueue", "child.json", "--input", "id=changed", "--idempotency-key", f"{parent}/0", "--parent", parent, "--parent-node", "dispatch", "--correlation", "batch=demo", "--correlation", "item=0", code=5)
    check(len(cli("run", "list", "--parent", parent, "--correlation", "item=3")["runs"]) == 1, "parent and opaque correlation filters compose")
    check(not cli("run","list","--correlation","missing=")["runs"],"empty correlation filter requires the label to exist")
    # The final external node retains two human waits alongside terminal siblings.
    expected = {"waiting_input": 2, "failed": 1, "succeeded": 7}
    check(cli("run", "get", parent)["child_status_counts"] == expected, "CLI parent view reports two waiting, one failed, seven succeeded")
    stop(worker)
    worker = service("run", "worker", "--concurrency", "4")
    wait(lambda: snapshot()["worker_online"], "final restart")
    check(cli("run", "get", parent)["child_status_counts"] == expected, "parent links and child counts survive restart")
    with socket.socket() as sock:
        sock.bind(("127.0.0.1",0));port=sock.getsockname()[1]
    observer = service("web", "--port", str(port))
    url=f"http://127.0.0.1:{port}"
    def web_json(path):
        try:
            with urllib.request.urlopen(url+path,timeout=2) as response:return json.load(response)
        except OSError:return None
    parent_view=wait(lambda:web_json("/api/runs/"+parent),"Web parent view")
    check(parent_view["relations"]["child_status_counts"]==expected and len(parent_view["relations"]["children"])==10,"Web exposes child links and state counts")
    child_view=web_json("/api/runs/"+ids["0"])
    check(child_view["relations"]["parent_run_id"]==parent and child_view["relations"]["parent_node_id"]=="dispatch","Web child links back to parent and source node")
    check("function Relations" in urllib.request.urlopen(url).read().decode(),"Web UI includes parent and child navigation")
    proof["parent_run_id"] = parent
    proof["child_run_ids"] = ids
    proof["final_counts"] = snapshot()["counts"]
    proof["ok"] = True
except Exception as exc:
    proof["ok"] = False
    proof["error"] = str(exc)
    raise
finally:
    (ROOT / "gate-phase1").touch()
    (ROOT / "gate-phase2").touch()
    for proc in reversed(processes):
        stop(proc)
    for output in logs:
        output.close()
    destination = Path(os.environ.get("AGENTFLOW_QUEUE_REPORT", str(ROOT / "proof.json")))
    destination.write_text(json.dumps(proof, indent=2) + "\n")
    print(json.dumps(proof, indent=2))
