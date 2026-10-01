package main

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

// A consumer that fails a few times and then succeeds must be re-entered by
// the retry loop instead of killing the process: one Redis blip is a retry,
// not a restart.
func TestConsumeWithRetryRecoversAfterFailures(t *testing.T) {
	var calls atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	consume := func(ctx context.Context) error {
		if calls.Add(1) <= 3 {
			return errors.New("redis read failed")
		}
		// Recovery: the re-entered consumer is healthy, and the shutdown
		// signal arrives. Cancellation is what lets the loop exit -- a real
		// Consume only returns on error or cancellation, never nil-forever.
		cancel()
		return errors.New("shutting down")
	}
	logger := slog.New(slog.NewTextHandler(discardWriter{}, nil))

	done := make(chan struct{})
	go func() {
		consumeWithRetry(ctx, logger, "test", consume)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("consumeWithRetry did not return after cancellation")
	}
	if calls.Load() < 4 {
		t.Fatalf("consume called %d times, want at least 4 (3 failures + re-entry)", calls.Load())
	}
}

// Cancellation during the backoff wait must return promptly: a worker shutting
// down must not sit in a retry sleep while the orchestrator is waiting.
func TestConsumeWithRetryReturnsDuringBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	logger := slog.New(slog.NewTextHandler(discardWriter{}, nil))
	consume := func(context.Context) error { return errors.New("boom") }

	done := make(chan struct{})
	go func() {
		consumeWithRetry(ctx, logger, "test", consume)
		close(done)
	}()
	// Let the first failure land and the backoff start, then shut down.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("consumeWithRetry did not return during backoff")
	}
}

// Before the loop ever runs, a cancelled context must return without calling
// consume at all: the drain path after a signal must not start new work.
func TestConsumeWithRetryHonoursPreCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	logger := slog.New(slog.NewTextHandler(discardWriter{}, nil))
	var calls atomic.Int32
	consume := func(context.Context) error {
		calls.Add(1)
		return nil
	}
	consumeWithRetry(ctx, logger, "test", consume)
	if calls.Load() != 0 {
		t.Fatalf("consume called %d times with a pre-cancelled context", calls.Load())
	}
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }
