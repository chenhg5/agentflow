#!/usr/bin/env python3
"""Positive and negative probes for the migration contract grader, no model calls."""
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile

from migration import ROOT, grade, prepare, write

BINARY = Path(os.environ.get('AGENTFLOW_BINARY', ROOT / 'bin/agentflow')).resolve()


def golden(work):
    prepare(work, BINARY)
    def cli(*args):
        r = subprocess.run([str(BINARY), *args, '--format', 'json'], cwd=work, capture_output=True, text=True)
        assert r.returncode == 0, r.stderr
    cli('workspace', 'init')
    cli('executor', 'init', 'review-claude', '--kind', 'claude-code')
    cli('executor', 'init', 'review-codex', '--kind', 'codex')
    cli('executor', 'init', 'load', '--kind', 'command')
    cli('executor', 'init', 'archive', '--kind', 'command')
    registry = json.loads((work / 'executors.json').read_text())
    registry['executors'].pop('step')
    registry['executors']['human'] = {'kind': 'external'}
    write(work / 'executors.json', registry)
    shutil.rmtree(work / 'executors/programs/step')
    for name, native in [('review-claude', 'CLAUDE.md'), ('review-codex', 'AGENTS.md')]:
        (work / 'executors/agents' / name / native).write_text('Review frozen PR patches under ../../../policy.md. Use workflow_tools.read_input and the local workflow-contract skill. Preserve pr_id/head/patch/feedback. Produce findings explaining default behavior and evidence; leave human decisions to the graph.\n')
    (work / 'executors/programs/load/main.py').write_text("import json,os\nfrom pathlib import Path\ni=json.loads(Path(os.environ['AGENTFLOW_INPUT_FILE']).read_text())\nPath(os.environ['AGENTFLOW_RESULT_FILE']).write_text(Path(i['case']).read_text())\n")
    (work / 'executors/programs/archive/main.py').write_text("import json,os\nfrom pathlib import Path\ni=json.loads(Path(os.environ['AGENTFLOW_INPUT_FILE']).read_text())\np=Path(os.environ['AGENTFLOW_ROOT'])/'output/report.json'\np.parent.mkdir(parents=True,exist_ok=True)\np.write_text(json.dumps(i))\nPath(os.environ['AGENTFLOW_RESULT_FILE']).write_text(json.dumps({'artifact':'output/report.json'}))\n")
    def shape(props):
        return {'type': 'object', 'required': list(props), 'properties': props, 'additionalProperties': False}
    fields = {key: {'type': 'string'} for key in ['pr_id', 'head', 'patch', 'feedback']}
    input_shape = shape(fields)
    output_shape = shape(dict(fields, findings={'type': 'array', 'items': {'type': 'string'}}))
    human_shape = shape({'action': {'type': 'string', 'enum': ['accept', 'revise', 'stop']}, 'comments': {'type': 'string'}})
    def execution(name):
        return {'candidates': [{'executor': name}]}
    flow = {'version': '3', 'name': 'migration-golden', 'entry': 'load', 'inputs': {'case': {'type': 'string', 'required': True}}, 'nodes': [
        {'id': 'load', 'execution': execution('load'), 'outputSchema': input_shape},
        {'id': 'review', 'inputSchema': input_shape, 'outputSchema': output_shape, 'execution': {'candidates': [{'executor': name, 'session': {'mode': 'fixed'}, 'config': {'instruction': 'Review this PR under the persistent role instructions.'}} for name in ['review-claude', 'review-codex']]}},
        {'id': 'human', 'inputSchema': output_shape, 'outputSchema': human_shape, 'execution': execution('human')},
        {'id': 'archive', 'execution': execution('archive')}], 'edges': [
        {'from': 'load', 'to': 'review'}, {'from': 'review', 'to': 'human'},
        {'from': 'human', 'to': 'review', 'when': "output.action == 'revise'", 'maxIterations': 2, 'map': {**{key: '{{ input.' + key + ' }}' for key in ['pr_id', 'head', 'patch']}, 'feedback': '{{ output.comments }}'}},
        {'from': 'human', 'to': 'archive', 'when': "output.action == 'accept'", 'map': {**{key: '{{ input.' + key + ' }}' for key in ['pr_id', 'head', 'patch', 'feedback', 'findings']}, 'action': '{{ output.action }}', 'comments': '{{ output.comments }}'}},
        {'from': 'human', 'to': '$end'}, {'from': 'archive', 'to': '$end'}]}
    write(work / 'workflow.json', flow)
    write(work / 'submission.json', {'workflow': 'workflow.json', 'roles': {'review': 'review', 'human': 'human', 'archive': 'archive'}})


def main():
    with tempfile.TemporaryDirectory(prefix='agentflow-migration-selftest-') as temporary:
        root = Path(temporary)
        baseline = root / 'golden'
        golden(baseline)
        result = grade(baseline, BINARY)
        assert result['contract_passed'], result
        mutations = ['missing-feedback', 'missing-head', 'invalid-human-contract', 'skipped-review', 'missing-role-resources', 'overbroad-fallback']
        for mutation in mutations:
            work = root / mutation
            shutil.copytree(baseline, work)
            flow = json.loads((work / 'workflow.json').read_text())
            if mutation in ('missing-feedback', 'missing-head'):
                field = 'feedback' if mutation == 'missing-feedback' else 'head'
                next(edge for edge in flow['edges'] if edge['from'] == 'human' and edge['to'] == 'review')['map'].pop(field)
            elif mutation == 'invalid-human-contract':
                next(n for n in flow['nodes'] if n['id'] == 'human')['outputSchema']['properties']['action'].pop('enum')
            elif mutation == 'skipped-review':
                flow['edges'][0]['to'] = '$end'
            elif mutation == 'overbroad-fallback':
                next(n for n in flow['nodes'] if n['id'] == 'review')['execution']['fallback'] = {'when': 'true'}
            else:
                (work / 'executors/agents/review-claude/CLAUDE.md').unlink()
            write(work / 'workflow.json', flow)
            bad = grade(work, BINARY)
            assert not bad['contract_passed'], (mutation, bad)
        print(json.dumps({'positive_checks': len(result['checks']), 'negative_mutations_rejected': len(mutations), 'native_model_verified': False}))


if __name__ == '__main__':
    main()
