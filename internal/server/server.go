// Package server exposes the agent's read-only HTTP surface:
//   - /healthz : liveness + readiness (git age, PVE age, recent cycle)
//   - /metrics : prometheus metrics (shared registry)
//   - /status  : JSON convergence table (last cycle + per-object state)
//
// Webhook endpoints land post-MVP; the mux is intentionally minimal.
package server

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/GizzmoShifu/proxmox-operator/internal/statusx"
)

// Info inputs to /healthz, captured by the caller (the agent) each poll.
type Info struct {
	// Ready is false before the first successful cycle.
	Ready bool
	// GitAgeSinceSync: time since last successful git sync.
	GitAge time.Duration
	// PVEAgeSinceRead: time since last successful PVE read.
	PVEAge time.Duration
}

// Server is the HTTP surface for one pveconform process.
type Server struct {
	registry *prometheus.Registry
	store    *statusx.Store
	info     func() Info
	start    time.Time
	listen   string
}

// New builds a Server.
func New(registry *prometheus.Registry, store *statusx.Store, info func() Info) *Server {
	return &Server{registry: registry, store: store, info: info, start: time.Now()}
}

// SetListen stores the bound address (informational for /healthz).
func (s *Server) SetListen(addr string) { s.listen = addr }

// Handle returns the root handler.
func (s *Server) Handle() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.Handle("/metrics", promhttp.HandlerFor(s.registry, promhttp.HandlerOpts{}))
	mux.HandleFunc("/status", s.handleStatus)
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("pveconform — endpoints: /healthz /metrics /status\n"))
	})
	return mux
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	info := s.info()
	w.Header().Set("Content-Type", "application/json")
	if !info.Ready {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"ok":false,"reason":"not_ready"}`))
		return
	}
	if info.GitAge > 2*time.Minute || info.PVEAge > 2*time.Minute {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"ok":false,"reason":"stale"}`))
		return
	}
	enc := json.NewEncoder(w)
	_ = enc.Encode(map[string]any{"ok": true, "since": s.start.Format(time.RFC3339)})
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	out := struct {
		ProcessStart time.Time        `json:"process_start"`
		LastCycle    *statusx.Cycle   `json:"last_cycle"`
		Objects      []statusx.Object `json:"objects"`
	}{}
	out.ProcessStart = s.start
	out.LastCycle = s.store.Last()
	out.Objects = s.store.Objects()
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	_ = enc.Encode(out)
}
