---
name: source-review
description: Independently check a workflow summary against the local field note and validate its source digest and word count with source_tools.verify_source.
---

# Source review

1. Call `source_tools.verify_source` with the structured input values.
2. Inspect the returned source text and compare it with the summary.
3. Accept only when the digest, count, nonempty summary, and factual claims all
   pass. Explain the decision briefly in `note`.
