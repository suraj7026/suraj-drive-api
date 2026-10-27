package joblease

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestTimeoutScalesToHundredsOfGiB(t *testing.T) {
	if got := Timeout(30*time.Minute, 100<<30); got < 5*time.Hour {
		t.Fatalf("large upload budget: %v", got)
	}
	if got := Timeout(5*time.Minute, 0); got != 5*time.Minute {
		t.Fatalf("baseline: %v", got)
	}
}

func TestLostLeaseCancelsWork(t *testing.T) {
	lost := errors.New("lease lost")
	err := Run(context.Background(), time.Millisecond, func(context.Context) error { return lost }, func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() })
	if !errors.Is(err, lost) {
		t.Fatalf("expected lease failure, got %v", err)
	}
}

func TestSuccessfulCommitIsNotFailedByConcurrentHeartbeat(t *testing.T) {
	renewing := make(chan struct{})
	committed := make(chan struct{})
	err := Run(context.Background(), time.Millisecond, func(context.Context) error {
		close(renewing)
		<-committed
		return errors.New("claim already marked succeeded")
	}, func(context.Context) error {
		<-renewing
		close(committed)
		return nil
	})
	if err != nil {
		t.Fatalf("successful commit was treated as failure: %v", err)
	}
}

func TestWorkFailureAndParentCancellationStopHeartbeat(t *testing.T) {
	failure := errors.New("storage unavailable")
	err := Run(context.Background(), time.Hour, func(context.Context) error { t.Fatal("unexpected renewal"); return nil }, func(context.Context) error { return failure })
	if !errors.Is(err, failure) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Run(ctx, time.Hour, func(context.Context) error { return nil }, func(ctx context.Context) error { return ctx.Err() }); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
