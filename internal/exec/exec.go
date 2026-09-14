// Package exec executes planned actions against PVE, awaits async tasks, and
// records results into the status store + metrics.
//
// Behavioral rules (plan §5/§10/§11):
//   - serial, in plan order; a single object's failure never aborts the cycle
//   - StopFirst updates stop → apply → restore start when desired state is
//     started; failures leave the object stopped and mark it Failed
//   - deletes ensure the object is stopped first; PVE rejects delete of a
//     running object — the MVP never force-destroys
//   - PVE "operation already in progress" is a transient, non-retried failure:
//     the executor records it and lets the next cycle re-diff (re-attaching to
//     an unknown task is not safe: the UPID is unknown here)
//   - all PVE calls propagate the caller's ctx so cancellation is honored
package exec

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"time"

	"github.com/GizzmoShifu/proxmox-operator/internal/metrics"
	"github.com/GizzmoShifu/proxmox-operator/internal/plan"
	"github.com/GizzmoShifu/proxmox-operator/internal/pveclient"
	"github.com/GizzmoShifu/proxmox-operator/internal/schema"
	"github.com/GizzmoShifu/proxmox-operator/internal/statusx"
)

// Executor applies plan actions. Only one cycle uses it at a time (the agent
// loop is serial).
type Executor struct {
	client      *pveclient.Client
	taskTimeout time.Duration
	store       *statusx.Store
	log         *slog.Logger
	// taskInterval is the polling cadence for task status; default 2s.
	taskInterval time.Duration
	// cluster is the proxops cluster this executor reconciles; every
	// statusx.Object it writes is tagged with it so a multi-cluster agent
	// keeps per-cluster records.
	cluster string
}

// SetCluster pins the proxops cluster identity on the executor. The agent
// composition root calls it once per cluster before any cycle runs.
func (e *Executor) SetCluster(c string) { e.cluster = c }

// New builds an Executor. taskTimeout bounds task awaits per action; interval
// is the task polling cadence (0 = 2s default).
func New(client *pveclient.Client, taskTimeout, taskInterval time.Duration, store *statusx.Store, log *slog.Logger) *Executor {
	if taskTimeout <= 0 {
		taskTimeout = 30 * time.Minute
	}
	if taskInterval <= 0 {
		taskInterval = 2 * time.Second
	}
	if log == nil {
		log = slog.Default()
	}
	return &Executor{
		client:       client,
		taskTimeout:  taskTimeout,
		taskInterval: taskInterval,
		store:        store,
		log:          log,
	}
}

// Result is one action's outcome.
type Result struct {
	Action   plan.Action
	OK       bool
	Err      error
	DidStop  bool
	Duration time.Duration
}

// Run executes every action in the plan and returns one result per action.
// It also records deferred prunes into the status store (as Skipped).
//
// Dependency deferral: the plan is ordered so prerequisites (lower
// Level) come before dependants (higher Level). While executing, if
// a prerequisite on the SAME node fails earlier this cycle, every later
// action that has that prereq in its Deps AND on that node is
// deferred for this cycle (recorded as Skipped, not attempted).
// The next cycle re-derives everything from live state, so deferral
// is safe and self-correcting — no persistent state.
func (e *Executor) Run(ctx context.Context, p *plan.Plan) []Result {
	results := make([]Result, 0, len(p.Actions))
	failed := map[key]bool{}
	for _, a := range p.Actions {
		deferred := false
		for _, dep := range a.Deps {
			if dep.Kind == "" || dep.Name == "" {
				continue
			}
			if failed[key{kind: dep.Kind, name: dep.Name, node: a.Node}] {
				e.deferByDependency(a, fmt.Sprintf("%s on node %s", dep, a.Node))
				results = append(results, Result{Action: a, OK: false,
					Err: fmt.Errorf("deferred: prerequisite %s on node %s failed earlier this cycle", dep, a.Node)})
				deferred = true
				break
			}
		}
		if deferred {
			continue
		}
		res := e.execute(ctx, a)
		results = append(results, res)
		if !res.OK && a.Ref.Kind != "" && a.Ref.Name != "" {
			failed[key{kind: a.Ref.Kind, name: a.Ref.Name, node: a.Node}] = true
		}
	}
	e.recordDeferred(p)
	return results
}

// key identifies a failed prerequisite per (Ref, Node). A multi-node
// artifact plans one Create per node; only the (Ref, Node) tuple that
// actually failed defers dependants that are on that node.
type key struct {
	kind schema.Kind
	name string
	node string
}

