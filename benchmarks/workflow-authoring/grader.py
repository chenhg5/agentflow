"""Private deterministic acceptance checks. Not copied to participant workspaces."""

import copy
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile

MOCK_AGENT = r"""import json, os
from pathlib import Path
roles = json.loads(os.environ['BENCH_ROLES'])
node = os.environ['AGENTFLOW_NODE_ID']
role = next(key for key,value in roles.items() if value == node)
count = Path(os.environ['BENCH_MODEL_COUNTER'])
visits = json.loads(count.read_text()) if count.exists() else {}
visits[role] = visits.get(role, 0) + 1
count.write_text(json.dumps(visits))
if role == 'draft': result = {'summary':'proposal-round-' + str(visits[role]),'check':'check-round-' + str(visits[role])}
else: result = {'next_step':'take-action','verification':'verify-action'}
Path(os.environ['AGENTFLOW_RESULT_FILE']).write_text(json.dumps(result))
"""


def command(binary, cwd, args, env=None):
    result = subprocess.run(
        [str(binary), *args, "--format", "json"],
        cwd=cwd,
        env=env,
        capture_output=True,
        text=True,
        timeout=20,
    )
    try:
        data = json.loads(result.stdout)
    except ValueError:
        data = {"raw_stdout": result.stdout, "stderr": result.stderr}
    return result.returncode, data


def includes(value, needle):
    if isinstance(value, dict):
        return any(includes(item, needle) for item in value.values())
    if isinstance(value, list):
        return any(includes(item, needle) for item in value)
    return needle in value if isinstance(value, str) else value == needle


def first_candidate(node):
    candidates = node.get("execution", {}).get("candidates", [])
    return candidates[0] if candidates else {}


def node_executor(node):
    return first_candidate(node).get("executor")


def node_config(node):
    return first_candidate(node).get("config", {})


