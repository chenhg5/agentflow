"""Deterministic local replacement for model nodes, only used by the test copy."""
import json
import os
import sys
from pathlib import Path

request = json.loads(Path(os.environ['AGENTFLOW_INPUT_FILE']).read_text())
role = sys.argv[1]
if role == 'draft':
    assert isinstance(request['topic'], str)
    assert request['feedback'] is None or isinstance(request['feedback'], str)
    if request['feedback']:
        assert request['feedback'] in request['prompt']
    result = {
        'summary': f"文案：{request['topic']}；反馈：{request['feedback']}",
        'check': '已检查主题与本轮反馈。',
    }
elif role == 'finalize':
    assert request['review']['mood'] == 'go'
    assert request['review']['confidence'] >= 0.8
    assert request['summary'] in request['prompt']
    assert request['review']['feedback'] in request['prompt']
    result = {'next_step': '整理：' + request['summary'], 'verification': '确认主题、正文与审核反馈一致。'}
else:
    raise ValueError(role)
Path(os.environ['AGENTFLOW_RESULT_FILE']).write_text(json.dumps(result, ensure_ascii=False))
