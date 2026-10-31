package billing

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// clock is a settable test clock.
type clock struct{ t time.Time }

func (c *clock) Now() time.Time { return c.t }

func TestSessions(t *testing.T) {
	a, b := newAccounts()
	c := &clock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	s := &Sessions{Blobs: b, Container: "billing", Accounts: a, TTL: 7 * 24 * time.Hour, Now: c.Now}
	id, _, err := a.FindOrCreate(ctx, "ann@example.com")
	if err != nil {
		t.Fatal(err)
	}

	token, err := s.Create(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range b.Names() {
		if strings.Contains(n, token) {
			t.Errorf("token in blob name %s", n)
		}
	}
	got, acc, err := s.Resolve(ctx, token)
	if err != nil || got != id || acc.Email != "ann@example.com" {
		t.Fatalf("Resolve = %q %+v %v", got, acc, err)
	}
	for _, bad := range []string{"", "nope", token + "x"} {
		if _, _, err := s.Resolve(ctx, bad); !errors.Is(err, ErrNoSession) {
			t.Errorf("Resolve(%q) err = %v", bad, err)
		}
	}

	c.t = c.t.Add(7*24*time.Hour - time.Second)
	if _, _, err := s.Resolve(ctx, token); err != nil {
		t.Errorf("Resolve before expiry: %v", err)
	}
	c.t = c.t.Add(time.Second)
	if _, _, err := s.Resolve(ctx, token); !errors.Is(err, ErrNoSession) {
		t.Errorf("Resolve after expiry err = %v", err)
	}
}

func TestSessionDeleted(t *testing.T) {
	a, b := newAccounts()
	s := &Sessions{Blobs: b, Container: "billing", Accounts: a, TTL: time.Hour}
	id, _, _ := a.FindOrCreate(ctx, "ann@example.com")
	t1, _ := s.Create(ctx, id)
	t2, _ := s.Create(ctx, id)

	if err := s.Delete(ctx, t1); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Resolve(ctx, t1); !errors.Is(err, ErrNoSession) {
		t.Errorf("Resolve after logout err = %v", err)
	}
	if err := s.Delete(ctx, t1); err != nil {
		t.Errorf("second Delete: %v", err)
	}

	// A session of a deleted account is no longer valid.
	if err := a.Delete(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Resolve(ctx, t2); !errors.Is(err, ErrNoSession) {
		t.Errorf("Resolve for a deleted account err = %v", err)
	}
}
