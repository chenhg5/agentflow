"""Reference solutions are private to the evaluator, never agent task context."""

import json
from pathlib import Path


def schema(properties, required=None):
    return {
        "type": "object",
        "properties": properties,
        "required": required or list(properties),
        "additionalProperties": False,
    }


S = {"type": "string"}
DRAFT = schema({"summary": S, "check": S})
FINAL = schema({"next_step": S, "verification": S})


def generate(directory):
    directory = Path(directory)
    for case in [
        "scheduled-review",
        "external-loop",
        "numeric-routing",
        "mixed-language",
    ]:
        root = directory / case
        root.mkdir(parents=True, exist_ok=True)
        config = {"executors": {}}
        if case in ["scheduled-review", "external-loop"]:
            feedback = "note" if case == "scheduled-review" else "feedback"
            draft = {
                "id": "draft",
                "executor": "codex",
                "input": {
                    "prompt": "Topic={{ inputs.topic }}. Previous feedback: {{ nodes.review.result."
                    + feedback
                    + " }}"
                },
                "inputSchema": schema({"prompt": S}),
                "outputSchema": DRAFT,
            }
            final = {
                "id": "finalize",
                "executor": "codex",
                "input": {
                    "prompt": "Latest proposal={{ nodes.draft.result.summary }}; feedback={{ nodes.review.result."
                    + feedback
                    + " }}"
                },
                "inputSchema": schema({"prompt": S}),
                "outputSchema": FINAL,
            }
            flow = {
                "version": "2",
                "name": case,
                "entry": "draft",
                "inputs": {"topic": {"type": "string", "required": True}},
                "nodes": [draft, None, final],
                "edges": [{"from": "draft", "to": "review"}],
            }
            if case == "scheduled-review":
                flow["inputs"]["reviewer"] = {"type": "string", "required": True}
                review = {
                    "id": "review",
                    "executor": "channel",
                    "input": {
                        "recipient": "{{ inputs.reviewer }}",
                        "content": "{{ nodes.draft.result.summary }}",
                        "actions": [
                            {"label": v, "output": {"signal": v, "note": v}}
                            for v in ["happy", "again", "sad"]
                        ],
                    },
                    "inputSchema": schema(
                        {"recipient": S, "content": S, "actions": {"type": "array"}}
                    ),
                    "outputSchema": schema(
                        {"signal": {"enum": ["happy", "again", "sad"]}, "note": S}
                    ),
                }
                config["executors"]["channel"] = {
                    "command": ["python3", "fixtures/channel.py"],
                    "workspace": ".",
                }
                flow["edges"] += [
                    {
                        "from": "review",
                        "to": "finalize",
                        "when": "nodes.review.result.signal == 'happy'",
                    },
                    {
                        "from": "review",
                        "to": "draft",
                        "when": "nodes.review.result.signal == 'again'",
                        "maxIterations": 2,
                    },
                    {"from": "review", "to": "$end"},
                ]
                triggers = {
                    "triggers": [
                        {
                            "id": "daily",
                            "enabled": True,
                            "source": {
                                "kind": "cron",
                                "expression": "15 9 * * 1-5",
                                "timezone": "Asia/Shanghai",
                            },
                            "target": {"workflow": "workflow.json"},
                            "inputs": {"topic": "proposal", "reviewer": "ou_local"},
                            "policy": {"concurrency": "forbid", "misfire": "skip"},
                        }
                    ]
                }
                (root / "triggers.json").write_text(json.dumps(triggers, indent=2))
            else:
                review = {
                    "id": "review",
                    "executor": "external",
                    "input": {},
                    "inputSchema": schema({}),
                    "outputSchema": schema(
                        {
                            "mood": {"enum": ["go", "back", "stop"]},
                            "feedback": S,
                            "confidence": {
                                "type": "number",
                                "minimum": 0,
                                "maximum": 1,
                            },
                        }
                    ),
                }
                flow["edges"] += [
                    {
                        "from": "review",
                        "to": "finalize",
                        "when": "nodes.review.result.mood == 'go' && nodes.review.result.confidence >= 0.8",
                    },
                    {
                        "from": "review",
                        "to": "draft",
                        "when": "nodes.review.result.mood == 'back' || (nodes.review.result.mood == 'go' && nodes.review.result.confidence < 0.8)",
                        "maxIterations": 2,
                    },
                    {"from": "review", "to": "$end"},
                ]
            flow["nodes"][1] = review
            flow["edges"].append({"from": "finalize", "to": "$end"})
            roles = {key: key for key in ["draft", "review", "finalize"]}
        elif case == "numeric-routing":
            value = {
                "score": {"type": "number"},
                "defects": {"type": "array", "items": S},
                "urgent": {"type": "boolean"},
            }
            nodes = [
                {
                    "id": "measure",
                    "executor": "measure",
                    "input": {"sample": "{{ inputs.sample }}"},
                    "inputSchema": schema({"sample": S}),
                    "outputSchema": schema(value),
                }
            ]
            for role in ["accept", "recheck"]:
                nodes.append(
                    {
                        "id": role,
                        "executor": "finish",
                        "input": {
                            "branch": role,
                            **{
                                key: "{{ nodes.measure.result." + key + " }}"
                                for key in value
                            },
                        },
                        "inputSchema": schema({"branch": S, **value}),
                        "outputSchema": schema({"branch": S, **value}),
                    }
                )
            config["executors"] = {
                "measure": {
                    "command": ["python3", "fixtures/measure.py"],
                    "workspace": ".",
                },
                "finish": {
                    "command": ["python3", "fixtures/finish.py"],
                    "workspace": ".",
                    "env": {"QUALITY_SITE": "plant-7"},
                },
            }
            flow = {
                "version": "2",
                "name": case,
                "entry": "measure",
                "inputs": {"sample": {"type": "string", "required": True}},
                "nodes": nodes,
                "edges": [
                    {
                        "from": "measure",
                        "to": "accept",
                        "when": "nodes.measure.result.score > 80 && len(nodes.measure.result.defects) == 0",
                    },
                    {
                        "from": "measure",
                        "to": "recheck",
                        "when": "nodes.measure.result.urgent || nodes.measure.result.score >= 50",
                    },
                    {"from": "measure", "to": "$end"},
                    {"from": "accept", "to": "$end"},
                    {"from": "recheck", "to": "$end"},
                ],
            }
            roles = {key: key for key in ["measure", "accept", "recheck"]}
        else:
            (root / "collect.py").write_text(
                """import json,os\nfrom pathlib import Path\nassert os.environ['FARM_SITE']=='orchard-2'\nx=json.loads(Path(os.environ['AGENTFLOW_INPUT_FILE']).read_text())\nPath(os.environ['AGENTFLOW_RESULT_FILE']).write_text(json.dumps({'batch':x['batch'],'readings':[3,7,11],'meta':{'unit':'kg','valid':True}}))\n"""
            )
            (root / "report.js").write_text(
                """const fs=require('node:fs');if(process.env.FARM_SITE!=='orchard-2')throw Error('site');const x=JSON.parse(fs.readFileSync(process.env.AGENTFLOW_INPUT_FILE,'utf8'));fs.writeFileSync(process.env.AGENTFLOW_RESULT_FILE,JSON.stringify({batch:x.batch,total:x.readings.reduce((a,b)=>a+b,0),unit:x.meta.unit,valid:x.meta.valid}));\n"""
            )
            data = schema(
                {
                    "batch": S,
                    "readings": {"type": "array", "items": {"type": "number"}},
                    "meta": schema({"unit": S, "valid": {"type": "boolean"}}),
                }
            )
            nodes = [
                {
                    "id": "collect",
                    "executor": "collect",
                    "input": {"batch": "{{ inputs.batch }}"},
                    "inputSchema": schema({"batch": S}),
                    "outputSchema": data,
                },
                {
                    "id": "report",
                    "executor": "report",
                    "input": {
                        key: "{{ nodes.collect.result." + key + " }}"
                        for key in ["batch", "readings", "meta"]
                    },
                    "inputSchema": data,
                    "outputSchema": schema(
                        {
                            "batch": S,
                            "total": {"type": "number"},
                            "unit": S,
                            "valid": {"type": "boolean"},
                        }
                    ),
                },
            ]
            config["executors"] = {
                key: {
                    "command": command,
                    "workspace": ".",
                    "env": {"FARM_SITE": "orchard-2"},
                }
                for key, command in [
                    ("collect", ["python3", "collect.py"]),
                    ("report", ["node", "report.js"]),
                ]
            }
            flow = {
                "version": "2",
                "name": case,
                "entry": "collect",
                "inputs": {"batch": {"type": "string", "required": True}},
                "nodes": nodes,
                "edges": [
                    {"from": "collect", "to": "report"},
                    {"from": "report", "to": "$end"},
                ],
            }
            roles = {key: key for key in ["collect", "report"]}
        (root / "workflow.json").write_text(
            json.dumps(flow, ensure_ascii=False, indent=2) + "\n"
        )
        (root / "executors.json").write_text(json.dumps(config, indent=2) + "\n")
        (root / "submission.json").write_text(
            json.dumps({"workflow": "workflow.json", "roles": roles}, indent=2) + "\n"
        )
        (root / "RUNBOOK.md").write_text(
            "Reference solution for deterministic evaluator self-test. Not participant context.\n"
        )
