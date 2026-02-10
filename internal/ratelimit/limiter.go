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
func (l *Limiter) WaitLaunch(ctx context.Context) error {
	l.launchMu.Lock()
	defer l.launchMu.Unlock()

	now := time.Now()
	next := l.lastLaunch.Add(l.launchMin)
	if now.Before(next) {
		t := time.NewTimer(next.Sub(now))
		select {
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		}
	}
	l.lastLaunch = time.Now()
	return nil
}
