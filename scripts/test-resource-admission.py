#!/usr/bin/env python3
"""AF-05 contract test using actual CLI/worker processes and bounded fixtures."""
import concurrent.futures
import ctypes
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import time

BIN = str(Path(os.environ.get("AGENTFLOW_BIN", "bin/agentflow")).resolve())
ROOT = Path(os.environ.get("AGENTFLOW_RESOURCE_WORKSPACE") or tempfile.mkdtemp(prefix="af-resources-"))
ROOT.mkdir(parents=True, exist_ok=True)
workers, outputs = [], []
proof = {"workspace": str(ROOT), "checks": [], "peak_executors": {}, "models_verified": False}


def alive(pid):
    if os.name == "nt":
        k = ctypes.windll.kernel32
        k.OpenProcess.restype = ctypes.c_void_p
        handle = k.OpenProcess(0x1000, False, pid)
        if not handle:
            return False
        code = ctypes.c_ulong()
        result = k.GetExitCodeProcess(ctypes.c_void_p(handle), ctypes.byref(code))
        k.CloseHandle(ctypes.c_void_p(handle))
        return bool(result) and code.value == 259
    try:
        os.kill(pid, 0)
        return True
    except ProcessLookupError:
        return False


def check(value, message):
    assert value, message
    proof["checks"].append(message)


def cli(*args, code=0):
    p = subprocess.run([BIN, *args, "--format", "json"], cwd=ROOT, capture_output=True, text=True, timeout=20)
    assert p.returncode == code, (args, p.returncode, p.stdout, p.stderr)
    return json.loads(p.stdout or p.stderr)


def state(rid):
    return json.loads((ROOT / "runs" / rid / "state.json").read_text())


def current():
    result = []
    for p in (ROOT / "active").glob("*.json"):
        try:
            item = json.loads(p.read_text())
        except FileNotFoundError:
            continue
        if alive(item["pid"]):
            result.append(item)
    return result


def wait(test, name, phase="", limit=30):
    deadline = time.monotonic() + limit
    while time.monotonic() < deadline:
        if phase:
            n = len(current())
            proof["peak_executors"][phase] = max(proof["peak_executors"].get(phase, 0), n)
        value = test()
        if value:
            return value
        time.sleep(.025)
    raise AssertionError("timeout: " + name)


def stable(rid, status):
    s = state(rid)
    job = json.loads((ROOT / "dispatch" / (rid + ".json")).read_text())
    return s["status"] == status and job["generation"] == job["completed"] and not job.get("owner")


def worker():
    f = open(ROOT / ("worker-" + str(len(workers)) + ".log"), "w")
    outputs.append(f)
    p = subprocess.Popen([BIN, "run", "worker", "--concurrency", "4"], cwd=ROOT, stdout=f, stderr=f)
    workers.append(p)
    wait(lambda: cli("run", "queue")["worker_online"], "worker ready")
    return p


def work(name, keys, gate="", mode="work"):
    return {"id": name, "resources": [{"key": k, "units": 1} for k in keys], "execution": {"candidates": [{"executor": "fixture", "config": {"mode": mode, "gate": gate}}]}}


def external(name, keys=()):
    return {"id": name, "resources": [{"key": k, "units": 1} for k in keys], "execution": {"candidates": [{"executor": "external"}]}}


def graph(name, nodes, edges=None):
    if edges is None:
        edges = [{"from": n["id"], "to": nodes[i + 1]["id"] if i + 1 < len(nodes) else "$end"} for i, n in enumerate(nodes)]
    (ROOT / (name + ".json")).write_text(json.dumps({"version": "3", "name": name, "entry": nodes[0]["id"], "nodes": nodes, "edges": edges}))


def enqueue(name, label):
    return cli("run", "enqueue", name + ".json", "--input", "id=" + label)["run_id"]


def gate(name):
    (ROOT / ("gate-" + name)).touch()


