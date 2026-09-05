package pveclient

import (
	"errors"
	"testing"
	"time"
)

func TestErrorClassification(t *testing.T) {
	t.Run("IsNotFound no-such-vm", func(t *testing.T) {
		ae := &APIError{StatusCode: 500, Message: "no such vm 100", Target: "x"}
		if !IsNotFound(ae) {
			t.Error("want IsNotFound true")
		}
	})

	t.Run("IsNotFound real 404", func(t *testing.T) {
		ae := &APIError{StatusCode: 404, Message: "not found", Target: "x"}
		if !IsNotFound(ae) {
			t.Error("want IsNotFound true")
		}
	})

	t.Run("IsNotFound negative", func(t *testing.T) {
		ae := &APIError{StatusCode: 500, Message: "vm must be stopped", Target: "x"}
		if IsNotFound(ae) {
			t.Error("want IsNotFound false")
		}
		if IsNotFound(errors.New("no such")) {
			t.Error("IsNotFound must return false for non-APIError")
		}
		if IsNotFound(nil) {
			t.Error("IsNotFound(nil) must be false")
		}
	})

	t.Run("IsInProgress", func(t *testing.T) {
		ae := &APIError{StatusCode: 400, Message: "Operation already in progress", Target: "x"}
		if !IsInProgress(ae) {
			t.Error("want IsInProgress true")
		}
		ae2 := &APIError{StatusCode: 400, Message: "vm is already running", Target: "x"}
		if IsInProgress(ae2) {
			t.Error("want IsInProgress false for already-running")
		}
	})

	t.Run("TaskFailedError", func(t *testing.T) {
		e := &TaskFailedError{UPID: "u@n!t", Message: "boom"}
		if e.Error() == "" {
			t.Error("empty error string")
		}
	})

	t.Run("TaskTimeoutError IsTimeout", func(t *testing.T) {
		e := &TaskTimeoutError{UPID: "u", Elapsed: time.Second, LastStatus: "running"}
		if !IsTimeout(e) {
			t.Error("want IsTimeout true")
		}
	})
}

func TestWriteBreaker(t *testing.T) {
	// Default: many failures allowed before opening.
	b := newWriteBreaker(3, time.Second)
	for i := 0; i < 2; i++ {
		b.recordFail()
	}
	if !b.allows() {
		t.Error("breaker should be closed before threshold")
	}
	// Third failure opens it.
	b.recordFail()
	if b.allows() {
		t.Error("breaker should be open at threshold")
	}
	if !b.isOpen() {
		t.Error("isOpen must be true")
	}

	// A successful write resets it.
	b.recordOK()
	if b.isOpen() || !b.allows() {
		t.Error("recordOK should close the breaker")
	}
}
