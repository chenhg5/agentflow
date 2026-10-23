# Provisional evaluator corrections

These are development observations, not additional independent model attempts and not raw transcript captures.

- The first provisional external-loop evaluation reported 35/38 because it imposed an unrequested first-node restriction and required loop exhaustion to be a technical failure. The artifact bounded the loop correctly and ended normally. The task allowed that result, so the evaluator was corrected. Final cohort reports use the same corrected evaluator for both stages; the provisional score is not counted as a model improvement.
- A later interface-completeness check verifies scheduled-review action outputs include all three task-defined signals. It applies equally to baseline and revised artifacts.
- The positive golden self-test initially failed external-loop revisits: explicit empty node input was erased on workflow snapshot serialization and restored as null. The tool was fixed to default omitted input to an empty object; the test was not weakened.
- A renamed/reordered positive variant and seven incorrect variants guard against exact-answer scoring and an evaluator that simply accepts every workflow.
