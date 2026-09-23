# ProxOps contributor instructions

Read `AGENTS.md` before making changes. It contains the authoritative
engineering, safety, testing, Git, and Proxmox interaction rules.

Keep these invariants in mind:

- Git is desired state; do not introduce hidden persistent controller state.
- Production PVE environments are read-only unless explicitly authorised.
- Use `conformance-dev` for destructive lifecycle and compatibility testing.
- Prefer fail-closed behaviour where state is ambiguous or destructive.
- Never expose credentials, tokens, passwords, private keys, or decrypted
  secret material.
- Preserve deterministic and idempotent reconciliation and adoption behaviour.
- Verify unknown Proxmox VE wire semantics rather than guessing them.
- Update authoritative documentation and `docs/GAPS.md` when behaviour changes.
- Use focused Conventional/Commitizen commits.
- Do not push unless explicitly requested.

`.github/instructions/proxops.instructions.md` contains the shared implementation
rules. Reusable prompts under `.github/prompts/` cover common project workflows.
