#!/usr/bin/env python3
import json
import os
from pathlib import Path

if os.environ.get('FARM_SITE') != 'orchard-2':
    raise ValueError('FARM_SITE must be orchard-2')
data = json.loads(Path(os.environ['AGENTFLOW_INPUT_FILE']).read_text())
if not isinstance(data, dict) or not isinstance(data.get('batch'), str):
    raise ValueError('batch must be a required string')
result = {'batch': data['batch'], 'readings': [3, 7, 11],
          'meta': {'unit': 'kg', 'valid': True}}
Path(os.environ['AGENTFLOW_RESULT_FILE']).write_text(json.dumps(result))
