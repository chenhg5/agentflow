---
name: source-brief
description: Summarize the local field note for this workflow using the source_tools.read_source tool and return a source digest with the summary.
---

# Source brief

1. Call `source_tools.read_source` with `{}` to read the field note.
2. Summarize the observed pilot and proposed next step in at most three sentences.
3. Copy `sha256` and `word_count` from the tool result into the workflow result as
   `source_sha256` and `word_count`. Do not calculate or guess them yourself.