// deferByDependency records a would-be action as Skipped because one of its
// prerequisites failed earlier in the same cycle.
func (e *Executor) deferByDependency(a plan.Action, dep string) {
	if e.store != nil {
		e.store.SetObject(&statusx.Object{
			Kind:        a.Kind,
			Name:        a.Name,
			Cluster:     e.cluster,
			Node:        a.Node,
			ID:          a.ID,
			State:       statusx.Skipped,
			LastAction:  string(a.What),
			LastError:   "deferred: prerequisite " + dep + " failed this cycle",
			PruneReason: a.Reason,
		})
	}
	e.log.Warn("proxops.action.deferred",
		slog.String("kind", string(a.Kind)),
		slog.String("name", a.Name),
		slog.String("node", a.Node),
		slog.String("what", string(a.What)),
		slog.String("blocked_by", dep),
	)
}

// execute applies one action and records status + metrics.
func (e *Executor) execute(ctx context.Context, a plan.Action) Result {
	if e.store != nil {
		e.store.SetObject(&statusx.Object{
			Kind: a.Kind, Name: a.Name, Node: a.Node, ID: a.ID,
			State: statusx.InProgress, LastAction: string(a.What),
			PruneReason: a.Reason,
		})
	}
	start := time.Now()
	didStop, err := e.apply(ctx, a)
	ok := err == nil
	if e.store != nil {
		state := e.stateFor(a, ok)
		e.store.SetObject(&statusx.Object{
			Cluster: e.cluster, Kind: a.Kind, Name: a.Name, Node: a.Node, ID: a.ID,
			State:           state,
			LastAction:      string(a.What),
			LastError:       errMsg(err),
			PruneReason:     a.Reason,
			LastConvergedAt: lastConverged(ok, state),
		})
		bucket := "error"
		if ok {
			bucket = "ok"
		}
		metrics.ActionsTotal.WithLabelValues(string(a.Kind), string(a.What), bucket).Inc()
		if ok {
			if a.Prune {
				e.store.BumpPruned()
			}
			e.store.BumpActionsOK()
		} else {
			e.store.BumpActionsError()
		}
		level := slog.LevelWarn
		if ok {
			level = slog.LevelInfo
		}
		e.log.Log(ctx, level, "proxops.action",
			slog.String("kind", string(a.Kind)),
			slog.String("name", a.Name),
			slog.Int("id", a.ID),
			slog.String("node", a.Node),
			slog.String("what", string(a.What)),
			slog.Bool("ok", ok),
			slog.Bool("did_stop", didStop),
			slog.Duration("duration", time.Since(start)),
			slog.String("err", errString(err)),
			slog.String("reason", a.Reason),
		)
	}
	return Result{Action: a, OK: ok, Err: err, DidStop: didStop, Duration: time.Since(start)}
}

// apply performs the PVE operation for one action.
func (e *Executor) apply(ctx context.Context, a plan.Action) (didStop bool, err error) {
	switch a.What {
	case plan.Delete:
		if a.LivePower == "running" {
			if didStop, err = e.stopAndWait(ctx, a); err != nil {
				return false, fmt.Errorf("pre-delete stop: %w", err)
			}
		}
		upid, delErr := e.delete(ctx, a)
		if delErr != nil {
			return didStop, delErr
		}
		return didStop, e.waitTask(ctx, a.Node, upid)
	case plan.Update:
		return e.update(ctx, a)
	case plan.Create:
		upid, cErr := e.create(ctx, a)
		if cErr != nil {
			return false, cErr
		}
		return false, e.waitTask(ctx, a.Node, upid)
	// M11: mark an existing qemu VM as a PVE template. proxops's
	// executor POSTs /qemu/{id}/template (PVE's one-way-only endpoint:
	// PVE 9.2 has no /qemu/{id}/untemplate — 501 "not implemented",
	// probed on conformance-dev 2026-09-11). The planner emits a
	// MarkTemplate action whenever a desired TemplateVM matches a PVE
	// object at the same (node, vmid) that is NOT template-flagged.
	case plan.MarkTemplate:
		upid, mErr := e.markTemplate(ctx, a)
		if mErr != nil {
			return false, mErr
		}
		return false, e.waitTask(ctx, a.Node, upid)
	case plan.Start:
		upid, sErr := e.start(ctx, a)
		if sErr != nil {
			return false, sErr
		}
		return false, e.waitTask(ctx, a.Node, upid)
	case plan.Stop:
		if _, err := e.stopAndWait(ctx, a); err != nil {
			return false, err
		}
		return true, nil
	case plan.StatusOnly:
		return false, nil
	default:
		return false, fmt.Errorf("unknown action kind %q", a.What)
	}
}

