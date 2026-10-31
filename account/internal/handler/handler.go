// Package handler implements the account app's HTTP endpoints: magic-link
// sign-in, the customer's API keys, and buying credit packs through Stripe
// Checkout (Managed Payments) and its webhook.
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/stripe/stripe-go/v87"

	"github.com/spyrosblock/text-extraction/account/internal/email"
	"github.com/spyrosblock/text-extraction/billing"
)

// maxBody bounds request bodies.
const maxBody = 4 << 10

// Pack is a credit pack customers can buy. Stripe charges the amount of the
// Price PriceID; PriceEUR is only displayed and should equal it. The
// credits always come from this server-side list.
type Pack struct {
	Credits  int64   `json:"credits"`
	PriceEUR float64 `json:"price_eur"`
	PriceID  string  `json:"price_id"`
}

type Sender interface {
	Send(ctx context.Context, m email.Message) error
}

type Handler struct {
	Store       *billing.Store
	Accounts    *billing.Accounts
	Sessions    *billing.Sessions
	LoginTokens *billing.LoginTokens
	Limits      *billing.RateLimiter
	// Locks serialize the webhook's credit-cap check per account.
	Locks *billing.Locks
	Email Sender

	// SiteURL is the website, e.g. https://<app>.azurestaticapps.net; the
	// magic link and the checkout's return URLs point to it.
	SiteURL string
	Packs   map[string]Pack
	// FreeCredits go to the first key of each email address, once.
	FreeCredits int64
	MaxKeys     int
	// MaxCredits caps the credits an account can hold, summed over its
	// keys: a checkout that would go over it is refused, and a paid order
	// that would (sessions opened in parallel) is refunded. 0: no cap.
	MaxCredits int64

	// Stripe creates Checkout Sessions and looks them up; nil when no API
	// key is configured, and then checkout answers 503.
	Stripe *stripe.Client
	// WebhookSecret verifies the Stripe-Signature of webhook events.
	WebhookSecret string
	// TestMode is true when Stripe runs in test mode; webhook events must
	// have livemode == !TestMode.
	TestMode bool
	// Now is the clock for checkout expiry; time.Now when nil.
	Now func() time.Time
}

func (h *Handler) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

// session is the signed-in caller.
type session struct {
	token     string
	accountID string
	account   billing.Account
}

// authed wraps f with the session check: a missing, expired or unknown
// session, or one whose account was deleted, gets 401.
func (h *Handler) authed(f func(http.ResponseWriter, *http.Request, session)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := bearer(r)
		id, acc, err := h.Sessions.Resolve(r.Context(), token)
		if errors.Is(err, billing.ErrNoSession) {
			writeError(w, http.StatusUnauthorized, "not signed in")
			return
		}
		if err != nil {
			slog.ErrorContext(r.Context(), "resolve session", "error", err)
			writeError(w, http.StatusInternalServerError, "could not check session")
			return
		}
		f(w, r, session{token: token, accountID: id, account: acc})
	}
}

func bearer(r *http.Request) string {
	scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	return strings.TrimSpace(token)
}

// clientIP is the first X-Forwarded-For entry (set by the Functions front
// end), without a port, or the connection's address.
func clientIP(r *http.Request) string {
	ip, _, _ := strings.Cut(r.Header.Get("X-Forwarded-For"), ",")
	ip = strings.TrimSpace(ip)
	if ip == "" {
		ip = r.RemoteAddr
	}
	if host, _, err := net.SplitHostPort(ip); err == nil {
		ip = host
	}
	if ip == "" {
		return "unknown"
	}
	return ip
}

// unexpected logs err and answers 500, or 503 for a balance that kept
// changing.
func unexpected(w http.ResponseWriter, r *http.Request, action string, err error) {
	if errors.Is(err, billing.ErrBusy) {
		writeError(w, http.StatusServiceUnavailable, "too many concurrent requests, retry")
		return
	}
	slog.ErrorContext(r.Context(), action, "error", err)
	writeError(w, http.StatusInternalServerError, "could not "+action)
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody)).Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return false
	}
	return true
}

type errorResponse struct {
	Error string `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, errorResponse{Error: msg})
}
