import json, os, sys
from pathlib import Path
request = json.loads(Path(os.environ['AGENTFLOW_INPUT_FILE']).read_text())
mode = sys.argv[1]
if mode == 'draft':
    result = {'summary': 'Draft: ' + request['topic'] + ' / feedback=' + str(request['feedback']), 'check': 'local deterministic check'}
else:
    result = {'next_step': 'publish after local review', 'verification': 'local deterministic verification'}
Path(os.environ['AGENTFLOW_RESULT_FILE']).write_text(json.dumps(result, ensure_ascii=False))