func (e *Executor) update(ctx context.Context, a plan.Action) (didStop bool, err error) {
	if a.StopFirst && a.LivePower == "running" {
		if didStop, err = e.stopAndWait(ctx, a); err != nil {
			return false, fmt.Errorf("stop-first: %w", err)
		}
	}
	upid, uErr := e.updateConfig(ctx, a)
	if uErr != nil {
		return didStop, uErr
	}
	if wErr := e.waitTask(ctx, a.Node, upid); wErr != nil {
		return didStop, wErr
	}
	// restore power when we stopped for a stop-required update
	if didStop && a.DesiredPower == "started" {
		supid, sErr := e.start(ctx, a)
		if sErr != nil {
			return didStop, fmt.Errorf("restart after stop-required update: %w (object left stopped; next cycle re-diffs power)", sErr)
		}
		if wErr := e.waitTask(ctx, a.Node, supid); wErr != nil {
			return didStop, fmt.Errorf("restart wait: %w", wErr)
		}
	}
	return didStop, nil
}

// --- PVE ops (return UPID; "" when PVE acted synchronously) ---

// create issues a PVE create for the action kind. ISO and CTTemplate are
// storage-artifact downloads; CTT no longer clones a source CT.
//
// Both go through PVE POST /nodes/{n}/storage/{s}/download-url with the
// `content` form parameter set to "iso" or "vztmpl" respectively.
//
// M11 TemplateVM: a single Create action performs two PVE writes:
//  1. POST /nodes/{n}/qemu with the manifest's create params + start=0
//     (a PVE template is stopped — DesiredState is always "stopped").
//  2. POST /nodes/{n}/qemu/{id}/template (the PVE mark endpoint).
//
// The two tasks run serially, and both UPIDs are waited on by
// waitTask (the second returns the mark UPID). This keeps a failed
// template-mark visible on /status as a Failed action, not a half-created
// VM that would show up as drift on the next cycle.
func (e *Executor) create(ctx context.Context, a plan.Action) (string, error) {
	switch a.Kind {
	case schema.KindISO, schema.KindCTTemplate, schema.KindDiskImage:
		p, _ := a.Params["storage"].(string)
		u, _ := a.Params["url"].(string)
		f, _ := a.Params["filename"].(string)
		ct, _ := a.Params["content"].(string)
		if p == "" || u == "" || f == "" {
			return "", fmt.Errorf("%s: storage/url/filename missing from params", a.Kind)
		}
		if ct == "" {
			switch a.Kind {
			case schema.KindCTTemplate:
				ct = "vztmpl"
			case schema.KindDiskImage:
				ct = "import"
			default:
				ct = "iso"
			}
		}
		return e.client.Storage().Download(ctx, a.Node, p, u, f, ct)
	case schema.KindTemplateVM:
		// M11: create a VM at the pinned vmid, then mark it as a
		// template. Both returns a UPID; waitTask drains sequentially.
		// DesiredPower is always "stopped" for a TemplateVM
		// (TemplateVM.DesiredState() returns "stopped"; the planner
		// asserts this in Validate). A template CANNOT be started.
		v := toValues(a.Params)
		upid, cErr := e.client.VM().Create(ctx, a.Node, v, false)
		if cErr != nil {
			return upid, cErr
		}
		waitErr := e.waitTask(ctx, a.Node, upid)
		if waitErr != nil {
			return upid, waitErr
		}
		mupid, mErr := e.client.VM().MarkTemplate(ctx, a.Node, a.ID)
		if mErr != nil {
			// A partially-created VM with a failed mark is visible as a
			// drift on the next cycle (proxops's VM↔TemplateVM
			// mismatch rule fires on a proxops TV desired vs a live
			// VM that is not yet template). No automatic retry: the
			// operator must investigate the PVE-side half-state.
			return mupid, fmt.Errorf("create template-mark: %w (created VM left un-marked)", mErr)
		}
		return mupid, nil
	default:
		v := toValues(a.Params)
		start := a.DesiredPower == "started"
		if a.Kind == schema.KindVM {
			return e.client.VM().Create(ctx, a.Node, v, start)
		}
		return e.client.LXC().Create(ctx, a.Node, v, start)
	}
}

// markTemplate performs a PVE-side VM template-mark for an already-existing
// qemu object (M11 desired TemplateVM vs. live proxops-created VM that
// is not yet PVE-templatel). PVE 9.2's mark endpoint is POST-only;
// there is no untemplate endpoint (501 "not implemented", probed
// conformance-dev 2026-09-11).
func (e *Executor) markTemplate(ctx context.Context, a plan.Action) (string, error) {
	upid, err := e.client.VM().MarkTemplate(ctx, a.Node, a.ID)
	if err != nil {
		return upid, fmt.Errorf("%s: mark-template: %w", a.Ref, err)
	}
	return upid, nil
}

