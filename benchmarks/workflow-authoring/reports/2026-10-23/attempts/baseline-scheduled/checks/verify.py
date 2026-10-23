"""Local checks; model nodes are external JSON submissions, never model CLIs."""
import json
import os
from pathlib import Path
import subprocess
import uuid

ROOT = Path(__file__).resolve().parent.parent
COMMANDS = ROOT / 'checks/validation-commands.ndjson'
COMMANDS.write_text('')

def call(args, env=None, expected=0):
    cmd = ['./agentflow', *args]
    result = subprocess.run(cmd, cwd=ROOT, env=env, text=True, capture_output=True)
    with COMMANDS.open('a') as log:
        log.write(json.dumps({'command': cmd, 'exit_code': result.returncode}, ensure_ascii=False) + '\n')
    assert result.returncode == expected, (cmd, result.returncode, result.stdout, result.stderr)
    return json.loads(result.stdout or result.stderr)

workflow = json.loads((ROOT/'workflow.json').read_text())
call(['workflow', 'validate', 'workflow.json', '--format', 'json'])
call(['trigger', 'validate', '--format', 'json'])
triggers = call(['trigger', 'list', '--format', 'json'])['triggers']
assert triggers[0]['source'] == {'kind': 'cron', 'expression': '15 9 * * 1-5', 'timezone': 'Asia/Shanghai'}
assert triggers[0]['enabled'] is True
assert set(triggers[0]['inputs']) == {'topic', 'reviewer'}
call(['run', 'start', 'workflow.json', '--input', 'topic=onboarding', '--input', 'reviewer=local-reviewer', '--dry-run', '--format', 'json'], expected=10)
for inputs in [[], ['--input', 'topic=onboarding'], ['--input', 'reviewer=local-reviewer']]:
    result = call(['run', 'start', 'workflow.json', *inputs, '--dry-run', '--format', 'json'], expected=2)
    assert result['error']['code'] == 'missing_input'

local = json.loads(json.dumps(workflow))
local['name'] += '-localcheck'
for node in local['nodes']:
    if node['executor'] == 'codex':
        node['executor'] = 'external'
local_path = ROOT / 'checks/workflow-external.json'
local_path.write_text(json.dumps(local, ensure_ascii=False, indent=2) + '\n')
call(['workflow', 'validate', 'checks/workflow-external.json', '--format', 'json'])

cases = {
    'happy': [{'signal':'happy', 'note':'ship v1'}],
    'sad': [{'signal':'sad', 'note':'archive this topic'}],
    'again-happy': [{'signal':'again', 'note':'add mobile details'}, {'signal':'happy', 'note':'mobile details accepted'}],
    'twice-happy': [{'signal':'again', 'note':'first refinement'}, {'signal':'again', 'note':'second refinement'}, {'signal':'happy', 'note':'third proposal accepted'}],
    'exhausted': [{'signal':'again', 'note':'revise again'}]*3,
    'invalid-review': [{'signal':'unknown', 'note':'invalid'}],
}
report = []
for label, responses in cases.items():
    counter = ROOT / f'checks/{label}-channel-count.txt'
    counter.unlink(missing_ok=True)
    env = dict(os.environ, BENCH_RESPONSES=json.dumps(responses), BENCH_CHANNEL_COUNTER=str(counter))
    state = call(['run', 'start', 'checks/workflow-external.json', '--input', 'topic=onboarding', '--input', 'reviewer=reviewer-42', '--format', 'json'], env)
    rid = state['run_id']
    assert state['nodes']['draft']['input']['review_note'] is None
    for visit, response in enumerate(responses, 1):
        assert state['active_node'] == 'draft', state
        if visit > 1:
            note = responses[visit-2]['note']
            assert state['nodes']['draft']['input']['review_note'] == note
            assert note in state['nodes']['draft']['input']['prompt']
        draft = {'summary': f'proposal-v{visit}', 'check': f'self-check-v{visit}'}
        expected = 1 if (label == 'exhausted' and visit == 3) or label == 'invalid-review' else 0
        state = call(['run', 'submit', rid, 'draft', '--output', json.dumps(draft), '--format', 'json'], env, expected)
        review_input = state['nodes']['review']['input']
        assert review_input['recipient'] == 'reviewer-42'
        assert review_input['content']['summary'] == draft['summary']
        assert review_input['content']['check'] == draft['check']
        assert {x['output']['signal'] for x in review_input['actions']} == {'happy','again','sad'}
        assert all(set(x) == {'label', 'output'} for x in review_input['actions'])
        if response['signal'] == 'happy':
            assert state['active_node'] == 'finalize'
            final_input = state['nodes']['finalize']['input']
            assert final_input['proposal'] == draft
            assert final_input['review'] == response
            assert all(value in final_input['prompt'] for value in [draft['summary'], draft['check'], response['note']])
            state = call(['run', 'submit', rid, 'finalize', '--output', json.dumps({'next_step':'test the change', 'verification':'measure activation'}), '--format', 'json'], env)
            assert state['status'] == 'succeeded'
        elif response['signal'] == 'sad':
            assert state['status'] == 'succeeded'
            assert state.get('visits', {}).get('finalize', 0) == 0
    if label == 'exhausted':
        assert state['status'] == 'failed' and state['outcome'] == 'max_iterations'
        assert state['visits']['draft'] == 3 and state['visits']['review'] == 3
        assert state['visits'].get('finalize', 0) == 0
    if label == 'invalid-review':
        assert state['status'] == 'failed' and state['nodes']['review']['status'] == 'failed'
        assert state['visits'].get('finalize', 0) == 0
    assert int(counter.read_text()) == len(responses)
    report.append({'case': label, 'run_id': rid, 'status': state['status'], 'outcome': state['outcome'], 'visits': state['visits']})

# Fire the same cron through a temporary external-model workflow target.
trigger_path = ROOT/'.agentflow/triggers.json'
original_triggers = trigger_path.read_text()
try:
    config = json.loads(original_triggers)
    config['triggers'][0]['target']['workflow'] = 'checks/workflow-external.json'
    trigger_path.write_text(json.dumps(config, indent=2)+'\n')
    call(['trigger', 'validate', '--format', 'json'])
    delivery_id = 'local-cron-validation-' + uuid.uuid4().hex
    fire_args = ['trigger', 'fire', 'weekday-research-review', '--scheduled-at', '2026-10-23T09:15:00+08:00', '--delivery-id', delivery_id, '--format', 'json']
    fired = call(fire_args)
    cron_run = call(['run', 'get', fired['run_id'], '--format', 'json'])
    assert cron_run['inputs'] == config['triggers'][0]['inputs']
    assert cron_run['trigger']['scheduledAt'] == '2026-10-23T01:15:00Z'
    assert cron_run['status'] == 'waiting_input' and cron_run['active_node'] == 'draft'
    duplicate = call(fire_args)
    assert duplicate['run_id'] == fired['run_id']
    call(['run', 'cancel', fired['run_id'], '--format', 'json'])
    (ROOT/'checks/trigger-fire.json').write_text(json.dumps(fired, indent=2)+'\n')
    report.append({'case':'cron-fire', 'receipt':fired})
finally:
    trigger_path.write_text(original_triggers)
call(['trigger', 'validate', '--format', 'json'])
(ROOT/'checks/validation-results.json').write_text(json.dumps(report, indent=2)+'\n')
print(json.dumps(report, indent=2))
