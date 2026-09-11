---
applyTo: "**/*pve*.go,**/*proxmox*.go,**/pveclient/**/*.go,**/pve/**/*.go"
---

# Proxmox API rules

Treat Proxmox VE as an external API contract.

Before changing PVE-facing code:
1. Read the relevant sections of `docs/ARCHITECTURE.md`,
   `docs/SCHEMA.md`, `docs/OPERATIONS.md`, and `docs/GAPS.md`.
2. Inspect existing client code and tests.
3. Determine whether the wire behaviour is already established.
4. When uncertain, verify against disposable `conformance-dev` PVE before
   implementing a new assumption.

Rules:
- Do not infer wire parameter names from UI labels.
- Preserve exact endpoint and HTTP-method semantics already established.
- Be explicit about PVE-version-specific behaviour.
- Do not silently translate unsupported or ambiguous state.
- Prefer fail-closed handling when an operation could be destructive.
- Keep production read-only during investigation and compatibility probing.
- Record newly discovered compatibility behaviour in regression tests and
  `docs/GAPS.md` where appropriate.
