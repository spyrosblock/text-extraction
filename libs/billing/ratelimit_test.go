package billing

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spyrosblock/text-extraction/libs/billing/blobs"
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

func TestDaily(t *testing.T) {
	c := &clock{t: time.Date(2026, 1, 1, 18, 30, 0, 0, time.UTC)}
	r := &RateLimiter{Blobs: &blobs.Fake{}, Container: "billing", Now: c.Now, backoff: func(int) time.Duration { return 0 }}
	var calls int
	next := func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusNoContent)
	}
	call := func(h http.HandlerFunc) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h(w, httptest.NewRequest("POST", "/api/extract", nil))
		return w
	}

	// limit 0: no cap, and no counter.
	off := r.Daily("producer", 0, next)
	for range 5 {
		call(off)
	}
	if calls != 5 {
		t.Fatalf("uncapped: next called %d times, want 5", calls)
	}
	if n, _ := r.Count(ctx, "app", "producer", 24*time.Hour); n != 0 {
		t.Errorf("uncapped: counted %d calls", n)
	}

	calls = 0
	h := r.Daily("producer", 3, next)
	for i := range 3 {
		if w := call(h); w.Code != http.StatusNoContent {
			t.Fatalf("call %d: status %d", i+1, w.Code)
		}
	}
	w := call(h)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("call 4: status %d, want 429", w.Code)
	}
	if calls != 3 {
		t.Errorf("next called %d times, want 3", calls)
	}
	if got := w.Header().Get("Retry-After"); got != "19800" { // 5h30m to midnight
		t.Errorf("Retry-After = %q, want 19800", got)
	}
	if got := w.Body.String(); !strings.Contains(got, `"error":"daily request limit reached, try again tomorrow"`) {
		t.Errorf("body = %s", got)
	}

	// Another app has its own counter.
	if w := call(r.Daily("account", 3, next)); w.Code != http.StatusNoContent {
		t.Errorf("other app: status %d", w.Code)
	}

	// The next UTC day starts a new count.
	c.t = time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	if w := call(h); w.Code != http.StatusNoContent {
		t.Errorf("next day: status %d", w.Code)
	}

	// Storage failures fail closed.
	calls = 0
	r.Blobs = &blobs.Fake{UploadErr: map[string]error{"billing/ratelimits/": errors.New("storage down")}}
	if w := call(h); w.Code != http.StatusServiceUnavailable {
		t.Errorf("storage failure: status %d, want 503", w.Code)
	}
	if calls != 0 {
		t.Error("storage failure: next called")
	}
}