try:
    for d in ("active", "started"):
        (ROOT / d).mkdir(exist_ok=True)
    helper = ROOT / "executor.py"
    helper.write_text('''import json, os, pathlib, sys, time
root=pathlib.Path(os.environ["AGENTFLOW_WORKSPACE"])
config=json.loads(pathlib.Path(os.environ["AGENTFLOW_CONFIG_FILE"]).read_text())
data=json.loads(pathlib.Path(os.environ["AGENTFLOW_INPUT_FILE"]).read_text())
mode=config["mode"]
if mode!="fast":
 token=os.environ["AGENTFLOW_NODE_TOKEN"]
 record={"pid":os.getpid(),"run":os.environ["AGENTFLOW_RUN_ID"],"node":os.environ["AGENTFLOW_NODE_ID"],"token":token,"claim":os.environ.get("AGENTFLOW_RESOURCE_CLAIM"),"mode":mode}
 payload=json.dumps(record)
 def publish_marker(path):
  temporary=path.with_suffix(".tmp-"+str(os.getpid()))
  temporary.write_text(payload)
  temporary.replace(path)
 publish_marker(root/"started"/(token+".json"))
 active=root/"active"/(token+".json");publish_marker(active)
 try:
  if mode=="fail" or (mode=="retryable" and not (root/"allow-retry").exists()):sys.exit(71)
  name=config.get("gate","")
  while name and not (root/("gate-"+name)).exists() and not (root/"cleanup").exists():
   if mode=="orphan":(root/("writing-"+token)).write_text(str(time.time_ns()))
   time.sleep(.02)
 finally:active.unlink(missing_ok=True)
pathlib.Path(os.environ["AGENTFLOW_RESULT_FILE"]).write_text(json.dumps(data))
''')
    entry = {"kind": "command", "workspace": str(ROOT), "command": [sys.executable, str(helper)]}
    (ROOT / "executors.json").write_text(json.dumps({"executors": {"fixture": entry, "backup": entry}}))
    (ROOT / "resources.json").write_text(json.dumps({"resources": {"A": {"capacity": 1}, "B": {"capacity": 1}, "pool": {"capacity": 2}}}))
    graph("ab", [work("work", ["A", "B"], "ab")])
    graph("ba", [work("work", ["B", "A"], "ab")])
    first = enqueue("ab", "first")
    w = worker()
    wait(lambda: len(current()) == 1, "first writer entered", "ab")
    cancelled, reverse = enqueue("ba", "cancel"), enqueue("ba", "reverse")
    wait(lambda: stable(cancelled, "waiting_resource") and stable(reverse, "waiting_resource"), "resource wait", "ab")
    check(state(cancelled)["nodes"]["work"].get("attempts",0) == 0, "05-01 capacity wait does not allocate an attempt")
    resources = cli("resource", "list")
    check(resources["used"]["A"] == resources["used"]["B"] == 1 and len([c for c in resources["claims"] if c["status"] == "active"]) == 1, "05-03 opposite multi-key requests allocate all or none")
    cli("run", "cancel", cancelled, "--async")
    wait(lambda: stable(cancelled, "cancelled"), "waiting cancellation", "ab")
    check(not any(json.loads(p.read_text())["run"] == cancelled for p in (ROOT / "started").glob("*.json")), "05-05 cancelling a resource waiter launches no process")
    gate("ab")
    wait(lambda: stable(first, "succeeded") and stable(reverse, "succeeded"), "opposite order completes", "ab")
    check(proof["peak_executors"]["ab"] == 1, "05-01 and 05-03 writer cap one and no multi-key deadlock")

    root = work("fan", [], mode="fast")
    root["dispatch"] = "all"
    branches = [work(k, ["pool"], "pool") for k in ("x", "y", "z")]
    graph("fan", [root, *branches], [{"from": "fan", "to": n["id"]} for n in branches] + [{"from": n["id"], "to": "$end"} for n in branches])
    graph("pool", [work("work", ["pool"], "pool")])
    runs = [enqueue("fan", "parallel"), *[enqueue("pool", str(i)) for i in range(9)]]
    wait(lambda: len(current()) == 2 and any(state(r)["status"] == "waiting_resource" for r in runs), "pool full", "pool")
    waiters = [r for r in runs if state(r)["status"] == "waiting_resource"]
    with concurrent.futures.ThreadPoolExecutor(max_workers=10) as pool:
        for future in [pool.submit(cli, "run", "resume", r) for r in waiters]:
            future.result()
    check(len(current()) == 2 and cli("resource", "list")["used"]["pool"] == 2, "05-02 and 05-04 ten runs plus graph parallelism and resumes obey executor quota two")
    gate("pool")
    wait(lambda: all(stable(r, "succeeded") for r in runs), "pool drains", "pool")
    check(proof["peak_executors"]["pool"] == 2, "05-02 measured actual executor peak remains two")

    n = work("work", ["A"], mode="fail")
    n["execution"]["candidates"].append({"executor": "backup", "config": {"mode": "work", "gate": "fallback"}})
    n["execution"]["fallback"] = {"when": "failure.kind == 'exit_code'"}
    graph("fallback", [n])
    fallback = enqueue("fallback", "fallback")
    wait(lambda: len(current()) == 1, "backup entered", "fallback")
    gate("fallback")
    wait(lambda: stable(fallback, "succeeded"), "backup complete", "fallback")
    attempts = state(fallback)["nodes"]["work"]["attemptSequence"]
    check(len(attempts) == 2 and len({a["resourceClaimId"] for a in attempts}) == 2, "05-05 fallback acquires a fresh claim after old process exit")

    graph("control", [external("human", ["A"]), work("work", ["A"], "control")])
    graph("retry", [work("work", ["A"], "control", "retryable")])
    controls = [enqueue("control", str(i)) for i in range(2)]
    failed = enqueue("retry", "retry")
    wait(lambda: all(stable(r, "waiting_input") for r in controls) and stable(failed, "failed"), "human and failed fixtures")
    check(cli("resource", "list")["used"].get("A", 0) == 0, "05-07 external waiting does not retain writer quota")
    (ROOT / "allow-retry").touch()
    with concurrent.futures.ThreadPoolExecutor(max_workers=4) as pool:
        pending = [pool.submit(cli, "run", "submit", r, "human", "--output", json.dumps({"id": r})) for r in controls]
        pending.append(pool.submit(cli, "run", "retry", failed))
        for f in pending:
            f.result()
    wait(lambda: len(current()) == 1 and sum(state(r)["status"] == "waiting_resource" for r in controls + [failed]) == 2, "submitted and retried quota", "control")
    for r in controls + [failed]:
        cli("run", "resume", r)
    check(len(current()) == 1 and cli("resource", "list")["used"]["A"] == 1, "05-04 concurrent submit retry and resume cannot bypass resource admission")
    proof["peak_executors"]["control"]=max(proof["peak_executors"]["control"],len(current()))
    gate("control")
    wait(lambda: all(stable(r, "succeeded") for r in controls + [failed]), "controls drain", "control")

    graph("active-cancel", [work("work", ["A"], "active-cancel")])
    active_cancel = enqueue("active-cancel", "active-cancel")
    wait(lambda: any(x["run"] == active_cancel for x in current()), "cancellable writer entered")
    cancel_contender = enqueue("active-cancel", "cancel-contender")
    wait(lambda: stable(cancel_contender, "waiting_resource"), "cancel contender waits")
    cli("run", "cancel", active_cancel, "--async")
    # Cancellation is applied at the process boundary. It cannot free the
    # still-running writer's claim while a contender can enter.
    time.sleep(.15)
    check(any(x["run"] == active_cancel for x in current()) and len(current()) == 1
          and stable(cancel_contender, "waiting_resource") and cli("resource", "list")["used"]["A"] == 1,
          "05-05 cancelling a running writer retains quota until its process exits")
    gate("active-cancel")
    wait(lambda: stable(active_cancel, "cancelled") and stable(cancel_contender, "succeeded"), "running cancellation drains safely")

    graph("orphan", [work("work", ["A"], "orphan", "orphan")])
    orphan = enqueue("orphan", "orphan")
    old = wait(lambda: next((x for x in current() if x["run"] == orphan), None), "orphan writer starts")
    wait(lambda: any(c.get("processPid") == old["pid"] for c in cli("resource", "list")["claims"]), "PID checkpoint")
    w.kill();w.wait(timeout=10)
    counter = ROOT / ("writing-" + old["token"])
    wait(lambda:counter.exists() and counter.read_text(),"synthetic writer counter")
    before = counter.read_text();time.sleep(.08)
    check(alive(old["pid"]) and counter.read_text() != before, "05-06 synthetic child survives worker kill and continues writing")
    w = worker()
    contender = enqueue("ab", "after-crash")
    wait(lambda: stable(orphan, "waiting_resource") and stable(contender, "waiting_resource"), "orphan quarantine")
    claims = cli("resource", "list")["claims"]
    quarantined = next(c for c in claims if c["id"] == old["claim"])
    check(quarantined["status"] == "orphaned" and len(current()) == 1, "05-06 new owner cannot steal orphaned writer quota")
    cli("resource", "resolve", old["claim"], "--confirm-stopped", "--reason", "not stopped", code=5)
    os.kill(old["pid"], signal.SIGTERM)
    wait(lambda: not alive(old["pid"]), "old synthetic process stopped")
    (ROOT / "active" / (old["token"] + ".json")).unlink(missing_ok=True)
    cli("resource", "resolve", old["claim"], "--confirm-stopped", "--reason", "synthetic PID terminated, no descendants, writing stopped")
    gate("orphan")
    wait(lambda: stable(orphan, "succeeded") and stable(contender, "succeeded"), "safe recovery")
    check(cli("resource", "list")["used"].get("A", 0) == 0, "05-06 explicit verified cleanup permits recovery with no residual allocation")
    proof["ok"] = True
except Exception as exc:
    proof["ok"], proof["error"] = False, str(exc)
    raise
finally:
    (ROOT / "cleanup").touch()
    for p in workers:
        if p.poll() is None:
            p.terminate()
            p.wait(timeout=15)
    for item in current():
        try:
            os.kill(item["pid"], signal.SIGTERM)
        except ProcessLookupError:
            pass
    for f in outputs:
        f.close()
    report = Path(os.environ.get("AGENTFLOW_RESOURCE_REPORT", str(ROOT / "proof.json")))
    report.write_text(json.dumps(proof, indent=2) + "\n")
    print(json.dumps(proof, indent=2))