// updateConfig issues a PVE config update. Artifacts (ISO / CTTemplate)
// never receive config updates — they have no PVE object id; their only
// PVE-side mutation is the POST /storage/download-url they trigger on Create.
// M11 TemplateVM shares PVE's /qemu/{id}/config surface with a regular VM
// (a PVE-side template is just a stopped qm flagged template=1). All four
// config/power/delete routes therefore dispatch a KindTemplateVM to the
// VM client with a.Kind == schema.KindVM || a.Kind == schema.KindTemplateVM.
func (e *Executor) updateConfig(ctx context.Context, a plan.Action) (string, error) {
	if schema.ArtifactKind(a.Kind) {
		return "", fmt.Errorf("%s: PVE storage artifacts do not take config updates", a.Kind)
	}
	v := toValues(a.Params)
	if a.Kind == schema.KindVM || a.Kind == schema.KindTemplateVM {
		return e.client.VM().Update(ctx, a.Node, a.ID, v)
	}
	return e.client.LXC().Update(ctx, a.Node, a.ID, v)
}

func (e *Executor) start(ctx context.Context, a plan.Action) (string, error) {
	if a.Kind == schema.KindVM || a.Kind == schema.KindTemplateVM {
		return e.client.VM().Start(ctx, a.Node, a.ID)
	}
	return e.client.LXC().Start(ctx, a.Node, a.ID)
}

func (e *Executor) delete(ctx context.Context, a plan.Action) (string, error) {
	if a.Kind == schema.KindVM || a.Kind == schema.KindTemplateVM {
		return e.client.VM().Delete(ctx, a.Node, a.ID)
	}
	return e.client.LXC().Delete(ctx, a.Node, a.ID)
}

// stopAndWait stops and awaits the stop task. It returns ""/nil when the
// object was already stopped (PVE may refuse the stop with "already stopped").
func (e *Executor) stopAndWait(ctx context.Context, a plan.Action) (didStop bool, err error) {
	if a.Kind == schema.KindVM || a.Kind == schema.KindTemplateVM {
		upid, err := e.client.VM().Stop(ctx, a.Node, a.ID)
		if err != nil {
			return false, err
		}
		return true, e.waitTask(ctx, a.Node, upid)
	}
	upid, err := e.client.LXC().Stop(ctx, a.Node, a.ID)
	if err != nil {
		return false, err
	}
	return true, e.waitTask(ctx, a.Node, upid)
}

// waitTask polls a PVE task up to e.taskTimeout (bounded by ctx).
func (e *Executor) waitTask(ctx context.Context, node, upid string) error {
	if upid == "" {
		return nil
	}
	tctx, cancel := context.WithTimeout(ctx, e.taskTimeout)
	defer cancel()
	w := pveclient.NewTaskWaiter(e.client, e.log)
	w.SetInterval(e.taskInterval)
	if _, err := w.Wait(tctx, pveclient.TaskID{Node: node, UPID: upid}); err != nil {
		return err
	}
	return nil
}

// recordDeferred marks deferred prunes in the status store as Skipped.
func (e *Executor) recordDeferred(p *plan.Plan) {
	if e.store == nil || len(p.Deferred) == 0 {
		return
	}
	for _, a := range p.Deferred {
		e.store.SetObject(&statusx.Object{
			Kind: a.Kind, Name: a.Name, Node: a.Node, ID: a.ID,
			State:       statusx.Skipped,
			LastAction:  string(plan.Delete),
			PruneReason: "prune deferred (budget or empty-desired anomaly): " + a.Reason,
		})
		e.store.BumpPruneDeferred()
		metrics.PruneDeferred.WithLabelValues(string(a.Kind)).Inc()
	}
}

// --- helpers ---

func (e *Executor) stateFor(a plan.Action, ok bool) statusx.State {
	if !ok {
		return statusx.Failed
	}
	switch a.What {
	case plan.Delete:
		return statusx.Pruned
	default:
		return statusx.Converged
	}
}

func lastConverged(ok bool, state statusx.State) time.Time {
	if ok && state == statusx.Converged {
		return time.Now()
	}
	return time.Time{}
}

// toValues converts PVE form-values map to url.Values (string form).
func toValues(params map[string]any) url.Values {
	v := url.Values{}
	if params == nil {
		return v
	}
	for k, val := range params {
		v.Set(k, fmt.Sprint(val))
	}
	return v
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func errMsg(err error) string { return errString(err) }
