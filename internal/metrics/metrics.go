// Package metrics centralises the prometheus collectors exposed by the
// agent. All counters/gauges are registered on a shared prometheus.Registry
// so /metrics can serve a single, stable set of series.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

var (
	registry = prometheus.NewRegistry()

	// CyclesTotal counts reconcile cycles by result.
	CyclesTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "proxops_cycles_total",
		Help: "Reconcile cycles completed, by result.",
	}, []string{"result"}) // ok | parse_error | git_error | aborted

	// GitLastFetchAgeSeconds is the age of the last successful git fetch.
	GitLastFetchAgeSeconds = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "proxops_git_last_fetch_age_seconds",
		Help: "Age of the last successful git fetch in seconds.",
	})

	// PVELastAuthAgeSeconds is the age of the last successful PVE auth/read.
	PVELastAuthAgeSeconds = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "proxops_pve_last_read_age_seconds",
		Help: "Age of the last successful PVE API read in seconds.",
	})

	// ActionsTotal counts executed plan actions by kind, action and result.
	ActionsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "proxops_actions_total",
		Help: "Actions executed, by kind, action and result.",
	}, []string{"kind", "action", "result"}) // result: ok | error | skipped

	// PruneDeferred counts objects whose deletion was deferred by the prune budget.
	PruneDeferred = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "proxops_prune_deferred_total",
		Help: "Prune actions deferred because the per-cycle budget was exhausted.",
	}, []string{"kind"})

	// Anomalies counts suspicious events (empty desired state with live objects).
	Anomalies = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "proxops_anomalies_total",
		Help: "Anomalous events detected by the reconciler.",
	}, []string{"type"}) // empty_desired | live_only_slot | dag_cycle | api_error_shaped_unexpected

	// AnomaliesTotal is a non-versioned alias for Anomalies used in new code
	// paths that bump the live-only-slot counter (drift anomalies). Same
	// counter family as `Anomalies`.
	AnomaliesTotal = Anomalies

	// ReadOnly is 1 when the write circuit breaker is open.
	ReadOnly = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "proxops_read_only",
		Help: "1 when the write circuit breaker is open (PVE is read-only).",
	})

	// DesiredStale is 1 when the reconciler is running against a cached tree
	// because git fetch keeps failing.
	DesiredStale = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "proxops_desired_stale",
		Help: "1 when desired state is stale due to repeated git fetch failures.",
	})

	// Objects tracks the number of managed objects per kind and state,
	// updated at the end of every cycle.
	Objects = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "proxops_objects",
		Help: "Number of managed objects per kind and state.",
	}, []string{"kind", "state"}) // state: wanted | present | converged | failed | skipped | missing | pruning
)

// Register wires all collectors into the shared registry and returns it.
func Register() *prometheus.Registry {
	registry.MustRegister(
		CyclesTotal,
		GitLastFetchAgeSeconds,
		PVELastAuthAgeSeconds,
		ActionsTotal,
		PruneDeferred,
		Anomalies,
		ReadOnly,
		DesiredStale,
		Objects,
		collectors.NewGoCollector(),
	)
	return registry
}
