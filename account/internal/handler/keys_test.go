package handler

import (
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/spyrosblock/text-extraction/billing"
)

func TestKeys(t *testing.T) {
	e := newEnv(t)
	session := e.signIn(t, "ann@example.com")

	first := e.createKey(t, session)
	if !strings.HasPrefix(first.Key, "tx_test_") || first.ID != billing.KeyID(first.Key) ||
		first.Name != "my key" || first.Hint != first.Key[len(first.Key)-4:] || first.Credits != 100 {
		t.Fatalf("first key = %+v", first)
	}
	if got := e.me(t, session).FreeCreditsAvailable; got != 0 {
		t.Errorf("free_credits_available after the first key = %d", got)
	}
	second := e.createKey(t, session)
	if second.Credits != 0 {
		t.Errorf("second key credits = %d, want 0", second.Credits)
	}
	// MAX_KEYS = 2.
	if w := e.call("POST", "/api/keys", session, `{"name":"third"}`); w.Code != http.StatusConflict {
		t.Errorf("third key: %d %s", w.Code, w.Body)
	}

	w := e.call("GET", "/api/keys", session, "")
	var list []keyResponse
	decodeBody(t, w, &list)
	if w.Code != http.StatusOK || len(list) != 2 || list[0].ID != first.ID || list[1].ID != second.ID ||
		list[0].Credits != 100 || list[0].Hint != first.Hint || list[0].Name != "my key" || list[0].CreatedAt.IsZero() {
		t.Fatalf("list: %d %s", w.Code, w.Body)
	}
	if strings.Contains(w.Body.String(), first.Key) {
		t.Error("list shows a key")
	}

	// The key works for the producer: it's in the shared store.
	if id, k, err := e.h.Store.Lookup(t.Context(), first.Key); err != nil || id != first.ID || k.AccountID == "" {
		t.Errorf("Lookup = %s %+v %v", id, k, err)
	}

	// Revoke the first key: a new one starts at 0.
	if w := e.call("DELETE", keyQuery(first.ID), session, ""); w.Code != http.StatusNoContent {
		t.Fatalf("revoke: %d %s", w.Code, w.Body)
	}
	if _, _, err := e.h.Store.Lookup(t.Context(), first.Key); !errors.Is(err, billing.ErrUnknownKey) {
		t.Errorf("Lookup after revoke err = %v", err)
	}
	if w := e.call("DELETE", keyQuery(first.ID), session, ""); w.Code != http.StatusNotFound {
		t.Errorf("second revoke: %d", w.Code)
	}
	if third := e.createKey(t, session); third.Credits != 0 {
		t.Errorf("key after revoking the first = %d credits, want 0", third.Credits)
	}
}

func TestKeyBadRequests(t *testing.T) {
	e := newEnv(t)
	session := e.signIn(t, "ann@example.com")
	for _, body := range []string{`{}`, `{"name":"  "}`, `{"name":"` + strings.Repeat("é", 65) + `"}`, `name=x`} {
		if w := e.call("POST", "/api/keys", session, body); w.Code != http.StatusBadRequest {
			t.Errorf("create %s: %d", body, w.Code)
		}
	}
	for _, target := range []string{"/api/keys", "/api/keys?key_id=../x", keyQuery(strings.ToUpper(billing.KeyID("x")))} {
		if w := e.call("DELETE", target, session, ""); w.Code != http.StatusBadRequest {
			t.Errorf("revoke %s: %d", target, w.Code)
		}
	}
	if w := e.call("PUT", "/api/keys", session, ""); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("PUT: %d", w.Code)
	}
}

// Another account's key and an admin-issued key are 404, as is an unknown id.
func TestKeyOwnership(t *testing.T) {
	e := newEnv(t)
	ann := e.signIn(t, "ann@example.com")
	bob := e.signIn(t, "bob@example.com")
	bobsKey := e.createKey(t, bob)
	_, adminKey, _, err := e.h.Store.Create(t.Context(), "admin", 50, "")
	if err != nil {
		t.Fatal(err)
	}
	// Even with an index entry, the key blob must name the account.
	annID, _, _ := e.h.Accounts.FindOrCreate(t.Context(), "ann@example.com")
	if err := e.h.Accounts.AddKey(t.Context(), annID, adminKey); err != nil {
		t.Fatal(err)
	}

	for _, id := range []string{bobsKey.ID, adminKey, billing.KeyID("unknown")} {
		if w := e.call("DELETE", keyQuery(id), ann, ""); w.Code != http.StatusNotFound {
			t.Errorf("revoke %s: %d", id, w.Code)
		}
		if w := e.call("POST", "/api/checkout", ann, `{"pack":"starter","key_id":"`+id+`"}`); w.Code != http.StatusNotFound {
			t.Errorf("checkout for %s: %d", id, w.Code)
		}
	}
	if got := e.balance(t, bobsKey.ID); got != 100 {
		t.Errorf("bob's balance = %d", got)
	}
	if n := e.stripe.createCount(); n != 0 {
		t.Errorf("%d sessions created at Stripe", n)
	}
	if got := e.balance(t, adminKey); got != 50 {
		t.Errorf("admin key's balance = %d", got)
	}
	var list []keyResponse
	decodeBody(t, e.call("GET", "/api/keys", ann, ""), &list)
	if len(list) != 0 {
		t.Errorf("ann's keys = %+v", list)
	}
}

