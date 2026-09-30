// Package handler implements the consumer's HTTP endpoint.
package handler

import (
	"context"
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

type textRequest struct {
	ID string `json:"id"`
}

type errorResponse struct {
	Error string `json:"error"`
}

type Handler struct {
	Blobs         BlobStore
	TextContainer string
	// Now defaults to time.Now; overridden in tests.
	Now func() time.Time
}

// ServeHTTP returns the contract.Document stored for the id in the JSON
// request body (POST /api/text, {"id": "<uuid>"}).
//
// 400 when the body is not valid JSON or the id is not a uuid, 404 when there is no text for it (unknown
// id, OCR still running, or expired), 200 with the document otherwise.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

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
	log := slog.With("id", id)

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
