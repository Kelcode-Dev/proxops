---
description: Perform a conservative PVE Conform security review
---

Review the current change for production safety and security.

Check:
- production read/write boundaries
- ownership and destructive-operation safety
- secret exposure through logs, errors, output, tests, and files
- fail-closed behaviour
- malformed/unsupported PVE state
- credential precedence and validation
- deterministic/idempotent behaviour
- regression-test coverage

Report concrete findings with severity and affected code.

Only make fixes that are directly related to findings. Add regression tests
for security-sensitive fixes and run the relevant quality gates.

Use focused commits with `<type>[optional scope]: <description>`. Do not push.
