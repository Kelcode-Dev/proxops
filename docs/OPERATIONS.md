# Operations

This is the day-to-day operator's guide. The data-plane concepts are in
[SCHEMA.md](SCHEMA.md); the why's and how's of each layer are in
[ARCHITECTURE.md](ARCHITECTURE.md). This file covers deploying, monitoring,
and recovering.

## Deployment

### Systemd (recommended for the agent)

`config/pveconform.service` is a template. Install it:

```sh
sudo systemctl edit pveconform   # if you want overrides
# or simply:
sudo install -Dm644 config/pveconform.service /etc/systemd/system/pveconform.service
sudo systemctl daemon-reload
sudo systemctl enable --now pveconform
```

Environment credentials (see README) must be set either via
`Environment=` lines in the unit, a `Drop-In` file under
`/etc/systemd/system/pveconform.service.d/`, or a `systemctl set-environment`
call. Never bake secrets into `pveconform.yaml`.

Example drop-in `/etc/systemd/system/pveconform.service.d/cred.env`:

```ini
[Service]
Environment="PVECONFORM_PVE_TOKEN_VALUE=root@pam!pveconform=0f63b28d-...."
Environment="PVECONFORM_GIT_TOKEN=ghp_...."
```

### Config file

`config/pveconform.yaml` is the canonical layout. All fields optional;
defaults are sensible. `internal/config.Load` merges over
`internal/config.Defaults()`; env vars overwrite credentials last.

```yaml
log:
  level: info            # debug | info | warn | error
pve:
  auth: token            # token | ticket
  user: root@pam
  token-id: pveconform   # part of user@realm!tokenid=value
  # token: <uuid>       # prefer PVECONFORM_PVE_TOKEN env
  # token-value: <full> # prefer PVECONFORM_PVE_TOKEN_VALUE env — overrides pair
  base-url: https://pve-dev-01.example:8006   # single endpoint for ALL traffic
  nodes:                # optional allowlist of PVE node names
    - pve-dev-01
    - pve-dev-02
  ca-file:              # optional path to PVE cluster CA
git:
  url: https://github.com/you/pveconform-manifests.git
  branch: main
  # path: /var/lib/pveconform/tree   # air-gapped mode (mutually exclusive)
  # token: <ghp_...>       # prefer PVECONFORM_GIT_TOKEN env
reconcile:
  poll-interval: 30s
  task-timeout: 30m
  prune-budget: 3
listen: 127.0.0.1:9494   # or 0.0.0.0:9494
data-dir: ~/.local/share/pveconform
```

### Air-gapped / local mode

Point `git.path` at a work tree you maintain yourself (or mount). The agent
never writes it. On the first cycle, `gitx.New` initializes the local branch
if it is not yet checked out. Use this mode when the cluster has no git
access but `rsync`/`ssh` is allowed.

## Observability

- `/healthz` — `200` when a reconcile cycle has finished within the last 2
  minutes; `503` otherwise. Use for liveness probes and load balancers.
- `/metrics` — Prometheus (labels: `result` ∈ {ok, error, aborted},
  `kind`, `what`).
- `/status` — JSON. Top-level fields:
  - `process_start`: RFC3339 UTC.
  - `last_cycle`: `{commit, started_at, finished_at, objects, actions_ok,
    actions_error, pruned, prune_deferred, desired_stale, read_only,
    aborted, abort_reason}` — counters of the most recent completed cycle.
  - `objects`: array of `{kind, name, node, id, state, last_action,
    last_error, last_converged_at, prune_reason, updated_at}`.
State values: `desired`, `drift`, `converged`, `in_progress`, `failed`,
`skipped`, `pruned`. The agent never marks anything `converged` without a
round-trip read back from PVE.

Useful `systemd` / shell checks:

```sh
curl -s 127.0.0.1:9494/healthz && echo OK
curl -s 127.0.0.1:9494/status | jq .counters
# Alert when anomalies > 0 (empty-desired guard or desired-stale on PVE read)
journalctl -u pveconform -f | grep 'anomaly\|abort\|stale'
```

## Recovery

### A cycle aborted with an anomaly

`pveconform status | jq '.objects'` and `/metrics` `pveconform_cycles_total{result="aborted"}`
both point at which kind. Most common cause: you deleted all `VM` manifests
but PVE still has tagged live VMs. Confirm the intent:

- If the intent was *keep the VMs*, restore the manifests.
- If the intent was *delete*, raise `reconcile.prune-budget` temporarily,
  verify with `diff`, then `apply`.

### PVE task failed, one object stuck

`pveconform status` will show the offending object with `state=failed` and
`lastError` from PVE's exit status. The agent will re-diff on next cycle;
if PVE's task is still in progress, the next attempt will return
`operation in progress` and be logged as a transient failure — the following
cycle re-diffs. There is no local retry state.

### `desired-stale` on every cycle

The git fetch is failing. `journalctl -u pveconform` will show the reason.
Common: git credentials rotated, DNS broke, or the remote deleted the branch.
Fix the cause; the agent returns to normal on the next successful fetch. No
manual intervention needed.

### PVE token rotated

Set `PVECONFORM_PVE_TOKEN_VALUE` (or `PVECONFORM_PVE_TOKEN`+`PVECONFORM_PVE_USER`
for token auth) to the new value, then `sudo systemctl restart pveconform`.
The agent does not pick up env changes live.

### `pveconform apply` exits non-zero

`apply` is a one-shot command with strict abort semantics: any failed action
returns non-zero. This is what you *want* in runbooks and CI. In daemon
mode (`run`) the loop tolerates cycle aborts and keeps ticking; check the
`/status` anomaly counter and `journalctl` for the reason.

## Working with PVE

### What pveconform does *not* do in MVP

- **No PVE pool management.** `pool` is not a schema field.
- **No `qm`/`pct` shell-outs.** PVE API is the only interface.
- **No deletion of ISOs.** ISO manifests only create (download). Remove the
  manifest to stop re-downloading; PVE keeps the file.
- **No PVE user/role management.** The PVE token used by pveconform is
  assumed to already exist with the right roles (see README).

### Idempotency by construction

Because `Drift` compares desired vs live by PVE-wire fields (memory in MiB,
disk size in PVE binary-suffix units, NIC model+bridge (auto Mac ignored),
scsihw + iothread + pool + size for disks), you
should **never** have to `pveconform apply` twice in a row: the second cycle
produces a zero-action plan for a converged cluster. Any non-empty plan is
drift, not state-machine confusion.

### Adopting existing PVE objects

`pveconform adopt` (M5+) scaffolds YAML from live tagged PVE objects so you
can `git push` then `pveconform apply` to move them under management.
Post-MVP.

## Runbook (typical incident)

```
1. pveconform status                       # which objects are in what state
2. journalctl -u pveconform -n 200         # last cycle's log lines
3. pveconform diff                         # would-be plan
4. pveconform apply --dry-run              # same as 3, through the full pipe
5. Fix the cause (manifest / PVE token / git)
6. pveconform apply                        # one-shot convergence
7. Verify: pveconform status again, /metrics counters reset
```

The agent is intentionally not a "runbook engine": it converges to a known
state every cycle. If it does not converge, the diff *is* the
runbook (it tells you exactly what PVE will do to reach state).
