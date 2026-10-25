#!/usr/bin/env python3
"""Deterministic source checks used by the verifier agent."""

import hashlib
import json
import os
import sys
from pathlib import Path


arguments = json.load(sys.stdin)
source = Path(os.environ["AGENTFLOW_ROOT"]) / "data" / "source.md"
raw = source.read_bytes()
text = raw.decode("utf-8")
digest = hashlib.sha256(raw).hexdigest()
word_count = len(text.split())
print(json.dumps({
    "digest_matches": arguments.get("source_sha256") == digest,
    "count_matches": arguments.get("word_count") == word_count,
    "summary_nonempty": bool(arguments.get("summary", "").strip()),
    "source_sha256": digest,
    "word_count": word_count,
    "source_text": text,
}, ensure_ascii=False))
