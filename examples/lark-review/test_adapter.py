"""Real AgentFlow CLI + fake Feishu transport; sends no network messages."""
import copy
import json
import os
from pathlib import Path
import subprocess
import tempfile
import threading
import time
import unittest
from lark_adapter import Adapter, card_for, encode, singleton, validate_config

BIN = os.environ.get('AGENTFLOW_BIN', 'agentflow')
CONFIG = {'profile':'test', 'recipient':'ou_reviewer', 'title':'一个待确认的改进',
          'sections':[{'title':'提案','content':'把下一步行动写清楚。'}, {'title':'验收','content':'能看到可检查的结果。'}],
          'actions':[{'label':'继续', 'style':'primary_filled', 'color':'green', 'output':{'score':8}},
                     {'label':'再来一次', 'output':{'score':2}}, {'label':'结束', 'output':{'score':0}}]}


class Transport:
    def __init__(self):
        self.sends, self.patches, self.fail_patch = {}, [], False

    def api(self, args):
        if args[1] == '+messages-send':
            key = args[args.index('--idempotency-key')+1]
            self.sends.setdefault(key, 'om_' + str(len(self.sends)))
            return {'message_id':self.sends[key]}
        if self.fail_patch:
            raise RuntimeError('simulated patch outage')
        self.patches.append(json.loads(json.loads(args[-1])['content']))
        return {}


