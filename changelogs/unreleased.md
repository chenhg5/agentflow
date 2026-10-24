# Unreleased

- Optional edge `map` projects source input, source output, run inputs, or prior node results into the next node's input. Omitting it still forwards the full source output; `{}` explicitly sends an empty object.
- Expose edge maps in graph descriptions and the web inspector, and document migration of workflows with per-edge data handoff.
- Permit unlimited graph cycles only when a workflow explicitly sets `allowUnboundedCycles: true`; the default validation still requires a bounded edge in every cycle.
- Add the missing `forkCommand` property to the executor registration schema.
