"""Run only deterministic substitutes; each CLI command is a fresh process."""
import json
import subprocess
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
EVIDENCE = ROOT / 'validation'
EVIDENCE.mkdir(exist_ok=True)
records = []

def call(label, *args, expected=0):
    command = ['./agentflow', *args, '--format', 'json']
    completed = subprocess.run(command, cwd=ROOT, text=True, capture_output=True)
    record = {'label': label, 'command': command, 'exit_code': completed.returncode,
              'stdout': completed.stdout, 'stderr': completed.stderr}
    records.append(record)
    (EVIDENCE / 'commands.json').write_text(json.dumps(records, ensure_ascii=False, indent=2) + '\n')
    if expected is not None:
        assert completed.returncode == expected, record
    raw = completed.stdout if completed.stdout.strip() else completed.stderr
    try:
        result = json.loads(raw)
    except json.JSONDecodeError:
        result = {}
    return completed.returncode, result

def start(label):
    _, state = call(label + '-start', 'run', 'start', 'tests/workflow.local.json', '--input', 'topic=' + label)
    assert state['status'] == 'waiting_input', state
    assert state['active_node'] == 'review', state
    assert state['visits'] == {'draft': 1, 'review': 1}, state
    assert state['nodes']['draft']['input']['feedback'] is None
    return state['run_id']

def submit(label, run_id, mood, confidence, feedback='local feedback', expected=0):
    return call(label, 'run', 'submit', run_id, 'review', '--output', json.dumps({
        'mood': mood, 'feedback': feedback, 'confidence': confidence}, ensure_ascii=False), expected=expected)

call('production-validate', 'workflow', 'validate', 'workflow.json', '--check-executors')
call('test-validate', 'workflow', 'validate', 'tests/workflow.local.json', '--check-executors')
call('production-preview', 'run', 'start', 'workflow.json', '--input', 'topic=preview only', '--dry-run', expected=10)
code, error = call('topic-required', 'run', 'start', 'workflow.json', '--dry-run', expected=None)
assert code != 0 and code != 10, error
call('doctor', 'doctor')

run_id = start('persist-and-threshold')
_, reloaded = call('fresh-process-get', 'run', 'get', run_id)
assert reloaded['status'] == 'waiting_input'
_, resumed = call('fresh-process-resume', 'run', 'resume', run_id)
assert resumed['status'] == 'waiting_input'
assert resumed['visits'] == {'draft': 1, 'review': 1}
_, done = submit('threshold-0.8-go', run_id, 'go', 0.8)
assert done['status'] == 'succeeded' and done['nodes']['finalize']['status'] == 'succeeded'
assert done['nodes']['finalize']['input']['summary'] == done['nodes']['draft']['result']['summary']

run_id = start('stop-normal')
_, done = submit('stop-succeeds', run_id, 'stop', 0)
assert done['status'] == 'succeeded' and done['outcome'] == 'completed'
assert done['nodes']['finalize']['status'] == 'skipped'
assert done['visits']['draft'] == 1

run_id = start('back-feedback')
_, waiting = submit('back-with-high-confidence', run_id, 'back', 1, '请补充明确卖点')
assert waiting['status'] == 'waiting_input' and waiting['visits']['draft'] == 2
assert waiting['nodes']['draft']['input']['feedback'] == '请补充明确卖点'
assert '请补充明确卖点' in waiting['nodes']['draft']['input']['prompt']
assert waiting['nodes']['review']['input']['summary'] == waiting['nodes']['draft']['result']['summary']
_, done = submit('back-then-stop', run_id, 'stop', 1)
assert done['status'] == 'succeeded' and done['nodes']['finalize']['status'] == 'skipped'

run_id = start('low-confidence-and-two-returns')
_, waiting = submit('low-go-first-return', run_id, 'go', 0.799999, 'first revision')
assert waiting['status'] == 'waiting_input' and waiting['visits']['draft'] == 2
assert waiting['nodes']['draft']['input']['feedback'] == 'first revision'
_, waiting = submit('back-second-return', run_id, 'back', 0, 'second revision')
assert waiting['status'] == 'waiting_input' and waiting['visits']['draft'] == 3
assert waiting['nodes']['draft']['input']['feedback'] == 'second revision'
_, done = submit('accept-after-two-returns', run_id, 'go', 1)
assert done['status'] == 'succeeded' and done['visits']['draft'] == 3
assert done['nodes']['finalize']['status'] == 'succeeded'

run_id = start('exhaustion')
submit('exhaustion-return-1', run_id, 'back', 0)
submit('exhaustion-return-2', run_id, 'go', 0)
code, failed = submit('exhaustion-return-3-rejected', run_id, 'back', 1, expected=None)
_, failed = call('exhaustion-persisted-state', 'run', 'get', run_id)
assert failed['status'] == 'failed', failed
assert failed['outcome'] == 'max_iterations', failed
assert failed['visits']['draft'] == 3, failed
assert failed['nodes']['finalize']['status'] != 'succeeded' and 'finalize' not in failed['visits'], failed

run_id = start('strict-contract')
invalids = [
    {},
    {'mood': 'approve', 'feedback': 'x', 'confidence': 0.9},
    {'decision': 'go', 'feedback': 'x', 'confidence': 0.9},
    {'mood': 'go', 'feedback': 'x'},
    {'mood': 'go', 'confidence': 0.9},
    {'mood': 'go', 'feedback': 3, 'confidence': 0.9},
    {'mood': 'go', 'feedback': 'x', 'confidence': '0.9'},
    {'mood': 'go', 'feedback': 'x', 'confidence': True},
    {'mood': 'go', 'feedback': 'x', 'confidence': -0.1},
    {'mood': 'go', 'feedback': 'x', 'confidence': 1.1},
    {'mood': 'go', 'feedback': 'x', 'confidence': 0.9, 'extra': 'forbidden'},
    [],
    None,
]
for index, payload in enumerate(invalids):
    code, error = call('invalid-payload-' + str(index), 'run', 'submit', run_id, 'review', '--output', json.dumps(payload), expected=None)
    assert code != 0, (payload, error)
    _, still_waiting = call('invalid-unchanged-' + str(index), 'run', 'get', run_id)
    assert still_waiting['status'] == 'waiting_input'
    assert still_waiting['visits'] == {'draft': 1, 'review': 1}
_, done = submit('valid-stop-after-invalids', run_id, 'stop', 0.5)
assert done['status'] == 'succeeded'

summary = {
    'passed': True,
    'cli_invocations': len(records),
    'invalid_payloads_rejected': len(invalids),
    'real_models_called': False,
    'checks': ['workflow validation', 'dry-run exit 10', 'required topic',
               'fresh-process durable reload and resume', 'go at confidence 0.8',
               'stop normal success', 'back independent of confidence', 'go below threshold returns',
               'feedback in next draft input and prompt', 'review sees latest draft',
               'two returns then acceptance', 'third return fails max_iterations without fourth draft',
               'strict review schema rejects malformed output and preserves pause'],
}
(EVIDENCE / 'summary.json').write_text(json.dumps(summary, ensure_ascii=False, indent=2) + '\n')
print(json.dumps(summary, ensure_ascii=False, indent=2))
