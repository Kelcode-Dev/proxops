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
}

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
func (e *Executor) Run(ctx context.Context, p *plan.Plan) []Result {
	results := make([]Result, 0, len(p.Actions))
	for _, a := range p.Actions {
		results = append(results, e.execute(ctx, a))
	}
	e.recordDeferred(p)
	return results
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
			Kind: a.Kind, Name: a.Name, Node: a.Node, ID: a.ID,
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
		e.log.Log(ctx, level, "pveconform.action",
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

// create issues a PVE create for the action kind. CTT (clone template) and
// ISO (storage download) take non-numeric PVE paths, so they are branched off
// from the standard VM/LXC create.
func (e *Executor) create(ctx context.Context, a plan.Action) (string, error) {
	switch a.Kind {
	case schema.KindISO:
		p, _ := a.Params["storage"].(string)
		u, _ := a.Params["url"].(string)
		f, _ := a.Params["filename"].(string)
		if p == "" || u == "" || f == "" {
			return "", fmt.Errorf("ISO: storage/url/filename missing from params")
		}
		return e.client.Storage().Download(ctx, a.Node, p, u, f)
	case schema.KindCTTemplate:
		// PVE accepts only newid/full (+ storage) on /clone. We therefore do
		// a three-step CTT create: clone → (optional description/tags update)
		// → mark-as-template. The plan carries the source cid in "source".
		var src int
		switch t := a.Params["source"].(type) {
		case int:
			src = t
		case int64:
			src = int(t)
		}
		dst := a.ID
		if src <= 0 || dst <= 0 {
			return "", fmt.Errorf("CTT %s: source/newid missing or invalid", a.Name)
		}
		cv := url.Values{}
		if f, ok := a.Params["full"].(string); ok && f == "1" {
			cv.Set("full", "1")
		}
		up, err := e.client.LXC().Clone(ctx, a.Node, src, dst, cv)
		if err != nil {
			return up, err
		}
		if err := e.waitTask(ctx, a.Node, up); err != nil {
			return up, err
		}
		// PVE /clone rejects extra form keys; apply description/tags after.
		if d, ok := a.Params["description"].(string); ok && d != "" {
			uv := url.Values{"description": {d}}
			dup, uerr := e.client.LXC().Update(ctx, a.Node, dst, uv)
			if uerr != nil {
				return dup, uerr
			}
			if werr := e.waitTask(ctx, a.Node, dup); werr != nil {
				return dup, werr
			}
		}
		if tg, ok := a.Params["tags"].(string); ok && tg != "" {
			uv := url.Values{"tags": {tg}}
			dup, uerr := e.client.LXC().Update(ctx, a.Node, dst, uv)
			if uerr != nil {
				return dup, uerr
			}
			if werr := e.waitTask(ctx, a.Node, dup); werr != nil {
				return dup, werr
			}
		}
		// Mark as template.
		tup, terr := e.client.LXC().CTTemplate(ctx, a.Node, dst)
		if terr != nil {
			return "", terr
		}
		return tup, nil
	default:
		v := toValues(a.Params)
		start := a.DesiredPower == "started"
		if a.Kind == schema.KindVM {
			return e.client.VM().Create(ctx, a.Node, v, start)
		}
		return e.client.LXC().Create(ctx, a.Node, v, start)
	}
}

// updateConfig issues a PVE config update. CTT "update" is just the
// mark-as-template op (the cid exists but is not templated).
func (e *Executor) updateConfig(ctx context.Context, a plan.Action) (string, error) {
	if a.Kind == schema.KindCTTemplate {
		return e.client.LXC().MarkTemplate(ctx, a.Node, a.ID)
	}
	v := toValues(a.Params)
	if a.Kind == schema.KindVM {
		return e.client.VM().Update(ctx, a.Node, a.ID, v)
	}
	return e.client.LXC().Update(ctx, a.Node, a.ID, v)
}

func (e *Executor) start(ctx context.Context, a plan.Action) (string, error) {
	if a.Kind == schema.KindVM {
		return e.client.VM().Start(ctx, a.Node, a.ID)
	}
	return e.client.LXC().Start(ctx, a.Node, a.ID)
}

func (e *Executor) delete(ctx context.Context, a plan.Action) (string, error) {
	if a.Kind == schema.KindVM {
		return e.client.VM().Delete(ctx, a.Node, a.ID)
	}
	return e.client.LXC().Delete(ctx, a.Node, a.ID)
}

// stopAndWait stops and awaits the stop task. It returns ""/nil when the
// object was already stopped (PVE may refuse the stop with "already stopped").
func (e *Executor) stopAndWait(ctx context.Context, a plan.Action) (didStop bool, err error) {
	if a.Kind == schema.KindVM {
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
