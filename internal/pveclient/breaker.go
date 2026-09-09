package pveclient

import (
	"sync"
	"time"
)

// writeBreaker is a simple open/close circuit breaker for PVE write operations.
// After threshold consecutive write failures it opens; it stays open for
// openDuration; once open it allows a single half-open probe to escape. Reads
// are never gated — failures are observed but writes are protected.
type writeBreaker struct {
	mu           sync.Mutex
	threshold    int
	openDuration time.Duration
	consec       int
	open         bool
	openUntil    time.Time
}

func newWriteBreaker(threshold int, openDuration time.Duration) *writeBreaker {
	if threshold <= 0 {
		threshold = 20
	}
	if openDuration <= 0 {
		openDuration = 10 * time.Minute
	}
	return &writeBreaker{threshold: threshold, openDuration: openDuration}
}

// allows reports whether a write may proceed.
func (b *writeBreaker) allows() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.open {
		return true
	}
	now := time.Now()
	if now.After(b.openUntil) {
		// half-open: permit the first probe.
		b.open = false
		b.consec = 0
		return true
	}
	return false
}

// recordOK resets the breaker after a successful write.
func (b *writeBreaker) recordOK() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.consec = 0
	b.open = false
}

// recordFail counts a write failure and opens the breaker at threshold.
func (b *writeBreaker) recordFail() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.consec++
	if b.consec >= b.threshold && !b.open {
		b.open = true
		b.openUntil = time.Now().Add(b.openDuration)
	}
}

// isOpen reports the current open state.
func (b *writeBreaker) isOpen() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.open
}
