#!/usr/bin/env python3
"""Repeat deterministic checks using only local AgentFlow and supplied fixtures."""
import json
import os
import subprocess
from pathlib import Path

ROOT = Path.cwd()
EVIDENCE = ROOT / 'validation'
EVIDENCE.mkdir(exist_ok=True)
checks = []

def invoke(name, args, measurement=None):
    env = os.environ.copy()
    env.pop('BENCH_MEASUREMENT', None)
    if measurement is not None:
        env['BENCH_MEASUREMENT'] = json.dumps(measurement)
    result = subprocess.run(['./agentflow', *args], cwd=ROOT, env=env, capture_output=True, text=True)
    (EVIDENCE / f'{name}.stdout.json').write_text(result.stdout)
    (EVIDENCE / f'{name}.stderr.txt').write_text(result.stderr)
    payload = json.loads(result.stdout or result.stderr)
    return result.returncode, payload

def check(name, okay, **details):
    checks.append({'name': name, 'passed': okay, **details})
    if not okay:
        raise AssertionError(f'{name}: {details}')

rc, payload = invoke('validate', ['workflow', 'validate', 'workflow.json', '--check-executors', '--format', 'json'])
check('workflow-and-executors', rc == 0 and payload.get('valid') is True, exit_code=rc)
rc, payload = invoke('doctor', ['doctor', '--format', 'json'])
registered = {item['name']: item for item in payload['executor_status']}
check('local-executors', rc == 0 and all(registered[name]['kind'] == 'command' and registered[name]['available'] for name in ['quality-measure', 'quality-finish']), exit_code=rc)
rc, payload = invoke('dry-run', ['run', 'start', 'workflow.json', '--input', 'sample=dry-run', '--dry-run', '--format', 'json'])
check('dry-run', rc == 10, exit_code=rc)

cases = [
    ('accept-default', 81, [], False, 'accept'),
    ('accept-priority-over-urgent', 81, [], True, 'accept'),
    ('accept-fractional-score', 80.1, [], False, 'accept'),
    ('recheck-score-80', 80, [], False, 'recheck'),
    ('recheck-high-score-with-defects', 99, ['scratch', 'dent'], False, 'recheck'),
    ('recheck-score-50', 50, [], False, 'recheck'),
    ('recheck-urgent-low-score', 49.5, ['scratch'], True, 'recheck'),
    ('recheck-urgent-negative-score', -1, [], True, 'recheck'),
    ('end-below-50', 49.9, [], False, None),
    ('end-low-score-with-defects', 20, ['crack'], False, None),
]
for name, score, defects, urgent, branch in cases:
    measurement = {'score': score, 'defects': defects, 'urgent': urgent}
    sample = '样本 A / ' + name
    rc, payload = invoke(name, ['run', 'start', 'workflow.json', '--input', f'sample={sample}', '--format', 'json'], measurement)
    expected_visits = ['measure'] + ([branch] if branch else [])
    actual_visits = [visit['nodeId'] for visit in payload.get('history', [])]
    okay = rc == 0 and payload.get('status') == 'succeeded' and payload.get('outcome') == 'completed' and actual_visits == expected_visits
    okay = okay and payload['nodes']['measure']['input'] == {'sample': sample}
    okay = okay and payload['nodes']['measure']['result'] == measurement
    if branch:
        expected = {**measurement, 'branch': branch}
        node = payload['nodes'][branch]
        okay = okay and node['input'] == expected and node['result'] == expected
        result = node['result']
        okay = okay and type(result['score']) in (int, float) and type(result['defects']) is list and type(result['urgent']) is bool
    for candidate in ['accept', 'recheck']:
        if candidate != branch:
            okay = okay and payload['nodes'][candidate]['status'] == 'skipped'
    check(name, okay, exit_code=rc, run_id=payload.get('run_id'), visits=actual_visits)

rc, payload = invoke('missing-sample', ['run', 'start', 'workflow.json', '--format', 'json'])
check('missing-required-sample', rc != 0 and payload.get('ok') is False, exit_code=rc, error=payload)
invalid_cases = [
    ('invalid-score-string', {'score': '81', 'defects': [], 'urgent': False}),
    ('invalid-defects-item', {'score': 81, 'defects': [7], 'urgent': False}),
    ('invalid-urgent-string', {'score': 81, 'defects': [], 'urgent': 'false'}),
]
for name, measurement in invalid_cases:
    rc, payload = invoke(name, ['run', 'start', 'workflow.json', '--input', 'sample=invalid-contract-test', '--format', 'json'], measurement)
    okay = rc != 0 and payload.get('status') == 'failed'
    okay = okay and payload['nodes']['measure']['status'] == 'failed'
    okay = okay and payload.get('visits') == {'measure': 1}
    okay = okay and all(payload['nodes'][node]['status'] == 'pending' for node in ['accept', 'recheck'])
    okay = okay and 'output schema validation failed' in payload['nodes']['measure'].get('error', '')
    check(name, okay, exit_code=rc, run_id=payload.get('run_id'), error=payload['nodes']['measure'].get('error'))

summary = {'passed': all(item['passed'] for item in checks), 'check_count': len(checks), 'checks': checks}
(EVIDENCE / 'summary.json').write_text(json.dumps(summary, ensure_ascii=False, indent=2) + '\n')
print(json.dumps(summary, ensure_ascii=False, indent=2))
