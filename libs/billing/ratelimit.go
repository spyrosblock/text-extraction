package billing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/spyrosblock/text-extraction/libs/billing/blobs"
)

// RateLimiter counts events in fixed windows, one blob per kind, value and
// window: ratelimits/<kind>/<value>/<window start>.json. Counters are
// incremented with If-Match. A lifecycle rule deletes them a day after their
// last write, so windows must be at most a day.
type RateLimiter struct {
	Blobs     BlobStore
	Container string
	// Now is the clock; time.Now if nil.
	Now func() time.Time

	// backoff is the wait before retry n (1-based); overridden in tests.
	backoff func(n int) time.Duration
}

type counter struct {
	Count int64 `json:"count"`
}

func (r *RateLimiter) blob(kind, value string, window time.Duration) string {
	start := now(r.Now).Truncate(window).Unix()
	return "ratelimits/" + kind + "/" + url.PathEscape(value) + "/" + strconv.FormatInt(start, 10) + ".json"
}

// Allow counts an event and reports whether it is within limit events per
// window.
func (r *RateLimiter) Allow(ctx context.Context, kind, value string, limit int64, window time.Duration) (bool, error) {
	n, err := r.Add(ctx, kind, value, window)
	return n <= limit, err
}

// Count returns the events counted in the current window.
func (r *RateLimiter) Count(ctx context.Context, kind, value string, window time.Duration) (int64, error) {
	var c counter
	err := getJSON(ctx, r.Blobs, r.Container, r.blob(kind, value, window), &c)
	if errors.Is(err, blobs.ErrNotFound) {
		return 0, nil
	}
	return c.Count, err
}

// Add counts an event and returns the count in the current window.
func (r *RateLimiter) Add(ctx context.Context, kind, value string, window time.Duration) (int64, error) {
	name := r.blob(kind, value, window)
	for attempt := 0; ; attempt++ {
		var c counter
		data, etag, err := r.Blobs.Get(ctx, r.Container, name)
		switch {
		case errors.Is(err, blobs.ErrNotFound):
			etag = "" // create
		case err != nil:
			return 0, err
		default:
			if err := json.Unmarshal(data, &c); err != nil {
				return 0, fmt.Errorf("%s: %w", name, err)
			}
		}
		c.Count++
		body, err := json.Marshal(c)
		if err != nil {
			return 0, err
		}
		err = r.Blobs.UploadIf(ctx, r.Container, name, body, "application/json", etag)
		if err == nil {
			return c.Count, nil
		}
		if !errors.Is(err, blobs.ErrConditionNotMet) {
			return 0, err
		}
		if attempt == maxRetries {
			return 0, ErrBusy
		}
		select {
		case <-time.After(wait(r.backoff, attempt+1)):
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
}

// Daily caps all calls to next at limit per UTC day, counted in one counter
// per app: ratelimits/app/<app>/<day start>.json. limit 0: no cap. Calls over
// the cap get 429 with Retry-After set to the next UTC midnight; if the
// counter can't be updated, calls get 503 (fail closed).
func (r *RateLimiter) Daily(app string, limit int64, next http.HandlerFunc) http.HandlerFunc {
	if limit == 0 {
		return next
	}
	const day = 24 * time.Hour
	return func(w http.ResponseWriter, req *http.Request) {
		ok, err := r.Allow(req.Context(), "app", app, limit, day)
		switch {
		case err != nil:
			slog.Error("daily request limit: counter update failed", "app", app, "error", err)
			writeLimitError(w, http.StatusServiceUnavailable, "service busy, try again")
		case !ok:
			// Truncate works from the zero time, which is UTC midnight.
			t := now(r.Now)
			retry := t.Truncate(day).Add(day).Sub(t)
			slog.Warn("daily request limit reached", "app", app, "limit", limit)
			w.Header().Set("Retry-After", strconv.FormatInt(int64((retry+time.Second-1)/time.Second), 10))
			writeLimitError(w, http.StatusTooManyRequests, "daily request limit reached, try again tomorrow")
		default:
			next(w, req)
		}
	}
}

func writeLimitError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
