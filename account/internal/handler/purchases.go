package handler

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/spyrosblock/text-extraction/billing"
)

// Wrong test-mode password attempts allowed before purchases answer 429,
// even with the right password.
const (
	wrongPasswordPerAccount       = 5
	wrongPasswordPerAccountWindow = 15 * time.Minute
	wrongPasswordPerIP            = 20
	wrongPasswordPerIPWindow      = time.Hour
)

var uuidRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

type purchaseResponse struct {
	OrderID string `json:"order_id"`
	Credits int64  `json:"credits"`
	Balance int64  `json:"balance"`
}

// Purchases serves POST /api/purchases {"pack","key_id","order_id","password"}:
// a mock purchase. No money moves; the pack's credits are added to the key
// at once. Every purchase needs MockPassword. A repeated order_id adds
// nothing and answers with the current balance, so a retry is safe.
func (h *Handler) Purchases(w http.ResponseWriter, r *http.Request) {
	h.authed(h.purchase)(w, r)
}

func (h *Handler) purchase(w http.ResponseWriter, r *http.Request, s session) {
	ctx := r.Context()
	if h.PaymentsMode != PaymentsMock {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	var req struct {
		Pack     string `json:"pack"`
		KeyID    string `json:"key_id"`
		OrderID  string `json:"order_id"`
		Password string `json:"password"`
	}
	if !decode(w, r, &req) {
		return
	}
	if !h.checkPassword(w, r, s, req.Password) {
		return
	}

	pack, ok := h.Packs[req.Pack]
	if !ok {
		writeError(w, http.StatusBadRequest, "unknown pack")
		return
	}
	orderID := strings.ToLower(req.OrderID)
	if !uuidRE.MatchString(orderID) {
		writeError(w, http.StatusBadRequest, "order_id must be a uuid")
		return
	}
	id, ok := h.ownedKeyID(w, r, s, req.KeyID)
	if !ok {
		return
	}

	balance, credited, err := h.Store.CreditOrder(ctx, id, orderID, req.Pack, pack.Credits)
	if errors.Is(err, billing.ErrUnknownKey) { // revoked meanwhile
		writeError(w, http.StatusNotFound, "no key with this id")
		return
	}
	if err != nil {
		unexpected(w, r, "credit purchase", err)
		return
	}
	slog.InfoContext(ctx, "mock purchase", "accountId", s.accountID, "keyId", id, "orderId", orderID,
		"pack", req.Pack, "credits", pack.Credits, "repeated", !credited, "balance", balance)
	writeJSON(w, http.StatusOK, purchaseResponse{OrderID: orderID, Credits: pack.Credits, Balance: balance})
}

// checkPassword answers 403 for a missing or wrong test-mode password (and
// when purchases are off), or 429 after too many wrong ones. Only wrong
// attempts are counted. The password is never logged.
func (h *Handler) checkPassword(w http.ResponseWriter, r *http.Request, s session, password string) bool {
	ctx := r.Context()
	if h.MockPassword == "" {
		writeError(w, http.StatusForbidden, "test-mode purchases are disabled")
		return false
	}
	ip := clientIP(r)
	limits := []struct {
		kind, value string
		limit       int64
		window      time.Duration
	}{
		{"password-account", s.accountID, wrongPasswordPerAccount, wrongPasswordPerAccountWindow},
		{"password-ip", ip, wrongPasswordPerIP, wrongPasswordPerIPWindow},
	}
	for _, l := range limits {
		n, err := h.Limits.Count(ctx, l.kind, l.value, l.window)
		if err != nil {
			unexpected(w, r, "check password", err)
			return false
		}
		if n >= l.limit {
			slog.WarnContext(ctx, "test-mode password rate limited", "accountId", s.accountID, "ip", ip, "by", l.kind)
			writeError(w, http.StatusTooManyRequests, "too many wrong test-mode passwords, try again later")
			return false
		}
	}

	// Hashing first makes the comparison constant-time whatever the lengths.
	got, want := sha256.Sum256([]byte(password)), sha256.Sum256([]byte(h.MockPassword))
	if subtle.ConstantTimeCompare(got[:], want[:]) == 1 {
		return true
	}
	slog.WarnContext(ctx, "wrong test-mode password", "accountId", s.accountID, "ip", ip)
	for _, l := range limits {
		if _, err := h.Limits.Add(ctx, l.kind, l.value, l.window); err != nil {
			slog.ErrorContext(ctx, "count wrong password", "error", err)
		}
	}
	writeError(w, http.StatusForbidden, "wrong test-mode password")
	return false
}
