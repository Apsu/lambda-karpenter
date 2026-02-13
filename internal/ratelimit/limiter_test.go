package ratelimit

import (
	"context"
	"sort"
	"sync"
	"testing"
	"time"
)

func TestNewDefaults(t *testing.T) {
	l := New(0, time.Second)
	if l.global == nil {
		t.Fatal("expected global limiter")
	}
}

func TestWaitLaunchPacing(t *testing.T) {
	l := New(100, 50*time.Millisecond)
	ctx := context.Background()

	start := time.Now()
	if err := l.WaitLaunch(ctx); err != nil {
		t.Fatalf("first WaitLaunch: %v", err)
	}
	if err := l.WaitLaunch(ctx); err != nil {
		t.Fatalf("second WaitLaunch: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed < 40*time.Millisecond {
		t.Fatalf("expected pacing delay, elapsed: %v", elapsed)
	}
}

func TestWaitLaunchContextCancel(t *testing.T) {
	l := New(100, 5*time.Second)
	ctx := context.Background()

	// First call to set lastLaunch
	if err := l.WaitLaunch(ctx); err != nil {
		t.Fatalf("WaitLaunch: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	err := l.WaitLaunch(ctx)
	if err == nil {
		t.Fatal("expected context error")
	}
}

func TestWaitLaunchConcurrentSpacing(t *testing.T) {
	const (
		n       = 5
		spacing = 50 * time.Millisecond
		slop    = 15 * time.Millisecond // timer jitter tolerance
	)

	l := New(100, spacing)
	ctx := context.Background()

	var mu sync.Mutex
	times := make([]time.Time, 0, n)
	var wg sync.WaitGroup

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := l.WaitLaunch(ctx); err != nil {
				t.Errorf("WaitLaunch: %v", err)
				return
			}
			mu.Lock()
			times = append(times, time.Now())
			mu.Unlock()
		}()
	}
	wg.Wait()

	if len(times) != n {
		t.Fatalf("expected %d completions, got %d", n, len(times))
	}

	sort.Slice(times, func(i, j int) bool { return times[i].Before(times[j]) })

	for i := 1; i < len(times); i++ {
		gap := times[i].Sub(times[i-1])
		if gap < spacing-slop {
			t.Errorf("launches %d→%d too close: %v (minimum %v)", i-1, i, gap, spacing)
		}
	}

	// Total duration should be at least (n-1) * spacing
	total := times[n-1].Sub(times[0])
	minTotal := time.Duration(n-2) * spacing // allow one interval of slop
	if total < minTotal {
		t.Errorf("total spread %v too short for %d launches at %v spacing", total, n, spacing)
	}
}

func TestWaitLaunchConcurrentContextCancel(t *testing.T) {
	l := New(100, 100*time.Millisecond)

	// First call reserves a slot
	if err := l.WaitLaunch(context.Background()); err != nil {
		t.Fatalf("WaitLaunch: %v", err)
	}

	// Cancel context before the reserved slot arrives
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	err := l.WaitLaunch(ctx)
	if err == nil {
		t.Fatal("expected context error for cancelled waiter")
	}
}
