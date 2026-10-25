#!/usr/bin/env python3
"""Unhinted brownfield authoring case and executable graph-contract grading."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[2]
CASE = Path(__file__).parent / 'cases/pr-migration'


def write(path, value):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, ensure_ascii=False, indent=2) + '\n')


def prepare(target, binary):
    target = Path(target).resolve()
    shutil.copytree(CASE, target)
    shutil.copytree(ROOT / 'skills/agentflow', target / 'skill')
    shutil.copy2(binary, target / 'agentflow')
    (target / 'agentflow').chmod(0o755)
    write(target / 'attempt.json', {'case': 'pr-migration', 'binary_sha256': hashlib.sha256((target / 'agentflow').read_bytes()).hexdigest(), 'skill_sha256': hashlib.sha256((target / 'skill/SKILL.md').read_bytes()).hexdigest(), 'context': 'Fresh author; task and raw fixtures; no directory answer or private grader.'})
    return target


TRANSPORT = '''#!/usr/bin/env python3
import json,os
from pathlib import Path
incoming=json.loads(Path(os.environ['AGENTFLOW_INPUT_FILE']).read_text())
out={key:incoming[key] for key in ['pr_id','head','patch','feedback']}
out['findings']=['Default behavior changes from True to False.'] if "settings.get('enabled', False)" in incoming['patch'] else []
Path(os.environ['AGENTFLOW_RESULT_FILE']).write_text(json.dumps(out))
Path(os.environ['AGENTFLOW_SESSION_FILE']).write_text(os.environ.get('AGENTFLOW_SESSION_ID') or 'fixture-role-session')
'''


def grade(source, binary):
    source, binary = Path(source).resolve(), Path(binary).resolve()
    checks = []

    def check(name, condition):
        checks.append({'name': name, 'passed': bool(condition)})
        if not condition:
            raise AssertionError(name)

    try:
        submission = json.loads((source / 'submission.json').read_text())
        roles = submission['roles']
        workflow = json.loads((source / submission['workflow']).read_text())
        nodes = {node['id']: node for node in workflow['nodes']}
        original = json.loads((source / 'executors.json').read_text())['executors']
        candidates = nodes[roles['review']]['execution']['candidates']
        check('native reviewer candidates preserve Claude then Codex', [original[c['executor']].get('kind') for c in candidates] == ['claude-code', 'codex'])
        check('review candidates retain independent run-local sessions', all(c.get('session', {}).get('mode') == 'fixed' for c in candidates))
        check('human waits for external input', original[nodes[roles['human']]['execution']['candidates'][0]['executor']].get('kind') == 'external')
        check('contracts describe reviewer input and output', bool(nodes[roles['review']].get('inputSchema')) and bool(nodes[roles['review']].get('outputSchema')))
        with tempfile.TemporaryDirectory(prefix='agentflow-migration-grade-') as temporary:
            sandbox = Path(temporary)
            transport = sandbox / 'transport'
            transport.write_text(TRANSPORT)
            transport.chmod(0o755)
            failed_transport = sandbox / 'failed-transport'
            failed_transport.write_text('#!/usr/bin/env python3\nraise SystemExit(7)\n')
            failed_transport.chmod(0o755)

            def cli(work, *args, success=True):
                result = subprocess.run([str(binary), *args, '--format', 'json'], cwd=work, capture_output=True, text=True)
                if success and result.returncode:
                    raise AssertionError(result.stderr or result.stdout)
                return json.loads(result.stdout) if result.returncode == 0 else None

            for scenario in ['accept', 'revise', 'stop', 'variant', 'fallback', 'started-error']:
                action = 'accept' if scenario in ['variant', 'fallback', 'started-error'] else scenario
                work = sandbox / scenario
                shutil.copytree(source, work, ignore=shutil.ignore_patterns('runs', 'dispatch', 'trigger-deliveries', 'scratch', '__pycache__'))
                evidence = json.loads((work / 'fixtures/pr.json').read_text())
                if scenario == 'variant':
                    evidence.update(pr_id='sample-99', head='1' * 40, patch='diff --git a/README.md b/README.md\n+Clarify usage.\n')
                    write(work / 'fixtures/pr.json', evidence)
                registry = json.loads((work / 'executors.json').read_text())
                for entry in registry['executors'].values():
                    directory = entry.get('workspace', '')
                    if directory and Path(directory).is_absolute() and Path(directory).is_relative_to(source):
                        entry['workspace'] = str(work / Path(directory).relative_to(source))
                    if entry.get('kind') in ('codex', 'claude-code'):
                        entry['binary'] = str(transport)
                        if entry['kind'] == 'claude-code' and scenario in ['fallback', 'started-error']:
                            entry['binary'] = str(sandbox / 'unavailable-executable') if scenario == 'fallback' else str(failed_transport)
                write(work / 'executors.json', registry)
                report = cli(work, 'workspace', 'validate', '--workflow', submission['workflow'])
                check(action + ': local role resources discovered', report['valid'] and len(report['agent_resources']) >= 2 and all(r['local_instructions'] for r in report['agent_resources']))
                started = cli(work, 'run', 'start', submission['workflow'], '--input', 'case=' + str(work / 'fixtures/pr.json'), success=scenario != 'started-error')
                if scenario == 'started-error':
                    run = cli(work, 'run', 'latest', workflow['name'])
                    attempts = run['nodes'][roles['review']].get('attemptSequence', [])
                    check('started-error: business work is not repeated through startup fallback', run['status'] == 'failed' and bool(attempts) and all(a['executor'] == candidates[0]['executor'] for a in attempts))
                    continue
                run_id = started['run_id']
                run = cli(work, 'run', 'get', run_id)
                review_result = run['nodes'][roles['review']].get('result', {})
                check(scenario + ': review actually executed with corresponding data', run['status'] == 'waiting_input' and run['nodes'][roles['review']]['status'] == 'succeeded' and bool(review_result.get('findings')) == (scenario != 'variant') and review_result.get('pr_id') == evidence['pr_id'] and review_result.get('head') == evidence['head'])
                if scenario == 'fallback':
                    check('fallback: secondary role selected on startup failure', run['nodes'][roles['review']]['selectedExecutor'] == candidates[1]['executor'])
                waiting = run['nodes'][roles['human']]
                token = waiting['leaseToken']
                invalid = cli(work, 'run', 'submit', run_id, roles['human'], '--token', token, '--output', json.dumps({'action': 'unexpected', 'comments': 'invalid'}), success=False)
                check(action + ': invalid human output rejected', invalid is None and cli(work, 'run', 'get', run_id)['status'] == 'waiting_input')
                comments = 'Explain the default change and its callers.'
                cli(work, 'run', 'submit', run_id, roles['human'], '--token', token, '--output', json.dumps({'action': action, 'comments': comments}))
                run = cli(work, 'run', 'get', run_id)
                if action == 'revise':
                    check('revise: returned to reviewer with feedback and task identity', run['status'] == 'waiting_input' and run['visits'][roles['review']] == 2 and run['nodes'][roles['review']]['result']['feedback'] == comments and run['nodes'][roles['review']]['result']['pr_id'] == 'sample-42')
                    stale = cli(work, 'run', 'submit', run_id, roles['human'], '--token', token, '--output', json.dumps({'action': 'accept', 'comments': 'stale'}), success=False)
                    check('revise: previous invocation token rejected', stale is None and cli(work, 'run', 'get', run_id)['status'] == 'waiting_input')
                    token = run['nodes'][roles['human']]['leaseToken']
                    cli(work, 'run', 'submit', run_id, roles['human'], '--token', token, '--output', json.dumps({'action': 'accept', 'comments': 'checked'}))
                    run = cli(work, 'run', 'get', run_id)
                check(action + ': intended path completed', run['status'] == 'succeeded')
                artifact = work / 'output/report.json'
                if action == 'stop':
                    check('stop: no accepted artifact produced', not artifact.exists() and run['nodes'][roles['archive']]['status'] == 'skipped')
                else:
                    data = json.loads(artifact.read_text())
                    check(scenario + ': actual program persisted corresponding task and findings', data['pr_id'] == evidence['pr_id'] and data['head'] == evidence['head'] and data['patch'] == evidence['patch'] and bool(data['findings']) == (scenario != 'variant'))
                    check(scenario + ': human result recorded', data['action'] == 'accept' and data['comments'] == ('checked' if action == 'revise' else comments))
                    if action == 'revise':
                        check('revise: feedback survives final output', data['feedback'] == comments)
        return {'contract_passed': True, 'checks': checks, 'native_role_behavior_verified': False, 'human_channel_verified': False, 'scope': 'Actual submitted graph/programs with substitute model transport and CLI human submissions. Native role quality and channel integration require separate evidence.'}
    except Exception as error:
        return {'contract_passed': False, 'checks': checks, 'error': str(error), 'native_role_behavior_verified': False, 'human_channel_verified': False}


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action', choices=['prepare', 'grade'])
    parser.add_argument('workspace')
    parser.add_argument('--binary', default=str(ROOT / 'bin/agentflow'))
    parser.add_argument('--output')
    args = parser.parse_args()
    result = {'workspace': str(prepare(args.workspace, args.binary))} if args.action == 'prepare' else grade(args.workspace, args.binary)
    if args.output:
        write(Path(args.output), result)
    print(json.dumps(result, ensure_ascii=False, indent=2))
    if result.get('contract_passed') is False:
        raise SystemExit(1)
