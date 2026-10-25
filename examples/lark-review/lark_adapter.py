#!/usr/bin/env python3
"""Durable external-node adapter. Only this program knows about Feishu.

One service per bot profile/workspace, many waiting nodes. Python 3 stdlib,
macOS/Linux. No credentials are stored here. See README.md for boundaries.
"""
import argparse
from contextlib import contextmanager
from datetime import datetime, timezone
import fcntl
import json
import os
from pathlib import Path
import select
import sqlite3
import subprocess
import sys
import threading
import time
import uuid


def encode(value):
    return json.dumps(value, ensure_ascii=False, separators=(",", ":"))


def now():
    return datetime.now(timezone.utc).isoformat(timespec="seconds")


def log(message):
    print(f"{now()} {message}", file=sys.stderr, flush=True)


def plain(text):
    return {"tag": "plain_text", "content": text}


def block(title, content, background="default"):
    return {"tag": "column_set", "flex_mode": "none", "columns": [{
        "tag": "column", "width": "weighted", "weight": 1,
        "background_style": background, "padding": "12px", "vertical_spacing": "8px",
        "elements": [{"tag": "markdown", "content": f"**{title}**"},
                     {"tag": "markdown", "content": content}]}]}


def validate_config(data):
    if not isinstance(data, dict):
        raise ValueError("config must be an object")
    unknown = set(data) - {"profile", "recipient", "title", "subtitle", "hint", "sections", "actions"}
    if unknown:
        raise ValueError(f"unknown config fields: {sorted(unknown)}")
    for key in ("recipient", "title"):
        if not isinstance(data.get(key), str) or not data[key].strip():
            raise ValueError(f"config.{key} must be a non-empty string")
    for key in ("profile", "subtitle", "hint"):
        if key in data and (not isinstance(data[key], str) or (key == "profile" and not data[key].strip())):
            raise ValueError(f"config.{key} must be a string (profile must be non-empty)")
    sections, actions = data.get("sections"), data.get("actions")
    if not isinstance(sections, list) or not 1 <= len(sections) <= 3:
        raise ValueError("sections must contain 1 to 3 title/content objects")
    if any(not isinstance(s, dict) or not isinstance(s.get("title"), str)
           or not isinstance(s.get("content"), str) or not s["title"].strip() or not s["content"].strip()
           or set(s) - {"title", "content"} for s in sections):
        raise ValueError("each section must contain only non-empty title and content strings")
    if not isinstance(actions, list) or not 1 <= len(actions) <= 3:
        raise ValueError("actions must contain 1 to 3 buttons")
    styles = {"default", "primary_filled", "danger", "danger_filled", "text"}
    colors = {"blue", "green", "orange", "red", "grey", "default"}
    for action in actions:
        if not isinstance(action, dict) or not isinstance(action.get("label"), str) or not isinstance(action.get("output"), dict):
            raise ValueError("each action requires label and JSON object output")
        if not action["label"].strip() or len(action["label"]) > 100:
            raise ValueError("action label must contain 1 to 100 characters")
        if set(action) - {"label", "output", "style", "color", "confirm", "confirmation"}:
            raise ValueError("unknown action fields; see config.schema.json")
        for key in ("confirm", "confirmation"):
            if key in action and (not isinstance(action[key], str) or not action[key].strip()):
                raise ValueError(f"action.{key} must be a non-empty string")
        if action.get("style", "default") not in styles or action.get("color", "blue") not in colors:
            raise ValueError("unsupported button style or result color")


