---
description: Add or extend a ProxOps resource capability safely
---

Implement the requested ProxOps resource capability.

Read `AGENTS.md`, `.github/instructions/proxops.instructions.md`, and the
relevant project documentation before changing behaviour.

Work through the capability end-to-end rather than adding only a schema field:

1. Inspect existing neighbouring resource implementations and tests.
2. Establish the required Proxmox VE API semantics.
3. If wire behaviour is uncertain, verify it against `conformance-dev` only.
4. Define the desired-state schema and validation rules.
5. Implement parsing and deterministic rendering.
6. Implement desired-to-PVE translation.
7. Implement live-state observation and drift comparison.
8. Implement planning and execution while preserving dependency and ownership
   safety.
9. Implement adoption for supported recoverable state.
10. Surface unsupported or unrecoverable state explicitly.
11. Add regression tests across the relevant layers.
12. Update authoritative documentation and `docs/GAPS.md` where appropriate.

Do not add raw arbitrary PVE passthrough as a shortcut.

Do not guess PVE values or semantics.

Do not write to production during investigation.

Keep destructive tests within `conformance-dev`.

Make focused Conventional/Commitizen commits as coherent work is completed.
Do not push unless explicitly requested.

Report:

- behaviour added
- PVE semantics verified
- tests/gates run
- documentation changed
- remaining gaps or intentionally unsupported cases
- commit hashes
