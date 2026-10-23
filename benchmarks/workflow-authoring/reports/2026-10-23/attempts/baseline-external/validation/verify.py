"""Exercise real CLI persistence and routing with local substitutes for model nodes."""
import json
import shlex
import subprocess
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
COMMANDS = []
RESULTS = []


def cli(*arguments, expected=0):
    command = [str(ROOT / 'agentflow'), *arguments, '--format', 'json']
    result = subprocess.run(command, cwd=ROOT, capture_output=True, text=True)
    COMMANDS.append({'command': shlex.join(command), 'exit_code': result.returncode})
    assert result.returncode == expected, (command, result.returncode, result.stdout, result.stderr)
    return json.loads(result.stdout if result.stdout.strip() else result.stderr)


def start(topic='test topic'):
    args = ['run', 'start', 'validation/workflow.test.json']
    if topic is not None:
        args += ['--input', 'topic=' + topic]
    return cli(*args)


def submit(run_id, output, expected=0):
    return cli('run', 'submit', run_id, 'review', '--output', json.dumps(output), expected=expected)


def review(mood, feedback='local review', confidence=0.9):
    return {'mood': mood, 'feedback': feedback, 'confidence': confidence}


def waiting(run):
    assert run['status'] == 'waiting_input' and run['active_node'] == 'review', run


def done(run, finalized):
    assert run['status'] == 'succeeded', run
    assert (run['nodes']['finalize']['status'] == 'succeeded') == finalized, run


def record(case, run):
    RESULTS.append({'case': case, 'run_id': run.get('run_id'), 'status': run.get('status'), 'visits': run.get('visits')})


def main():
    production = json.loads((ROOT / 'workflow.json').read_text())
    test = json.loads(json.dumps(production))
    test['name'] = 'durable-copy-review-local-test'
    for node in test['nodes']:
        if node['executor'] == 'codex':
            node['executor'] = 'process'
            node['input']['command'] = ['python3', 'validation/model_stub.py', node['id']]
            if 'inputSchema' in node:
                node['inputSchema']['properties']['command'] = {'type': 'array', 'items': {'type': 'string'}}
    (ROOT / 'validation/workflow.test.json').write_text(json.dumps(test, ensure_ascii=False, indent=2) + '\n')
    assert cli('workflow', 'validate', 'workflow.json')['valid']
    assert cli('workflow', 'validate', 'validation/workflow.test.json')['valid']
    preview = cli('run', 'start', 'workflow.json', '--input', 'topic=preview topic', '--dry-run', expected=10)
    assert all(n['input'].get('topic') == 'preview topic' for n in preview['nodes'])

    # Required run input is checked by the entry node before its executor starts.
    for input_args in [[], ['--input', 'topic=']]:
        run = cli('run', 'start', 'validation/workflow.test.json', *input_args, expected=1)
        assert run['status'] == 'failed', run
        assert run['nodes']['revision']['status'] == 'failed', run
        assert run['nodes']['draft']['status'] == 'pending', run
        record('missing or empty topic rejected before any model step', run)

    # Each CLI invocation is a different process; no parent worker stays alive.
    run = start('persistent review')
    waiting(run)
    rid = run['run_id']
    restored = cli('run', 'get', rid)
    waiting(restored)
    assert restored['nodes']['review']['input'] == run['nodes']['review']['input']
    resumed = cli('run', 'resume', rid)
    waiting(resumed)
    assert resumed['visits'] == run['visits']
    run = submit(rid, review('go', confidence=0.8))
    done(run, True)
    assert run['nodes']['finalize']['input']['confidence'] == 0.8
    record('process exit/get/resume/submit, go boundary accepted', run)

    run = start('stop topic')
    run = submit(run['run_id'], review('stop', confidence=0))
    done(run, False)
    assert run['visits']['draft'] == 1
    record('stop succeeds without finalize or rewriting', run)

    run = start('low confidence topic')
    rid = run['run_id']
    old_summary = run['nodes']['draft']['result']['summary']
    run = submit(rid, review('go', 'add examples', 0.799999))
    waiting(run)
    assert run['visits']['draft'] == 2
    assert run['nodes']['draft']['input']['feedback'] == 'add examples'
    assert run['nodes']['draft']['input']['previous_summary'] == old_summary
    assert run['nodes']['review']['input']['reworks'] == 1
    run = submit(rid, review('go', confidence=1))
    done(run, True)
    record('low go loops with typed feedback and prior draft, then high go finalizes', run)

    run = start('bounded back topic')
    rid = run['run_id']
    for iteration in [1, 2]:
        run = submit(rid, review('back', f'change {iteration}', 1))
        waiting(run)
        assert run['visits']['draft'] == iteration + 1
        assert run['nodes']['review']['input']['reworks'] == iteration
        assert run['nodes']['draft']['input']['feedback'] == f'change {iteration}'
    run = submit(rid, review('back', 'still needs work', 1))
    done(run, False)
    assert run['visits']['draft'] == 3 and run['visits']['review'] == 3
    record('back reworks twice; third request ends normally', run)

    run = start('bounded low confidence topic')
    rid = run['run_id']
    for iteration in [1, 2]:
        run = submit(rid, review('go', f'uncertain {iteration}', 0))
        waiting(run)
    run = submit(rid, review('go', 'still uncertain', 0.79))
    done(run, False)
    assert run['visits']['draft'] == 3
    record('low-confidence go limited to two reworks', run)

    run = start('approval after last revision')
    rid = run['run_id']
    for _ in range(2):
        run = submit(rid, review('back'))
        waiting(run)
    run = submit(rid, review('go', confidence=0.8))
    done(run, True)
    record('high go still finalizes after second rework', run)

    run = start('stop after revisions')
    rid = run['run_id']
    for _ in range(2):
        run = submit(rid, review('back'))
        waiting(run)
    run = submit(rid, review('stop', confidence=1))
    done(run, False)
    record('stop after revisions ends normally', run)

    run = start('strict review contract')
    rid = run['run_id']
    invalid = [
        {},
        {'mood': 'approve', 'feedback': '', 'confidence': 0.9},
        {'decision': 'go', 'feedback': '', 'confidence': 0.9},
        {'mood': 'go', 'feedback': None, 'confidence': 0.9},
        {'mood': 'go', 'feedback': 1, 'confidence': 0.9},
        {'mood': 'go', 'feedback': '', 'confidence': '0.9'},
        {'mood': 'go', 'feedback': '', 'confidence': True},
        {'mood': 'go', 'feedback': '', 'confidence': -0.01},
        {'mood': 'go', 'feedback': '', 'confidence': 1.01},
        {'mood': 'go', 'feedback': '', 'confidence': 0.9, 'extra': 1},
        [],
    ]
    for output in invalid:
        rejection = submit(rid, output, expected=2)
        assert rejection.get('ok') is False, rejection
        current = cli('run', 'get', rid)
        waiting(current)
        assert current['visits'] == run['visits'], current
    run = submit(rid, review('stop', feedback='', confidence=0))
    done(run, False)
    record('11 invalid payloads rejected without changing wait; valid stop works', run)


try:
    main()
    print(json.dumps({'ok': True, 'cases': RESULTS, 'cli_invocations': len(COMMANDS)}, ensure_ascii=False, indent=2))
finally:
    (ROOT / 'validation/commands.json').write_text(json.dumps(COMMANDS, ensure_ascii=False, indent=2) + '\n')
    (ROOT / 'validation/results.json').write_text(json.dumps(RESULTS, ensure_ascii=False, indent=2) + '\n')
