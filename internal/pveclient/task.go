package pveclient

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// TaskPollInterval is the default cadence for polling a running PVE task.
const TaskPollInterval = 5 * time.Second

// TaskStatus is the subset of PVE's task-status response we consume.
//
// GET /nodes/{n}/tasks/{upid}/status returns, when a task settles:
//
//	{"data": {"status": "stopped", "exitstatus": "OK"}}
//	{"data": {"status": "stopped", "exitstatus": "unable to create VM 9100 -
//	          lvcreate 'pve/vm-9100-disk-0' error: Volume too large (8.00 EiB)"}}
//
// i.e. PVE reports a failed task's reason in the exitstatus STRING (verified
// against PVE 9.2's task-status endpoint; success is the literal "OK"). The
// "errors" field belongs to the *parameter-validation* envelope of HTTP 4xx
// responses and is never populated on task status, so it is not decoded here
// — decoding it previously surfaced every failed task as "unknown error".
//
// While running: {"status": "running", "progress": 0.42, ...}.
type TaskStatus struct {
	Status     string   `json:"status"`
	Progress   *float64 `json:"progress"`
	ExitStatus string   `json:"exitstatus"`
}

// IsStopped reports whether the task has terminated.
func (s TaskStatus) IsStopped() bool { return s.Status == "stopped" }

// IsOK reports whether the task terminated successfully.
//
// PVE task exit-status vocabulary:
//   - "OK"               → success
//   - "WARNINGS: N"      → success with N cosmetic warnings (PVE's exit code
//     "0 with warnings"; still a success)
//   - "ERROR: <text>" /
//     "FAILED: <text>"   → failure
//
// Treating "WARNINGS:" as success matches PVE's own semantics (e.g. `qm`
// returns exit code 0 when it emits a warning).
func (s TaskStatus) IsOK() bool {
	if s.Status != "stopped" {
		return false
	}
	switch {
	case strings.EqualFold(s.ExitStatus, "OK"),
		strings.HasPrefix(strings.ToUpper(s.ExitStatus), "WARNINGS:"):
		return true
	}
	return false
}

// TaskID identifies a PVE task.
type TaskID struct {
	Node string
	UPID string
}

// TaskNotFound is returned when a task UPID cannot be found on PVE.
type TaskNotFound struct {
	TaskID
}

func (e *TaskNotFound) Error() string {
	return fmt.Sprintf("pve task %s on node %s not found", e.UPID, e.Node)
}

// TaskWaiter polls PVE task status until completion.
//
// The overall deadline is carried on ctx — the reconcile executor wraps
// Wait with context.WithTimeout(Rec.TaskTimeout).
type TaskWaiter struct {
	client   *Client
	interval time.Duration
	log      *slog.Logger
}

// NewTaskWaiter builds a TaskWaiter with the default 5s poll interval.
func NewTaskWaiter(c *Client, log *slog.Logger) *TaskWaiter {
	if log == nil {
		log = slog.Default()
	}
	return &TaskWaiter{client: c, interval: TaskPollInterval, log: log}
}

// SetInterval overrides the polling interval.
func (w *TaskWaiter) SetInterval(d time.Duration) {
	if d > 0 {
		w.interval = d
	}
}

// Status fetches the task's current state.
func (w *TaskWaiter) Status(ctx context.Context, tid TaskID) (TaskStatus, error) {
	return w.statusNow(ctx, tid)
}

// StatusPath builds the task-status API path for a TaskID.
func StatusPath(node, upid string) string {
	return fmt.Sprintf("/nodes/%s/tasks/%s/status", node, url.PathEscape(upid))
}

func (w *TaskWaiter) statusNow(ctx context.Context, tid TaskID) (TaskStatus, error) {
	var out TaskStatus
	if _, err := w.client.Do(ctx, http.MethodGet, tid.Node, StatusPath(tid.Node, tid.UPID), nil, &out); err != nil {
		if nf, ok := err.(*TaskNotFound); ok {
			return out, nf
		}
		if IsNotFound(err) {
			return out, &TaskNotFound{TaskID: tid}
		}
		return out, err
	}
	return out, nil
}

// Wait polls the task until it stops and returns the final status.
//
// Results:
//   - (*TaskStatus, nil)         → task completed with exitstatus OK.
//   - (*TaskFailedError, err)    → task finished, exitstatus != OK.
//   - (*TaskTimeoutError, err)   → ctx deadline exceeded; last observed status
//     is attached to the error via LastStatus.
//   - (*TaskNotFound, err)       → task vanished from PVE (reaped / foreign).
//
// Transient read errors during polling are logged and retried.
func (w *TaskWaiter) Wait(ctx context.Context, tid TaskID) (*TaskStatus, error) {
	interval := w.interval
	var last TaskStatus
	start := time.Now()

	for {
		s, err := w.statusNow(ctx, tid)
		if err != nil {
			if nf, ok := err.(*TaskNotFound); ok {
				return &last, nf
			}
			// transient: log and keep polling.
			s = last
			w.log.Warn("task status read failed; retrying", "node", tid.Node, "upid", tid.UPID, "err", err.Error())
		} else {
			last = s
		}

		if last.IsStopped() {
			if last.IsOK() {
				return &last, nil
			}
			// PVE reports the failure reason in exitstatus (a human
			// sentence, e.g. "unable to create VM 9100 - lvcreate ... error:").
			msg := strings.TrimSpace(last.ExitStatus)
			if msg == "" || msg == "OK" {
				msg = "task stopped with a non-OK exit status"
			}
			return &last, &TaskFailedError{UPID: tid.UPID, Message: msg}
		}

		select {
		case <-ctx.Done():
			elapsed := time.Since(start)
			return &last, &TaskTimeoutError{UPID: tid.UPID, Elapsed: elapsed, LastStatus: last.Status}
		case <-time.After(interval):
		}
	}
}