class Grade:
    def __init__(self, workspace, case, binary=None):
        self.workspace = Path(workspace).resolve()
        self.case = case
        self.binary_override = Path(binary).resolve() if binary else None
        self.checks = []
        self.traces = []
        self.meta = json.loads((self.workspace / "attempt.json").read_text())

    def check(self, name, passed, detail=""):
        self.checks.append({"name": name, "passed": bool(passed), "detail": detail})

    def load(self):
        submission = json.loads((self.workspace / "submission.json").read_text())
        workflow = (self.workspace / submission["workflow"]).resolve()
        if not workflow.is_relative_to(self.workspace):
            raise ValueError("workflow must be inside attempt workspace")
        self.flow_path = workflow.relative_to(self.workspace)
        self.flow = json.loads(workflow.read_text())
        self.roles = submission["roles"]
        self.nodes = {node["id"]: node for node in self.flow["nodes"]}
        self.check(
            "role_mapping",
            all(
                role in self.roles and self.roles[role] in self.nodes
                for role in self.case["roles"]
            )
            and len(set(self.roles.values())) == len(self.roles),
        )
        code, data = command(
            self.binary_override
            or (
                self.workspace / ".tool/agentflow"
                if (self.workspace / ".tool/agentflow").exists()
                else self.workspace / "agentflow"
            ),
            self.workspace,
            ["workflow", "validate", str(self.flow_path)],
        )
        self.check(
            "definition_valid",
            code == 0,
            json.dumps(data, ensure_ascii=False) if code else "",
        )
        self.config = (
            json.loads((self.workspace / "executors.json").read_text())
            if (self.workspace / "executors.json").exists()
            else {"executors": {}}
        )
        builtins = {
            "codex": {"kind": "codex"},
            "claude-code": {"kind": "claude-code"},
            "process": {"kind": "command"},
            "external": {"kind": "external"},
        }
        self.registry = copy.deepcopy(builtins)
        for name, override in self.config.get("executors", {}).items():
            entry = {**self.registry.get(name, {}), **override}
            if override.get("command") and not override.get("kind"):
                entry["kind"] = "command"
            entry.setdefault("kind", "command")
            self.registry[name] = entry

        self.check(
            "all_executor_references_bound",
            all(
                candidate.get("executor") in self.registry
                for node in self.flow["nodes"]
                for candidate in node.get("execution", {}).get("candidates", [])
            ),
        )
        self.check("runbook_written", (self.workspace / "RUNBOOK.md").exists())
        expected_inputs = {
            "scheduled-review": ["topic", "reviewer"],
            "external-loop": ["topic"],
            "numeric-routing": ["sample"],
            "mixed-language": ["batch"],
        }[self.case["id"]]
        self.quality = [
            {
                "name": "required_inputs_declared",
                "passed": all(
                    self.flow.get("inputs", {}).get(name, {}).get("required") is True
                    for name in expected_inputs
                ),
            }
        ]
        if self.case["id"] in ["scheduled-review", "external-loop"]:
            for role in ["draft", "finalize"]:
                node = self.nodes[self.roles[role]]
                definition = self.registry.get(node_executor(node), {})
                self.check(role + "_uses_codex", definition.get("kind") == "codex")
                self.check(
                    role + "_instructions_present",
                    isinstance(node_config(node).get("instruction"), str)
                    or (self.workspace / definition.get("workspace", node_config(node).get("workspace", ".")) / "AGENTS.md").is_file(),
                )
            if self.case["id"] == "scheduled-review":
                self.check(
                    "agent_starts_workflow", self.flow["entry"] == self.roles["draft"]
                )
        elif self.case["id"] == "numeric-routing":
            self.check(
                "programs_do_not_require_prompt",
                all(
                    "instruction" not in node_config(self.nodes[self.roles[role]])
                    for role in self.case["roles"]
                ),
            )
        if self.case["id"] in ["scheduled-review", "mixed-language"]:
            self.check(
                "input_and_output_contracts",
                all(
                    self.nodes[self.roles[role]].get("inputSchema")
                    and self.nodes[self.roles[role]].get("outputSchema")
                    for role in self.case["roles"]
                ),
            )
        if self.case["id"] == "scheduled-review":
            role = self.nodes[self.roles["review"]]
            self.check(
                "interaction_entry_registered",
                bool(self.registry.get(node_executor(role), {}).get("command")),
            )
            path = self.workspace / "triggers.json"
            data = json.loads(path.read_text()) if path.exists() else {}
            triggers = data.get("triggers", [])
            self.check(
                "weekday_schedule",
                any(
                    t.get("enabled")
                    and t.get("source", {}).get("kind") == "cron"
                    and t["source"].get("expression") == self.case["schedule"]
                    and t["source"].get("timezone") == self.case["timezone"]
                    and (
                        self.workspace / t.get("target", {}).get("workflow", "")
                    ).resolve()
                    == (self.workspace / self.flow_path).resolve()
                    and t.get("inputs", {}).get("topic")
                    and t.get("inputs", {}).get("reviewer")
                    for t in triggers
                ),
            )
            code, data = command(
                self.workspace / "agentflow", self.workspace, ["trigger", "validate"]
            )
            self.check(
                "trigger_config_valid",
                code == 0,
                json.dumps(data, ensure_ascii=False) if code else "",
            )
        return all(
            check["passed"]
            for check in self.checks
            if check["name"]
            in [
                "role_mapping",
                "definition_valid",
                "all_executor_references_bound",
                "draft_uses_codex",
                "finalize_uses_codex",
            ]
        )

    def sandbox(self):
        holder = tempfile.TemporaryDirectory(prefix="agentflow-grade-")
        root = Path(holder.name)
        shutil.copytree(
            self.workspace,
            root,
            dirs_exist_ok=True,
            ignore=shutil.ignore_patterns(
                "runs", "dispatch", "trigger-deliveries", "*.log", "__pycache__"
            ),
        )
        for folder in ["runs", "dispatch", "trigger-deliveries"]:
            shutil.rmtree(root / folder, ignore_errors=True)
        if self.binary_override:
            target = (
                root / ".tool/agentflow"
                if (root / ".tool/agentflow").exists()
                else root / "agentflow"
            )
            shutil.copy2(self.binary_override, target)
        config = copy.deepcopy(self.config)
        private = root / ".evaluation"
        shutil.rmtree(private, ignore_errors=True)
        private.mkdir()
        mock = private / "mock-agent.py"
        mock.write_text(MOCK_AGENT)
        for name, definition in self.registry.items():
            if definition.get("kind") in ["codex", "claude-code"]:
                config.setdefault("executors", {})[name] = {
                    "command": ["python3", str(mock)]
                }
        (root / "executors.json").write_text(json.dumps(config))
        inherited = {
            key: value
            for key, value in os.environ.items()
            if key
            in [
                "PATH",
                "SYSTEMROOT",
                "COMSPEC",
                "PATHEXT",
                "TMPDIR",
                "TEMP",
                "TMP",
                "LANG",
                "LC_ALL",
            ]
        }
        env = dict(
            inherited,
            BENCH_ROLES=json.dumps(self.roles),
            BENCH_MODEL_COUNTER=str(private / "model-counter.json"),
            BENCH_CHANNEL_COUNTER=str(private / "channel-counter.txt"),
        )
        return holder, root, env

    def run(self, root, env, inputs=None):
        args = ["run", "start", str(self.flow_path)]
        for key, value in (inputs or {}).items():
            args += ["--input", f"{key}={value}"]
        code, data = command(root / "agentflow", root, args, env)
        return code, data

    def trace(self, label, code, state):
        self.traces.append({"scenario": label, "exit_code": code, "state": state})

    def review_scenario(self, label, responses, expected, limit=False):
        holder, root, env = self.sandbox()
        with holder:
            inputs = {"topic": "benchmark-topic"}
            if self.case["id"] == "scheduled-review":
                inputs["reviewer"] = "ou_benchmark"
                env["BENCH_RESPONSES"] = json.dumps(responses)
            code, state = self.run(root, env, inputs)
            if self.case["id"] == "external-loop":
                self.check(
                    label + "_persisted_wait",
                    code == 0 and state.get("status") == "waiting_input",
                )
                if not state.get("run_id"):
                    self.trace(label, code, state)
                    return
                run_id = state["run_id"]
                for response in responses:
                    code, state = command(
                        root / "agentflow", root, ["run", "get", run_id], env
                    )
                    if state.get("status") != "waiting_input":
                        break
                    code, state = command(
                        root / "agentflow",
                        root,
                        [
                            "run",
                            "submit",
                            run_id,
                            self.roles["review"],
                            "--output",
                            json.dumps(response),
                        ],
                        env,
                    )
            self.trace(label, code, state)
            self.check(
                label + "_status",
                (
                    state.get("status") in ["succeeded", "failed"]
                    if limit
                    else state.get("status") == "succeeded"
                ),
                json.dumps(
                    {
                        "status": state.get("status"),
                        "error": state.get("error"),
                        "nodes": state.get("nodes"),
                    },
                    ensure_ascii=False,
                )
                if state.get("status") not in ["succeeded", "failed"]
                else "",
            )
            history = [entry["nodeId"] for entry in (state.get("history") or [])]
            expected_ids = [self.roles[role] for role in expected]
            actual = [node for node in history if node in self.roles.values()]
            self.check(
                label + "_route",
                actual == expected_ids,
                f"actual={actual}; expected={expected_ids}",
            )
            if limit:
                self.check(
                    label + "_bounded",
                    actual.count(self.roles["draft"]) == 3
                    and self.roles["finalize"] not in actual
                    and (
                        state.get("status") == "succeeded"
                        or state.get("outcome") == "max_iterations"
                    ),
                )
                return
            if self.roles["finalize"] in actual:
                final_input = state["nodes"][self.roles["finalize"]].get("input")
                config_artifact = state["nodes"][self.roles["finalize"]].get("artifacts", {}).get("config")
                final_config = {}
                if config_artifact and state.get("run_id"):
                    path = root / "runs" / state["run_id"] / config_artifact
                    if path.exists():
                        final_config = json.loads(path.read_text())
                drafts = actual.count(self.roles["draft"])
                self.check(
                    label + "_latest_proposal",
                    includes(final_input, f"proposal-round-{drafts}") or includes(final_config, f"proposal-round-{drafts}"),
                )
                feedback = responses[-1].get("note", responses[-1].get("feedback"))
                self.check(label + "_final_feedback", includes(final_input, feedback) or includes(final_config, feedback))
            if actual.count(self.roles["draft"]) > 1:
                draft_input = state["nodes"][self.roles["draft"]].get("input")
                feedback = responses[-2].get("note", responses[-2].get("feedback"))
                self.check(
                    label + "_feedback_returned", includes(draft_input, feedback)
                )
            if self.case["id"] == "scheduled-review" and state.get("nodes", {}).get(
                self.roles["review"], {}
            ).get("input"):
                request = state["nodes"][self.roles["review"]]["input"]
                artifact = state["nodes"][self.roles["review"]]["artifacts"]["config"]
                config = json.loads((root / "runs" / state["run_id"] / artifact).read_text())
                self.check(
                    label + "_recipient", config.get("recipient") == inputs["reviewer"]
                )
                actions = config.get("actions", [])
                self.check(
                    label + "_interaction_actions_complete",
                    isinstance(actions, list)
                    and all(isinstance(action, dict) and isinstance(action.get("label"), str) and isinstance(action.get("output"), dict) for action in actions)
                    and {action["output"].get("signal") for action in actions} == {"happy", "again", "sad"},
                )
                self.check(
                    label + "_review_content",
                    includes(
                        request,
                        f"proposal-round-{actual.count(self.roles['draft'])}",
                    ),
                )

    def evaluate(self):
        try:
            if not self.load():
                return self.report()
            if self.case["id"] == "scheduled-review":
                self.review_scenario(
                    "accept",
                    [{"signal": "happy", "note": "ok-701"}],
                    ["draft", "review", "finalize"],
                )
                self.review_scenario(
                    "stop", [{"signal": "sad", "note": "stop-702"}], ["draft", "review"]
                )
                self.review_scenario(
                    "rework",
                    [
                        {"signal": "again", "note": "tighten-703"},
                        {"signal": "happy", "note": "ok-704"},
                    ],
                    ["draft", "review", "draft", "review", "finalize"],
                )
                self.review_scenario(
                    "bound",
                    [{"signal": "again", "note": "x"}] * 3,
                    ["draft", "review"] * 3,
                    True,
                )
            elif self.case["id"] == "external-loop":
                self.review_scenario(
                    "accept",
                    [{"mood": "go", "feedback": "ok-801", "confidence": 0.9}],
                    ["draft", "review", "finalize"],
                )
                self.review_scenario(
                    "stop",
                    [{"mood": "stop", "feedback": "stop-802", "confidence": 0}],
                    ["draft", "review"],
                )
                self.review_scenario(
                    "low-confidence",
                    [
                        {"mood": "go", "feedback": "clarify-803", "confidence": 0.79},
                        {"mood": "go", "feedback": "ok-804", "confidence": 0.8},
                    ],
                    ["draft", "review", "draft", "review", "finalize"],
                )
                self.review_scenario(
                    "back",
                    [
                        {"mood": "back", "feedback": "revise-805", "confidence": 0.99},
                        {"mood": "go", "feedback": "ok-806", "confidence": 1},
                    ],
                    ["draft", "review", "draft", "review", "finalize"],
                )
                self.review_scenario(
                    "bound",
                    [{"mood": "back", "feedback": "x", "confidence": 0.3}] * 3,
                    ["draft", "review"] * 3,
                    True,
                )
                holder, root, env = self.sandbox()
                with holder:
                    code, state = self.run(root, env, {"topic": "benchmark-topic"})
                    for index, output in enumerate(
                        [
                            {"mood": "go", "feedback": "bad", "confidence": 1.1},
                            {"mood": "go", "feedback": "bad", "confidence": -0.1},
                            {"mood": "other", "feedback": "bad", "confidence": 1},
                            {"mood": "go", "confidence": 1},
                            {
                                "mood": "go",
                                "feedback": "bad",
                                "confidence": 1,
                                "extra": True,
                            },
                        ]
                    ):
                        code, data = command(
                            root / "agentflow",
                            root,
                            [
                                "run",
                                "submit",
                                state["run_id"],
                                self.roles["review"],
                                "--output",
                                json.dumps(output),
                            ],
                            env,
                        )
                        self.check("invalid_output_" + str(index), code == 2)
            elif self.case["id"] == "numeric-routing":
                for index, scenario in enumerate(self.case["scenarios"]):
                    holder, root, env = self.sandbox()
                    with holder:
                        env["BENCH_MEASUREMENT"] = json.dumps(
                            {
                                key: value
                                for key, value in scenario.items()
                                if key != "target"
                            }
                        )
                        code, state = self.run(root, env, {"sample": "batch-042"})
                        self.trace(str(index), code, state)
                        self.check(
                            f"scenario_{index}_status",
                            code == 0 and state.get("status") == "succeeded",
                        )
                        history = [
                            entry["nodeId"] for entry in (state.get("history") or [])
                        ]
                        actual = [
                            role
                            for role in ["accept", "recheck"]
                            if self.roles[role] in history
                        ]
                        expected = [scenario["target"]] if scenario["target"] else []
                        self.check(
                            f"scenario_{index}_branch",
                            actual == expected,
                            f"actual={actual}; expected={expected}",
                        )
                        if expected and actual == expected:
                            request = state["nodes"][self.roles[expected[0]]].get(
                                "input", {}
                            )
                            self.check(
                                f"scenario_{index}_types",
                                type(request.get("score")) in [int, float]
                                and isinstance(request.get("defects"), list)
                                and type(request.get("urgent")) is bool,
                            )
            elif self.case["id"] == "mixed-language":
                holder, root, env = self.sandbox()
                with holder:
                    code, state = self.run(root, env, {"batch": "orchard-batch-019"})
                    self.trace("typed-handoff", code, state)
                    self.check(
                        "execution", code == 0 and state.get("status") == "succeeded"
                    )
                    result = (
                        state.get("nodes", {})
                        .get(self.roles["report"], {})
                        .get("result", {})
                        or {}
                    )
                    self.check(
                        "calculated_result",
                        result
                        == {
                            "batch": "orchard-batch-019",
                            "total": 21,
                            "unit": "kg",
                            "valid": True,
                        },
                    )
                    source = node_executor(self.nodes[self.roles["collect"]])
                    target = node_executor(self.nodes[self.roles["report"]])
                    entries = [
                        self.config.get("executors", {}).get(name, {})
                        for name in [source, target]
                    ]
                    self.check(
                        "both_program_entries_registered",
                        all(entry.get("command") for entry in entries),
                    )
                    self.check(
                        "program_environments",
                        all(
                            entry.get("env", {}).get("FARM_SITE") == "orchard-2"
                            for entry in entries
                        ),
                    )
                    commands = [entry.get("command", []) for entry in entries]
                    self.check(
                        "requested_languages",
                        any("python" in Path(part).name for part in commands[0])
                        and any(
                            Path(part).name in ["node", "nodejs"]
                            for part in commands[1]
                        ),
                    )
                    request = (
                        state.get("nodes", {})
                        .get(self.roles["report"], {})
                        .get("input", {})
                        or {}
                    )
                    self.check(
                        "nested_types_preserved",
                        request.get("readings") == [3, 7, 11]
                        and isinstance(request.get("meta"), dict)
                        and type(request["meta"].get("valid")) is bool,
                    )
        except Exception as error:
            self.check("grader_completed", False, f"{type(error).__name__}: {error}")
        return self.report()

    def report(self):
        return {
            "attempt": self.meta,
            "evaluation_binary_sha256": hashlib.sha256(
                (
                    self.binary_override
                    or (
                        self.workspace / ".tool/agentflow"
                        if (self.workspace / ".tool/agentflow").exists()
                        else self.workspace / "agentflow"
                    )
                ).read_bytes()
            ).hexdigest(),
            "grader_sha256": hashlib.sha256(Path(__file__).read_bytes()).hexdigest(),
            "passed": bool(self.checks)
            and all(check["passed"] for check in self.checks),
            "checks_passed": sum(check["passed"] for check in self.checks),
            "checks_total": len(self.checks),
            "checks": self.checks,
            "quality_checks": getattr(self, "quality", []),
            "traces": self.traces,
        }
