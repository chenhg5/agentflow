# Unreleased

Changes since [v0.5.0](v0.5.0.md).

- Hub: each step's latest attempt reports the end of its stdout/stderr, a preview of its result JSON, and its files by name and size, masked and within a 32 KB budget per report (failed and running steps first). The run page shows them under each step; failed steps open by default.

