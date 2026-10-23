"""Exercise the delivered workflow with Codex replaced locally, then restore it."""
import json
import os
import subprocess
from pathlib import Path

root = Path(__file__).resolve().parent.parent
assert Path.cwd().resolve() == root
out = root / 'tests/evidence'
out.mkdir(parents=True, exist_ok=True)
config_path = root / '.agentflow/executors.json'
original = config_path.read_text()
config = json.loads(original)
config['executors']['codex'] = {'kind': 'command', 'command': ['python3', './tests/model_stub.py'], 'workspace': '.'}
report = []

def cli(args, name, env=None, codes=(0,)):
    result = subprocess.run([str(root/'agentflow'), *args], env=env, capture_output=True, text=True)
    (out / (name+'.stdout.json')).write_text(result.stdout)
    (out / (name+'.stderr.txt')).write_text(result.stderr)
    assert result.returncode in codes, (name, result.returncode, result.stdout, result.stderr)
    payload = result.stdout.strip() or result.stderr.strip()
    return json.loads(payload), result.returncode

try:
    config_path.write_text(json.dumps(config, indent=2)+'\n')
    scenarios = [
        ('happy', [{'signal':'happy','note':'approved-initial'}], [None], 'succeeded', 1, 1),
        ('sad', [{'signal':'sad','note':'stop-initial'}], [None], 'succeeded', 1, 0),
        ('again_happy', [{'signal':'again','note':'revise-one'},{'signal':'happy','note':'approved-second'}], [None,'revise-one'], 'succeeded', 2, 1),
        ('twice_again_happy', [{'signal':'again','note':'revise-one'},{'signal':'again','note':'revise-two'},{'signal':'happy','note':'approved-third'}], [None,'revise-one','revise-two'], 'succeeded', 3, 1),
        ('exhausted', [{'signal':'again','note':'revise-one'},{'signal':'again','note':'revise-two'},{'signal':'again','note':'revise-three'}], [None,'revise-one','revise-two'], 'failed', 3, 0),
        ('invalid_review', [{'signal':'unknown','note':'bad-output'}], [None], 'failed', 1, 0),
    ]
    for name, responses, feedback, status, draft_count, final_count in scenarios:
        paths = {key:out/(name+suffix) for key,suffix in [('counter','.model-count.txt'),('channel','.channel-count.txt'),('log','.model.jsonl')]}
        for p in paths.values():
            p.unlink(missing_ok=True)
        env = dict(os.environ, BENCH_RESPONSES=json.dumps(responses), BENCH_CHANNEL_COUNTER=str(paths['channel']), TEST_MODEL_COUNTER=str(paths['counter']), TEST_MODEL_LOG=str(paths['log']), TEST_EXPECTED_NOTES=json.dumps(feedback), TEST_FINAL_NOTE=responses[-1]['note'])
        started, code = cli(['run','start','workflow.json','--input','topic=deterministic local test','--input','reviewer=reviewer-local','--format','json'], name+'.start', env, (0,1))
        run_id = started.get('run',{}).get('id') or started.get('run_id') or started.get('id')
        assert run_id, started
        run, _ = cli(['run','get',run_id,'--format','json'],name+'.get',env)
        state = run.get('run',run)
        assert state['status'] == status, state
        log = [json.loads(line) for line in paths['log'].read_text().splitlines()]
        assert sum(c['role']=='draft' for c in log) == draft_count
        assert sum(c['role']=='finalize' for c in log) == final_count
        assert int(paths['channel'].read_text()) == draft_count
        assert state['visits']['draft'] == draft_count
        assert state['visits']['review'] == draft_count
        if name == 'exhausted':
            assert state['outcome'] == 'max_iterations'
        if name == 'invalid_review':
            assert 'output schema validation failed' in state['nodes']['review']['error']
        reviews = [h['state']['input'] for h in state['history'] if h['nodeId'] == 'review']
        if state['nodes']['review']['status'] == 'failed':
            reviews.append(state['nodes']['review']['input'])
        for revision, request in enumerate(reviews, 1):
            assert request['recipient'] == 'reviewer-local'
            assert request['content']['summary'] == f'proposal-v{revision}'
            assert all(set(action) == {'label', 'output'} for action in request['actions'])
        report.append({'scenario':name,'run_id':run_id,'status':state['status'],'draft_visits':draft_count,'finalize_visits':final_count,'start_exit':code})
finally:
    config_path.write_text(original)
(out/'report.json').write_text(json.dumps(report,indent=2)+'\n')
print(json.dumps(report,indent=2))
