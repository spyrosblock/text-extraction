package handler

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/spyrosblock/text-extraction/billing"
)

// maxKeyName is the longest key name accepted, in characters.
const maxKeyName = 64

type keyResponse struct {
	ID        string    `json:"key_id"`
	Name      string    `json:"name"`
	Hint      string    `json:"hint"`
	Credits   int64     `json:"credits"`
	CreatedAt time.Time `json:"created_at"`
}

type newKeyResponse struct {
	Key     string `json:"key"`
	ID      string `json:"key_id"`
	Name    string `json:"name"`
	Hint    string `json:"hint"`
	Credits int64  `json:"credits"`
}

// Keys serves /api/keys for the signed-in account:
//
//	GET                       list its keys
//	POST   {"name"}           create a key; the response is the only time the key is shown
//	DELETE ?key_id=<key id>   revoke a key; its credits are lost
func (h *Handler) Keys(w http.ResponseWriter, r *http.Request) {
	h.authed(func(w http.ResponseWriter, r *http.Request, s session) {
		switch r.Method {
		case http.MethodGet:
			h.listKeys(w, r, s)
		case http.MethodPost:
			h.createKey(w, r, s)
		case http.MethodDelete:
			h.revokeKey(w, r, s)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	})(w, r)
}

func (h *Handler) listKeys(w http.ResponseWriter, r *http.Request, s session) {
	keys, err := h.ownedKeys(r.Context(), s.accountID)
	if err != nil {
		unexpected(w, r, "list keys", err)
		return
	}
	writeJSON(w, http.StatusOK, keys)
}

// ownedKeys returns the account's keys, oldest first.
func (h *Handler) ownedKeys(ctx context.Context, accountID string) ([]keyResponse, error) {
	ids, err := h.Accounts.KeyIDs(ctx, accountID)
	if err != nil {
		return nil, err
	}
	keys := []keyResponse{}
	for _, id := range ids {
		k, err := h.Store.Get(ctx, id)
		if errors.Is(err, billing.ErrUnknownKey) { // revoked meanwhile
			continue
		}
		if err != nil {
			return nil, err
		}
		if k.AccountID != accountID {
			continue
		}
		keys = append(keys, keyResponse{ID: id, Name: k.Name, Hint: k.Hint, Credits: k.Credits, CreatedAt: k.CreatedAt})
	}
	slices.SortFunc(keys, func(a, b keyResponse) int { return a.CreatedAt.Compare(b.CreatedAt) })
	return keys, nil
}

// createKey issues a key. The account's email address gets FreeCredits on
// its first key ever, recorded by a marker that outlives revoked keys and
// deleted accounts; later keys start at 0.
func (h *Handler) createKey(w http.ResponseWriter, r *http.Request, s session) {
	ctx := r.Context()
	var req struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &req) {
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" || utf8.RuneCountInString(name) > maxKeyName {
		writeError(w, http.StatusBadRequest, `body must be {"name": "<name, up to 64 characters>"}`)
		return
	}
	if n, err := h.keyCount(ctx, s.accountID); err != nil {
		unexpected(w, r, "create key", err)
		return
	} else if n >= h.MaxKeys {
		writeError(w, http.StatusConflict, "this account already has the maximum number of keys")
		return
	}

	key, err := h.Store.NewKey()
	if err != nil {
		unexpected(w, r, "create key", err)
		return
	}
	id := billing.KeyID(key)
	log := slog.With("accountId", s.accountID, "keyId", id)

	var credits int64
	claimed := false
	if h.FreeCredits > 0 {
		if claimed, err = h.Accounts.ClaimFreeCredits(ctx, s.account.Email, id); err != nil {
			unexpected(w, r, "create key", err)
			return
		}
		if claimed {
			credits = h.FreeCredits
		}
	}

	// Undo whatever was written if a later step fails.
	inserted, indexed := false, false
	rollback := func() {
		ctx := context.WithoutCancel(ctx)
		if indexed {
			if err := h.Accounts.RemoveKey(ctx, s.accountID, id); err != nil {
				log.ErrorContext(ctx, "roll back key index", "error", err)
			}
		}
		if inserted {
			if err := h.Store.Delete(ctx, id); err != nil {
				log.ErrorContext(ctx, "roll back key", "error", err)
			}
		}
		if claimed {
			if err := h.Accounts.ReleaseFreeCredits(ctx, s.account.Email); err != nil {
				log.ErrorContext(ctx, "release free credits", "error", err)
			}
		}
	}

	_, k, err := h.Store.Insert(ctx, key, name, credits, s.accountID)
	if err != nil {
		rollback()
		unexpected(w, r, "create key", err)
		return
	}
	inserted = true
	if err := h.Accounts.AddKey(ctx, s.accountID, id); err != nil {
		rollback()
		unexpected(w, r, "create key", err)
		return
	}
	indexed = true
	// Parallel creates can each pass the first check; recount with this key
	// in the index and back out if that went over the limit.
	if n, err := h.keyCount(ctx, s.accountID); err != nil {
		rollback()
		unexpected(w, r, "create key", err)
		return
	} else if n > h.MaxKeys {
		rollback()
		writeError(w, http.StatusConflict, "this account already has the maximum number of keys")
		return
	}

	log.InfoContext(ctx, "key created", "credits", k.Credits, "freeCredits", claimed)
	writeJSON(w, http.StatusCreated, newKeyResponse{Key: key, ID: id, Name: k.Name, Hint: k.Hint, Credits: k.Credits})
}

// keyCount returns how many keys the account owns. Index entries whose key
// is gone don't count.
func (h *Handler) keyCount(ctx context.Context, accountID string) (int, error) {
	keys, err := h.ownedKeys(ctx, accountID)
	return len(keys), err
}

func (h *Handler) revokeKey(w http.ResponseWriter, r *http.Request, s session) {
	ctx := r.Context()
	id, ok := h.ownedKeyID(w, r, s, r.URL.Query().Get("key_id"))
	if !ok {
		return
	}
	if err := h.Store.Delete(ctx, id); err != nil && !errors.Is(err, billing.ErrUnknownKey) {
		unexpected(w, r, "revoke key", err)
		return
	}
	if err := h.Accounts.RemoveKey(ctx, s.accountID, id); err != nil {
		unexpected(w, r, "revoke key", err)
		return
	}
	slog.InfoContext(ctx, "key revoked", "accountId", s.accountID, "keyId", id)
	w.WriteHeader(http.StatusNoContent)
}

// ownedKeyID checks that the account owns the key: it is in the account's
// index and the key blob names the account. Anything else, including an
// admin-issued key, is 404, so key ids can't be probed.
func (h *Handler) ownedKeyID(w http.ResponseWriter, r *http.Request, s session, id string) (string, bool) {
	if !billing.ValidID(id) {
		writeError(w, http.StatusBadRequest, "key_id must be a key id (64 lowercase hex characters)")
		return "", false
	}
	indexed, err := h.Accounts.HasKey(r.Context(), s.accountID, id)
	if err != nil {
		unexpected(w, r, "find key", err)
		return "", false
	}
	if indexed {
		k, err := h.Store.Get(r.Context(), id)
		if err != nil && !errors.Is(err, billing.ErrUnknownKey) {
			unexpected(w, r, "find key", err)
			return "", false
		}
		if err == nil && k.AccountID == s.accountID {
			return id, true
		}
	}
	writeError(w, http.StatusNotFound, "no key with this id")
	return "", false
}
