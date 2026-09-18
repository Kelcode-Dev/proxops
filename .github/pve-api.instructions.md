---
applyTo: "**/internal/schema/**/*.go,**/schema/**/*.go,**/internal/parse/**/*.go,**/examples/**/*.yaml"
---

# Schema and parser rules

`docs/SCHEMA.md` is the authoritative resource contract **overview**
(kind at-a-glance + cross-cutting guarantees: ownership tag, dependency
model, config layout). The per-kind field-level schema lives in the
dedicated reference pages: `docs/ref-vm.md` (VM + TemplateVM + clone),
`docs/ref-lxc.md` (LXC + TemplateCT), `docs/reference-artifacts.md`
(ISO + CTTemplate + DiskImage), and `docs/cloudinit.md` (cloud-init data +
drive). `docs/repo-layout.md` describes the repository tree and
composition; `docs/OPERATIONS.md` covers runtime; `docs/GAPS.md` covers
known compatibility limitations.

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
