---
applyTo: "docs/**/*.md"
---

# Documentation rules

The project documentation is an authoritative engineering contract.

Use:
- `docs/ARCHITECTURE.md` for architecture and data flow
- `docs/SCHEMA.md` for the resource overview + cross-cutting guarantees
- `docs/ref-vm.md`, `docs/ref-lxc.md`, `docs/reference-artifacts.md`,
  `docs/cloudinit.md` for the kind-level schema detail
- `docs/repo-layout.md` + `docs/reference-config.md` for repository/config model
- `docs/OPERATIONS.md` for runtime/operator behaviour
- `docs/GAPS.md` for known compatibility limitations

When code changes behaviour:
- update the authoritative document when its contract changes
- avoid duplicating the same detail across documents
- document why important safety decisions exist, not only what they do
- distinguish current behaviour from planned work
- keep examples consistent with the implementation

Do not invent behaviour that the code and tests do not support.