def card_for(record):
    data, phase = record["config"], record["phase"]
    action = data["actions"][record["selection"]] if "selection" in record else None
    color, badge, feedback = "blue", "待处理", data.get("hint", "请选择一项，结果将自动提交给工作流。")
    if phase == "recorded":
        color, badge, feedback = "orange", "已记录", f"已记录：{action['label']}。正在提交，暂时无需再次操作。"
    elif phase == "accepted":
        color, badge = action.get("color", "blue"), "已处理"
        feedback = action.get("confirmation", f"已选择：{action['label']}。工作流已接收结果。")
    elif phase == "expired":
        color, badge, feedback = "grey", "已失效", "该次请求已结束或已被其他操作处理，请查看最新消息。"
    elements = [block(s["title"], s["content"], "grey-50" if i == 0 else "default")
                for i, s in enumerate(data["sections"])]
    if phase == "waiting":
        columns = []
        for index, item in enumerate(data["actions"]):
            button = {"tag": "button", "text": plain(item["label"]),
                      "type": item.get("style", "default"), "width": "fill", "size": "medium",
                      "behaviors": [{"type": "callback", "value": {
                          "invocation": record["token"], "actionIndex": index}}]}
            if item.get("confirm"):
                button["confirm"] = {"title": plain(item["label"]), "text": plain(item["confirm"])}
            columns.append({"tag": "column", "width": "weighted", "weight": 1, "elements": [button]})
        elements.append({"tag": "column_set", "flex_mode": "none", "horizontal_spacing": "8px", "columns": columns})
    else:
        elements.append(block(badge, feedback, color + "-50" if color != "default" else "grey-50"))
    footer = feedback if phase == "waiting" else f"记录时间：{record.get('selected_at', record['created_at'])}"
    elements.append({"tag": "markdown", "content": f"<font color='grey'>{footer}</font>", "text_size": "notation"})
    return {"schema": "2.0", "config": {"update_multi": True, "width_mode": "default", "enable_forward": False,
            "summary": {"content": f"[{badge}] {data['title']}"}},
        "header": {"title": plain(data["title"]), "subtitle": plain(data.get("subtitle", "AgentFlow · 人工确认")),
                   "icon": {"tag": "standard_icon", "token": "approval_colorful"},
                   "template": color, "text_tag_list": [{"tag": "text_tag", "text": plain(badge), "color": color if color not in {"grey", "default"} else "neutral"}]},
        "body": {"direction": "vertical", "padding": "12px", "vertical_spacing": "12px", "elements": elements}}


class Journal:
    def __init__(self, path):
        self.path = Path(path)
        self.path.parent.mkdir(parents=True, exist_ok=True)
        with self.connect() as db:
            db.execute("PRAGMA journal_mode=WAL")
            db.execute("CREATE TABLE IF NOT EXISTS requests (token TEXT PRIMARY KEY, body TEXT NOT NULL)")
            db.execute("CREATE TABLE IF NOT EXISTS inbox (id INTEGER PRIMARY KEY, body TEXT NOT NULL)")

    @contextmanager
    def connect(self):
        db = sqlite3.connect(self.path, timeout=10)
        try:
            db.execute("PRAGMA synchronous=FULL")
            with db:
                yield db
        finally:
            db.close()

    def save(self, record):
        with self.connect() as db:
            db.execute("INSERT OR REPLACE INTO requests VALUES (?,?)", (record["token"], encode(record)))

    def records(self):
        with self.connect() as db:
            return [json.loads(row[0]) for row in db.execute("SELECT body FROM requests")]

    def receive(self, event):
        with self.connect() as db:
            db.execute("INSERT INTO inbox(body) VALUES (?)", (encode(event),))

    def drain(self):
        with self.connect() as db:
            return [(row[0], json.loads(row[1])) for row in db.execute("SELECT id,body FROM inbox ORDER BY id LIMIT 100")]

    def ack(self, event_id):
        with self.connect() as db:
            db.execute("DELETE FROM inbox WHERE id=?", (event_id,))


@contextmanager
def singleton(path):
    with open(str(path) + ".lock", "a") as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise RuntimeError("adapter is already running for this state file") from None
        yield


