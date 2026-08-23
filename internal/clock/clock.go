// Package clock abstracts wall clock access so business deadlines, session
// expiry and retry backoff can be exercised deterministically in tests.
package clock

import (
	"sync"
	"time"
)

// BusinessLocation is the authoritative timezone for editorial deadlines.
var BusinessLocation = time.FixedZone("CUT", 8*3600)

// Clock exposes the subset of time operations used by the services.
type Clock interface {
	Now() time.Time
}

// SystemClock reads the real wall clock in the business timezone.
type SystemClock struct{}

// Now returns the current time in the business timezone.
func (SystemClock) Now() time.Time { return time.Now().In(BusinessLocation) }

// Fixed is a controllable clock used by tests and the deterministic worker
// harness.
type Fixed struct {
	mu  sync.Mutex
	now time.Time
}

// NewFixed builds a controllable clock anchored at the given instant.
func NewFixed(at time.Time) *Fixed {
	return &Fixed{now: at.In(BusinessLocation)}
}

// Now returns the currently configured instant.
func (f *Fixed) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

// Advance moves the clock forward and returns the new instant.
func (f *Fixed) Advance(d time.Duration) time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
	return f.now
}

// Set overrides the clock instant.
func (f *Fixed) Set(at time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = at.In(BusinessLocation)
}

// BusinessDay renders the calendar day used by editorial reports.
func BusinessDay(at time.Time) string {
	return at.In(BusinessLocation).Format("2006-01-02")
}

// Truncate normalizes a timestamp to millisecond precision so values survive a
// round trip through the relational store without drift.
func Truncate(at time.Time) time.Time {
	return at.In(BusinessLocation).Truncate(time.Millisecond)
}
