package billing

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spyrosblock/text-extraction/billing/blobs"
)

func TestLoginTokens(t *testing.T) {
	b := &blobs.Fake{}
	c := &clock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	l := &LoginTokens{Blobs: b, Container: "billing", TTL: 15 * time.Minute, Now: c.Now}

	token, err := l.Create(ctx, "ann@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if email, err := l.Consume(ctx, token); err != nil || email != "ann@example.com" {
		t.Fatalf("Consume = %q, %v", email, err)
	}
	if _, err := l.Consume(ctx, token); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("second Consume err = %v", err)
	}
	if _, err := l.Consume(ctx, ""); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("empty token err = %v", err)
	}

	expired, _ := l.Create(ctx, "ann@example.com")
	c.t = c.t.Add(15 * time.Minute)
	if _, err := l.Consume(ctx, expired); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("expired token err = %v", err)
	}
}

// Of parallel verifies of one token, exactly one gets the email.
func TestLoginTokenConsumedOnce(t *testing.T) {
	l := &LoginTokens{Blobs: &blobs.Fake{}, Container: "billing", TTL: time.Minute}
	token, err := l.Create(ctx, "ann@example.com")
	if err != nil {
		t.Fatal(err)
	}
	var won atomic.Int32
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			_, err := l.Consume(ctx, token)
			switch {
			case err == nil:
				won.Add(1)
			case !errors.Is(err, ErrInvalidToken):
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if won.Load() != 1 {
		t.Errorf("%d verifies won, want 1", won.Load())
	}
}
