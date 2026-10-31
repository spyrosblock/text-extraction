package handler

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/stripe/stripe-go/v87"
	"github.com/stripe/stripe-go/v87/webhook"

	"github.com/spyrosblock/text-extraction/billing"
)

const (
	// Checkouts allowed per account; each one creates a session at Stripe.
	checkoutsPerAccount       = 10
	checkoutsPerAccountWindow = time.Hour
	// checkoutTimeout bounds the call to Stripe, retries included.
	checkoutTimeout = 10 * time.Second
	// checkoutTTL is how long a Checkout Session can be paid.
	checkoutTTL = time.Hour
	// maxWebhookBody bounds a webhook event's body.
	maxWebhookBody = 1 << 20
	// The webhook holds the account's credit lock while it checks the cap
	// and credits an order, for at most creditLockTTL.
	creditLockTTL = 30 * time.Second
	// refundReasonCap is the metadata reason of the refunds the webhook makes.
	refundReasonCap = "credit_cap"
)

// The webhook waits for a held credit lock creditLockTries times,
// creditLockPoll apart (5s in all); tests shorten it.
var (
	creditLockTries = 20
	creditLockPoll  = 250 * time.Millisecond
)

// Stripe event types the webhook acts on.
const (
	eventSessionCompleted    = "checkout.session.completed"
	eventSessionAsyncSuccess = "checkout.session.async_payment_succeeded"
	eventChargeRefunded      = "charge.refunded"
	eventDisputeClosed       = "charge.dispute.closed"
)

type checkoutResponse struct {
	URL string `json:"url"`
}

// Checkout serves POST /api/checkout {"pack","key_id"}: it creates a Stripe
// Checkout Session (Managed Payments) for the pack, to be credited to the
// key, and answers with its URL. The credits are added by the webhook once
// the payment succeeds.
//
// It answers 409 when the pack would take the account's credits, summed over
// its keys, over MaxCredits. Sessions opened in parallel are each checked
// against the current balances, so the webhook checks again when one is paid
// and refunds it if it no longer fits.
func (h *Handler) Checkout(w http.ResponseWriter, r *http.Request) {
	h.authed(h.checkout)(w, r)
}

func (h *Handler) checkout(w http.ResponseWriter, r *http.Request, s session) {
	ctx := r.Context()
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.Stripe == nil {
		writeError(w, http.StatusServiceUnavailable, "payments are not configured")
		return
	}
	var req struct {
		Pack  string `json:"pack"`
		KeyID string `json:"key_id"`
	}
	if !decode(w, r, &req) {
		return
	}
	pack, ok := h.Packs[req.Pack]
	if !ok {
		writeError(w, http.StatusBadRequest, "unknown pack")
		return
	}
	keyID, ok := h.ownedKeyID(w, r, s, req.KeyID)
	if !ok {
		return
	}
	if h.MaxCredits > 0 {
		keys, err := h.ownedKeys(ctx, s.accountID)
		if err != nil {
			unexpected(w, r, "start checkout", err)
			return
		}
		var total int64
		for _, k := range keys {
			total += k.Credits
		}
		if total+pack.Credits > h.MaxCredits {
			slog.InfoContext(ctx, "checkout over the credit cap", "accountId", s.accountID, "pack", req.Pack, "credits", total, "maxCredits", h.MaxCredits)
			writeError(w, http.StatusConflict, fmt.Sprintf(
				"an account can hold at most %d credits; yours has %d, so the %s pack (%d credits) would go over", h.MaxCredits, total, req.Pack, pack.Credits))
			return
		}
	}
	allowed, err := h.Limits.Allow(ctx, "checkout-account", s.accountID, checkoutsPerAccount, checkoutsPerAccountWindow)
	if err != nil {
		unexpected(w, r, "start checkout", err)
		return
	}
	if !allowed {
		slog.WarnContext(ctx, "checkout rate limited", "accountId", s.accountID)
		writeError(w, http.StatusTooManyRequests, "too many checkouts, try again later")
		return
	}

	site := strings.TrimSuffix(h.SiteURL, "/")
	params := &stripe.CheckoutSessionCreateParams{
		Mode:            stripe.String(string(stripe.CheckoutSessionModePayment)),
		ManagedPayments: &stripe.CheckoutSessionCreateManagedPaymentsParams{Enabled: stripe.Bool(true)},
		LineItems: []*stripe.CheckoutSessionCreateLineItemParams{
			{Price: stripe.String(pack.PriceID), Quantity: stripe.Int64(1)},
		},
		Metadata:          map[string]string{"account_id": s.accountID, "key_id": keyID, "pack": req.Pack},
		ClientReferenceID: stripe.String(keyID),
		CustomerEmail:     stripe.String(s.account.Email),
		SuccessURL:        stripe.String(site + "/dashboard.html?paid=1"),
		CancelURL:         stripe.String(site + "/dashboard.html"),
		ExpiresAt:         stripe.Int64(h.now().Add(checkoutTTL).Unix()),
	}
	// A new key per request: the SDK's own retries reuse it, a second click
	// is a second session.
	params.SetIdempotencyKey("checkout-" + rand.Text())
	callCtx, cancel := context.WithTimeout(ctx, checkoutTimeout)
	defer cancel()
	cs, err := h.Stripe.V1CheckoutSessions.Create(callCtx, params)
	if err != nil {
		logStripeError(ctx, "create checkout session", err, "accountId", s.accountID, "keyId", keyID, "pack", req.Pack)
		writeError(w, http.StatusBadGateway, "could not start the payment, try again")
		return
	}
	slog.InfoContext(ctx, "checkout started", "accountId", s.accountID, "keyId", keyID, "pack", req.Pack, "sessionId", cs.ID)
	writeJSON(w, http.StatusOK, checkoutResponse{URL: cs.URL})
}

