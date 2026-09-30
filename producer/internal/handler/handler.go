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

	"github.com/google/uuid"

	"github.com/knowledge/text-extraction/producer/internal/contract"
	"github.com/knowledge/text-extraction/producer/internal/extract"
)

// MaxPDFSize is the largest PDF the producer accepts (90MB).
const MaxPDFSize = 90 << 20

// multipartOverhead is the extra request body allowed for multipart framing
// and other form fields on top of MaxPDFSize.
const multipartOverhead = 1 << 20

// formField is the multipart field that carries the PDF.
const formField = "file"

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
// consumer.
type Response struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

type errorResponse struct {
	Error string `json:"error"`
}

type Handler struct {
	Extractor     Extractor
	Blobs         BlobStore
	Queue         Queue
	PDFContainer  string
	TextContainer string
}

// ServeHTTP accepts a PDF either as a raw application/pdf body or as the
// "file" field of a multipart/form-data body.
//
// If every page has a text layer the text is stored in text_storage right
// away. Otherwise the PDF is stored in pdf_storage and an OCR request is put
// on pdf_queue. Either way the caller gets back an id to query the consumer.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

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
	if err != nil {
		slog.ErrorContext(ctx, "extract text", "error", err)
		writeError(w, http.StatusInternalServerError, "text extraction failed")
		return
	}

	id := uuid.NewString()
	log := slog.With("id", id, "pages", len(pages), "bytes", len(pdf))

	if extract.AllPagesHaveText(pages) {
		if err := h.storeText(ctx, id, pages); err != nil {
			log.ErrorContext(ctx, "store text", "error", err)
			writeError(w, http.StatusInternalServerError, "could not store text")
			return
		}
		log.InfoContext(ctx, "text extracted")
		writeJSON(w, http.StatusAccepted, Response{ID: id, Status: StatusCompleted})
		return
	}

	if err := h.queueOCR(ctx, id, pdf); err != nil {
		log.ErrorContext(ctx, "queue ocr", "error", err)
		writeError(w, http.StatusInternalServerError, "could not queue pdf for ocr")
		return
	}
	log.InfoContext(ctx, "queued for ocr")
	writeJSON(w, http.StatusAccepted, Response{ID: id, Status: StatusQueued})
}

func (h *Handler) storeText(ctx context.Context, id string, pages []contract.Page) error {
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
