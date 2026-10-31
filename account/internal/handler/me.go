package handler

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/spyrosblock/text-extraction/billing"
)

type prices struct {
	TextPage int64 `json:"text_page"`
	OCRPage  int64 `json:"ocr_page"`
}

type meResponse struct {
	Email                string          `json:"email"`
	Payments             string          `json:"payments"`
	Prices               prices          `json:"prices"`
	Packs                map[string]Pack `json:"packs"`
	MaxKeys              int             `json:"max_keys"`
	FreeCreditsAvailable int64           `json:"free_credits_available"`
}

// Me serves /api/me for the signed-in account:
//
//	GET     the account, prices and packs, and the free credits its next key gets
//	DELETE  delete the account and revoke its keys
func (h *Handler) Me(w http.ResponseWriter, r *http.Request) {
	h.authed(func(w http.ResponseWriter, r *http.Request, s session) {
		switch r.Method {
		case http.MethodGet:
			h.getMe(w, r, s)
		case http.MethodDelete:
			h.deleteMe(w, r, s)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	})(w, r)
}

func (h *Handler) getMe(w http.ResponseWriter, r *http.Request, s session) {
	var free int64
	if h.FreeCredits > 0 {
		had, err := h.Accounts.HadFreeCredits(r.Context(), s.account.Email)
		if err != nil {
			unexpected(w, r, "read account", err)
			return
		}
		if !had {
			free = h.FreeCredits
		}
	}
	writeJSON(w, http.StatusOK, meResponse{
		Email:                s.account.Email,
		Payments:             h.PaymentsMode,
		Prices:               prices{TextPage: h.Store.TextPageCredits, OCRPage: h.Store.OCRPageCredits},
		Packs:                h.Packs,
		MaxKeys:              h.MaxKeys,
		FreeCreditsAvailable: free,
	})
}

// deleteMe revokes the account's keys, then deletes the account, its key
// index and its email blob, and ends the session. The free-credit marker
// is kept, so signing up again with the address doesn't grant them again.
func (h *Handler) deleteMe(w http.ResponseWriter, r *http.Request, s session) {
	ctx := r.Context()
	if err := h.revokeAll(ctx, s.accountID); err != nil {
		unexpected(w, r, "delete account", err)
		return
	}
	if err := h.Accounts.Delete(ctx, s.accountID); err != nil && !errors.Is(err, billing.ErrUnknownAccount) {
		unexpected(w, r, "delete account", err)
		return
	}
	if err := h.Sessions.Delete(ctx, s.token); err != nil {
		// The session is useless without its account.
		slog.ErrorContext(ctx, "delete session", "error", err)
	}
	slog.InfoContext(ctx, "account deleted", "accountId", s.accountID)
	w.WriteHeader(http.StatusNoContent)
}

// revokeAll deletes every key in the account's index that the account owns.
func (h *Handler) revokeAll(ctx context.Context, accountID string) error {
	keys, err := h.ownedKeys(ctx, accountID)
	if err != nil {
		return err
	}
	for _, k := range keys {
		if err := h.Store.Delete(ctx, k.ID); err != nil && !errors.Is(err, billing.ErrUnknownKey) {
			return err
		}
		slog.InfoContext(ctx, "key revoked", "accountId", accountID, "keyId", k.ID)
	}
	return nil
}
