---
description: Safely investigate and implement PVE adoption changes
---

Work on the requested adoption change.

First read the relevant project docs and inspect the existing adoption,
schema, PVE client, tests, and gap handling.

Preserve production read-only behaviour, deterministic output, ownership
safety, and explicit handling of unsupported or unrecoverable state.

Use `conformance-dev` for any destructive or compatibility testing. Add
regression tests and update authoritative docs when the contract changes.

Make focused Commitizen/Conventional commits as coherent work is completed.
Do not push.

Report what was changed, what was verified, known gaps, tests run, and commit
hashes.
