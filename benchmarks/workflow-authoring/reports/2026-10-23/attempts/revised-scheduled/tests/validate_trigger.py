"""Test the durable trigger dispatcher and worker with a model substitute."""
import json
import os
import subprocess
import time
import uuid
from pathlib import Path

root = Path(__file__).resolve().parent.parent
assert Path.cwd().resolve() == root
out = root/'tests/evidence'
config_path = root/'.agentflow/executors.json'
original = config_path.read_text()
config = json.loads(original)
config['executors']['codex'] = {'kind':'command','command':['python3','./tests/model_stub.py'],'workspace':'.'}
for suffix in ['model-count.txt','channel-count.txt','model.jsonl']:
    (out/('trigger.'+suffix)).unlink(missing_ok=True)
env = dict(os.environ, BENCH_RESPONSES='[{"signal":"happy","note":"scheduled-approval"}]', BENCH_CHANNEL_COUNTER=str(out/'trigger.channel-count.txt'), TEST_MODEL_COUNTER=str(out/'trigger.model-count.txt'), TEST_MODEL_LOG=str(out/'trigger.model.jsonl'), TEST_EXPECTED_NOTES='[null]', TEST_FINAL_NOTE='scheduled-approval')
worker = None
worker_log = (out/'trigger.serve.log').open('w')
try:
    config_path.write_text(json.dumps(config,indent=2)+'\n')
    worker = subprocess.Popen([str(root/'agentflow'),'trigger','serve','--host','127.0.0.1','--port','0'],env=env,stdout=worker_log,stderr=subprocess.STDOUT)
    time.sleep(.3)
    assert worker.poll() is None, 'worker failed to start'
    delivery='bench-local-'+uuid.uuid4().hex
    args=[str(root/'agentflow'),'trigger','fire','weekday-research-0915','--scheduled-at','2026-10-26T09:15:00+08:00','--delivery-id',delivery,'--format','json']
    fire=subprocess.run(args,env=env,text=True,capture_output=True)
    (out/'trigger.fire.stdout.json').write_text(fire.stdout)
    (out/'trigger.fire.stderr.txt').write_text(fire.stderr)
    assert fire.returncode==0, (fire.returncode,fire.stdout,fire.stderr)
    result=json.loads(fire.stdout)
    print(json.dumps(result,indent=2))
    # The public trigger response is persisted verbatim before looking up its run.
    run_id=result.get('run_id') or result.get('run',{}).get('id') or result.get('delivery',{}).get('run_id')
    if not run_id:
        listing=subprocess.run([str(root/'agentflow'),'run','list','--format','json'],env=env,text=True,capture_output=True)
        (out/'trigger.run-list.json').write_text(listing.stdout)
        raise AssertionError(('No run_id found in trigger fire response',result,listing.stdout))
    for _ in range(100):
        get=subprocess.run([str(root/'agentflow'),'run','get',run_id,'--format','json'],env=env,text=True,capture_output=True)
        state=json.loads(get.stdout)
        if state['status'] in ['succeeded','failed','cancelled']:
            break
        time.sleep(.1)
    (out/'trigger.get.stdout.json').write_text(get.stdout)
    (out/'trigger.get.stderr.txt').write_text(get.stderr)
    assert state['status']=='succeeded', state
    assert state['inputs']==json.loads((root/'.agentflow/triggers.json').read_text())['triggers'][0]['inputs']
    assert state['nodes']['review']['input']['recipient']==state['inputs']['reviewer']
    assert state['nodes']['finalize']['input']['proposal']==state['nodes']['draft']['result']
    repeat=subprocess.run(args,env=env,text=True,capture_output=True)
    (out/'trigger.repeat.stdout.json').write_text(repeat.stdout)
    (out/'trigger.repeat.stderr.txt').write_text(repeat.stderr)
    assert repeat.returncode==0, (repeat.stdout,repeat.stderr)
    duplicate=json.loads(repeat.stdout)
    assert duplicate.get('run_id')==run_id,duplicate
    assert int((out/'trigger.model-count.txt').read_text())==1
    report={'delivery_id':delivery,'run_id':run_id,'status':state['status'],'same_delivery_reuses_run':True,'worker_stopped':True}
    (out/'trigger.report.json').write_text(json.dumps(report,indent=2)+'\n')
    print(json.dumps(report,indent=2))
finally:
    if worker is not None:
        worker.terminate()
        try:
            worker.wait(timeout=5)
        except subprocess.TimeoutExpired:
            worker.kill()
            worker.wait()
    worker_log.close()
    config_path.write_text(original)
