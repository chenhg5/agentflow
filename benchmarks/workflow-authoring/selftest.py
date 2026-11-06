#!/usr/bin/env python3
"""Check evaluator sensitivity using positive goldens and intentional defects."""

import json
from pathlib import Path
import shutil
import tempfile
import unittest

from bench import case_for, prepare
from goldens import generate
from grader import Grade


class EvaluatorTest(unittest.TestCase):
    def fixture(self, case):
        holder = tempfile.TemporaryDirectory(prefix="agentflow-golden-test-")
        base = Path(holder.name)
        sources = base / "goldens"
        generate(sources)
        root = base / "attempt"
        prepare(case, root)
        source = sources / case
        shutil.copytree(source, root, dirs_exist_ok=True)
        return holder, root

    def test_all_goldens(self):
        for case in [
            "scheduled-review",
            "numeric-routing",
            "external-loop",
            "mixed-language",
        ]:
            with self.subTest(case=case):
                holder, root = self.fixture(case)
                with holder:
                    report = Grade(root, case_for(case)).evaluate()
                    self.assertTrue(
                        report["passed"],
                        [check for check in report["checks"] if not check["passed"]],
                    )

    def test_equivalent_node_order_and_names(self):
        holder, root = self.fixture("scheduled-review")
        with holder:
            flow = json.loads((root / "workflow.json").read_text())
            rename = {"draft": "node_17", "review": "node_3", "finalize": "node_91"}
            text = json.dumps(flow)
            for old, new in rename.items():
                text = text.replace("nodes." + old + ".", "nodes." + new + ".")
            flow = json.loads(text)
            for node in flow["nodes"]:
                node["id"] = rename[node["id"]]
            flow["nodes"].reverse()
            flow["entry"] = rename[flow["entry"]]
            for edge in flow["edges"]:
                edge["from"] = rename[edge["from"]]
                if edge["to"] != "$end":
                    edge["to"] = rename[edge["to"]]
            # Equivalent data/config bindings and file-based role instructions
            # must not be rejected for differing from the reference syntax.
            review = next(n for n in flow["nodes"] if n["id"] == rename["review"])
            review_config = review["execution"]["candidates"][0]["config"]
            actions = review_config["actions"]
            review_config["recipient"] = "{{ input.recipient }}"
            review_config["actions"] = "{{ input.actions }}"
            review["inputSchema"] = {"type": "object"}
            next(e for e in flow["edges"] if e["from"] == rename["draft"] and e["to"] == rename["review"])["map"] = {
                "recipient": "{{ inputs.reviewer }}", "actions": actions,
                "summary": "{{ output.summary }}", "check": "{{ output.check }}",
            }
            draft = next(n for n in flow["nodes"] if n["id"] == rename["draft"])
            draft_candidate = draft["execution"]["candidates"][0]
            draft_candidate["config"].pop("instruction")
            draft_candidate["executor"] = "owned-author"
            directory = root / "executors/agents/author"
            directory.mkdir(parents=True)
            (directory / "AGENTS.md").write_text("Produce summary/check from the workflow input and any returned review note.\n")
            registry = json.loads((root / "executors.json").read_text())
            registry["executors"]["owned-author"] = {"kind": "codex", "workspace": "executors/agents/author"}
            (root / "executors.json").write_text(json.dumps(registry))
            (root / "workflow.json").write_text(json.dumps(flow))
            (root / "submission.json").write_text(
                json.dumps({"workflow": "workflow.json", "roles": rename})
            )
            self.assertTrue(
                Grade(root, case_for("scheduled-review")).evaluate()["passed"]
            )

    def test_defects_are_detected(self):
        defects = [
            ("numeric-routing", "boundary"),
            ("numeric-routing", "unregistered"),
            ("numeric-routing", "missing-branch-config"),
            ("scheduled-review", "missing-instruction"),
            ("scheduled-review", "stop-routes-forward"),
            ("scheduled-review", "unbounded"),
            ("mixed-language", "missing-environment"),
        ]
        for case, defect in defects:
            with self.subTest(defect=defect):
                holder, root = self.fixture(case)
                with holder:
                    flow = json.loads((root / "workflow.json").read_text())
                    if defect == "boundary":
                        flow["edges"][0]["when"] = flow["edges"][0]["when"].replace(
                            "> 80", ">= 80"
                        )
                    if defect == "unregistered":
                        flow["nodes"][0]["execution"]["candidates"][0]["executor"] = "missing-executor"
                    if defect == "missing-branch-config":
                        flow["nodes"][1]["execution"]["candidates"][0]["config"].pop("branch")
                    if defect == "missing-instruction":
                        flow["nodes"][0]["execution"]["candidates"][0]["config"].pop("instruction")
                    if defect == "stop-routes-forward":
                        flow["edges"][3]["to"] = "finalize"
                    if defect == "unbounded":
                        flow["edges"][2].pop("maxIterations")
                    if defect == "missing-environment":
                        p = root / "executors.json"
                        config = json.loads(p.read_text())
                        config["executors"]["collect"].pop("env")
                        p.write_text(json.dumps(config))
                    (root / "workflow.json").write_text(json.dumps(flow))
                    report = Grade(root, case_for(case)).evaluate()
                    self.assertFalse(
                        report["passed"], defect + " escaped the evaluator"
                    )


if __name__ == "__main__":
    unittest.main()
