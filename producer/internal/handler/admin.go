package handler

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/spyrosblock/text-extraction/producer/internal/credits"
)

// maxAdminBody bounds admin request bodies.
const maxAdminBody = 1 << 10

// Admin issues API keys and manages their credits. Its endpoints use the
// function app's master key (admin auth level); infra/keys.sh wraps them.
type Admin struct {
	Credits *credits.Store
}

type keyResponse struct {
	Key     string `json:"key"`
	ID      string `json:"key_id"`
	Name    string `json:"name"`
	Credits int64  `json:"credits"`
}

// Keys serves /api/manage/keys:
//
//	POST   {"name","credits"}  create a key; the response is the only time the key is shown
//	GET                        list the keys
//	DELETE ?key_id=<key id>    revoke a key
func (a *Admin) Keys(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	switch r.Method {
	case http.MethodPost:
		var req struct {
			Name    string `json:"name"`
			Credits *int64 `json:"credits"`
		}
		if !decode(w, r, &req) {
			return
		}
		if req.Name == "" || req.Credits == nil || *req.Credits < 0 {
			writeError(w, http.StatusBadRequest, `body must be {"name": "<name>", "credits": <credits ≥ 0>}`)
			return
		}
		key, id, k, err := a.Credits.Create(ctx, req.Name, *req.Credits)
		if err != nil {
			a.fail(w, r, "create key", err)
			return
		}
		slog.InfoContext(ctx, "key created", "keyId", id, "name", k.Name, "credits", k.Credits)
		writeJSON(w, http.StatusCreated, keyResponse{Key: key, ID: id, Name: k.Name, Credits: k.Credits})

	case http.MethodGet:
		keys, err := a.Credits.List(ctx)
		if err != nil {
			a.fail(w, r, "list keys", err)
			return
		}
		writeJSON(w, http.StatusOK, keys)

	case http.MethodDelete:
		id, ok := queryID(w, r)
		if !ok {
			return
		}
		if err := a.Credits.Delete(ctx, id); err != nil {
			a.fail(w, r, "revoke key", err)
			return
		}
		slog.InfoContext(ctx, "key revoked", "keyId", id)
		w.WriteHeader(http.StatusNoContent)

	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// Balance serves /api/manage/credits?key_id=<key id>:
//
//	PUT  {"credits"}  set the balance
//	POST {"delta"}    add to the balance (take from it if negative)
func (a *Admin) Balance(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := queryID(w, r)
	if !ok {
		return
	}
	var (
		k   credits.Key
		err error
	)
	switch r.Method {
	case http.MethodPut:
		var req struct {
			Credits *int64 `json:"credits"`
		}
		if !decode(w, r, &req) {
			return
		}
		if req.Credits == nil || *req.Credits < 0 {
			writeError(w, http.StatusBadRequest, `body must be {"credits": <credits ≥ 0>}`)
			return
		}
		k, err = a.Credits.Set(ctx, id, *req.Credits)
	case http.MethodPost:
		var req struct {
			Delta *int64 `json:"delta"`
		}
		if !decode(w, r, &req) {
			return
		}
		if req.Delta == nil {
			writeError(w, http.StatusBadRequest, `body must be {"delta": <credits>}`)
			return
		}
		k, err = a.Credits.Add(ctx, id, *req.Delta)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if err != nil {
		a.fail(w, r, "update credits", err)
		return
	}
	slog.InfoContext(ctx, "credits updated", "keyId", id, "credits", k.Credits)
	writeJSON(w, http.StatusOK, credits.ListedKey{ID: id, Key: k})
}

func (a *Admin) fail(w http.ResponseWriter, r *http.Request, action string, err error) {
	switch {
	case errors.Is(err, credits.ErrUnknownKey):
		writeError(w, http.StatusNotFound, "no key with this id")
	case errors.Is(err, credits.ErrBusy):
		writeError(w, http.StatusServiceUnavailable, err.Error())
	default:
		slog.ErrorContext(r.Context(), action, "error", err)
		writeError(w, http.StatusInternalServerError, "could not "+action)
	}
}

func queryID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := r.URL.Query().Get("key_id")
	if !credits.ValidID(id) {
		writeError(w, http.StatusBadRequest, "key_id must be a key id (64 lowercase hex characters)")
		return "", false
	}
	return id, true
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAdminBody)).Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return false
	}
	return true
}