class Integration(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name)
        self.db = self.root/'adapter/state.sqlite3'
        self.transport = Transport()
        (self.root/'executors.json').write_text(encode({'executors':{'lark-review':{'kind':'external'}}}))
        workflow = {'version':'3','name':'durable-gate','entry':'gate','nodes':[
            {'id':'gate','execution':{'candidates':[{'executor':'lark-review','config':CONFIG}]},
             'outputSchema':{'type':'object','required':['score'],'properties':{'score':{'type':'number'}},'additionalProperties':False}},
            {'id':'next','execution':{'candidates':[{'executor':'process','config':{'command':[
                'python3','-c', 'import os,json; open(os.environ["AGENTFLOW_RESULT_FILE"],"w").write(json.dumps({"ran":True}))']}}]}}],
            'edges':[{'from':'gate','to':'next','when':'output.score > 5'},
                     {'from':'gate','to':'gate','when':'output.score > 0','maxIterations':2},
                     {'from':'gate','to':'$end'},{'from':'next','to':'$end'}]}
        (self.root/'workflow.json').write_text(encode(workflow))
        result = self.cli('start','workflow.json')
        self.run_id = next((self.root/'runs').glob('run_*')).name
        self.adapter = self.new_adapter()
        self.adapter.tick()
        self.record = self.adapter.journal.records()[0]
        self.assertEqual(self.state()['nodes']['gate']['status'],'waiting_input')

    def tearDown(self):
        for job in self.adapter.jobs.values():
            job.wait(timeout=10)
        self.temp.cleanup()

    def new_adapter(self):
        a = Adapter(self.root,self.db,'test',agentflow=BIN)
        a.api = self.transport.api
        a.ready.set()
        return a

    def cli(self,*args,ok=True):
        p = subprocess.run([BIN,'run',*args,'--format','json'],cwd=self.root,text=True,capture_output=True)
        if ok: self.assertEqual(p.returncode,0,p.stdout+p.stderr)
        return p

    def state(self):
        return json.loads((self.root/'runs'/self.run_id/'state.json').read_text())

    def event(self,index=0,record=None,**overrides):
        r = record or self.record
        e = {'type':'card.action.trigger','action_tag':'button','event_id':'event-1',
             'message_id':r['message_id'],'operator_id':'ou_reviewer',
             'action_value':encode({'invocation':r['token'],'actionIndex':index})}
        e.update(overrides)
        return e

    def wait(self,predicate):
        deadline=time.monotonic()+10
        while time.monotonic()<deadline:
            self.adapter.tick()
            if predicate(): return
            time.sleep(.05)
        self.fail('timeout: '+encode(self.state()))

    def test_restart_before_and_after_click_and_patch_outage(self):
        self.adapter=self.new_adapter()
        self.adapter.tick()
        self.assertEqual(len(self.transport.sends),1)
        # Receive durably; restart before event processing.
        self.adapter.journal.receive(self.event())
        self.adapter=self.new_adapter()
        self.adapter.event(self.adapter.journal.drain()[0][1])
        # Restart after intent commit but before submit. Duplicate delivery stays safe.
        self.adapter=self.new_adapter()
        self.transport.fail_patch=True
        self.wait(lambda:self.state()['status']=='succeeded')
        self.assertTrue(self.state()['nodes']['next']['result']['ran'])
        self.assertEqual(self.adapter.journal.records()[0]['phase'],'accepted')
        self.transport.fail_patch=False
        record=self.adapter.journal.records()[0]
        record['card_retry_after']=0
        self.adapter.journal.save(record)
        self.adapter.tick()
        self.assertEqual(json.loads(self.adapter.journal.records()[0]['painted'])[:2],['accepted','succeeded'])
        self.assertNotIn('"tag":"button"',encode(self.transport.patches[-1]))
        self.assertEqual(len(self.transport.sends),1)

    def test_wrong_operator_message_and_malformed_action_ignored(self):
        for event in [self.event(operator_id='other'),self.event(message_id='other'),
                      self.event(action_value='bad'),self.event(action_value=encode({'invocation':self.record['token'],'actionIndex':True}))]:
            self.adapter.event(event)
        self.assertEqual(self.adapter.journal.records()[0]['phase'],'waiting')
        self.assertEqual(self.state()['nodes']['gate']['status'],'waiting_input')

    def test_duplicate_and_old_token_cannot_advance_next_visit(self):
        self.adapter.journal.receive(self.event(1))
        self.adapter.journal.receive(self.event(0))
        self.wait(lambda:self.state()['nodes']['gate']['leaseToken']!=self.record['token'])
        self.adapter.tick()
        self.assertEqual(len(self.transport.sends),2)
        self.assertEqual(self.state()['nodes']['gate']['status'],'waiting_input')
        p=self.cli('submit',self.run_id,'gate','--token',self.record['token'],'--output','{"score":8}',ok=False)
        self.assertNotEqual(p.returncode,0)
        self.adapter.event(self.event())
        self.adapter.tick()
        self.assertEqual(self.state()['nodes']['gate']['status'],'waiting_input')

    def test_cancel_expires_card(self):
        self.cli('cancel',self.run_id)
        self.adapter.tick()
        self.assertEqual(self.adapter.journal.records()[0]['phase'],'expired')
        self.adapter.event(self.event())
        self.assertEqual(self.state()['status'],'cancelled')

    def test_listener_not_ready_prevents_new_send(self):
        self.transport.sends.clear()
        r=self.record
        r.pop('message_id'); r.pop('painted')
        self.adapter.journal.save(r)
        self.adapter.ready.clear()
        self.adapter.tick()
        self.assertEqual(len(self.transport.sends),0)

    def test_invalid_output_is_rejected_before_card_send(self):
        config=copy.deepcopy(CONFIG)
        config['actions'][0]['output']={'score':'not-a-number'}
        with self.assertRaisesRegex(ValueError,'preflight'):
            self.adapter.validate_outputs(self.run_id,'gate',self.record['token'],config)
        self.assertEqual(self.state()['nodes']['gate']['status'],'waiting_input')

    def test_local_config_checker_and_profile_mismatch(self):
        script=Path(__file__).with_name('lark_adapter.py')
        config_file=self.root/'card-config.json'
        config_file.write_text(encode(CONFIG))
        def check(profile):
            return subprocess.run(['python3',str(script),'--profile',profile,'--check-config',str(config_file)],
                                  cwd=self.root,text=True,capture_output=True)
        valid=check('test')
        self.assertEqual(valid.returncode,0,valid.stderr)
        self.assertFalse(json.loads(valid.stdout)['network_verified'])
        invalid=check('wrong-bot')
        self.assertEqual(invalid.returncode,2)
        self.assertIn('profile',json.loads(invalid.stdout)['error'])
        self.assertEqual(len(self.transport.sends),1)

    def test_config_typo_and_invalid_fields_rejected(self):
        for edit in [lambda c:c.update(reciepient='oops'),
                     lambda c:c.update(recipient=123),
                     lambda c:c['actions'][0].update(lable='oops'),
                     lambda c:c['sections'][0].update(content=''),
                     lambda c:c['actions'][0].update(confirm=123)]:
            config=copy.deepcopy(CONFIG)
            edit(config)
            with self.assertRaises(ValueError): validate_config(config)

    def test_singleton(self):
        with singleton(self.db):
            with self.assertRaises(RuntimeError):
                with singleton(self.db): pass

    def test_listener_reconnects_and_journals_event(self):
        fake = self.root/'fake-lark'
        fake.write_text('#!/usr/bin/env python3\n' + '''import pathlib,time,json
p=pathlib.Path('connections')
n=int(p.read_text())+1 if p.exists() else 1
p.write_text(str(n))
print('[event] ready event_key=card.action.trigger',file=__import__('sys').stderr,flush=True)
if n == 1: raise SystemExit(0)
print(json.dumps({'type':'card.action.trigger','event_id':'durable-event'}),flush=True)
time.sleep(30)
''')
        fake.chmod(0o755)
        self.adapter.lark=str(fake)
        self.adapter.ready.clear()
        thread=threading.Thread(target=self.adapter.listen)
        thread.start()
        try:
            deadline=time.monotonic()+6
            while not self.adapter.journal.drain() and time.monotonic()<deadline:
                time.sleep(.05)
            self.assertTrue(self.adapter.ready.is_set())
            self.assertEqual(self.adapter.journal.drain()[0][1]['event_id'],'durable-event')
            self.assertGreaterEqual(int((self.root/'connections').read_text()),2)
        finally:
            self.adapter.stop.set()
            thread.join(timeout=7)
        self.assertFalse(thread.is_alive())

    def test_generic_payload_and_card_states(self):
        r=copy.deepcopy(self.record)
        r['config']['actions'][0]['output']={'happy':True,'rating':9}
        for phase in ['waiting','recorded','accepted','expired']:
            r.update(phase=phase,selection=0)
            card=card_for(r)
            self.assertEqual(card['schema'],'2.0')
            self.assertLessEqual(len(card['body']['elements']),5)
            self.assertEqual('"tag":"button"' in encode(card),phase=='waiting')


if __name__=='__main__': unittest.main()