// Two keys created in parallel on a new account: exactly one gets the free
// credits.
func TestCreateKeysParallel(t *testing.T) {
	e := newEnv(t)
	session := e.signIn(t, "ann@example.com")
	var mu sync.Mutex
	var credits []int64
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			w := e.call("POST", "/api/keys", session, `{"name":"k"}`)
			if w.Code != http.StatusCreated {
				t.Errorf("create: %d %s", w.Code, w.Body)
				return
			}
			var resp newKeyResponse
			decodeBody(t, w, &resp)
			mu.Lock()
			credits = append(credits, resp.Credits)
			mu.Unlock()
		})
	}
	wg.Wait()
	if len(credits) != 2 || credits[0]+credits[1] != 100 {
		t.Errorf("credits = %v, want one 100 and one 0", credits)
	}
}

// Parallel creates can't go over MAX_KEYS.
func TestCreateKeysParallelLimit(t *testing.T) {
	e := newEnv(t)
	session := e.signIn(t, "ann@example.com")
	var wg sync.WaitGroup
	for range 6 {
		wg.Go(func() { e.call("POST", "/api/keys", session, `{"name":"k"}`) })
	}
	wg.Wait()
	var list []keyResponse
	decodeBody(t, e.call("GET", "/api/keys", session, ""), &list)
	if len(list) > 2 {
		t.Errorf("%d keys, want at most 2", len(list))
	}
}

// A failed write after the claim releases it, and leaves no key behind.
func TestCreateKeyFailureReleasesClaim(t *testing.T) {
	e := newEnv(t)
	session := e.signIn(t, "ann@example.com")
	e.blobs.UploadErr = map[string]error{"billing/accounts/": errors.New("down")}
	if w := e.call("POST", "/api/keys", session, `{"name":"k"}`); w.Code != http.StatusInternalServerError {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	for _, n := range e.blobs.Names() {
		if strings.HasPrefix(n, "billing/keys/") || strings.HasPrefix(n, "billing/freecredits/") {
			t.Errorf("left behind: %s", n)
		}
	}
	e.blobs.UploadErr = nil
	if got := e.me(t, session).FreeCreditsAvailable; got != 100 {
		t.Errorf("free_credits_available = %d, want 100", got)
	}
	if k := e.createKey(t, session); k.Credits != 100 {
		t.Errorf("key after the failure = %d credits, want 100", k.Credits)
	}
}

// Deleting the account revokes its keys; signing up again with the same
// address doesn't bring the free credits back.
func TestDeleteAccount(t *testing.T) {
	e := newEnv(t)
	session := e.signIn(t, "ann@example.com")
	other := e.signIn(t, "ann@example.com")
	key := e.createKey(t, session)

	if w := e.call("DELETE", "/api/me", session, ""); w.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", w.Code, w.Body)
	}
	for _, s := range []string{session, other} {
		if w := e.call("GET", "/api/me", s, ""); w.Code != http.StatusUnauthorized {
			t.Errorf("session after delete: %d", w.Code)
		}
	}
	if _, _, err := e.h.Store.Lookup(t.Context(), key.Key); !errors.Is(err, billing.ErrUnknownKey) {
		t.Errorf("key after delete: %v", err)
	}
	var markers int
	for _, n := range e.blobs.Names() {
		switch {
		case strings.HasPrefix(n, "billing/freecredits/"):
			markers++
		case strings.HasPrefix(n, "billing/accounts/"), strings.HasPrefix(n, "billing/emails/"), strings.HasPrefix(n, "billing/keys/"):
			t.Errorf("left behind: %s", n)
		}
	}
	if markers != 1 {
		t.Errorf("%d free-credit markers, want 1", markers)
	}

	again := e.signIn(t, "ann@example.com")
	if got := e.me(t, again).FreeCreditsAvailable; got != 0 {
		t.Errorf("free_credits_available after re-signing up = %d", got)
	}
	if k := e.createKey(t, again); k.Credits != 0 {
		t.Errorf("first key of the new account = %d credits, want 0", k.Credits)
	}
}
