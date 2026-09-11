---
applyTo: "docs/**/*.md"
---

# Documentation rules

The project documentation is an authoritative engineering contract.

Use:
- `docs/ARCHITECTURE.md` for architecture and data flow
- `docs/SCHEMA.md` for resource semantics
- `docs/OPERATIONS.md` for runtime/operator behaviour
- `docs/GAPS.md` for known compatibility limitations

When code changes behaviour:
- update the authoritative document when its contract changes
- avoid duplicating the same detail across documents
- document why important safety decisions exist, not only what they do
- distinguish current behaviour from planned work
- keep examples consistent with the implementation

Do not invent behaviour that the code and tests do not support.