// logStripeError logs a Stripe API error with its type, code and request id.
func logStripeError(ctx context.Context, msg string, err error, attrs ...any) {
	var se *stripe.Error
	if errors.As(err, &se) {
		attrs = append(attrs, "stripeType", se.Type, "stripeCode", se.Code, "httpStatus", se.HTTPStatusCode, "requestId", se.RequestID)
	}
	slog.ErrorContext(ctx, msg, append(attrs, "error", err)...)
}

// StripeWebhook serves POST /api/webhooks/stripe: Stripe's events, signed
// with WebhookSecret. A paid Checkout Session credits its pack to the key in
// its metadata; a refund or a lost dispute takes the refunded share back.
//
// Stripe retries an event that doesn't get a 2xx for up to 3 days, so
// failures a retry can fix (blobs, Stripe lookups, ErrBusy) answer 5xx, and
// everything else answers 200 or 400 and is logged.
func (h *Handler) StripeWebhook(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.WebhookSecret == "" { // main refuses to start without it
		writeError(w, http.StatusServiceUnavailable, "webhooks are not configured")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxWebhookBody))
	if err != nil {
		writeError(w, http.StatusBadRequest, "could not read body")
		return
	}
	event, err := webhook.ConstructEvent(body, r.Header.Get("Stripe-Signature"), h.WebhookSecret)
	if err != nil {
		slog.WarnContext(ctx, "stripe webhook rejected", "error", err)
		writeError(w, http.StatusBadRequest, "invalid signature")
		return
	}
	log := slog.With("eventId", event.ID, "eventType", string(event.Type))
	if event.Livemode == h.TestMode {
		log.ErrorContext(ctx, "stripe event in the wrong mode", "livemode", event.Livemode, "testMode", h.TestMode)
		writeError(w, http.StatusBadRequest, "event livemode doesn't match")
		return
	}
	if event.Data == nil {
		log.ErrorContext(ctx, "stripe event without data")
		writeError(w, http.StatusBadRequest, "event has no data")
		return
	}

	switch event.Type {
	case eventSessionCompleted, eventSessionAsyncSuccess:
		var cs stripe.CheckoutSession
		if !decodeObject(w, r, log, event, &cs) {
			return
		}
		h.sessionPaid(w, r, log, event, &cs)
	case eventChargeRefunded:
		var ch stripe.Charge
		if !decodeObject(w, r, log, event, &ch) {
			return
		}
		pi := ""
		if ch.PaymentIntent != nil {
			pi = ch.PaymentIntent.ID
		}
		amount, refunded := ch.Amount, ch.AmountRefunded
		h.debit(w, r, log, event, pi, func(credits int64) int64 {
			if amount <= 0 {
				return 0
			}
			// ceil(credits × refunded / amount); amount_refunded is cumulative.
			return (credits*refunded + amount - 1) / amount
		})
	case eventDisputeClosed:
		var d stripe.Dispute
		if !decodeObject(w, r, log, event, &d) {
			return
		}
		if d.Status != stripe.DisputeStatusLost {
			log.InfoContext(ctx, "stripe dispute closed", "status", string(d.Status))
			w.WriteHeader(http.StatusOK)
			return
		}
		pi := ""
		if d.PaymentIntent != nil {
			pi = d.PaymentIntent.ID
		}
		h.debit(w, r, log, event, pi, func(credits int64) int64 { return credits })
	default:
		log.InfoContext(ctx, "stripe event ignored")
		w.WriteHeader(http.StatusOK)
	}
}

