#!/usr/bin/env python3
"""Durable external-node adapter. Only this program knows about Feishu.

One service per bot profile/workspace, many waiting nodes. Python 3 stdlib,
macOS/Linux. No credentials are stored here. See README.md for boundaries.
"""
import argparse
from contextlib import contextmanager
from datetime import datetime, timezone
import copy
import fcntl
import json
import os
from pathlib import Path
import re
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


def timestamp(epoch):
    try:
        return datetime.fromtimestamp(epoch, timezone.utc).isoformat(timespec="microseconds")
    except (ValueError, OverflowError, OSError):
        raise ValueError("timestamp is outside the supported datetime range") from None


def epoch(value):
    if not isinstance(value, str):
        raise ValueError("deadline.at must be an ISO 8601 timestamp with timezone")
    date = datetime.fromisoformat(value.replace("Z", "+00:00"))
    if date.tzinfo is None:
        raise ValueError("deadline.at must include timezone")
    return date.timestamp()


def form_fields(config):
    return {f["name"]: f for f in config.get("form", {}).get("fields", [])}


def form_button(token, index):
    return f"af_{token}_{index}"


def merge_form(config, index, raw):
    if isinstance(raw, str):
        try:
            raw = json.loads(raw) if raw else {}
        except ValueError:
            raise ValueError("form_invalid: form_value must encode a JSON object") from None
    if not isinstance(raw, dict):
        raise ValueError("form_type: form_value must be an object")
    fields = form_fields(config)
    if set(raw) - set(fields):
        raise ValueError("form_unknown: unexpected form field")
    values = {name: raw.get(name, "") for name in fields}
    for name, value in values.items():
        if not isinstance(value, str):
            raise ValueError(f"form_type: {name} must be text")
        if len(value) > fields[name]["maxLength"]:
            raise ValueError(f"form_length: {name} exceeds maxLength")
    action = config["actions"][index]
    for name in action.get("requiredFormFields", []):
        if not values[name].strip():
            raise ValueError(f"form_required: {name} must not be blank")
    output = copy.deepcopy(action["output"])
    output.update({key: values[name] for key, name in action.get("formOutput", {}).items()})
    return values, output


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
    unknown = set(data) - {"profile", "recipient", "title", "subtitle", "hint", "sections", "actions", "form", "deadline", "preSubmit"}
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
    form = data.get("form", {"fields": []})
    if not isinstance(form, dict) or set(form) != {"fields"} or not isinstance(form["fields"], list) or len(form["fields"]) > 8:
        raise ValueError("form requires only fields (at most eight text fields)")
    names = set()
    for field in form["fields"]:
        if not isinstance(field, dict) or set(field) != {"name", "type", "label", "maxLength"}:
            raise ValueError("form field requires name, type, label and maxLength")
        name = field["name"]
        if not isinstance(name, str) or not re.fullmatch(r"[A-Za-z][A-Za-z0-9_]{0,63}", name) or name.startswith("af_") or name in names:
            raise ValueError("form field names must be unique identifiers outside the af_ namespace")
        names.add(name)
        if field["type"] not in {"text", "multiline"} or not isinstance(field["label"], str) or not field["label"].strip():
            raise ValueError("form field requires text/multiline type and non-empty label")
        if type(field["maxLength"]) is not int or not 1 <= field["maxLength"] <= 1000:
            raise ValueError("form maxLength must be 1..1000 (Card 2.0 input limit)")
    for action in actions:
        if not isinstance(action, dict) or not isinstance(action.get("label"), str) or not isinstance(action.get("output"), dict):
            raise ValueError("each action requires label and JSON object output")
        if not action["label"].strip() or len(action["label"]) > 100:
            raise ValueError("action label must contain 1 to 100 characters")
        if set(action) - {"label", "output", "style", "color", "confirm", "confirmation", "formOutput", "requiredFormFields"}:
            raise ValueError("unknown action fields; see config.schema.json")
        for key in ("confirm", "confirmation"):
            if key in action and (not isinstance(action[key], str) or not action[key].strip()):
                raise ValueError(f"action.{key} must be a non-empty string")
        if action.get("style", "default") not in styles or action.get("color", "blue") not in colors:
            raise ValueError("unsupported button style or result color")
        mapping, required = action.get("formOutput", {}), action.get("requiredFormFields", [])
        if not isinstance(mapping, dict) or any(not isinstance(k, str) or not k.strip() or not isinstance(v, str) or v not in names for k, v in mapping.items()):
            raise ValueError("formOutput must map output keys to known form field names")
        if set(mapping) & set(action["output"]):
            raise ValueError("formOutput must not override fixed output keys")
        if not isinstance(required, list) or any(not isinstance(v, str) or v not in names for v in required) or len(set(required)) != len(required):
            raise ValueError("requiredFormFields must list unique known fields")
    if "deadline" in data:
        deadline = data["deadline"]
        if not isinstance(deadline, dict) or set(deadline) not in ({"at", "output"}, {"afterSeconds", "output"}) or not isinstance(deadline["output"], dict):
            raise ValueError("deadline requires at OR afterSeconds, and explicit timeout output")
        if "at" in deadline:
            epoch(deadline["at"])
        elif type(deadline["afterSeconds"]) is not int or deadline["afterSeconds"] < 1:
            raise ValueError("deadline.afterSeconds must be a positive integer")
    if "preSubmit" in data:
        hook = data["preSubmit"]
        if not isinstance(hook, dict) or set(hook) != {"command", "timeoutSeconds"} or not isinstance(hook["command"], list) or not hook["command"] or any(not isinstance(v, str) or not v for v in hook["command"]):
            raise ValueError("preSubmit requires command argv and timeoutSeconds")
        if type(hook["timeoutSeconds"]) is not int or not 1 <= hook["timeoutSeconds"] <= 60:
            raise ValueError("preSubmit.timeoutSeconds must be 1..60")


