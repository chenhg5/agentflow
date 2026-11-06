#!/usr/bin/env python3
"""Deterministic local validation; invokes only the bundled CLI and fixtures."""
import json
import os
from pathlib import Path
import subprocess

root = Path(__file__).resolve().parent
outdir = root / 'validation'
outdir.mkdir(exist_ok=True)
report = []

def call(arguments, measurement=None):
    env = os.environ.copy()
    env.pop('BENCH_MEASUREMENT', None)
    if measurement is not None:
        env['BENCH_MEASUREMENT'] = json.dumps(measurement)
    proc = subprocess.run(['./agentflow', *arguments], cwd=root, env=env, text=True, capture_output=True)
    data = json.loads(proc.stdout or proc.stderr)
    return proc.returncode, data

code, data = call(['workflow', 'validate', 'workflow.json', '--format', 'json'])
assert code == 0 and data['valid'], data
(outdir / 'workflow-validation.json').write_text(json.dumps(data, indent=2) + '\n')

code, data = call(['run', 'start', 'workflow.json', '--format', 'json'])
assert code == 2 and data['error']['code'] == 'missing_input', data
report.append({'case': 'required-sample', 'expected': 'missing_input', 'passed': True})

cases = [
    ('above-80-clean', 81, [], False, 'accept'),
    ('fraction-above-80-clean', 80.5, [], False, 'accept'),
    ('at-80-clean', 80, [], False, 'recheck'),
    ('above-80-defect', 90, ['scratch'], False, 'recheck'),
    ('at-50', 50, ['scratch'], False, 'recheck'),
    ('below-50', 49.9, [], False, None),
    ('below-50-defect', 49, ['scratch'], False, None),
    ('below-50-urgent', 20, ['scratch', 'dent'], True, 'recheck'),
    ('accept-priority-over-urgent', 81, [], True, 'accept'),
]
for name, score, defects, urgent, branch in cases:
    measurement = {'score': score, 'defects': defects, 'urgent': urgent}
    args = ['run', 'start', 'workflow.json', '--input', f'sample={name}', '--format', 'json']
    code, data = call(args, measurement)
    assert code == 0 and data['status'] == 'succeeded', (name, data)
    assert data['nodes']['measure']['input'] == {'sample': name}
    expected_visits = {'measure': 1}
    if branch:
        expected_visits[branch] = 1
        expected = {'branch': branch, **measurement}
        state = data['nodes'][branch]
        assert state['input'] == expected and state['result'] == expected, (name, state)
        assert isinstance(state['input']['score'], (int, float))
        assert type(state['input']['urgent']) is bool
        assert isinstance(state['input']['defects'], list)
    assert data['visits'] == expected_visits, (name, data['visits'])
    for candidate in ['accept', 'recheck']:
        if candidate != branch:
            assert data['nodes'][candidate]['status'] == 'skipped', (name, candidate)
    report.append({'case': name, 'measurement': measurement, 'expected_branch': branch, 'run_id': data['run_id'], 'passed': True})
    (outdir / f'{name}.json').write_text(json.dumps(data, indent=2) + '\n')

invalid = [
    ('score-string', {'score': '81', 'defects': [], 'urgent': False}),
    ('defect-item-number', {'score': 81, 'defects': [3], 'urgent': False}),
    ('urgent-string', {'score': 81, 'defects': [], 'urgent': 'false'}),
]
for name, measurement in invalid:
    code, data = call(['run', 'start', 'workflow.json', '--input', f'sample={name}', '--format', 'json'], measurement)
    assert code == 1 and data['status'] == 'failed', (name, data)
    assert 'output schema validation failed' in data['nodes']['measure']['error'], (name, data)
    assert data['nodes']['accept']['status'] == 'pending' and data['nodes']['recheck']['status'] == 'pending'
    report.append({'case': name, 'expected': 'output schema validation failed', 'run_id': data['run_id'], 'passed': True})

workflow = json.loads((root / 'workflow.json').read_text())
assert all('prompt' not in node and 'prompt' not in node['input'] for node in workflow['nodes'])
assert {node['executor'] for node in workflow['nodes']} == {'quality-measure', 'quality-finish'}
(outdir / 'results.json').write_text(json.dumps(report, indent=2) + '\n')
print(json.dumps({'passed': len(report), 'results': 'validation/results.json'}, indent=2))