func decodeObject(w http.ResponseWriter, r *http.Request, log *slog.Logger, event stripe.Event, v any) bool {
	if err := json.Unmarshal(event.Data.Raw, v); err != nil {
		log.ErrorContext(r.Context(), "decode stripe event", "error", err)
		writeError(w, http.StatusBadRequest, "invalid event object")
		return false
	}
	return true
}

// sessionPaid credits a paid Checkout Session's pack to the key in its
// metadata, once per PaymentIntent. An order that would take the account
// over MaxCredits is refunded in full instead.
func (h *Handler) sessionPaid(w http.ResponseWriter, r *http.Request, log *slog.Logger, event stripe.Event, cs *stripe.CheckoutSession) {
	ctx := r.Context()
	accountID, keyID, packName := cs.Metadata["account_id"], cs.Metadata["key_id"], cs.Metadata["pack"]
	orderID := ""
	if cs.PaymentIntent != nil {
		orderID = cs.PaymentIntent.ID
	}
	email := cs.CustomerEmail
	if cs.CustomerDetails != nil && cs.CustomerDetails.Email != "" {
		email = cs.CustomerDetails.Email
	}
	log = log.With("sessionId", cs.ID, "orderId", orderID, "accountId", accountID, "keyId", keyID, "pack", packName)

	if cs.PaymentStatus != stripe.CheckoutSessionPaymentStatusPaid {
		// A delayed payment method; its async_payment_succeeded event credits it.
		log.InfoContext(ctx, "stripe session not paid yet", "paymentStatus", string(cs.PaymentStatus))
		w.WriteHeader(http.StatusOK)
		return
	}
	pack, ok := h.Packs[packName]
	if !ok || orderID == "" {
		// A retry wouldn't help; support credits it by hand.
		log.ErrorContext(ctx, "stripe order not credited: unknown pack or no payment intent", "email", email)
		w.WriteHeader(http.StatusOK)
		return
	}
	log = log.With("credits", pack.Credits)

	orphan := func(reason string) {
		log.ErrorContext(ctx, "stripe order not credited: "+reason+"; credit another key with keys.sh add or refund it", "email", email)
		w.WriteHeader(http.StatusOK)
	}
	if !billing.ValidID(keyID) {
		orphan("no key id in the metadata")
		return
	}
	k, err := h.Store.Get(ctx, keyID)
	if errors.Is(err, billing.ErrUnknownKey) {
		orphan("the key was revoked")
		return
	}
	if err != nil {
		unexpected(w, r, "credit order", err)
		return
	}
	if accountID == "" || k.AccountID != accountID {
		orphan("the account doesn't own the key")
		return
	}

	balance, credited, overCap, err := h.creditOrder(ctx, log, accountID, keyID, orderID, packName, pack.Credits)
	if errors.Is(err, billing.ErrUnknownKey) { // revoked meanwhile
		orphan("the key was revoked")
		return
	}
	if err != nil {
		unexpected(w, r, "credit order", err)
		return
	}
	if overCap {
		h.refundOverCap(w, r, log, accountID, keyID, orderID, packName)
		return
	}
	if !credited {
		log.InfoContext(ctx, "stripe order already credited", "balance", balance)
	} else {
		log.InfoContext(ctx, "stripe order", "balance", balance)
	}
	w.WriteHeader(http.StatusOK)
}

