package handler

import (
	"fmt"
	"net/http"
	"testing"
	"time"
)

const orderID = "0b6f8f8e-5c1a-4f57-9a43-3f1c2b9d7e10"

func purchaseBody(pack, keyID, order, pw string) string {
	return fmt.Sprintf(`{"pack":%q,"key_id":%q,"order_id":%q,"password":%q}`, pack, keyID, order, pw)
}

func TestPurchase(t *testing.T) {
	e := newEnv()
	session := e.signIn(t, "ann@example.com")
	key := e.createKey(t, session)

	w := e.call("POST", "/api/purchases", session, purchaseBody("starter", key.ID, orderID, password))
	if w.Code != http.StatusOK {
		t.Fatalf("purchase: %d %s", w.Code, w.Body)
	}
	var resp purchaseResponse
	decodeBody(t, w, &resp)
	if resp.OrderID != orderID || resp.Credits != 2000 || resp.Balance != 2100 || e.balance(t, key.ID) != 2100 {
		t.Fatalf("purchase = %+v", resp)
	}

	// A retry of the same order adds nothing.
	w = e.call("POST", "/api/purchases", session, purchaseBody("starter", key.ID, orderID, password))
	decodeBody(t, w, &resp)
	if w.Code != http.StatusOK || resp.Balance != 2100 || e.balance(t, key.ID) != 2100 {
		t.Fatalf("retry: %d %s", w.Code, w.Body)
	}

	// Purchases are unlimited: every new order adds its pack.
	for i := range 3 {
		order := fmt.Sprintf("0b6f8f8e-5c1a-4f57-9a43-3f1c2b9d7e%02d", 20+i)
		if w := e.call("POST", "/api/purchases", session, purchaseBody("pro", key.ID, order, password)); w.Code != http.StatusOK {
			t.Fatalf("order %d: %d %s", i, w.Code, w.Body)
		}
	}
	if got := e.balance(t, key.ID); got != 2100+3*12000 {
		t.Errorf("balance = %d", got)
	}
}

func TestPurchaseBadRequests(t *testing.T) {
	e := newEnv()
	session := e.signIn(t, "ann@example.com")
	key := e.createKey(t, session)
	for name, body := range map[string]string{
		"unknown pack":   purchaseBody("huge", key.ID, orderID, password),
		"no pack":        purchaseBody("", key.ID, orderID, password),
		"order not uuid": purchaseBody("starter", key.ID, "order-1", password),
		"no order":       purchaseBody("starter", key.ID, "", password),
		"bad key id":     purchaseBody("starter", "../x", orderID, password),
		"not json":       "pack=starter",
	} {
		if w := e.call("POST", "/api/purchases", session, body); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, w.Code, w.Body)
		}
	}
	// The credits come from the pack, not the request.
	body := fmt.Sprintf(`{"pack":"starter","key_id":%q,"order_id":%q,"password":%q,"credits":1000000}`, key.ID, orderID, password)
	if w := e.call("POST", "/api/purchases", session, body); w.Code != http.StatusOK {
		t.Fatalf("purchase: %d %s", w.Code, w.Body)
	}
	if got := e.balance(t, key.ID); got != 100+2000 {
		t.Errorf("balance = %d, want 2100", got)
	}
}

func TestPurchasePassword(t *testing.T) {
	e := newEnv()
	session := e.signIn(t, "ann@example.com")
	key := e.createKey(t, session)

	for _, pw := range []string{"", "wrong", password + " "} {
		w := e.call("POST", "/api/purchases", session, purchaseBody("starter", key.ID, orderID, pw))
		if w.Code != http.StatusForbidden || w.Body.String() != `{"error":"wrong test-mode password"}`+"\n" {
			t.Errorf("password %q: %d %s", pw, w.Code, w.Body)
		}
	}
	if got := e.balance(t, key.ID); got != 100 {
		t.Fatalf("balance after wrong passwords = %d", got)
	}
	// Five wrong attempts in 15 minutes; the next one is 429, even with the
	// right password.
	for range 2 {
		e.call("POST", "/api/purchases", session, purchaseBody("starter", key.ID, orderID, "wrong"))
	}
	if w := e.call("POST", "/api/purchases", session, purchaseBody("starter", key.ID, orderID, password)); w.Code != http.StatusTooManyRequests {
		t.Fatalf("6th attempt: %d %s", w.Code, w.Body)
	}
	if got := e.balance(t, key.ID); got != 100 {
		t.Fatalf("balance = %d", got)
	}
	e.clock.advance(15 * time.Minute)
	if w := e.call("POST", "/api/purchases", session, purchaseBody("starter", key.ID, orderID, password)); w.Code != http.StatusOK {
		t.Fatalf("after the window: %d %s", w.Code, w.Body)
	}
}

// 20 wrong attempts per hour from one IP, across accounts.
func TestPurchasePasswordIPLimit(t *testing.T) {
	e := newEnv()
	ip := []string{"X-Forwarded-For", "203.0.113.5"}
	for i := range 5 {
		session := e.signIn(t, fmt.Sprintf("user%d@example.com", i))
		key := e.createKey(t, session)
		for range 4 {
			e.call("POST", "/api/purchases", session, purchaseBody("starter", key.ID, orderID, "wrong"), ip...)
		}
		if i == 4 {
			if w := e.call("POST", "/api/purchases", session, purchaseBody("starter", key.ID, orderID, password), ip...); w.Code != http.StatusTooManyRequests {
				t.Fatalf("21st attempt from the IP: %d %s", w.Code, w.Body)
			}
			if w := e.call("POST", "/api/purchases", session, purchaseBody("starter", key.ID, orderID, password)); w.Code != http.StatusOK {
				t.Fatalf("from another IP: %d %s", w.Code, w.Body)
			}
		}
	}
}

// An empty MOCK_TOKENS_PASSWORD turns purchases off, even for an empty
// password.
func TestPurchasesDisabled(t *testing.T) {
	e := newEnv()
	e.h.MockPassword = ""
	session := e.signIn(t, "ann@example.com")
	key := e.createKey(t, session)
	for _, pw := range []string{"", password} {
		w := e.call("POST", "/api/purchases", session, purchaseBody("starter", key.ID, orderID, pw))
		if w.Code != http.StatusForbidden || w.Body.String() != `{"error":"test-mode purchases are disabled"}`+"\n" {
			t.Errorf("password %q: %d %s", pw, w.Code, w.Body)
		}
	}
	if got := e.balance(t, key.ID); got != 100 {
		t.Errorf("balance = %d", got)
	}
}

func TestPurchasesOffOutsideMockMode(t *testing.T) {
	e := newEnv()
	session := e.signIn(t, "ann@example.com")
	key := e.createKey(t, session)
	e.h.PaymentsMode = "lemonsqueezy"
	if w := e.call("POST", "/api/purchases", session, purchaseBody("starter", key.ID, orderID, password)); w.Code != http.StatusNotFound {
		t.Errorf("purchase: %d", w.Code)
	}
}
