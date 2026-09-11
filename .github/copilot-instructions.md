---
description: Investigate Proxmox API behaviour safely
---

Investigate the requested Proxmox behaviour.

Start with existing code, tests, and project documentation. Verify unknown
wire semantics against `conformance-dev` only.

Capture the exact endpoint, method, request fields, response shape, PVE
version behaviour, and failure modes. Prefer observed behaviour over
assumptions.

Turn confirmed behaviour into regression tests and update `docs/GAPS.md` or
other authoritative documentation when appropriate.

Do not write to production. Make a focused commit using
`<type>[optional scope]: <description>`. Do not push.