// creditOrder credits the order to the key, unless that would take the
// account's credits, summed over its keys, over MaxCredits: then it records
// the order as OverCap, to be refunded. The check and the credit run under
// the account's credit lock, so parallel orders are checked one after the
// other. An order seen before keeps its outcome: credited is false, and
// overCap says whether it was refused.
func (h *Handler) creditOrder(ctx context.Context, log *slog.Logger, accountID, keyID, orderID, packName string, credits int64) (balance int64, credited, overCap bool, err error) {
	if h.MaxCredits <= 0 {
		balance, credited, err = h.Store.CreditOrder(ctx, keyID, orderID, packName, credits)
		return balance, credited, false, err
	}
	release, err := h.lockCredits(ctx, accountID)
	if err != nil {
		return 0, false, false, err
	}
	defer func() {
		if err := release(context.WithoutCancel(ctx)); err != nil {
			log.ErrorContext(ctx, "release credit lock", "error", err)
		}
	}()

	k, err := h.Store.Get(ctx, keyID)
	if err != nil {
		return 0, false, false, err
	}
	if o, ok := k.Orders[orderID]; ok {
		return k.Credits, false, o.OverCap, nil
	}
	keys, err := h.ownedKeys(ctx, accountID)
	if err != nil {
		return 0, false, false, err
	}
	var total int64
	for _, k := range keys {
		total += k.Credits
	}
	if total+credits > h.MaxCredits {
		log.WarnContext(ctx, "stripe order over the credit cap", "accountCredits", total, "maxCredits", h.MaxCredits)
		o, err := h.Store.RefuseOrder(ctx, keyID, orderID, packName)
		if err != nil {
			return 0, false, false, err
		}
		return k.Credits, false, o.OverCap, nil
	}
	balance, credited, err = h.Store.CreditOrder(ctx, keyID, orderID, packName, credits)
	return balance, credited, false, err
}

