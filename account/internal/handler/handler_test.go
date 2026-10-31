package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spyrosblock/text-extraction/account/internal/email"
	"github.com/spyrosblock/text-extraction/billing"
	"github.com/spyrosblock/text-extraction/billing/blobs"
)

const (
	siteURL  = "https://site.example"
	password = "test-mode-password"
)

type fakeSender struct {
	mu   sync.Mutex
	sent []email.Message
	err  error
}

func (f *fakeSender) Send(_ context.Context, m email.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.sent = append(f.sent, m)
	return nil
}

func (f *fakeSender) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}

// lastToken returns the token in the last sign-in link sent.
func (f *fakeSender) lastToken(t *testing.T) string {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sent) == 0 {
		t.Fatal("no email sent")
	}
	m := f.sent[len(f.sent)-1]
	prefix := siteURL + "/auth.html#token="
	i := strings.Index(m.Text, prefix)
	if i < 0 {
		t.Fatalf("no sign-in link in %q", m.Text)
	}
	return strings.Fields(m.Text[i+len(prefix):])[0]
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type env struct {
	h     *Handler
	blobs *blobs.Fake
	mail  *fakeSender
	clock *clock
}

func newEnv() *env {
	b, mail := &blobs.Fake{}, &fakeSender{}
	c := &clock{t: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
	accounts := &billing.Accounts{Blobs: b, Container: "billing", FreeCreditsSecret: []byte(strings.Repeat("s", 32))}
	return &env{
		blobs: b, mail: mail, clock: c,
		h: &Handler{
			Store:        &billing.Store{Blobs: b, Container: "billing", KeyPrefix: "tx_test_", TextPageCredits: 1, OCRPageCredits: 5},
			Accounts:     accounts,
			Sessions:     &billing.Sessions{Blobs: b, Container: "billing", Accounts: accounts, TTL: 7 * 24 * time.Hour, Now: c.Now},
			LoginTokens:  &billing.LoginTokens{Blobs: b, Container: "billing", TTL: 15 * time.Minute, Now: c.Now},
			Limits:       &billing.RateLimiter{Blobs: b, Container: "billing", Now: c.Now},
			Email:        mail,
			SiteURL:      siteURL + "/",
			PaymentsMode: PaymentsMock,
			Packs:        map[string]Pack{"starter": {Credits: 2000, PriceEUR: 10}, "pro": {Credits: 12000, PriceEUR: 50}},
			FreeCredits:  100,
			MaxKeys:      2,
			MockPassword: password,
		},
	}
}

// call sends a request to the endpoint its path names. session may be "".
func (e *env) call(method, target, session, body string, header ...string) *httptest.ResponseRecorder {
	routes := map[string]http.HandlerFunc{
		"/api/auth/start":  e.h.Start,
		"/api/auth/verify": e.h.Verify,
		"/api/auth/logout": e.h.Logout,
		"/api/me":          e.h.Me,
		"/api/keys":        e.h.Keys,
		"/api/purchases":   e.h.Purchases,
	}
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	if session != "" {
		r.Header.Set("Authorization", "Bearer "+session)
	}
	for i := 0; i+1 < len(header); i += 2 {
		r.Header.Set(header[i], header[i+1])
	}
	w := httptest.NewRecorder()
	routes[r.URL.Path](w, r)
	return w
}

// signIn goes through the magic link and returns a session.
func (e *env) signIn(t *testing.T, addr string) string {
	t.Helper()
	if w := e.call("POST", "/api/auth/start", "", `{"email":"`+addr+`"}`); w.Code != http.StatusAccepted {
		t.Fatalf("start: %d %s", w.Code, w.Body)
	}
	w := e.call("POST", "/api/auth/verify", "", `{"token":"`+e.mail.lastToken(t)+`"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("verify: %d %s", w.Code, w.Body)
	}
	var resp verifyResponse
	decodeBody(t, w, &resp)
	return resp.Session
}

// createKey creates a key and returns the response.
func (e *env) createKey(t *testing.T, session string) newKeyResponse {
	t.Helper()
	w := e.call("POST", "/api/keys", session, `{"name":"my key"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create key: %d %s", w.Code, w.Body)
	}
	var resp newKeyResponse
	decodeBody(t, w, &resp)
	return resp
}

func (e *env) balance(t *testing.T, keyID string) int64 {
	t.Helper()
	k, err := e.h.Store.Get(context.Background(), keyID)
	if err != nil {
		t.Fatal(err)
	}
	return k.Credits
}

func (e *env) me(t *testing.T, session string) meResponse {
	t.Helper()
	w := e.call("GET", "/api/me", session, "")
	if w.Code != http.StatusOK {
		t.Fatalf("me: %d %s", w.Code, w.Body)
	}
	var resp meResponse
	decodeBody(t, w, &resp)
	return resp
}

func decodeBody(t *testing.T, w *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(w.Body.Bytes(), v); err != nil {
		t.Fatalf("decode %s: %v", w.Body, err)
	}
}

func keyQuery(id string) string { return "/api/keys?key_id=" + url.QueryEscape(id) }

func TestClientIP(t *testing.T) {
	for _, tt := range []struct{ xff, remote, want string }{
		{"203.0.113.7, 10.0.0.1", "10.0.0.2:1234", "203.0.113.7"},
		{"203.0.113.7:5555", "", "203.0.113.7"},
		{"[2001:db8::1]:443", "", "2001:db8::1"},
		{"2001:db8::1", "", "2001:db8::1"},
		{"", "10.0.0.2:1234", "10.0.0.2"},
		{"", "", "unknown"},
	} {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = tt.remote
		if tt.xff != "" {
			r.Header.Set("X-Forwarded-For", tt.xff)
		}
		if got := clientIP(r); got != tt.want {
			t.Errorf("clientIP(%q, %q) = %q, want %q", tt.xff, tt.remote, got, tt.want)
		}
	}
}

func TestSessionRequired(t *testing.T) {
	e := newEnv()
	session := e.signIn(t, "ann@example.com")
	for _, c := range []struct{ method, target, body string }{
		{"GET", "/api/me", ""},
		{"DELETE", "/api/me", ""},
		{"GET", "/api/keys", ""},
		{"POST", "/api/keys", `{"name":"x"}`},
		{"DELETE", keyQuery(billing.KeyID("x")), ""},
		{"POST", "/api/purchases", `{"password":"` + password + `"}`},
		{"POST", "/api/auth/logout", ""},
	} {
		for _, bad := range []string{"", "nope", session + "x"} {
			if w := e.call(c.method, c.target, bad, c.body); w.Code != http.StatusUnauthorized {
				t.Errorf("%s %s with session %q: %d, want 401", c.method, c.target, bad, w.Code)
			}
		}
	}
	// "Bearer" is matched case-insensitively; another scheme is no session.
	if w := e.call("GET", "/api/me", "", "", "Authorization", "bearer "+session); w.Code != http.StatusOK {
		t.Errorf("lowercase bearer: %d", w.Code)
	}
	if w := e.call("GET", "/api/me", "", "", "Authorization", "Basic "+session); w.Code != http.StatusUnauthorized {
		t.Errorf("basic: %d", w.Code)
	}

	// Sessions last 7 days.
	e.clock.advance(7*24*time.Hour - time.Minute)
	e.me(t, session)
	e.clock.advance(time.Minute)
	if w := e.call("GET", "/api/me", session, ""); w.Code != http.StatusUnauthorized {
		t.Errorf("expired session: %d", w.Code)
	}
}

func TestMe(t *testing.T) {
	e := newEnv()
	session := e.signIn(t, "ann@example.com")
	w := e.call("GET", "/api/me", session, "")
	if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("me: %d %v", w.Code, w.Header())
	}
	var got map[string]any
	decodeBody(t, w, &got)
	want := `{"email":"ann@example.com","free_credits_available":100,"max_keys":2,` +
		`"packs":{"pro":{"credits":12000,"price_eur":50},"starter":{"credits":2000,"price_eur":10}},` +
		`"payments":"mock","prices":{"ocr_page":5,"text_page":1}}`
	if b, _ := json.Marshal(got); string(b) != want {
		t.Errorf("me =\n%s\nwant\n%s", b, want)
	}
}
