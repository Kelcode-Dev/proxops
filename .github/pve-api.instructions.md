---
applyTo: "**/internal/schema/**/*.go,**/schema/**/*.go,**/internal/parse/**/*.go,**/examples/**/*.yaml"
---

# Schema and parser rules

`docs/SCHEMA.md` is the authoritative resource contract.

When changing a resource kind or field:
- inspect neighbouring resource types first
- preserve established naming and YAML conventions
- consider validation, parsing, PVE wire translation, live-state comparison,
  execution, adoption, dependencies, and ownership
- reject conflicting or ambiguous representations
- keep resource identity explicit
- do not add fields merely because PVE exposes them
- distinguish desired state from PVE-owned/generated values
- preserve deterministic parsing and ordering
- add parser/schema regression tests

When the supported contract changes, update `docs/SCHEMA.md`.
When a limitation or PVE compatibility gap changes, update `docs/GAPS.md`.
