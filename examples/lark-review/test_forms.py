"""AF-06: actual engine, simulated channel events/transport and injected clock.

No real user input or Feishu send is claimed by this suite.
"""
import concurrent.futures
import copy
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import time
import unittest

from lark_adapter import Adapter, card_for, encode, epoch, form_button, timestamp, validate_config
from test_adapter import Transport

BIN = os.environ.get("AGENTFLOW_BIN", "agentflow")


def config():
    return {"profile": "test", "recipient": "ou_reviewer", "title": "Review case sha-one",
            "sections": [{"title": "Report", "content": "[Case report](https://example.org/case) · sha-one"}],
            "form": {"fields": [{"name": "comments", "type": "multiline", "label": "修改意见", "maxLength": 1000}]},
            "actions": [{"label": "Continue", "output": {"score": 8, "case": "case-1", "sha": "sha-one"}, "formOutput": {"comments": "comments"}},
                        {"label": "Revise", "output": {"score": 2, "case": "case-1", "sha": "sha-one"}, "formOutput": {"comments": "comments"}, "requiredFormFields": ["comments"]}],
            "deadline": {"afterSeconds": 60, "output": {"score": 0, "case": "case-1", "sha": "sha-one", "comments": "explicit timeout"}}}


class Forms(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name)
        self.db = self.root / "adapter/state.sqlite3"
        self.transport, self.adapters = Transport(), []
        self.clock = [time.time()]
        (self.root / "executors.json").write_text(encode({"executors": {"lark-review": {"kind": "external"}}}))
        self.start(config())

    def tearDown(self):
        for adapter in self.adapters:
            for job in adapter.jobs.values():
                job.wait(timeout=10)
        evidence = os.environ.get("AF06_EVIDENCE_DIR")
        if evidence:
            directory = Path(evidence)
            directory.mkdir(parents=True, exist_ok=True)
            (directory / (self._testMethodName + ".json")).write_text(json.dumps({
                "simulated_transport": True, "real_human_callback_verified": False,
                "state": self.state(), "requests": self.a.journal.records(),
                "rejections": self.a.journal.rejections()}, ensure_ascii=False, indent=2) + "\n")
        self.temp.cleanup()

    def adapter(self):
        adapter = Adapter(self.root, self.db, "test", agentflow=BIN, clock=lambda: self.clock[0])
        adapter.api = self.transport.api
        adapter.ready.set()
        self.adapters.append(adapter)
        return adapter

    def start(self, cfg, loop=False):
        output = {"type": "object", "required": ["score", "comments", "case", "sha"], "additionalProperties": False,
                  "properties": {"score": {"type": "number"}, "comments": {"type": "string"}, "case": {"type": "string"}, "sha": {"type": "string"}}}
        flow = {"version": "3", "name": "dynamic-review", "entry": "gate", "nodes": [
            {"id": "gate", "execution": {"candidates": [{"executor": "lark-review", "config": cfg}]}, "outputSchema": output},
            {"id": "next", "execution": {"candidates": [{"executor": "process", "config": {"command": [sys.executable, "-c",
                "import os,pathlib; pathlib.Path(os.environ['AGENTFLOW_RESULT_FILE']).write_text(pathlib.Path(os.environ['AGENTFLOW_INPUT_FILE']).read_text())"]}}]}}],
            "edges": ([{"from": "gate", "to": "gate", "when": "output.score == 2", "maxIterations": 1}] if loop else []) +
                     [{"from": "gate", "to": "next"}, {"from": "next", "to": "$end"}]}
        path = self.root / "workflow.json"
        path.write_text(encode(flow))
        result = subprocess.run([BIN, "run", "start", str(path), "--format", "json"], cwd=self.root, capture_output=True, text=True, timeout=10)
        self.assertEqual(result.returncode, 0, result.stderr + result.stdout)
        self.run_id = json.loads(result.stdout)["run_id"]
        self.a = self.adapter()
        self.a.tick()
        self.record = next(r for r in self.a.journal.records() if r["run_id"] == self.run_id)

    def state(self):
        return json.loads((self.root / "runs" / self.run_id / "state.json").read_text())

    def saved(self):
        return self.a.journal.get(self.record["token"])

    def event(self, index=1, values=None, record=None, **overrides):
        record = record or self.record
        event = {"type": "card.action.trigger", "action_tag": "button", "event_id": "simulated-event",
                 "operator_id": "ou_reviewer", "message_id": record["message_id"], "action_value": "",
                 "action_name": form_button(record["token"], index), "form_value": encode(values if values is not None else {"comments": "补充重试失败断言"})}
        event.update(overrides)
        return event

    def wait(self, predicate):
        end = time.monotonic() + 10
        while time.monotonic() < end:
            self.a.tick()
            if predicate():
                return
            time.sleep(.025)
        self.fail("timeout: " + encode(self.state()))

    def test_06_01_actual_form_is_durable_and_passed_with_case_version(self):
        self.a.event(self.event())
        saved = self.saved()
        self.assertEqual(saved["phase"], "recorded")
        self.assertEqual(saved["form_values"]["comments"], "补充重试失败断言")
        self.assertEqual(saved["output"], {"score": 2, "case": "case-1", "sha": "sha-one", "comments": "补充重试失败断言"})
        self.assertEqual(self.state()["status"], "waiting_input")
        self.a = self.adapter()  # saved-before-submit recovery
        self.wait(lambda: self.state()["status"] == "succeeded" and self.saved().get("workflow_status") == "succeeded")
        self.assertEqual(self.state()["nodes"]["next"]["result"], saved["output"])
        self.assertEqual(self.saved()["workflow_status"], "succeeded")

    def test_06_02_blank_long_unknown_or_typed_fields_do_not_submit(self):
        for values, code in [({"comments": "  \n "}, "form_required"), ({"comments": "x" * 1001}, "form_length"),
                             ({"comments": "ok", "case": "evil"}, "form_unknown"), ({"comments": False}, "form_type")]:
            self.a.event(self.event(values=values))
            self.a.tick()
            self.assertEqual(self.saved()["phase"], "waiting")
            self.assertEqual(self.a.journal.rejections()[-1]["reason_code"], code)
            self.assertEqual(self.state()["status"], "waiting_input")
        # Requiredness is action-specific: empty approval comment is legal.
        self.a.event(self.event(0, values={"comments": ""}))
        self.wait(lambda: self.state()["status"] == "succeeded" and self.saved().get("workflow_status") == "succeeded")

    def test_06_03_old_form_cannot_affect_revisit(self):
        self.start(config(), loop=True)
        old = self.record
        self.a.event(self.event(record=old))
        self.wait(lambda: self.state()["nodes"]["gate"]["leaseToken"] != old["token"])
        self.a.tick()
        new_token = self.state()["nodes"]["gate"]["leaseToken"]
        self.a.event(self.event(0, record=old))
        self.a.tick()
        self.assertEqual(self.state()["nodes"]["gate"]["leaseToken"], new_token)
        self.assertEqual(self.state()["nodes"]["gate"]["status"], "waiting_input")

    def test_06_04_restart_inbox_selection_and_patch_failure(self):
        self.a.journal.receive(self.event())
        self.a = self.adapter()
        self.a.event(self.a.journal.drain()[0][1])
        self.a = self.adapter()
        self.transport.fail_patch = True
        self.wait(lambda: self.state()["status"] == "succeeded" and self.saved().get("workflow_status") == "succeeded")
        self.a = self.adapter()
        self.transport.fail_patch = False
        self.a.journal.update(self.record["token"], {"card_retry_after": 0})
        self.a.tick()
        self.assertEqual(self.saved()["phase"], "accepted")
        self.assertEqual(self.saved()["form_values"]["comments"], "补充重试失败断言")
        self.assertIn("后续已完成", encode(self.transport.patches[-1]))
        self.assertEqual(len(self.state()["nodes"]["gate"]["attemptSequence"]), 1)

    def test_06_05_identity_and_immutable_object_fields(self):
        for event in [self.event(operator_id="other"), self.event(message_id="other"), self.event(values={"comments": "ok", "sha": "tampered"})]:
            self.a.event(event)
        self.assertEqual(self.saved()["phase"], "waiting")
        self.assertEqual(len(self.a.journal.rejections()), 3)
        cfg = config()
        cfg["actions"][1]["formOutput"]["case"] = "comments"
        with self.assertRaisesRegex(ValueError, "override"):
            validate_config(cfg)

    def test_06_06_version_hook_blocks_before_output_acceptance(self):
        hook = self.root / "freshness.py"
        hook.write_text("import json,sys,pathlib\ne=json.load(sys.stdin)\nprint(json.dumps({'ok':e['output']['sha']==pathlib.Path('version').read_text(),'reason':'version changed'}))\n")
        (self.root / "version").write_text("sha-one")
        cfg = config()
        cfg["preSubmit"] = {"command": [sys.executable, str(hook)], "timeoutSeconds": 2}
        self.start(cfg)
        self.a.event(self.event(0))
        (self.root / "version").write_text("sha-two")
        self.a.tick()
        self.assertEqual(self.saved()["phase"], "blocked")
        self.assertIn("version changed", self.saved()["submit_error"])
        self.assertEqual(self.state()["status"], "waiting_input")
        self.assertFalse(self.a.jobs)
        self.a = self.adapter()
        self.a.tick()
        self.assertEqual(self.saved()["phase"], "blocked")

    def test_06_07_restart_after_deadline_and_click_race_select_timeout_once(self):
        deadline = self.record["deadline_at"]
        # Discovery used the persisted external start timestamp, not our clock.
        self.assertAlmostEqual(epoch(deadline) - epoch(self.state()["nodes"]["gate"]["attemptSequence"][-1]["startedAt"]), 60, places=4)
        self.a = self.adapter()
        self.clock[0] = epoch(deadline) + 1
        with concurrent.futures.ThreadPoolExecutor(2) as pool:
            futures = [pool.submit(self.a.event, self.event(0)), pool.submit(self.a.journal.choose, self.record["token"], None, lambda: self.clock[0])]
            for future in futures:
                future.result()
        self.assertEqual(self.saved()["deadline_at"], deadline)
        self.assertEqual(self.saved()["source"], "timeout")
        self.assertEqual(self.saved()["output"]["score"], 0)
        self.a = self.adapter()
        self.wait(lambda: self.state()["status"] == "succeeded" and self.saved().get("workflow_status") == "succeeded")
        self.assertEqual(self.state()["nodes"]["gate"]["result"]["score"], 0)
        self.assertEqual(len(self.state()["nodes"]["gate"]["attemptSequence"]), 1)

    def test_selection_before_deadline_cannot_be_overwritten_by_later_timeout(self):
        self.a.event(self.event(0))
        self.clock[0] = epoch(self.record["deadline_at"]) + 1
        self.assertIsNone(self.a.journal.choose(self.record["token"], None, lambda: self.clock[0]))
        self.wait(lambda: self.state()["status"] == "succeeded" and self.saved().get("workflow_status") == "succeeded")
        self.assertEqual(self.saved()["source"], "human")
        self.assertEqual(self.state()["nodes"]["gate"]["result"]["score"], 8)

    def test_card_form_contract_and_acceptance_completion_are_distinct(self):
        card = card_for(self.record)
        form = next(e for e in card["body"]["elements"] if e["tag"] == "form")
        self.assertEqual(form["elements"][0]["input_type"], "multiline_text")
        button = form["elements"][-1]["columns"][0]["elements"][0]
        self.assertEqual(button["form_action_type"], "submit")
        self.assertNotIn("behaviors", button)
        record = copy.deepcopy(self.record)
        record.update(phase="accepted", selection=0, workflow_status="running")
        self.assertIn("后续节点未必已完成", encode(card_for(record)))
        self.assertNotIn("后续已完成", encode(card_for(record)))

    def test_config_rejects_ambiguous_forms_and_implicit_deadlines(self):
        mutations = [lambda c: c["form"]["fields"].append(copy.deepcopy(c["form"]["fields"][0])),
                     lambda c: c["form"]["fields"][0].update(maxLength=2000),
                     lambda c: c["form"]["fields"][0].update(maxLength=True),
                     lambda c: c["actions"][0].update(requiredFormFields=["unknown"]),
                     lambda c: c["actions"][0].update(formOutput={"comments": "unknown"}),
                     lambda c: c.update(deadline={"afterSeconds": 5}),
                     lambda c: c["deadline"].update(at="2026-10-26T00:00:00Z"),
                     lambda c: c.update(deadline={"at": "2026-10-26T00:00:00", "output": {}}),
                     lambda c: c.update(preSubmit={"command": [], "timeoutSeconds": 1})]
        for mutate in mutations:
            cfg = config()
            mutate(cfg)
            with self.assertRaises(ValueError):
                validate_config(cfg)

    def test_actual_merged_output_is_checked_against_engine_schema(self):
        cfg = config()
        cfg["actions"][1]["output"]["score"] = "invalid number"
        self.start(cfg)
        self.a.event(self.event())
        self.assertEqual(self.saved()["phase"], "waiting")
        self.assertEqual(self.a.journal.rejections()[-1]["reason_code"], "output_schema")
        self.assertEqual(self.state()["status"], "waiting_input")

    def test_absolute_deadline_and_late_discovery_do_not_extend_wait(self):
        cfg = config()
        deadline = timestamp(self.clock[0] + 10)
        cfg["deadline"] = {"at": deadline, "output": cfg["deadline"]["output"]}
        self.start(cfg)
        self.assertEqual(self.saved()["deadline_at"], deadline)
        self.clock[0] += 11
        self.a = self.adapter()
        self.wait(lambda: self.saved()["phase"] == "accepted")
        self.assertEqual(self.saved()["source"], "timeout")
        self.assertEqual(self.saved()["deadline_at"], deadline)

    def test_two_valid_human_callbacks_commit_only_one_selection(self):
        with concurrent.futures.ThreadPoolExecutor(2) as pool:
            for future in [pool.submit(self.a.event, self.event(i, values={"comments": str(i)})) for i in range(2)]:
                future.result()
        chosen = self.saved()["output"]
        self.wait(lambda: self.saved()["phase"] == "accepted")
        self.assertEqual(self.state()["nodes"]["gate"]["result"], chosen)
        self.assertEqual(len(self.state()["nodes"]["gate"]["attemptSequence"]), 1)


if __name__ == "__main__":
    unittest.main()
