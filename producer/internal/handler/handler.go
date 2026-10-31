// Package handler implements the producer's HTTP endpoint.
package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/spyrosblock/text-extraction/billing"
	"github.com/spyrosblock/text-extraction/producer/internal/contract"
	"github.com/spyrosblock/text-extraction/producer/internal/extract"
)

// MaxPDFSize is the largest PDF the producer accepts (90MB).
const MaxPDFSize = 90 << 20

// multipartOverhead is the extra request body allowed for multipart framing
// and other form fields on top of MaxPDFSize.
const multipartOverhead = 1 << 20

// formField is the multipart field that carries the PDF.
const formField = "file"

// APIKeyHeader carries the caller's API key.
const APIKeyHeader = "X-API-Key"

const (
	StatusCompleted = "completed"
	StatusQueued    = "queued"
)

type Extractor interface {
	Extract(ctx context.Context, pdf []byte) ([]contract.Page, error)
}

type BlobStore interface {
	Upload(ctx context.Context, container, name string, data []byte, contentType string) error
	Delete(ctx context.Context, container, name string) error
}

type Queue interface {
	Send(ctx context.Context, req contract.OCRRequest) error
}

// Response is returned to the caller. ID is used to fetch the text from the
// consumer. Credits were charged for the PDF; Balance is what the key has
// left.
type Response struct {
	ID      string `json:"request_id"`
	Status  string `json:"status"`
	Credits int64  `json:"credits"`
	Balance int64  `json:"balance"`
}

type errorResponse struct {
	Error string `json:"error"`
}

// creditsResponse is the 402 body. Required is only known once the PDF has
// been read.
type creditsResponse struct {
	Error    string `json:"error"`
	Required int64  `json:"required,omitempty"`
	Balance  int64  `json:"balance"`
}

type Handler struct {
	Extractor     Extractor
	Blobs         BlobStore
	Queue         Queue
	Credits       *billing.Store
	PDFContainer  string
	TextContainer string
}

// ServeHTTP accepts a PDF either as a raw application/pdf body or as the
// "file" field of a multipart/form-data body, with an API key in X-API-Key.
//
// The PDF's cost is charged to the key: a page with a text layer costs
// TextPageCredits, a page that needs OCR OCRPageCredits. If every page has a
// text layer the text is stored in text_storage right away. Otherwise the PDF
// is stored in pdf_storage and an OCR request is put on pdf_queue. Either way
// the caller gets back an id to query the consumer.
//
// Credits are not returned if storing or queueing fails after the charge.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Reject unknown and empty keys before reading the PDF. Nothing is
	// charged yet: the cost depends on the pages.
	keyID, key, err := h.Credits.Lookup(ctx, r.Header.Get(APIKeyHeader))
	if errors.Is(err, billing.ErrUnknownKey) {
		writeError(w, http.StatusUnauthorized, "missing or unknown api key")
		return
	}
	if err != nil {
		slog.ErrorContext(ctx, "look up api key", "error", err)
		writeError(w, http.StatusInternalServerError, "could not check api key")
		return
	}
	if key.Credits <= 0 {
		writeJSON(w, http.StatusPaymentRequired, creditsResponse{Error: "no credits left", Balance: key.Credits})
		return
	}

	pdf, status, err := readPDF(w, r)
	if err != nil {
		writeError(w, status, err.Error())
		return
	}

	pages, err := h.Extractor.Extract(ctx, pdf)
	if errors.Is(err, extract.ErrInvalidPDF) {
		writeError(w, http.StatusUnprocessableEntity, "could not read pdf")
		return
	}
	if errors.Is(err, extract.ErrTooManyPages) {
		writeError(w, http.StatusRequestEntityTooLarge, err.Error())
		return
	}
	if err != nil {
		slog.ErrorContext(ctx, "extract text", "error", err)
		writeError(w, http.StatusInternalServerError, "text extraction failed")
		return
	}

	id := uuid.NewString()
	ocrPages := extract.PagesWithoutText(pages)
	cost := h.Credits.Cost(len(pages), ocrPages)
	log := slog.With("id", id, "keyId", keyID, "pages", len(pages), "ocrPages", ocrPages, "bytes", len(pdf))

	balance, err := h.Credits.Charge(ctx, keyID, cost)
	var short *billing.InsufficientError
	switch {
	case errors.As(err, &short):
		writeJSON(w, http.StatusPaymentRequired, creditsResponse{Error: "not enough credits", Required: short.Required, Balance: short.Balance})
		return
	case errors.Is(err, billing.ErrUnknownKey): // revoked meanwhile
		writeError(w, http.StatusUnauthorized, "missing or unknown api key")
		return
	case errors.Is(err, billing.ErrBusy):
		writeError(w, http.StatusServiceUnavailable, "too many concurrent requests for this api key, retry")
		return
	case err != nil:
		log.ErrorContext(ctx, "charge credits", "error", err)
		writeError(w, http.StatusInternalServerError, "could not charge credits")
		return
	}
	// Usage history: one line per charged job, queryable in App Insights.
	log.InfoContext(ctx, "charged", "credits", cost, "balance", balance)

	job := billing.Job{KeyID: keyID, Pages: len(pages), OCRPages: ocrPages, Credits: cost, CreatedAt: time.Now().UTC()}
	if err := h.Credits.RecordJob(ctx, id, job); err != nil {
		log.ErrorContext(ctx, "record job", "error", err)
		writeError(w, http.StatusInternalServerError, "could not record job")
		return
	}

	if ocrPages == 0 {
		if err := h.storeText(ctx, id, pages); err != nil {
			log.ErrorContext(ctx, "store text", "error", err)
			writeError(w, http.StatusInternalServerError, "could not store text")
			return
		}
		log.InfoContext(ctx, "text extracted")
		writeJSON(w, http.StatusAccepted, Response{ID: id, Status: StatusCompleted, Credits: cost, Balance: balance})
		return
	}

	if err := h.queueOCR(ctx, id, pdf); err != nil {
		log.ErrorContext(ctx, "queue ocr", "error", err)
		writeError(w, http.StatusInternalServerError, "could not queue pdf for ocr")
		return
	}
	log.InfoContext(ctx, "queued for ocr")
	writeJSON(w, http.StatusAccepted, Response{ID: id, Status: StatusQueued, Credits: cost, Balance: balance})
}

