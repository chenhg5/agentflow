"""Deterministic model substitute used only by tests/validate_local.py."""
import json
import os
from pathlib import Path

request = json.loads(Path(os.environ['AGENTFLOW_INPUT_FILE']).read_text())
counter = Path(os.environ['TEST_MODEL_COUNTER'])
revision = int(counter.read_text()) if counter.exists() else 0
if 'proposal' in request:
    assert request['proposal']['summary'] == f'proposal-v{revision}'
    assert request['proposal']['check'] == f'check-v{revision}'
    assert request['review']['signal'] == 'happy'
    assert request['review']['note'] == os.environ['TEST_FINAL_NOTE']
    assert f'proposal-v{revision}' in request['prompt']
    assert os.environ['TEST_FINAL_NOTE'] in request['prompt']
    output = {'next_step': f'Execute proposal-v{revision}', 'verification': f'Use check-v{revision}'}
    role = 'finalize'
else:
    expected_notes = json.loads(os.environ['TEST_EXPECTED_NOTES'])
    assert request['feedback'] == expected_notes[revision], (request, expected_notes, revision)
    if request['feedback']:
        assert request['feedback'] in request['prompt']
    revision += 1
    counter.write_text(str(revision))
    output = {'summary': f'proposal-v{revision}', 'check': f'check-v{revision}'}
    role = 'draft'
with Path(os.environ['TEST_MODEL_LOG']).open('a') as log:
    log.write(json.dumps({'role': role, 'input': request, 'output': output}, ensure_ascii=False) + '\n')
Path(os.environ['AGENTFLOW_RESULT_FILE']).write_text(json.dumps(output))
