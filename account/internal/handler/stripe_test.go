package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stripe/stripe-go/v87"
	"github.com/stripe/stripe-go/v87/webhook"
)

// orderID is the PaymentIntent in the testdata events.
const orderID = "pi_3SNhSzGx8KbqW3cZ0aBcDeFg"

// fakeStripe is the part of the Stripe API the handler uses: creating
// Checkout Sessions, listing them by PaymentIntent, and refunds.
type fakeStripe struct {
	srv *httptest.Server

	mu      sync.Mutex
	created []url.Values  // forms of the create calls
	headers []http.Header // their headers
	// sessions are the Checkout Sessions by PaymentIntent, for the list.
	sessions map[string]map[string]string
	// createStatus/listStatus, when set, make those calls fail.
	createStatus, listStatus int
	refunds                  []url.Values  // forms of the refund calls
	refundHeaders            []http.Header // their headers
	// refundStatus, when set, makes refunds fail; refundCode is the error
	// code then.
	refundStatus int
	refundCode   string
}

func newFakeStripe(t *testing.T) *fakeStripe {
	f := &fakeStripe{sessions: map[string]map[string]string{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeStripe) client() *stripe.Client {
	backends := stripe.NewBackendsWithConfig(&stripe.BackendConfig{
		URL:               stripe.String(f.srv.URL),
		MaxNetworkRetries: stripe.Int64(0),
		LeveledLogger:     &stripe.LeveledLogger{Level: stripe.LevelNull},
	})
	return stripe.NewClient("rk_test_fake", stripe.WithBackends(backends))
}

func (f *fakeStripe) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Request-Id", "req_fake")
	fail := func(status int) {
		w.WriteHeader(status)
		fmt.Fprint(w, `{"error":{"type":"invalid_request_error","code":"resource_missing","message":"No such price: 'price_starter'","param":"line_items[0][price]"}}`)
	}
	switch {
	case r.Method == "POST" && r.URL.Path == "/v1/checkout/sessions":
		if err := r.ParseForm(); err != nil {
			fail(http.StatusBadRequest)
			return
		}
		f.created = append(f.created, r.PostForm)
		f.headers = append(f.headers, r.Header.Clone())
		if f.createStatus != 0 {
			fail(f.createStatus)
			return
		}
		id := fmt.Sprintf("cs_test_%d", len(f.created))
		fmt.Fprintf(w, `{"id":%q,"object":"checkout.session","url":"https://checkout.stripe.com/c/pay/%s"}`, id, id)
	case r.Method == "GET" && r.URL.Path == "/v1/checkout/sessions":
		if f.listStatus != 0 {
			w.WriteHeader(f.listStatus)
			fmt.Fprint(w, `{"error":{"type":"api_error","message":"boom"}}`)
			return
		}
		pi := r.URL.Query().Get("payment_intent")
		data := []any{}
		if md, ok := f.sessions[pi]; ok {
			data = append(data, map[string]any{"id": "cs_test_" + pi, "object": "checkout.session", "payment_intent": pi, "metadata": md})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "url": "/v1/checkout/sessions", "has_more": false, "data": data})
	case r.Method == "POST" && r.URL.Path == "/v1/refunds":
		if err := r.ParseForm(); err != nil {
			fail(http.StatusBadRequest)
			return
		}
		f.refunds = append(f.refunds, r.PostForm)
		f.refundHeaders = append(f.refundHeaders, r.Header.Clone())
		if f.refundStatus != 0 {
			w.WriteHeader(f.refundStatus)
			fmt.Fprintf(w, `{"error":{"type":"invalid_request_error","code":%q,"message":"refund failed"}}`, f.refundCode)
			return
		}
		fmt.Fprintf(w, `{"id":"re_test_%d","object":"refund","status":"succeeded","amount":1240,"payment_intent":%q}`,
			len(f.refunds), r.PostForm.Get("payment_intent"))
	default:
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"error":{"type":"invalid_request_error","message":"unrecognized request URL"}}`)
	}
}

func (f *fakeStripe) addSession(pi, accountID, keyID, pack string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sessions[pi] = map[string]string{"account_id": accountID, "key_id": keyID, "pack": pack}
}

// refundCalls returns the forms and Idempotency-Key headers of the refund
// calls.
func (f *fakeStripe) refundCalls() ([]url.Values, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	keys := make([]string, len(f.refundHeaders))
	for i, h := range f.refundHeaders {
		keys[i] = h.Get("Idempotency-Key")
	}
	return slices.Clone(f.refunds), keys
}

func (f *fakeStripe) createCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.created)
}

// customer signs in and creates a key; it returns the session, account id
// and key.
func (e *env) customer(t *testing.T, addr string) (string, string, newKeyResponse) {
	t.Helper()
	session := e.signIn(t, addr)
	key := e.createKey(t, session)
	accountID, _, err := e.h.Accounts.FindOrCreate(t.Context(), addr)
	if err != nil {
		t.Fatal(err)
	}
	return session, accountID, key
}

func checkoutBody(pack, keyID string) string {
	return fmt.Sprintf(`{"pack":%q,"key_id":%q}`, pack, keyID)
}

func TestCheckout(t *testing.T) {
	e := newEnv(t)
	session, accountID, key := e.customer(t, "ann@example.com")

	w := e.call("POST", "/api/checkout", session, checkoutBody("pro", key.ID))
	if w.Code != http.StatusOK {
		t.Fatalf("checkout: %d %s", w.Code, w.Body)
	}
	var resp checkoutResponse
	decodeBody(t, w, &resp)
	if resp.URL != "https://checkout.stripe.com/c/pay/cs_test_1" {
		t.Errorf("url = %q", resp.URL)
	}

	form, header := e.stripe.created[0], e.stripe.headers[0]
	want := map[string]string{
		"mode":                      "payment",
		"managed_payments[enabled]": "true",
		"line_items[0][price]":      "price_pro",
		"line_items[0][quantity]":   "1",
		"metadata[account_id]":      accountID,
		"metadata[key_id]":          key.ID,
		"metadata[pack]":            "pro",
		"client_reference_id":       key.ID,
		"customer_email":            "ann@example.com",
		"success_url":               siteURL + "/dashboard.html?paid=1",
		"cancel_url":                siteURL + "/dashboard.html",
		"expires_at":                fmt.Sprint(e.clock.Now().Add(time.Hour).Unix()),
	}
	for k, v := range want {
		if got := form.Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	for _, k := range []string{"automatic_tax[enabled]", "payment_method_types[0]", "tax_id_collection[enabled]", "invoice_creation[enabled]"} {
		if form.Has(k) {
			t.Errorf("%s sent; Managed Payments manages it", k)
		}
	}
	if len(form) != len(want) {
		t.Errorf("form has %d fields, want %d: %v", len(form), len(want), form)
	}
	if header.Get("Authorization") != "Bearer rk_test_fake" || header.Get("Stripe-Version") != stripe.APIVersion {
		t.Errorf("headers = %v", header)
	}

	// Every call is a new session with its own idempotency key.
	if w := e.call("POST", "/api/checkout", session, checkoutBody("pro", key.ID)); w.Code != http.StatusOK {
		t.Fatalf("second checkout: %d %s", w.Code, w.Body)
	}
	k1, k2 := header.Get("Idempotency-Key"), e.stripe.headers[1].Get("Idempotency-Key")
	if k1 == "" || k1 == k2 {
		t.Errorf("idempotency keys %q, %q", k1, k2)
	}
	// Nothing is credited before the webhook.
	if got := e.balance(t, key.ID); got != 100 {
		t.Errorf("balance = %d", got)
	}
}

func TestCheckoutBadRequests(t *testing.T) {
	e := newEnv(t)
	session, _, key := e.customer(t, "ann@example.com")
	_, _, bobsKey := e.customer(t, "bob@example.com")
	_, adminKey, _, err := e.h.Store.Create(t.Context(), "admin", 50, "")
	if err != nil {
		t.Fatal(err)
	}
	revoked := e.createKey(t, session)
	if w := e.call("DELETE", keyQuery(revoked.ID), session, ""); w.Code != http.StatusNoContent {
		t.Fatalf("revoke: %d", w.Code)
	}

	for name, c := range map[string]struct {
		body string
		code int
	}{
		"another account's key": {checkoutBody("starter", bobsKey.ID), http.StatusNotFound},
		"admin-issued key":      {checkoutBody("starter", adminKey), http.StatusNotFound},
		"revoked key":           {checkoutBody("starter", revoked.ID), http.StatusNotFound},
		"unknown pack":          {checkoutBody("huge", key.ID), http.StatusBadRequest},
		"bad key id":            {checkoutBody("starter", "../x"), http.StatusBadRequest},
		"not json":              {"pack=starter", http.StatusBadRequest},
	} {
		if w := e.call("POST", "/api/checkout", session, c.body); w.Code != c.code {
			t.Errorf("%s: %d %s, want %d", name, w.Code, w.Body, c.code)
		}
	}
	if n := e.stripe.createCount(); n != 0 {
		t.Errorf("%d sessions created", n)
	}
}

func TestCheckoutCreditCap(t *testing.T) {
	e := newEnv(t)
	session, _, first := e.customer(t, "ann@example.com")
	second := e.createKey(t, session)
	set := func(id string, credits int64) {
		t.Helper()
		if _, err := e.h.Store.Set(t.Context(), id, credits); err != nil {
			t.Fatal(err)
		}
	}
	checkout := func(pack, keyID string) *httptest.ResponseRecorder {
		return e.call("POST", "/api/checkout", session, checkoutBody(pack, keyID))
	}

	// The balances of all the account's keys count, whichever key buys.
	set(first.ID, 6000)
	set(second.ID, 2000)
	if w := checkout("pro", second.ID); w.Code != http.StatusOK {
		t.Fatalf("up to the cap: %d %s", w.Code, w.Body)
	}
	set(second.ID, 2001)
	w := checkout("pro", first.ID)
	want := `{"error":"an account can hold at most 20000 credits; yours has 8001, so the pro pack (12000 credits) would go over"}` + "\n"
	if w.Code != http.StatusConflict || w.Body.String() != want {
		t.Errorf("over the cap: %d %s", w.Code, w.Body)
	}
	if w := checkout("starter", first.ID); w.Code != http.StatusOK {
		t.Errorf("smaller pack: %d %s", w.Code, w.Body)
	}
	if n := e.stripe.createCount(); n != 2 {
		t.Errorf("%d sessions created, want 2", n)
	}

	// Another account's balance doesn't count.
	other, _, otherKey := e.customer(t, "bob@example.com")
	if w := e.call("POST", "/api/checkout", other, checkoutBody("pro", otherKey.ID)); w.Code != http.StatusOK {
		t.Errorf("other account: %d %s", w.Code, w.Body)
	}
	// Refused checkouts don't use up the rate limit.
	set(first.ID, 20000)
	for range 12 {
		if w := checkout("starter", first.ID); w.Code != http.StatusConflict {
			t.Fatalf("at the cap: %d %s", w.Code, w.Body)
		}
	}
	set(first.ID, 0)
	if w := checkout("starter", first.ID); w.Code != http.StatusOK {
		t.Errorf("after refusals: %d %s", w.Code, w.Body)
	}

	// 0 is no cap.
	e.h.MaxCredits = 0
	set(first.ID, 1_000_000)
	if w := checkout("pro", first.ID); w.Code != http.StatusOK {
		t.Errorf("no cap: %d %s", w.Code, w.Body)
	}
}

func TestCheckoutStripeError(t *testing.T) {
	e := newEnv(t)
	session, _, key := e.customer(t, "ann@example.com")
	e.stripe.createStatus = http.StatusBadRequest
	if w := e.call("POST", "/api/checkout", session, checkoutBody("starter", key.ID)); w.Code != http.StatusBadGateway {
		t.Errorf("checkout: %d %s", w.Code, w.Body)
	}
}

func TestCheckoutNotConfigured(t *testing.T) {
	e := newEnv(t)
	session, _, key := e.customer(t, "ann@example.com")
	e.h.Stripe = nil
	w := e.call("POST", "/api/checkout", session, checkoutBody("starter", key.ID))
	if w.Code != http.StatusServiceUnavailable || w.Body.String() != `{"error":"payments are not configured"}`+"\n" {
		t.Errorf("checkout: %d %s", w.Code, w.Body)
	}
}

func TestCheckoutRateLimit(t *testing.T) {
	e := newEnv(t)
	session, _, key := e.customer(t, "ann@example.com")
	for i := range 10 {
		if w := e.call("POST", "/api/checkout", session, checkoutBody("starter", key.ID)); w.Code != http.StatusOK {
			t.Fatalf("checkout %d: %d %s", i+1, w.Code, w.Body)
		}
	}
	if w := e.call("POST", "/api/checkout", session, checkoutBody("starter", key.ID)); w.Code != http.StatusTooManyRequests {
		t.Fatalf("11th checkout: %d %s", w.Code, w.Body)
	}
	if n := e.stripe.createCount(); n != 10 {
		t.Errorf("%d sessions created, want 10", n)
	}
	// Another account isn't limited.
	other, _, otherKey := e.customer(t, "bob@example.com")
	if w := e.call("POST", "/api/checkout", other, checkoutBody("starter", otherKey.ID)); w.Code != http.StatusOK {
		t.Errorf("other account: %d", w.Code)
	}
	e.clock.advance(time.Hour)
	if w := e.call("POST", "/api/checkout", session, checkoutBody("starter", key.ID)); w.Code != http.StatusOK {
		t.Errorf("after the window: %d", w.Code)
	}
}

// event loads testdata/stripe_<name>.json and applies set, a map from
// dotted paths under data.object (or the event's own "livemode", "type" and
// "api_version") to values. "__KEY_ID__" and "__ACCOUNT_ID__" in the file
// are replaced first.
func event(t *testing.T, name, accountID, keyID string, set map[string]any) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/stripe_" + name + ".json")
	if err != nil {
		t.Fatal(err)
	}
	s := strings.NewReplacer("__KEY_ID__", keyID, "__ACCOUNT_ID__", accountID).Replace(string(raw))
	var ev map[string]any
	if err := json.Unmarshal([]byte(s), &ev); err != nil {
		t.Fatal(err)
	}
	for path, v := range set {
		m := ev
		parts := strings.Split(path, ".")
		if parts[0] != "livemode" && parts[0] != "type" && parts[0] != "api_version" {
			m = ev["data"].(map[string]any)["object"].(map[string]any)
		}
		for _, p := range parts[:len(parts)-1] {
			m = m[p].(map[string]any)
		}
		m[parts[len(parts)-1]] = v
	}
	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// send posts a webhook event signed with secret at time ts.
func (e *env) send(payload []byte, secret string, ts time.Time) *httptest.ResponseRecorder {
	signed := webhook.GenerateTestSignedPayload(&webhook.UnsignedPayload{Payload: payload, Secret: secret, Timestamp: ts})
	return e.call("POST", "/api/webhooks/stripe", "", string(payload), "Stripe-Signature", signed.Header)
}

// deliver posts a correctly signed event and expects 200.
func (e *env) deliver(t *testing.T, payload []byte) {
	t.Helper()
	if w := e.send(payload, webhookSecret, time.Now()); w.Code != http.StatusOK {
		t.Fatalf("webhook: %d %s", w.Code, w.Body)
	}
}

func TestWebhookSignature(t *testing.T) {
	e := newEnv(t)
	_, accountID, key := e.customer(t, "ann@example.com")
	payload := event(t, "checkout_session_completed", accountID, key.ID, nil)
	signed := webhook.GenerateTestSignedPayload(&webhook.UnsignedPayload{Payload: payload, Secret: webhookSecret, Timestamp: time.Now()})
	tampered := strings.Replace(string(payload), `"pack":"starter"`, `"pack":"pro"`, 1)

	for name, w := range map[string]*httptest.ResponseRecorder{
		"tampered body":  e.call("POST", "/api/webhooks/stripe", "", tampered, "Stripe-Signature", signed.Header),
		"wrong secret":   e.send(payload, "whsec_other", time.Now()),
		"old timestamp":  e.send(payload, webhookSecret, time.Now().Add(-6*time.Minute)),
		"missing header": e.call("POST", "/api/webhooks/stripe", "", string(payload)),
		"garbage header": e.call("POST", "/api/webhooks/stripe", "", string(payload), "Stripe-Signature", "t=1,v1=00"),
	} {
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, w.Code, w.Body)
		}
	}
	if got := e.balance(t, key.ID); got != 100 {
		t.Errorf("balance = %d", got)
	}
	// An event from another API release train is refused too: the webhook
	// endpoint must use the SDK's API version.
	old := event(t, "checkout_session_completed", accountID, key.ID, map[string]any{"api_version": "2026-04-22.dahlia"})
	if w := e.send(old, webhookSecret, time.Now()); w.Code != http.StatusBadRequest {
		t.Errorf("other api version: %d", w.Code)
	}
	if got := e.balance(t, key.ID); got != 100 {
		t.Errorf("balance = %d", got)
	}
}

func TestWebhookLivemode(t *testing.T) {
	e := newEnv(t)
	_, accountID, key := e.customer(t, "ann@example.com")
	if w := e.send(event(t, "checkout_session_completed", accountID, key.ID, map[string]any{"livemode": true}), webhookSecret, time.Now()); w.Code != http.StatusBadRequest {
		t.Errorf("live event in test mode: %d %s", w.Code, w.Body)
	}
	e.h.TestMode = false
	if w := e.send(event(t, "checkout_session_completed", accountID, key.ID, nil), webhookSecret, time.Now()); w.Code != http.StatusBadRequest {
		t.Errorf("test event in live mode: %d %s", w.Code, w.Body)
	}
	if got := e.balance(t, key.ID); got != 100 {
		t.Errorf("balance = %d", got)
	}
	// Live mode takes live events.
	e.deliver(t, event(t, "checkout_session_completed", accountID, key.ID, map[string]any{"livemode": true}))
	if got := e.balance(t, key.ID); got != 2100 {
		t.Errorf("balance = %d, want 2100", got)
	}
}

func TestWebhookCredits(t *testing.T) {
	e := newEnv(t)
	_, accountID, key := e.customer(t, "ann@example.com")
	payload := event(t, "checkout_session_completed", accountID, key.ID, nil)
	e.deliver(t, payload)
	e.deliver(t, payload)
	if got := e.balance(t, key.ID); got != 2100 {
		t.Errorf("balance = %d, want 2100", got)
	}
	k, _ := e.h.Store.Get(t.Context(), key.ID)
	if o := k.Orders[orderID]; o.Pack != "starter" || o.Credits != 2000 {
		t.Errorf("order = %+v", o)
	}
	// Its async_payment_succeeded (if Stripe sent one) adds nothing either.
	e.deliver(t, event(t, "checkout_session_async_payment_succeeded", accountID, key.ID, nil))
	if got := e.balance(t, key.ID); got != 2100 {
		t.Errorf("balance = %d, want 2100", got)
	}
}

func TestWebhookDelayedPayment(t *testing.T) {
	e := newEnv(t)
	_, accountID, key := e.customer(t, "ann@example.com")
	e.deliver(t, event(t, "checkout_session_completed", accountID, key.ID, map[string]any{"payment_status": "unpaid"}))
	if got := e.balance(t, key.ID); got != 100 {
		t.Fatalf("balance after unpaid session = %d", got)
	}
	payload := event(t, "checkout_session_async_payment_succeeded", accountID, key.ID, nil)
	e.deliver(t, payload)
	e.deliver(t, payload)
	if got := e.balance(t, key.ID); got != 2100 {
		t.Errorf("balance = %d, want 2100", got)
	}
}

func TestWebhookNotCredited(t *testing.T) {
	e := newEnv(t)
	session, accountID, key := e.customer(t, "ann@example.com")
	_, bobID, _ := e.customer(t, "bob@example.com")
	revoked := e.createKey(t, session)
	if w := e.call("DELETE", keyQuery(revoked.ID), session, ""); w.Code != http.StatusNoContent {
		t.Fatalf("revoke: %d", w.Code)
	}
	for name, payload := range map[string][]byte{
		"unknown pack":          event(t, "checkout_session_completed", accountID, key.ID, map[string]any{"metadata.pack": "huge"}),
		"no pack":               event(t, "checkout_session_completed", accountID, key.ID, map[string]any{"metadata": map[string]any{}}),
		"account not the owner": event(t, "checkout_session_completed", bobID, key.ID, nil),
		"no account":            event(t, "checkout_session_completed", "", key.ID, nil),
		"revoked key":           event(t, "checkout_session_completed", accountID, revoked.ID, nil),
		"bad key id":            event(t, "checkout_session_completed", accountID, "../x", nil),
	} {
		if w := e.send(payload, webhookSecret, time.Now()); w.Code != http.StatusOK {
			t.Errorf("%s: %d %s", name, w.Code, w.Body)
		}
	}
	if got := e.balance(t, key.ID); got != 100 {
		t.Errorf("balance = %d", got)
	}
}

// Failures a retry can fix answer 5xx, so Stripe retries.
func TestWebhookRetryableErrors(t *testing.T) {
	e := newEnv(t)
	_, accountID, key := e.customer(t, "ann@example.com")
	e.blobs.BeforeUploadIf = func(container, name string) {
		if strings.HasPrefix(name, "keys/") {
			e.blobs.Put(container, name, e.blobs.Data(container, name)) // always a conflict
		}
	}
	payload := event(t, "checkout_session_completed", accountID, key.ID, nil)
	if w := e.send(payload, webhookSecret, time.Now()); w.Code != http.StatusServiceUnavailable {
		t.Errorf("busy key: %d %s", w.Code, w.Body)
	}
	e.blobs.BeforeUploadIf = nil
	e.deliver(t, payload)

	e.stripe.listStatus = http.StatusInternalServerError
	if w := e.send(event(t, "charge_refunded", "", "", nil), webhookSecret, time.Now()); w.Code != http.StatusInternalServerError {
		t.Errorf("stripe lookup error: %d %s", w.Code, w.Body)
	}
	e.h.Stripe = nil
	if w := e.send(event(t, "charge_refunded", "", "", nil), webhookSecret, time.Now()); w.Code != http.StatusServiceUnavailable {
		t.Errorf("no stripe client: %d %s", w.Code, w.Body)
	}
	if got := e.balance(t, key.ID); got != 2100 {
		t.Errorf("balance = %d", got)
	}
}

func refund(t *testing.T, refunded int64) []byte {
	return event(t, "charge_refunded", "", "", map[string]any{"amount_refunded": refunded, "refunded": refunded == 1240})
}

func TestWebhookRefunds(t *testing.T) {
	e := newEnv(t)
	_, accountID, key := e.customer(t, "ann@example.com")
	e.stripe.addSession(orderID, accountID, key.ID, "starter")
	e.deliver(t, event(t, "checkout_session_completed", accountID, key.ID, nil))

	// The charge is 1240 cents; half refunded takes back half the credits.
	e.deliver(t, refund(t, 620))
	if got := e.balance(t, key.ID); got != 2100-1000 {
		t.Fatalf("balance after half refund = %d", got)
	}
	e.deliver(t, refund(t, 620))
	if got := e.balance(t, key.ID); got != 2100-1000 {
		t.Fatalf("balance after repeated refund = %d", got)
	}
	// A third more, rounded up: ceil(2000 × 1033 / 1240) = 1667 in all.
	e.deliver(t, refund(t, 1033))
	if got := e.balance(t, key.ID); got != 2100-1667 {
		t.Fatalf("balance after 1033 refunded = %d", got)
	}
	e.deliver(t, refund(t, 1240))
	if got := e.balance(t, key.ID); got != 100 {
		t.Fatalf("balance after full refund = %d", got)
	}
	k, _ := e.h.Store.Get(t.Context(), key.ID)
	if o := k.Orders[orderID]; o.Debited != 2000 {
		t.Errorf("order = %+v", o)
	}
}

func TestWebhookRefundAfterSpending(t *testing.T) {
	e := newEnv(t)
	_, accountID, key := e.customer(t, "ann@example.com")
	e.stripe.addSession(orderID, accountID, key.ID, "starter")
	e.deliver(t, event(t, "checkout_session_completed", accountID, key.ID, nil))
	if _, err := e.h.Store.Charge(t.Context(), key.ID, 2050); err != nil {
		t.Fatal(err)
	}
	e.deliver(t, refund(t, 1240))
	if got := e.balance(t, key.ID); got != -1950 {
		t.Errorf("balance = %d, want -1950", got)
	}
}

func TestWebhookDisputes(t *testing.T) {
	e := newEnv(t)
	_, accountID, key := e.customer(t, "ann@example.com")
	e.stripe.addSession(orderID, accountID, key.ID, "starter")
	e.deliver(t, event(t, "checkout_session_completed", accountID, key.ID, nil))
	e.deliver(t, refund(t, 310)) // a quarter: 500 credits

	for _, status := range []string{"won", "warning_closed"} {
		e.deliver(t, event(t, "charge_dispute_closed", "", "", map[string]any{"status": status}))
		if got := e.balance(t, key.ID); got != 2100-500 {
			t.Fatalf("balance after %s dispute = %d", status, got)
		}
	}
	lost := event(t, "charge_dispute_closed", "", "", nil)
	e.deliver(t, lost)
	e.deliver(t, lost)
	if got := e.balance(t, key.ID); got != 100 {
		t.Errorf("balance after lost dispute = %d, want 100", got)
	}
}

func TestWebhookDebitNotFound(t *testing.T) {
	e := newEnv(t)
	session, accountID, key := e.customer(t, "ann@example.com")

	// No session at Stripe for the payment (not one of ours).
	e.deliver(t, refund(t, 1240))
	// A refund before its session's credit debits nothing; the later
	// credit then stands.
	e.stripe.addSession(orderID, accountID, key.ID, "starter")
	e.deliver(t, refund(t, 1240))
	if got := e.balance(t, key.ID); got != 100 {
		t.Fatalf("balance = %d", got)
	}
	e.deliver(t, event(t, "checkout_session_completed", accountID, key.ID, nil))
	if got := e.balance(t, key.ID); got != 2100 {
		t.Fatalf("balance = %d", got)
	}
	// A revoked key.
	if w := e.call("DELETE", keyQuery(key.ID), session, ""); w.Code != http.StatusNoContent {
		t.Fatal(w.Code)
	}
	e.deliver(t, event(t, "charge_dispute_closed", "", "", nil))
	// No payment intent at all.
	e.deliver(t, event(t, "charge_refunded", "", "", map[string]any{"payment_intent": nil}))
}

func TestWebhookIgnoredEvent(t *testing.T) {
	e := newEnv(t)
	_, accountID, key := e.customer(t, "ann@example.com")
	e.deliver(t, event(t, "checkout_session_completed", accountID, key.ID, map[string]any{"type": "checkout.session.expired"}))
	if got := e.balance(t, key.ID); got != 100 {
		t.Errorf("balance = %d", got)
	}
}

// paid returns a checkout.session.completed event for the pack, paid with
// the PaymentIntent pi.
func paid(t *testing.T, accountID, keyID, pack, pi string) []byte {
	return event(t, "checkout_session_completed", accountID, keyID, map[string]any{"metadata.pack": pack, "payment_intent": pi})
}

func TestWebhookCreditCap(t *testing.T) {
	e := newEnv(t)
	session, accountID, first := e.customer(t, "ann@example.com")
	second := e.createKey(t, session)
	set := func(id string, credits int64) {
		t.Helper()
		if _, err := e.h.Store.Set(t.Context(), id, credits); err != nil {
			t.Fatal(err)
		}
	}
	set(first.ID, 5000)
	set(second.ID, 3001)

	// 8001 + 12000 is over 20000, whichever key buys: refunded, not credited.
	over := paid(t, accountID, second.ID, "pro", "pi_over")
	e.deliver(t, over)
	if got := e.balance(t, second.ID); got != 3001 {
		t.Errorf("balance = %d, want 3001", got)
	}
	k, _ := e.h.Store.Get(t.Context(), second.ID)
	if o := k.Orders["pi_over"]; !o.OverCap || o.Credits != 0 || o.Pack != "pro" {
		t.Errorf("order = %+v", o)
	}
	forms, keys := e.stripe.refundCalls()
	if len(forms) != 1 {
		t.Fatalf("%d refunds, want 1", len(forms))
	}
	f := forms[0]
	if f.Get("payment_intent") != "pi_over" || f.Get("amount") != "" || f.Get("metadata[reason]") != "credit_cap" ||
		f.Get("metadata[account_id]") != accountID || f.Get("metadata[key_id]") != second.ID || f.Get("metadata[pack]") != "pro" {
		t.Errorf("refund form = %v", f)
	}
	if keys[0] != "overcap-pi_over" {
		t.Errorf("Idempotency-Key = %q", keys[0])
	}

	// A retried event refunds again (the same idempotency key), and never
	// credits, even once the credits would fit.
	set(second.ID, 0)
	e.deliver(t, over)
	if got := e.balance(t, second.ID); got != 0 {
		t.Errorf("balance after retry = %d, want 0", got)
	}
	if _, keys := e.stripe.refundCalls(); len(keys) != 2 || keys[1] != "overcap-pi_over" {
		t.Errorf("refund keys = %v", keys)
	}
	// Refunded already, e.g. after the idempotency key expired: done.
	e.stripe.refundStatus, e.stripe.refundCode = http.StatusBadRequest, "charge_already_refunded"
	e.deliver(t, over)
	e.stripe.refundStatus, e.stripe.refundCode = 0, ""

	// The refund's own event takes nothing back.
	e.stripe.addSession("pi_over", accountID, second.ID, "pro")
	e.deliver(t, event(t, "charge_refunded", "", "", map[string]any{
		"payment_intent": "pi_over", "amount_refunded": 1240, "refunded": true}))
	if got := e.balance(t, second.ID); got != 0 {
		t.Errorf("balance after the refund event = %d, want 0", got)
	}

	// Up to the cap is credited: 5000 + 0 + 12000 + 2000 = 19000.
	e.deliver(t, paid(t, accountID, second.ID, "pro", "pi_fits"))
	e.deliver(t, paid(t, accountID, first.ID, "starter", "pi_fits_too"))
	if a, b := e.balance(t, first.ID), e.balance(t, second.ID); a != 7000 || b != 12000 {
		t.Errorf("balances = %d, %d; want 7000, 12000", a, b)
	}
	// A retried credited order stays credited, though it no longer fits.
	e.deliver(t, paid(t, accountID, second.ID, "pro", "pi_fits"))
	if got := e.balance(t, second.ID); got != 12000 {
		t.Errorf("balance after retry = %d, want 12000", got)
	}
	if forms, _ := e.stripe.refundCalls(); len(forms) != 3 {
		t.Errorf("%d refunds, want 3", len(forms))
	}
}

func TestWebhookCreditCapParallel(t *testing.T) {
	e := newEnv(t)
	session, accountID, first := e.customer(t, "ann@example.com")
	second := e.createKey(t, session)
	// Two pro packs fit one at a time, not both: one is refunded.
	var wg sync.WaitGroup
	codes := make([]int, 2)
	for i, keyID := range []string{first.ID, second.ID} {
		wg.Go(func() {
			codes[i] = e.send(paid(t, accountID, keyID, "pro", fmt.Sprint("pi_", i)), webhookSecret, time.Now()).Code
		})
	}
	wg.Wait()
	if codes[0] != http.StatusOK || codes[1] != http.StatusOK {
		t.Fatalf("codes = %v", codes)
	}
	total := e.balance(t, first.ID) + e.balance(t, second.ID)
	if total != 12100 {
		t.Errorf("total = %d, want 12100", total)
	}
	if forms, _ := e.stripe.refundCalls(); len(forms) != 1 {
		t.Errorf("%d refunds, want 1", len(forms))
	}
}

func TestWebhookCreditCapErrors(t *testing.T) {
	e := newEnv(t)
	_, accountID, key := e.customer(t, "ann@example.com")
	if _, err := e.h.Store.Set(t.Context(), key.ID, 19000); err != nil {
		t.Fatal(err)
	}
	over := paid(t, accountID, key.ID, "starter", "pi_over")

	// A held lock: 503, nothing recorded, so Stripe retries.
	tries, poll := creditLockTries, creditLockPoll
	creditLockTries, creditLockPoll = 2, time.Millisecond
	t.Cleanup(func() { creditLockTries, creditLockPoll = tries, poll })
	release, err := e.h.Locks.Acquire(t.Context(), "credits-"+accountID, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if w := e.send(over, webhookSecret, time.Now()); w.Code != http.StatusServiceUnavailable {
		t.Errorf("locked: %d %s", w.Code, w.Body)
	}
	if k, _ := e.h.Store.Get(t.Context(), key.ID); len(k.Orders) != 0 {
		t.Errorf("orders = %+v", k.Orders)
	}
	if err := release(t.Context()); err != nil {
		t.Fatal(err)
	}

	// A failed refund: 500, and the retry refunds.
	e.stripe.refundStatus = http.StatusInternalServerError
	if w := e.send(over, webhookSecret, time.Now()); w.Code != http.StatusInternalServerError {
		t.Errorf("refund failed: %d %s", w.Code, w.Body)
	}
	e.stripe.refundStatus = 0
	e.deliver(t, over)
	if forms, _ := e.stripe.refundCalls(); len(forms) != 2 {
		t.Errorf("%d refund calls, want 2", len(forms))
	}
	// Without a Stripe client: 503.
	e.h.Stripe = nil
	if w := e.send(over, webhookSecret, time.Now()); w.Code != http.StatusServiceUnavailable {
		t.Errorf("no stripe client: %d %s", w.Code, w.Body)
	}
	if got := e.balance(t, key.ID); got != 19000 {
		t.Errorf("balance = %d, want 19000", got)
	}

	// No cap: credited.
	e.h.MaxCredits = 0
	e.deliver(t, paid(t, accountID, key.ID, "pro", "pi_nocap"))
	if got := e.balance(t, key.ID); got != 31000 {
		t.Errorf("balance without a cap = %d, want 31000", got)
	}
}
