package handler

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSignIn(t *testing.T) {
	e := newEnv()
	w := e.call("POST", "/api/auth/start", "", `{"email":"  Ann@Example.COM "}`)
	if w.Code != http.StatusAccepted || w.Body.Len() != 0 {
		t.Fatalf("start: %d %s", w.Code, w.Body)
	}
	if e.mail.count() != 1 {
		t.Fatalf("%d emails sent", e.mail.count())
	}
	m := e.mail.sent[0]
	if m.To != "ann@example.com" || m.Subject == "" || !strings.Contains(m.HTML, siteURL+"/auth.html#token=") {
		t.Errorf("email = %+v", m)
	}
	token := e.mail.lastToken(t)

	w = e.call("POST", "/api/auth/verify", "", `{"token":"`+token+`"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("verify: %d %s", w.Code, w.Body)
	}
	var resp verifyResponse
	decodeBody(t, w, &resp)
	if resp.Email != "ann@example.com" || resp.Session == "" {
		t.Fatalf("verify = %+v", resp)
	}
	if me := e.me(t, resp.Session); me.Email != "ann@example.com" {
		t.Errorf("me = %+v", me)
	}

	// The link works once.
	if w := e.call("POST", "/api/auth/verify", "", `{"token":"`+token+`"}`); w.Code != http.StatusUnauthorized {
		t.Errorf("second verify: %d %s", w.Code, w.Body)
	}

	// A second sign-in reaches the same account.
	session2 := e.signIn(t, "ann@example.com")
	if session2 == resp.Session {
		t.Error("same session twice")
	}
	accounts := 0
	for _, n := range e.blobs.Names() {
		if strings.HasPrefix(n, "billing/emails/") {
			accounts++
		}
	}
	if accounts != 1 {
		t.Errorf("%d email blobs, want 1", accounts)
	}
}

func TestStartMalformed(t *testing.T) {
	e := newEnv()
	for _, body := range []string{
		`{"email":""}`,
		`{"email":"ann"}`,
		`{"email":"ann@example"}`,
		`{"email":"@example.com"}`,
		`{"email":"ann@.com"}`,
		`{"email":"ann@example."}`,
		`{"email":"ann@bob@example.com"}`,
		`{"email":"ann smith@example.com"}`,
		`{"email":"` + strings.Repeat("a", 250) + `@example.com"}`,
		`email=ann@example.com`,
	} {
		if w := e.call("POST", "/api/auth/start", "", body); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", body, w.Code)
		}
	}
	if e.mail.count() != 0 {
		t.Errorf("%d emails sent", e.mail.count())
	}
}

// Failures to send don't show in the response.
func TestStartSendFails(t *testing.T) {
	e := newEnv()
	e.mail.err = errors.New("acs down")
	if w := e.call("POST", "/api/auth/start", "", `{"email":"ann@example.com"}`); w.Code != http.StatusAccepted {
		t.Errorf("start: %d", w.Code)
	}
}

func TestStartRateLimits(t *testing.T) {
	e := newEnv()
	start := func(addr, ip string) {
		t.Helper()
		w := e.call("POST", "/api/auth/start", "", `{"email":"`+addr+`"}`, "X-Forwarded-For", ip)
		if w.Code != http.StatusAccepted {
			t.Fatalf("start: %d", w.Code)
		}
	}

	// 3 per 15 minutes per address: the 4th sends nothing, still 202.
	for i := range 4 {
		start("ann@example.com", fmt.Sprint("198.51.100.", i))
	}
	if n := e.mail.count(); n != 3 {
		t.Fatalf("%d emails sent, want 3", n)
	}
	e.clock.advance(15 * time.Minute)
	start("ann@example.com", "198.51.100.9")
	if n := e.mail.count(); n != 4 {
		t.Fatalf("after the window: %d emails sent, want 4", n)
	}

	// 20 per hour per IP, whatever the address.
	for i := range 21 {
		start(fmt.Sprintf("user%d@example.com", i), "203.0.113.1")
	}
	if n := e.mail.count(); n != 4+20 {
		t.Fatalf("%d emails sent, want %d", n, 4+20)
	}
	for _, n := range e.blobs.Names() {
		if strings.Contains(n, "example.com") {
			t.Errorf("address in blob name %s", n)
		}
	}
}

func TestVerifyExpired(t *testing.T) {
	e := newEnv()
	e.call("POST", "/api/auth/start", "", `{"email":"ann@example.com"}`)
	token := e.mail.lastToken(t)
	e.clock.advance(15 * time.Minute)
	if w := e.call("POST", "/api/auth/verify", "", `{"token":"`+token+`"}`); w.Code != http.StatusUnauthorized {
		t.Errorf("expired token: %d", w.Code)
	}
	for _, body := range []string{`{"token":""}`, `{"token":"nope"}`, `{}`} {
		if w := e.call("POST", "/api/auth/verify", "", body); w.Code != http.StatusUnauthorized {
			t.Errorf("%s: %d", body, w.Code)
		}
	}
}

// Parallel verifies of one link: one session.
func TestVerifyParallel(t *testing.T) {
	e := newEnv()
	e.call("POST", "/api/auth/start", "", `{"email":"ann@example.com"}`)
	token := e.mail.lastToken(t)
	var ok atomic.Int32
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			switch w := e.call("POST", "/api/auth/verify", "", `{"token":"`+token+`"}`); w.Code {
			case http.StatusOK:
				ok.Add(1)
			case http.StatusUnauthorized:
			default:
				t.Errorf("verify: %d %s", w.Code, w.Body)
			}
		})
	}
	wg.Wait()
	if ok.Load() != 1 {
		t.Errorf("%d verifies succeeded, want 1", ok.Load())
	}
	sessions := 0
	for _, n := range e.blobs.Names() {
		if strings.HasPrefix(n, "billing/sessions/") {
			sessions++
		}
	}
	if sessions != 1 {
		t.Errorf("%d sessions, want 1", sessions)
	}
}

func TestLogout(t *testing.T) {
	e := newEnv()
	s1 := e.signIn(t, "ann@example.com")
	s2 := e.signIn(t, "ann@example.com")
	if w := e.call("POST", "/api/auth/logout", s1, ""); w.Code != http.StatusNoContent {
		t.Fatalf("logout: %d", w.Code)
	}
	if w := e.call("GET", "/api/me", s1, ""); w.Code != http.StatusUnauthorized {
		t.Errorf("me after logout: %d", w.Code)
	}
	e.me(t, s2) // other sessions stay
}