def card_for(record):
    data, phase = record["config"], record["phase"]
    action = data["actions"][record["selection"]] if "selection" in record else None
    color, badge, feedback = "blue", "待处理", data.get("hint", "请选择一项，结果将自动提交给工作流。")
    if phase == "recorded":
        label = action["label"] if action else "已到审核期限"
        color, badge, feedback = "orange", "已保存", f"{label}。选择已持久化，等待工作流接收。"
    elif phase == "accepted":
        color, badge = (action or {}).get("color", "blue"), "已接收"
        feedback = (action or {}).get("confirmation", "工作流已接收本次结果，后续节点未必已完成。")
        if record.get("workflow_status") == "succeeded":
            badge, feedback = "后续已完成", "本次选择已接收，且已观察到整个 run 成功结束。"
        elif record.get("workflow_status") in {"failed", "cancelled"}:
            color, badge, feedback = "orange", "后续未完成", "本次选择已接收；后续 run 已失败或取消，请查看运行记录。"
    elif phase == "blocked":
        color, badge, feedback = "red", "提交被拦截", "选择已保存，但当前校验未通过。请核对 adapter 记录和引擎接收状态；不会自动重新选择。"
    elif phase == "expired":
        color, badge, feedback = "grey", "已失效", "该次请求已结束或已被其他操作处理，请查看最新消息。"
    if record.get("source") == "timeout" and phase in {"recorded", "accepted", "blocked"}:
        feedback = "审核期限已到，使用显式配置的超时结果。" + feedback
        if phase == "recorded":
            badge = "超时已保存"
        elif phase == "accepted":
            badge = "超时分支已完成" if record.get("workflow_status") == "succeeded" else "超时已接收"
    elements = [block(s["title"], s["content"], "grey-50" if i == 0 else "default")
                for i, s in enumerate(data["sections"])]
    if phase == "waiting":
        fields = form_fields(data)
        columns = []
        for index, item in enumerate(data["actions"]):
            button = {"tag": "button", "text": plain(item["label"]),
                      "type": item.get("style", "default"), "width": "fill", "size": "medium"}
            if fields:
                button.update(name=form_button(record["token"], index), form_action_type="submit")
            else:
                button["behaviors"] = [{"type": "callback", "value": {"invocation": record["token"], "actionIndex": index}}]
            if item.get("confirm"):
                button["confirm"] = {"title": plain(item["label"]), "text": plain(item["confirm"])}
            columns.append({"tag": "column", "width": "weighted", "weight": 1, "elements": [button]})
        buttons = {"tag": "column_set", "flex_mode": "none", "horizontal_spacing": "8px", "columns": columns}
        if fields:
            inputs = [{"tag": "input", "name": name, "label": plain(field["label"]),
                       "input_type": "multiline_text" if field["type"] == "multiline" else "text",
                       "max_length": field["maxLength"], "width": "fill", "required": False}
                      for name, field in fields.items()]
            elements.append({"tag": "form", "name": "af_form_" + record["token"], "elements": inputs + [buttons]})
        else:
            elements.append(buttons)
        if record.get("last_rejection"):
            elements.append(block("未提交", record["last_rejection"]["message"]))
    else:
        elements.append(block(badge, feedback, color + "-50" if color != "default" else "grey-50"))
    footer = feedback if phase == "waiting" else f"记录时间：{record.get('selected_at', record['created_at'])}"
    if record.get("deadline_at"):
        footer += " · 审核期限：" + record["deadline_at"]
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
            db.execute("CREATE TABLE IF NOT EXISTS rejections (id INTEGER PRIMARY KEY, body TEXT NOT NULL)")

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

    def get(self, token):
        with self.connect() as db:
            row = db.execute("SELECT body FROM requests WHERE token=?", (token,)).fetchone()
            return json.loads(row[0]) if row else None

    def update(self, token, updates, expected=None):
        with self.connect() as db:
            db.execute("BEGIN IMMEDIATE")
            row = db.execute("SELECT body FROM requests WHERE token=?", (token,)).fetchone()
            if not row:
                return None
            record = json.loads(row[0])
            if expected is not None and record["phase"] != expected:
                return None
            record.update(updates)
            db.execute("UPDATE requests SET body=? WHERE token=?", (encode(record), token))
            return record

    def choose(self, token, choice, clock):
        """Serialize the deadline check and first terminal intent in one commit."""
        with self.connect() as db:
            db.execute("BEGIN IMMEDIATE")
            row = db.execute("SELECT body FROM requests WHERE token=?", (token,)).fetchone()
            if not row:
                return None
            record = json.loads(row[0])
            if record["phase"] != "waiting":
                return None
            at = clock()
            if record.get("deadline_at") and epoch(record["deadline_at"]) <= at:
                choice = {"source": "timeout", "output": record["config"]["deadline"]["output"], "form_values": {}}
            elif choice is None:
                return None
            record.update(choice, phase="recorded", selected_at=timestamp(at))
            record.pop("last_rejection", None)
            db.execute("UPDATE requests SET body=? WHERE token=?", (encode(record), token))
            return record

    def reject(self, record, event, code, message):
        rejection = {"token": record["token"], "reason_code": code, "message": message,
                     "event_id": event.get("event_id"), "operator_id": event.get("operator_id"), "at": now()}
        with self.connect() as db:
            db.execute("INSERT INTO rejections(body) VALUES (?)", (encode(rejection),))
        self.update(record["token"], {"last_rejection": rejection}, expected="waiting")

    def rejections(self):
        with self.connect() as db:
            return [json.loads(row[0]) for row in db.execute("SELECT body FROM rejections ORDER BY id")]

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
    def __init__(self, root, state, profile, executor="lark-review", agentflow="agentflow", lark="lark-cli", clock=time.time):
        self.root, self.profile, self.executor = Path(root).resolve(), profile, executor
        self.agentflow, self.lark = agentflow, lark
        self.clock = clock
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
            # Dynamic values do not exist yet: validate the actual merged result
            # at click time, never fabricate sample user input for preflight.
            if not action.get("formOutput"):
                self.validate_output(run_id, node_id, token, action["output"])
        if "deadline" in config:
            self.validate_output(run_id, node_id, token, config["deadline"]["output"])

    def validate_output(self, run_id, node_id, token, output):
        check = subprocess.run([self.agentflow, "run", "submit", run_id, node_id,
            "--token", token, "--output", encode(output), "--dry-run", "--format", "json"],
            cwd=self.root, text=True, capture_output=True, timeout=10)
        if check.returncode != 10:
            log(f"submission preflight failed: {check.stderr or check.stdout}")
            raise ValueError("output_schema: submission preflight failed; output or invocation is invalid")

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
                    record = {"token": token, "run_id": state["runId"], "node_id": node_id,
                        "config": config, "phase": "waiting", "created_at": timestamp(self.clock()),
                        "send_key": str(uuid.uuid5(uuid.NAMESPACE_URL, str(self.root) + token))}
                    if "deadline" in config:
                        deadline = config["deadline"]
                        at = epoch(deadline["at"]) if "at" in deadline else epoch(node["attemptSequence"][-1]["startedAt"]) + deadline["afterSeconds"]
                        record["deadline_at"] = timestamp(at)
                    self.journal.save(record)
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
                value = None
        for record in self.journal.records():
            if form_fields(record["config"]):
                matches = [i for i in range(len(record["config"]["actions"])) if event.get("action_name") == form_button(record["token"], i)]
                if not matches:
                    continue
                index = matches[0]
            else:
                if not isinstance(value, dict) or record["token"] != value.get("invocation"):
                    continue
                index = value.get("actionIndex")
            if record["phase"] != "waiting":
                self.journal.reject(record, event, "already_chosen", "该次请求已有已保存选择，不能再次提交。")
                return
            if event.get("message_id") != record.get("message_id") or event.get("operator_id") != record["config"]["recipient"]:
                self.journal.reject(record, event, "binding_mismatch", "消息或操作者与审核请求不一致。")
                return
            if type(index) is not int or not 0 <= index < len(record["config"]["actions"]):
                self.journal.reject(record, event, "action_invalid", "未知按钮。")
                return
            state, node = self.current(record)
            if state["status"] in {"cancelled", "failed", "succeeded"} or node.get("status") != "waiting_input" or node.get("leaseToken") != record["token"]:
                self.journal.reject(record, event, "stale_invocation", "该审核轮次已失效，请使用最新卡片。")
                return
            try:
                values, output = merge_form(record["config"], index, event.get("form_value", {}))
                self.validate_output(record["run_id"], record["node_id"], record["token"], output)
            except (ValueError, TypeError, subprocess.TimeoutExpired) as exc:
                message = str(exc)
                self.journal.reject(record, event, message.split(":", 1)[0] if ":" in message else "form_invalid", message)
                return
            choice = self.journal.choose(record["token"], {"source": "human", "selection": index,
                "output": output, "form_values": values, "operator_id": event["operator_id"], "event_id": event.get("event_id")}, self.clock)
            if choice:
                if choice["source"] == "timeout":
                    self.journal.reject(record, event, "deadline_elapsed", "期限已到，本次点击未被接受，使用显式超时输出。")
                log(f"selection persisted: {record['run_id']}/{record['node_id']}")
            else:
                self.journal.reject(record, event, "already_chosen", "另一项操作已保存，不能再次提交。")
            return

    def launch(self, record, command):
        run_id = record["run_id"]
        existing = self.jobs.get(run_id)
        if existing and existing.poll() is None:
            return
        if time.time() < record.get("retry_after", 0):
            return
        record = self.journal.update(record["token"], {"retry_after": time.time() + 5, "submit_attempted_at": timestamp(self.clock())}, expected=record["phase"])
        if record is None:
            return
        path = self.journal.path.parent / (run_id + ".log")
        with path.open("a") as out:
            self.jobs[run_id] = subprocess.Popen([self.agentflow, "run", *command, "--format", "json"],
                cwd=self.root, stdin=subprocess.DEVNULL, stdout=out, stderr=out, start_new_session=True)

    def check_hook(self, record):
        hook = record["config"].get("preSubmit")
        if not hook:
            return
        envelope = {key: record.get(key) for key in ("run_id", "node_id", "token", "selection", "source", "output", "form_values", "selected_at")}
        envelope["config"] = record["config"]
        result = subprocess.run(hook["command"], input=encode(envelope), cwd=self.root,
                                capture_output=True, text=True, timeout=hook["timeoutSeconds"])
        payload = json.loads(result.stdout) if result.stdout else {}
        if result.returncode or not isinstance(payload, dict) or payload.get("ok") is not True:
            raise ValueError("pre_submit_rejected: " + str(payload.get("reason", "hook rejected selection") if isinstance(payload, dict) else "invalid hook response"))

    def reconcile(self, record):
        record = self.journal.get(record["token"])
        state, node = self.current(record)
        visits = [node] + [h["state"] for h in state.get("history", []) if h["nodeId"] == record["node_id"]]
        completed = next((v for v in visits if v.get("leaseToken") == record["token"] and v.get("status") == "succeeded"), None)
        stale = state["status"] in {"cancelled", "failed", "succeeded"} or node.get("leaseToken") != record["token"]
        if record["phase"] in {"waiting", "recorded", "blocked"}:
            phase = record["phase"]
            if completed:
                updates = {"phase": "accepted" if "output" in record and completed.get("result") == record["output"] else "expired"}
                if updates["phase"] == "accepted":
                    updates["engine_accepted_at"] = timestamp(self.clock())
                record = self.journal.update(record["token"], updates, expected=phase)
            elif stale:
                record = self.journal.update(record["token"], {"phase": "expired", "expiry_reason": "invocation_ended"}, expected=phase)
            elif phase == "waiting":
                record = self.journal.choose(record["token"], None, self.clock) or self.journal.get(record["token"])
        if record is None:
            return
        if record["phase"] == "recorded":
            job = self.jobs.get(record["run_id"])
            try:
                if not job or job.poll() is not None:
                    self.validate_output(record["run_id"], record["node_id"], record["token"], record["output"])
                    self.check_hook(record)
            except (OSError, ValueError, TypeError, subprocess.TimeoutExpired) as exc:
                latest, latest_node = self.current(record)
                if latest_node.get("status") != "waiting_input" or latest_node.get("leaseToken") != record["token"] or latest["status"] in {"cancelled", "failed", "succeeded"}:
                    # The engine may accept while preflight/hook was running.
                    # Reconcile its checkpoint next tick, never paint a false block.
                    return
                record = self.journal.update(record["token"], {"phase": "blocked", "submit_error": str(exc)}, expected="recorded")
                log(f"submission blocked: {exc}")
                if record is None:
                    return
            else:
                self.launch(record, ["submit", record["run_id"], record["node_id"], "--token", record["token"],
                                     "--output", encode(record["output"])])
        elif record["phase"] == "accepted" and state["status"] == "running":
            # Recovery after acceptance before traversing edges. Engine run lock
            # serializes this with a surviving worker; acceptance is not completion.
            self.launch(record, ["resume", record["run_id"]])
        if record["phase"] == "accepted":
            record = self.journal.update(record["token"], {"workflow_status": state["status"]}, expected="accepted")
        if record is None or not self.ready.is_set() or time.time() < record.get("card_retry_after", 0):
            return
        if record["phase"] in {"expired", "blocked"} and not record.get("message_id"):
            return
        painted = encode([record["phase"], record.get("workflow_status"), record.get("last_rejection")])
        if record.get("painted") == painted:
            return
        updates = {}
        try:
            card = card_for(record)
            if len(encode(card).encode()) > 28000:
                raise ValueError("card exceeds safe 28 KB payload size; shorten sections")
            if not record.get("message_id"):
                sent = self.api(["im", "+messages-send", "--user-id", record["config"]["recipient"],
                    "--msg-type", "interactive", "--content", encode(card), "--idempotency-key", record["send_key"]])
                updates["message_id"] = sent["message_id"]
            else:
                self.api(["im", "messages", "patch", "--message-id", record["message_id"], "--data", encode({"content": encode(card)})])
            updates.update(painted=painted, card_error=None)
        except Exception as exc:
            updates.update(card_error=str(exc), card_retry_after=time.time() + 15)
            log(f"card delivery/update failed: {exc}")
        self.journal.update(record["token"], updates, expected=record["phase"])

    def tick(self):
        for run_id, job in list(self.jobs.items()):
            if job.poll() is not None:
                del self.jobs[run_id]
        self.discover()
        for event_id, event in self.journal.drain():
            try:
                self.event(event)
            except (OSError, ValueError, KeyError, TypeError, subprocess.TimeoutExpired) as exc:
                # Retain this inbox item if state/preflight is temporarily unreadable.
                log(f"event processing deferred: {exc}")
            else:
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
