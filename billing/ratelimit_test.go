package billing

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spyrosblock/text-extraction/billing/blobs"
)

func TestRateLimiter(t *testing.T) {
	b := &blobs.Fake{}
	c := &clock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	r := &RateLimiter{Blobs: b, Container: "billing", Now: c.Now, backoff: func(int) time.Duration { return 0 }}
	const window = 15 * time.Minute

	for i := range 3 {
		if ok, err := r.Allow(ctx, "email", "x", 3, window); err != nil || !ok {
			t.Fatalf("event %d: %v, %v", i+1, ok, err)
		}
	}
	if ok, _ := r.Allow(ctx, "email", "x", 3, window); ok {
		t.Error("4th event allowed")
	}
	if ok, _ := r.Allow(ctx, "email", "y", 3, window); !ok {
		t.Error("another value limited")
	}
	if ok, _ := r.Allow(ctx, "ip", "x", 3, window); !ok {
		t.Error("another kind limited")
	}
	if n, err := r.Count(ctx, "email", "x", window); err != nil || n != 4 {
		t.Errorf("Count = %d, %v", n, err)
	}

	c.t = c.t.Add(window)
	if n, _ := r.Count(ctx, "email", "x", window); n != 0 {
		t.Errorf("Count in the next window = %d", n)
	}
	if ok, _ := r.Allow(ctx, "email", "x", 3, window); !ok {
		t.Error("next window limited")
	}

	// Values are escaped into one path segment.
	if _, err := r.Add(ctx, "ip", "2001:db8::1/../x", window); err != nil {
		t.Fatal(err)
	}
	for _, n := range b.Names() {
		if strings.Count(n, "/") != 4 {
			t.Errorf("blob %s has more than one value segment", n)
		}
	}
}

func TestRateLimiterParallel(t *testing.T) {
	r := &RateLimiter{Blobs: &blobs.Fake{}, Container: "billing"}
	var wg sync.WaitGroup
	for range 5 {
		wg.Go(func() {
			if _, err := r.Add(ctx, "ip", "1.2.3.4", time.Hour); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if n, _ := r.Count(ctx, "ip", "1.2.3.4", time.Hour); n != 5 {
		t.Errorf("Count = %d, want 5", n)
	}
}
