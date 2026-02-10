package ratelimit

import (
	"context"
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

func TestWaitLaunchConcurrency(t *testing.T) {
	l := New(100, 10*time.Millisecond)
	ctx := context.Background()
	var wg sync.WaitGroup

	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = l.WaitLaunch(ctx)
		}()
	}
	wg.Wait()
}
