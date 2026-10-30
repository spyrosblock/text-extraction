// Package handler implements the consumer's HTTP endpoint.
package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/knowledge/text-extraction/consumer/internal/azure"
	"github.com/knowledge/text-extraction/consumer/internal/contract"
)

// Retention is how long extracted text is served. The text_storage lifecycle
// policy deletes blobs after a day but only runs once a day, so expired blobs
// can linger; the handler treats them as gone.
const Retention = 24 * time.Hour

type BlobStore interface {
	Open(ctx context.Context, container, name string) (io.ReadCloser, time.Time, error)
}

// maxRequestBytes bounds the request body; a valid one is ~50 bytes.
const maxRequestBytes = 1 << 10

// maxJobBytes bounds a billing job blob; a valid one is ~150 bytes.
const maxJobBytes = 1 << 12

// APIKeyHeader carries the caller's API key.
const APIKeyHeader = "X-API-Key"

type textRequest struct {
	ID string `json:"id"`
}

type errorResponse struct {
	Error string `json:"error"`
}

type Handler struct {
	Blobs            BlobStore
	TextContainer    string
	BillingContainer string
	// Now defaults to time.Now; overridden in tests.
	Now func() time.Time
}

// ServeHTTP returns the contract.Document stored for the id in the JSON
// request body (POST /api/text, {"id": "<uuid>"}), to the API key in
// X-API-Key that submitted it.
//
// 401 when the key is missing or unknown, 400 when the body is not valid JSON
// or the id is not a uuid, 404 when there is no text for it (unknown id,
// another key's job, OCR still running, or expired), 200 with the document
// otherwise.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	keyID, ok, err := h.lookupKey(ctx, r.Header.Get(APIKeyHeader))
	if err != nil {
		slog.ErrorContext(ctx, "look up api key", "error", err)
		writeError(w, http.StatusInternalServerError, "could not check api key")
		return
	}
	if !ok {
		writeError(w, http.StatusUnauthorized, "missing or unknown api key")
		return
	}

	var req textRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBytes))
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, `body must be {"id": "<uuid>"}`)
		return
	}
	id, ok := parseID(req.ID)
	if !ok {
		writeError(w, http.StatusBadRequest, "id must be a uuid")
		return
	}
	log := slog.With("id", id, "keyId", keyID)

	// 404 rather than 403 for another key's job, so ids can't be probed.
	owner, err := h.jobOwner(ctx, id)
	if errors.Is(err, azure.ErrNotFound) || err == nil && owner != keyID {
		writeError(w, http.StatusNotFound, "text does not exist")
		return
	}
	if err != nil {
		log.ErrorContext(ctx, "read job", "error", err)
		writeError(w, http.StatusInternalServerError, "could not read text")
		return
	}

	body, modified, err := h.Blobs.Open(ctx, h.TextContainer, contract.TextBlobName(id))
	if errors.Is(err, azure.ErrNotFound) {
		writeError(w, http.StatusNotFound, "text does not exist")
		return
	}
	if err != nil {
		log.ErrorContext(ctx, "read text", "error", err)
		writeError(w, http.StatusInternalServerError, "could not read text")
		return
	}
	defer body.Close()

	if !modified.IsZero() && h.now().Sub(modified) > Retention {
		writeError(w, http.StatusNotFound, "text does not exist")
		return
	}

	// The blob already is a contract.Document; stream it instead of decoding.
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(w, body); err != nil {
		log.ErrorContext(ctx, "write text", "error", err)
	}
}

// keyID returns the id of an API key: the hex sha256 of the key. It must
// match credits.KeyID in the producer; keep them in sync.
func keyID(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// lookupKey returns the key's id and whether the key exists, i.e. was
// issued by the producer and not revoked. The producer stores each key as
// keys/<id>.json in the billing container.
func (h *Handler) lookupKey(ctx context.Context, key string) (string, bool, error) {
	if key == "" {
		return "", false, nil
	}
	id := keyID(key)
	body, _, err := h.Blobs.Open(ctx, h.BillingContainer, "keys/"+id+".json")
	if errors.Is(err, azure.ErrNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	body.Close()
	return id, true, nil
}

// jobOwner returns the id of the key that submitted job id, from the
// producer's jobs/<id>.json in the billing container.
func (h *Handler) jobOwner(ctx context.Context, id string) (string, error) {
	body, _, err := h.Blobs.Open(ctx, h.BillingContainer, "jobs/"+id+".json")
	if err != nil {
		return "", err
	}
	defer body.Close()
	var job struct {
		KeyID string `json:"keyId"`
	}
	if err := json.NewDecoder(io.LimitReader(body, maxJobBytes)).Decode(&job); err != nil {
		return "", err
	}
	return job.KeyID, nil
}

func (h *Handler) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

// parseID returns the id in canonical lowercase form. Only canonical uuids
// are accepted so the blob name can't be anything the producer didn't write.
func parseID(raw string) (string, bool) {
	u, err := uuid.Parse(raw)
	if err != nil || len(raw) != 36 {
		return "", false
	}
	return u.String(), true
}

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errorResponse{Error: msg})
}
