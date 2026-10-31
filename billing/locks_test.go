package billing

import (
	"errors"
	"testing"
	"time"

	"github.com/spyrosblock/text-extraction/billing/blobs"
)

func TestLocks(t *testing.T) {
	b := &blobs.Fake{}
	clock := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	l := &Locks{Blobs: b, Container: "billing", Now: func() time.Time { return clock }}

	release, err := l.Acquire(ctx, "a", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Acquire(ctx, "a", time.Minute); !errors.Is(err, ErrLocked) {
		t.Errorf("second Acquire err = %v, want ErrLocked", err)
	}
	other, err := l.Acquire(ctx, "b", time.Minute)
	if err != nil {
		t.Fatalf("another lock: %v", err)
	}
	if err := other(ctx); err != nil {
		t.Fatal(err)
	}
	if err := release(ctx); err != nil {
		t.Fatal(err)
	}
	if err := release(ctx); err != nil {
		t.Errorf("second release: %v", err)
	}
	again, err := l.Acquire(ctx, "a", time.Minute)
	if err != nil {
		t.Fatalf("after release: %v", err)
	}

	// An expired lock is taken over; its old holder's release leaves the
	// new lock alone.
	clock = clock.Add(time.Minute)
	takeover, err := l.Acquire(ctx, "a", time.Minute)
	if err != nil {
		t.Fatalf("expired lock: %v", err)
	}
	if err := again(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Acquire(ctx, "a", time.Minute); !errors.Is(err, ErrLocked) {
		t.Errorf("after the old holder's release err = %v, want ErrLocked", err)
	}
	if err := takeover(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Acquire(ctx, "a", time.Minute); err != nil {
		t.Errorf("after release: %v", err)
	}

	// An unreadable lock is taken over.
	b.Put("billing", "locks/c.json", []byte("{"))
	if _, err := l.Acquire(ctx, "c", time.Minute); err != nil {
		t.Errorf("unreadable lock: %v", err)
	}
}
