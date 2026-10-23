import json,os
from pathlib import Path
assert os.environ['FARM_SITE']=='orchard-2'
x=json.loads(Path(os.environ['AGENTFLOW_INPUT_FILE']).read_text())
Path(os.environ['AGENTFLOW_RESULT_FILE']).write_text(json.dumps({'batch':x['batch'],'readings':[3,7,11],'meta':{'unit':'kg','valid':True}}))
