package ratelimit

import (
	"context"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Limiter provides a global token bucket and a launch pacing gate.
type Limiter struct {
	global *rate.Limiter

	launchMu   sync.Mutex
	launchMin  time.Duration
	lastLaunch time.Time
}

func New(rps int, launchMinInterval time.Duration) *Limiter {
	if rps <= 0 {
		rps = 1
	}
	return &Limiter{
		global:    rate.NewLimiter(rate.Limit(rps), rps),
		launchMin: launchMinInterval,
	}
}

func (l *Limiter) Wait(ctx context.Context) error {
	return l.global.Wait(ctx)
}

// WaitLaunch enforces a minimum spacing between launch requests.
// Each caller reserves the next available slot under the lock, then sleeps
// outside the lock until its slot arrives. This guarantees spacing even when
// multiple goroutines call WaitLaunch concurrently.
func (l *Limiter) WaitLaunch(ctx context.Context) error {
	l.launchMu.Lock()
	now := time.Now()
	next := l.lastLaunch.Add(l.launchMin)
	if now.After(next) {
		next = now
	}
	l.lastLaunch = next
	l.launchMu.Unlock()

	wait := time.Until(next)
	if wait > 0 {
		t := time.NewTimer(wait)
		select {
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		}
	}
	return nil
}