class Adapter:
    def __init__(self, root, state, profile, executor="lark-review", agentflow="agentflow", lark="lark-cli"):
        self.root, self.profile, self.executor = Path(root).resolve(), profile, executor
        self.agentflow, self.lark = agentflow, lark
        self.journal = Journal(state)
        self.jobs = {}
        self.profile_mismatches = set()
        self.ready, self.stop = threading.Event(), threading.Event()
        self.identity = ["--profile", profile, "--as", "bot"]

    def api(self, arguments):
        result = subprocess.run([self.lark, *arguments, *self.identity], cwd=self.root,
                                text=True, capture_output=True, timeout=20)
        if result.returncode:
            raise RuntimeError(result.stderr.strip() or result.stdout.strip())
        payload = json.loads(result.stdout)
        if payload.get("ok") is not True:
            raise RuntimeError(encode(payload))
        return payload.get("data", {})

    def state(self, run_id):
        return json.loads((self.root / "runs" / run_id / "state.json").read_text())

    def validate_outputs(self, run_id, node_id, token, config):
        for action in config["actions"]:
            check = subprocess.run([self.agentflow, "run", "submit", run_id, node_id,
                "--token", token, "--output", encode(action["output"]), "--dry-run", "--format", "json"],
                cwd=self.root, text=True, capture_output=True, timeout=10)
            if check.returncode != 10:
                raise ValueError(f"action {action['label']!r} failed submission preflight: {check.stderr or check.stdout}")

    def discover(self):
        known = {r["token"] for r in self.journal.records()}
        for path in (self.root / "runs").glob("*/state.json"):
            try:
                state = json.loads(path.read_text())
                if state["status"] in {"cancelled", "failed", "succeeded"}:
                    continue
                for node_id, node in state["nodes"].items():
                    token = node.get("leaseToken")
                    if node["status"] != "waiting_input" or node.get("selectedExecutor") != self.executor or not token or token in known:
                        continue
                    artifact = (path.parent / node["artifacts"]["config"]).resolve()
                    if not artifact.is_relative_to(path.parent.resolve()):
                        raise ValueError("config artifact escaped run directory")
                    config = json.loads(artifact.read_text())
                    if config.get("profile", self.profile) != self.profile:
                        if token not in self.profile_mismatches:
                            log(f"profile mismatch for {state['runId']}/{node_id}: config.profile={config.get('profile')!r}, service --profile={self.profile!r}; request not sent")
                            self.profile_mismatches.add(token)
                        continue
                    validate_config(config)
                    self.validate_outputs(state["runId"], node_id, token, config)
                    self.journal.save({"token": token, "run_id": state["runId"], "node_id": node_id,
                        "config": config, "phase": "waiting", "created_at": now(),
                        "send_key": str(uuid.uuid5(uuid.NAMESPACE_URL, str(self.root) + token))})
            except (OSError, ValueError, KeyError, TypeError, subprocess.TimeoutExpired) as exc:
                log(f"discovery failed for {path.name}: {exc}")

    def current(self, record):
        state = self.state(record["run_id"])
        node = state["nodes"].get(record["node_id"], {})
        return state, node

    def event(self, event):
        if not isinstance(event, dict) or event.get("type") != "card.action.trigger" or event.get("action_tag") != "button":
            return
        value = event.get("action_value")
        if isinstance(value, str):
            try:
                value = json.loads(value)
            except ValueError:
                return
        if not isinstance(value, dict):
            return
        for record in self.journal.records():
            if record["token"] != value.get("invocation") or record["phase"] != "waiting":
                continue
            if event.get("message_id") != record.get("message_id") or event.get("operator_id") != record["config"]["recipient"]:
                return
            index = value.get("actionIndex")
            if type(index) is not int or not 0 <= index < len(record["config"]["actions"]):
                return
            state, node = self.current(record)
            if state["status"] in {"cancelled", "failed", "succeeded"} or node.get("status") != "waiting_input" or node.get("leaseToken") != record["token"]:
                return
            record.update(phase="recorded", selection=index, selected_at=now(),
                          operator_id=event["operator_id"], event_id=event.get("event_id"))
            self.journal.save(record)  # Commit intent BEFORE submit or cosmetic update.
            log(f"selection persisted: {record['run_id']}/{record['node_id']}")
            return

    def launch(self, record, command):
        run_id = record["run_id"]
        existing = self.jobs.get(run_id)
        if existing and existing.poll() is None:
            return
        if time.time() < record.get("retry_after", 0):
            return
        record["retry_after"] = time.time() + 5
        self.journal.save(record)
        path = self.journal.path.parent / (run_id + ".log")
        with path.open("a") as out:
            self.jobs[run_id] = subprocess.Popen([self.agentflow, "run", *command, "--format", "json"],
                cwd=self.root, stdin=subprocess.DEVNULL, stdout=out, stderr=out, start_new_session=True)

    def reconcile(self, record):
        state, node = self.current(record)
        visits = [node] + [h["state"] for h in state.get("history", []) if h["nodeId"] == record["node_id"]]
        completed = next((v for v in visits if v.get("leaseToken") == record["token"] and v.get("status") == "succeeded"), None)
        if record["phase"] not in {"accepted", "expired"}:
            if completed:
                output = record["config"]["actions"][record["selection"]]["output"] if "selection" in record else None
                record["phase"] = "accepted" if "selection" in record and completed.get("result") == output else "expired"
            elif state["status"] in {"cancelled", "failed", "succeeded"} or node.get("leaseToken") != record["token"]:
                record["phase"] = "expired"
            self.journal.save(record)
        if record["phase"] == "recorded":
            self.launch(record, ["submit", record["run_id"], record["node_id"], "--token", record["token"],
                        "--output", encode(record["config"]["actions"][record["selection"]]["output"])])
        elif record["phase"] == "accepted" and state["status"] == "running":
            # Recover the crash window after acceptance but before traversing edges.
            # AgentFlow's own run lock serializes this with a surviving worker.
            self.launch(record, ["resume", record["run_id"]])
        if not self.ready.is_set() or time.time() < record.get("card_retry_after", 0):
            return
        if record["phase"] == "expired" and not record.get("message_id"):
            return
        if record.get("painted") == record["phase"]:
            return
        try:
            card = card_for(record)
            if len(encode(card).encode()) > 28000:
                raise ValueError("card exceeds safe 28 KB payload size; shorten sections")
            if not record.get("message_id"):
                sent = self.api(["im", "+messages-send", "--user-id", record["config"]["recipient"],
                    "--msg-type", "interactive", "--content", encode(card), "--idempotency-key", record["send_key"]])
                record["message_id"] = sent["message_id"]
            else:
                self.api(["im", "messages", "patch", "--message-id", record["message_id"], "--data", encode({"content": encode(card)})])
            record["painted"] = record["phase"]
            record.pop("card_error", None)
        except Exception as exc:
            record["card_error"] = str(exc)
            record["card_retry_after"] = time.time() + 15
            log(f"card delivery/update failed: {exc}")
        self.journal.save(record)

    def tick(self):
        for run_id, job in list(self.jobs.items()):
            if job.poll() is not None:
                del self.jobs[run_id]
        self.discover()
        for event_id, event in self.journal.drain():
            self.event(event)
            self.journal.ack(event_id)
        for record in self.journal.records():
            try:
                self.reconcile(record)
            except (OSError, ValueError, KeyError, TypeError) as exc:
                log(f"reconcile failed for {record['run_id']}: {exc}")

    def listen(self):
        backoff = 1
        while not self.stop.is_set():
            proc = None
            try:
                proc = subprocess.Popen([self.lark, "event", "consume", "card.action.trigger", *self.identity],
                    cwd=self.root, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, bufsize=0)
                buffers = {proc.stdout: b"", proc.stderr: b""}
                deadline = time.monotonic() + 20
                while not self.stop.is_set():
                    streams, _, _ = select.select(list(buffers), [], [], 0.5)
                    for stream in streams:
                        chunk = os.read(stream.fileno(), 65536)
                        if not chunk:
                            raise RuntimeError("Lark event consumer disconnected")
                        buffers[stream] += chunk
                        while b"\n" in buffers[stream]:
                            line, buffers[stream] = buffers[stream].split(b"\n", 1)
                            if stream is proc.stderr:
                                text = line.decode(errors="replace")
                                log(text)
                                if "[event] ready event_key=card.action.trigger" in text:
                                    self.ready.set()
                                    backoff = 1
                            else:
                                try:
                                    self.journal.receive(json.loads(line))
                                except ValueError:
                                    log("ignored malformed event JSON")
                    if not self.ready.is_set() and time.monotonic() > deadline:
                        raise RuntimeError("Lark event consumer readiness timeout")
            except Exception as exc:
                log(f"listener reconnecting: {exc}")
            finally:
                self.ready.clear()
                if proc:
                    proc.stdin.close()
                    if proc.poll() is None:
                        proc.terminate()
                    try:
                        proc.wait(timeout=5)
                    except subprocess.TimeoutExpired:
                        proc.kill()
                        proc.wait()
                    proc.stdout.close()
                    proc.stderr.close()
            self.stop.wait(backoff)
            backoff = min(backoff * 2, 30)

    def serve(self):
        with singleton(self.journal.path):
            listener = threading.Thread(target=self.listen, daemon=True)
            listener.start()
            try:
                while True:
                    self.tick()
                    time.sleep(1)
            finally:
                self.stop.set()
                listener.join(timeout=7)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--workspace", default=".")
    parser.add_argument("--profile", required=True)
    parser.add_argument("--executor", default="lark-review")
    parser.add_argument("--agentflow", default="agentflow")
    parser.add_argument("--lark", default="lark-cli")
    parser.add_argument("--state", help="Journal path; defaults to workspace/executors/programs/lark/state.sqlite3")
    parser.add_argument("--check-config", metavar="FILE", help="Validate config shape and profile locally; no network, messages, or journal writes")
    args = parser.parse_args()
    if args.check_config:
        try:
            config = json.loads(Path(args.check_config).read_text())
            validate_config(config)
            if config.get("profile", args.profile) != args.profile:
                raise ValueError("config.profile must match service --profile")
        except (OSError, ValueError, TypeError) as exc:
            print(encode({"ok": False, "error": str(exc)}))
            return 2
        print(encode({"ok": True, "profile": args.profile, "network_verified": False,
                      "output_schema_verified": False, "templates_rendered": False}))
        return 0
    root = Path(args.workspace).resolve()
    state = args.state or root / "executors/programs/lark/state.sqlite3"
    try:
        Adapter(root, state, args.profile, args.executor, args.agentflow, args.lark).serve()
    except KeyboardInterrupt:
        pass


if __name__ == "__main__":
    sys.exit(main())
