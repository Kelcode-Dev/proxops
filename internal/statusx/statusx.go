// Package statusx is proxops's in-memory convergence status store.
//
// There is no on-disk state (plan §7): the store is rebuilt from git + PVE
// each process start and reflects only the live process's reconcile results.
// It is exposed over /status and drives the health/readiness checks.
//
// The store is a concurrent container guarded by RWMutex. The reconcile
// pipeline writes; the HTTP server reads.
package statusx

import (
	"sync"
	"time"

	"github.com/Kelcode-Dev/proxops/internal/schema"
)

// State is the convergence state of one managed object.
type State string

const (
	// Desired: present in git, not yet known on PVE.
	Desired State = "desired"
	// Drift: on PVE but diverging from git.
	Drift State = "drift"
	// Converged: git and PVE agree.
	Converged State = "converged"
	// Orphan: on PVE (tagged) but absent from git.
	Orphan State = "orphan"
	// Pruned: an orphan that was deleted this cycle.
	Pruned State = "pruned"
	// Skipped: not acted on this cycle (deferred / untagged / read-only).
	Skipped State = "skipped"
	// Anomalous: live-only PVE devices that proxops intentionally does
	// NOT auto-delete; surfaced to the operator on /status.
	Anomalous State = "anomalous"
	// InProgress: an action is underway.
	InProgress State = "in_progress"
	// Failed: the last action errored.
	Failed State = "failed"
)

// Object records one managed object's latest status.
//
// Cluster scopes the record in a multi-cluster deployment: the same PVE
// name/ID can exist on different clusters, so per-cluster reconcilers tag
// every record with their cluster identity. Single-cluster operators see
// exactly one cluster value on every object.
type Object struct {
	Kind            schema.Kind `json:"kind"`
	Name            string      `json:"name"`
	Cluster         string      `json:"cluster,omitempty"`
	Node            string      `json:"node"`
	ID              int         `json:"id"`
	State           State       `json:"state"`
	LastAction      string      `json:"last_action,omitempty"`
	LastError       string      `json:"last_error,omitempty"`
	LastConvergedAt time.Time   `json:"last_converged_at,omitempty"`
	PruneReason     string      `json:"prune_reason,omitempty"`
	UpdatedAt       time.Time   `json:"updated_at"`
}

// Ref is the logical key for an object.
//
// Cluster is part of the key in M8 multi-cluster deployments so two
// clusters that both reconcile an object named "cache-01" keep separate
// status records in the shared Store.
type Ref struct {
	Cluster string
	Kind    schema.Kind
	Name    string
}

// Cycle summarizes the most recent reconcile cycle.
type Cycle struct {
	Commit        string    `json:"commit"`
	StartedAt     time.Time `json:"started_at"`
	FinishedAt    time.Time `json:"finished_at"`
	Objects       int       `json:"objects"`
	ActionsOK     int       `json:"actions_ok"`
	ActionsError  int       `json:"actions_error"`
	Pruned        int       `json:"pruned"`
	PruneDeferred int       `json:"prune_deferred"`
	Skipped       int       `json:"skipped"`
	Anomalies     int       `json:"anomalies"`
	DesiredStale  bool      `json:"desired_stale"`
	ReadOnly      bool      `json:"read_only"`
	Aborted       bool      `json:"aborted"`
	AbortReason   string    `json:"abort_reason,omitempty"`
}

// Store is the thread-safe status container.
type Store struct {
	mu      sync.RWMutex
	objects map[Ref]Object
	last    *Cycle
}

// New returns an empty Store.
func New() *Store {
	return &Store{objects: map[Ref]Object{}}
}

// BeginCycle marks the start of a new reconcile cycle. Existing objects are
// retained so a /status call mid-cycle still reflects the most recent known
// state for each object.
func (s *Store) BeginCycle(commit string, desiredStale, readOnly bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.last = &Cycle{
		Commit:       commit,
		StartedAt:    time.Now(),
		DesiredStale: desiredStale,
		ReadOnly:     readOnly,
	}
}

// SetObject records (or updates) the status of one object.
func (s *Store) SetObject(o *Object) {
	if o == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *o
	cp.UpdatedAt = time.Now()
	s.objects[Ref{Cluster: cp.Cluster, Kind: cp.Kind, Name: cp.Name}] = cp
}

// BumpActionsOK increments the action success counter.
func (s *Store) BumpActionsOK() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.last != nil {
		s.last.ActionsOK++
	}
}

// BumpActionsError increments the action error counter.
func (s *Store) BumpActionsError() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.last != nil {
		s.last.ActionsError++
	}
}

// BumpPruned increments the pruned counter.
func (s *Store) BumpPruned() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.last != nil {
		s.last.Pruned++
	}
}

// BumpPruneDeferred increments the prune-deferred counter.
func (s *Store) BumpPruneDeferred() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.last != nil {
		s.last.PruneDeferred++
	}
}

// BumpAnomaly increments the anomaly counter on the current cycle.
// Anomalies are live-only-slot observations proxops intentionally did
// not act on (non-destructive by design).
func (s *Store) BumpAnomaly() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.last != nil {
		s.last.Anomalies++
	}
}

// BumpSkipped counts a single skip (untagged objects, fail-closed reads).
// Called once per skipped action.
func (s *Store) BumpSkipped() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.last != nil {
		s.last.Skipped++
	}
}

// FinishCycle closes the cycle and records object totals.
func (s *Store) FinishCycle(aborted bool, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.last == nil {
		return
	}
	s.last.FinishedAt = time.Now()
	s.last.Aborted = aborted
	s.last.AbortReason = reason
	s.last.Objects = len(s.objects)
}

// Objects returns a snapshot of all object records, in deterministic
// (cluster, kind, name) order.
func (s *Store) Objects() []Object {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Object, 0, len(s.objects))
	for _, o := range s.objects {
		out = append(out, o)
	}
	sortObjects(out)
	return out
}

func sortObjects(o []Object) {
	for i := 1; i < len(o); i++ {
		for j := i; j > 0; j-- {
			if !lessObject(o[j], o[j-1]) {
				break
			}
			o[j], o[j-1] = o[j-1], o[j]
		}
	}
}

// lessObject orders records by cluster, then kind, then name.
func lessObject(a, b Object) bool {
	if a.Cluster != b.Cluster {
		return a.Cluster < b.Cluster
	}
	if string(a.Kind) != string(b.Kind) {
		return string(a.Kind) < string(b.Kind)
	}
	return a.Name < b.Name
}

// ObjectCount returns the number of distinct managed objects.
func (s *Store) ObjectCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.objects)
}

// Last returns the in-progress or most recent cycle.
func (s *Store) Last() *Cycle {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.last == nil {
		return nil
	}
	cp := *s.last
	return &cp
}
