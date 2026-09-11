---
applyTo: "**/internal/adopt/**/*.go,**/internal/adoption/**/*.go"
---

# Adoption rules

Adoption is reverse engineering of observed PVE state into the project's
resource model.

Before changing adoption, read:
- `docs/ARCHITECTURE.md`
- `docs/SCHEMA.md`
- `docs/OPERATIONS.md`
- `docs/GAPS.md`

Preserve:
- read-only PVE behaviour unless explicitly authorised
- deterministic output
- faithful recovery of supported state
- explicit reporting of unsupported/unrecoverable state
- ownership safety
- secret redaction and non-disclosure

Never guess a value that PVE does not retain.

If adoption discovers a new compatibility limitation:
- make the limitation explicit in output
- add a regression test where practical
- update `docs/GAPS.md` when it represents a lasting project limitation

Production adoption must not be turned into reconciliation or mutation.
