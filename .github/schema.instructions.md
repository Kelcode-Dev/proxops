---
applyTo: "**/internal/plan/**/*.go,**/internal/exec/**/*.go,**/internal/reconcile/**/*.go"
---

# Planning and reconciliation rules

Read `docs/ARCHITECTURE.md`, `docs/OPERATIONS.md`, and relevant schema sections
before changing reconciliation behaviour.

Preserve these invariants:
- planning is derived from current desired and observed state
- plan generation does not perform PVE writes
- execution follows the established action ordering
- dependencies are honoured
- failed prerequisites defer dependants where the architecture requires it
- failed actions are not reported as converged
- destructive operations remain bounded and ownership-gated
- live state that cannot be safely reconciled is surfaced as an anomaly/error
- repeated convergence is idempotent

Avoid embedding persistent state into reconciliation unless the architecture
explicitly requires it.

For destructive lifecycle changes, use `conformance-dev` only.
