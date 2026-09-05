package pveclient

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// APIError is a semantic PVE API failure: an HTTP error status with a
// decodable PVE error envelope, or a non-recoverable authentication failure.
//
// PVE does not use 404 semantics consistently — a missing VM typically
// returns HTTP 500 with the message "no such vm" (PVE-VERIFY on a live host).
type APIError struct {
	StatusCode int
	PVECode    int
	Message    string
	Target     string
}

func (e *APIError) Error() string {
	if e.PVECode != 0 {
		return fmt.Sprintf("pve api error (http %d, pve code %d) on %s: %s", e.StatusCode, e.PVECode, e.Target, e.Message)
	}
	return fmt.Sprintf("pve api error (http %d) on %s: %s", e.StatusCode, e.Target, e.Message)
}

// IsNotFound reports whether err indicates a missing PVE resource. It matches
// both real HTTP 404s and PVE's 500-with-"no such ..." convention.
func IsNotFound(err error) bool {
	var api *APIError
	if err == nil || !errors.As(err, &api) {
		return false
	}
	if api.StatusCode == http.StatusNotFound {
		return true
	}
	m := strings.ToLower(api.Message)
	return strings.Contains(m, "no such")
}

// IsInProgress reports whether err is PVE's "operation already in progress".
// The executor uses this to re-attach to the existing task instead of failing
// (statelessness: after a restart an in-flight op may still be running on PVE).
func IsInProgress(err error) bool {
	var api *APIError
	if err == nil || !errors.As(err, &api) {
		return false
	}
	return strings.Contains(strings.ToLower(api.Message), "operation already in progress")
}

// TaskFailedError is returned by WaitTask when a PVE task terminates without an
// OK exit status.
type TaskFailedError struct {
	UPID    string
	Message string
}

func (e *TaskFailedError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("task %s failed", e.UPID)
	}
	return fmt.Sprintf("task %s failed: %s", e.UPID, e.Message)
}

// TaskTimeoutError is returned by WaitTask when a task had not completed by the
// deadline. The task may still complete on PVE's side; the next reconcile cycle
// re-diffs live state, which is authoritative for reporting.
type TaskTimeoutError struct {
	UPID       string
	Elapsed    time.Duration
	LastStatus string
}

func (e *TaskTimeoutError) Error() string {
	return fmt.Sprintf("task %s did not complete in %s (last status: %s)", e.UPID, e.Elapsed.Round(time.Second), e.LastStatus)
}

// IsTimeout reports whether err is a TaskTimeoutError.
func IsTimeout(err error) bool {
	var t *TaskTimeoutError
	return errors.As(err, &t)
}