// storeText stores pages that all came from the text layer.
func (h *Handler) storeText(ctx context.Context, id string, pages []contract.Page) error {
	for i := range pages {
		pages[i].Confidence = contract.TextLayer
	}
	body, err := json.Marshal(contract.Document{ID: id, Pages: pages})
	if err != nil {
		return err
	}
	return h.Blobs.Upload(ctx, h.TextContainer, contract.TextBlobName(id), body, "application/json")
}

// queueOCR uploads the PDF before sending the message so the ocr_container
// never sees a message for a missing blob. If the send fails the blob is
// removed again.
func (h *Handler) queueOCR(ctx context.Context, id string, pdf []byte) error {
	name := contract.PDFBlobName(id)
	if err := h.Blobs.Upload(ctx, h.PDFContainer, name, pdf, "application/pdf"); err != nil {
		return err
	}
	if err := h.Queue.Send(ctx, contract.OCRRequest{ID: id, Blob: name}); err != nil {
		if derr := h.Blobs.Delete(context.WithoutCancel(ctx), h.PDFContainer, name); derr != nil {
			slog.ErrorContext(ctx, "delete orphaned pdf", "id", id, "error", derr)
		}
		return err
	}
	return nil
}

// readPDF reads the PDF from the request body. On failure it returns the HTTP
// status to respond with.
func readPDF(w http.ResponseWriter, r *http.Request) ([]byte, int, error) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		return nil, http.StatusUnsupportedMediaType, errors.New("missing or invalid Content-Type")
	}

	var pdf []byte
	switch mediaType {
	case "application/pdf", "application/octet-stream":
		r.Body = http.MaxBytesReader(w, r.Body, MaxPDFSize)
		pdf, err = io.ReadAll(r.Body)
	case "multipart/form-data":
		r.Body = http.MaxBytesReader(w, r.Body, MaxPDFSize+multipartOverhead)
		pdf, err = readMultipartPDF(r)
	default:
		return nil, http.StatusUnsupportedMediaType, fmt.Errorf("unsupported Content-Type %q", mediaType)
	}

	var tooLarge *http.MaxBytesError
	switch {
	case errors.As(err, &tooLarge), errors.Is(err, errTooLarge):
		return nil, http.StatusRequestEntityTooLarge, fmt.Errorf("pdf exceeds %dMB", MaxPDFSize>>20)
	case err != nil:
		return nil, http.StatusBadRequest, err
	case !looksLikePDF(pdf):
		return nil, http.StatusBadRequest, errors.New("body is not a pdf")
	}
	return pdf, 0, nil
}

var errTooLarge = errors.New("pdf too large")

func readMultipartPDF(r *http.Request) ([]byte, error) {
	mr, err := r.MultipartReader()
	if err != nil {
		return nil, fmt.Errorf("invalid multipart body: %w", err)
	}
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			return nil, fmt.Errorf("missing %q form field", formField)
		}
		if err != nil {
			return nil, fmt.Errorf("invalid multipart body: %w", err)
		}
		if part.FormName() != formField {
			part.Close()
			continue
		}
		defer part.Close()
		pdf, err := io.ReadAll(io.LimitReader(part, MaxPDFSize+1))
		if err != nil {
			return nil, err
		}
		if len(pdf) > MaxPDFSize {
			return nil, errTooLarge
		}
		return pdf, nil
	}
}

// looksLikePDF checks for the %PDF- header, which the PDF spec allows to
// appear anywhere in the first 1024 bytes.
func looksLikePDF(b []byte) bool {
	return bytes.Contains(b[:min(len(b), 1024)], []byte("%PDF-"))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, errorResponse{Error: msg})
}