// lockCredits takes the account's credit lock, waiting for it a while. A
// lock held longer gives billing.ErrBusy, so Stripe retries the event.
func (h *Handler) lockCredits(ctx context.Context, accountID string) (func(context.Context) error, error) {
	for attempt := 1; ; attempt++ {
		release, err := h.Locks.Acquire(ctx, "credits-"+accountID, creditLockTTL)
		if !errors.Is(err, billing.ErrLocked) {
			return release, err
		}
		if attempt >= creditLockTries {
			return nil, fmt.Errorf("credit lock of account %s: %w", accountID, billing.ErrBusy)
		}
		select {
		case <-time.After(creditLockPoll):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// refundOverCap refunds an order recorded as OverCap in full. The
// idempotency key and Stripe's charge_already_refunded make a retried event
// refund it only once.
func (h *Handler) refundOverCap(w http.ResponseWriter, r *http.Request, log *slog.Logger, accountID, keyID, orderID, packName string) {
	ctx := r.Context()
	if h.Stripe == nil {
		log.ErrorContext(ctx, "stripe order over the credit cap needs a refund, but payments are not configured")
		writeError(w, http.StatusServiceUnavailable, "payments are not configured")
		return
	}
	params := &stripe.RefundCreateParams{
		PaymentIntent: stripe.String(orderID),
		Metadata:      map[string]string{"reason": refundReasonCap, "account_id": accountID, "key_id": keyID, "pack": packName},
	}
	params.SetIdempotencyKey("overcap-" + orderID)
	callCtx, cancel := context.WithTimeout(ctx, checkoutTimeout)
	defer cancel()
	refund, err := h.Stripe.V1Refunds.Create(callCtx, params)
	var se *stripe.Error
	if errors.As(err, &se) && se.Code == stripe.ErrorCodeChargeAlreadyRefunded {
		log.InfoContext(ctx, "stripe order over the credit cap already refunded")
		w.WriteHeader(http.StatusOK)
		return
	}
	if err != nil {
		logStripeError(ctx, "refund order over the credit cap", err, "orderId", orderID, "accountId", accountID, "keyId", keyID)
		writeError(w, http.StatusInternalServerError, "could not refund the order")
		return
	}
	log.WarnContext(ctx, "stripe order refunded: over the credit cap", "refundId", refund.ID, "refundStatus", string(refund.Status), "amount", refund.Amount)
	w.WriteHeader(http.StatusOK)
}

// debit takes credits back for the order orderID (a PaymentIntent id): it
// finds the order's Checkout Session at Stripe for the key, then debits
// total(order credits) in all for the order.
func (h *Handler) debit(w http.ResponseWriter, r *http.Request, log *slog.Logger, event stripe.Event, orderID string, total func(credits int64) int64) {
	ctx := r.Context()
	log = log.With("orderId", orderID)
	if orderID == "" {
		log.ErrorContext(ctx, "stripe event without a payment intent")
		w.WriteHeader(http.StatusOK)
		return
	}
	if h.Stripe == nil {
		log.ErrorContext(ctx, "stripe event needs a session lookup, but payments are not configured")
		writeError(w, http.StatusServiceUnavailable, "payments are not configured")
		return
	}

	list := h.Stripe.V1CheckoutSessions.List(ctx, &stripe.CheckoutSessionListParams{PaymentIntent: stripe.String(orderID)})
	var cs *stripe.CheckoutSession
	for s, err := range list.All(ctx) {
		if err != nil {
			logStripeError(ctx, "find checkout session", err, "eventId", event.ID, "orderId", orderID)
			writeError(w, http.StatusInternalServerError, "could not find the checkout session")
			return
		}
		cs = s
		break
	}
	if cs == nil {
		log.ErrorContext(ctx, "stripe order not debited: no checkout session for it")
		w.WriteHeader(http.StatusOK)
		return
	}
	accountID, keyID, packName := cs.Metadata["account_id"], cs.Metadata["key_id"], cs.Metadata["pack"]
	log = log.With("sessionId", cs.ID, "accountId", accountID, "keyId", keyID, "pack", packName)

	if !billing.ValidID(keyID) {
		log.ErrorContext(ctx, "stripe order not debited: no key id in the session metadata")
		w.WriteHeader(http.StatusOK)
		return
	}
	k, err := h.Store.Get(ctx, keyID)
	if errors.Is(err, billing.ErrUnknownKey) {
		log.ErrorContext(ctx, "stripe order not debited: the key was revoked")
		w.WriteHeader(http.StatusOK)
		return
	}
	if err != nil {
		unexpected(w, r, "debit order", err)
		return
	}
	order, ok := k.Orders[orderID]
	if !ok {
		log.ErrorContext(ctx, "stripe order not debited: the key has no such order")
		w.WriteHeader(http.StatusOK)
		return
	}
	if order.OverCap {
		log.InfoContext(ctx, "stripe order over the credit cap: nothing to debit")
		w.WriteHeader(http.StatusOK)
		return
	}

	balance, debited, err := h.Store.DebitOrder(ctx, keyID, orderID, total(order.Credits))
	switch {
	case errors.Is(err, billing.ErrUnknownKey):
		log.ErrorContext(ctx, "stripe order not debited: the key was revoked")
	case errors.Is(err, billing.ErrUnknownOrder):
		log.ErrorContext(ctx, "stripe order not debited: the key has no such order")
	case err != nil:
		unexpected(w, r, "debit order", err)
		return
	default:
		log.InfoContext(ctx, "stripe order", "credits", -(debited - order.Debited), "debited", debited, "balance", balance)
	}
	w.WriteHeader(http.StatusOK)
}
