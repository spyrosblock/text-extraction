package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/knowledge/text-extraction/producer/internal/contract"
	"github.com/knowledge/text-extraction/producer/internal/extract"
)

var fakePDF = []byte("%PDF-1.7\nfake")

type fakeExtractor struct {
	pages []contract.Page
	err   error
}

func (f fakeExtractor) Extract(context.Context, []byte) ([]contract.Page, error) {
	return f.pages, f.err
}

type fakeBlobs struct {
	uploaded  map[string][]byte // "container/name" -> data
	deleted   []string
	uploadErr error
}

func (f *fakeBlobs) Upload(_ context.Context, container, name string, data []byte, _ string) error {
	if f.uploadErr != nil {
		return f.uploadErr
	}
	if f.uploaded == nil {
		f.uploaded = map[string][]byte{}
	}
	f.uploaded[container+"/"+name] = data
	return nil
}

func (f *fakeBlobs) Delete(_ context.Context, container, name string) error {
	f.deleted = append(f.deleted, container+"/"+name)
	return nil
}

type fakeQueue struct {
	sent []contract.OCRRequest
	err  error
}

func (f *fakeQueue) Send(_ context.Context, req contract.OCRRequest) error {
	if f.err != nil {
		return f.err
	}
	f.sent = append(f.sent, req)
	return nil
}

func newHandler(ex Extractor) (*Handler, *fakeBlobs, *fakeQueue) {
	b, q := &fakeBlobs{}, &fakeQueue{}
	return &Handler{Extractor: ex, Blobs: b, Queue: q, PDFContainer: "pdf-storage", TextContainer: "text-storage"}, b, q
}

func rawRequest(body []byte) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/api/extract", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/pdf")
	return r
}

func multipartRequest(t *testing.T, field string, body []byte) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("note", "ignored")
	fw, err := mw.CreateFormFile(field, "doc.pdf")
	if err != nil {
		t.Fatal(err)
	}
	fw.Write(body)
	mw.Close()
	r := httptest.NewRequest(http.MethodPost, "/api/extract", &buf)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	return r
}

func serve(h *Handler, r *http.Request) (*httptest.ResponseRecorder, Response) {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var resp Response
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	return w, resp
}

var textPages = []contract.Page{{Number: 1, Text: "one"}, {Number: 2, Text: "two"}}

func TestAllPagesHaveTextStoresText(t *testing.T) {
	for name, req := range map[string]func(t *testing.T) *http.Request{
		"raw":       func(*testing.T) *http.Request { return rawRequest(fakePDF) },
		"multipart": func(t *testing.T) *http.Request { return multipartRequest(t, "file", fakePDF) },
	} {
		t.Run(name, func(t *testing.T) {
			h, blobs, queue := newHandler(fakeExtractor{pages: textPages})
			w, resp := serve(h, req(t))

			if w.Code != http.StatusAccepted || resp.Status != StatusCompleted || resp.ID == "" {
				t.Fatalf("got %d %s", w.Code, w.Body)
			}
			stored, ok := blobs.uploaded["text-storage/"+resp.ID+".json"]
			if !ok {
				t.Fatalf("text not stored, uploads: %v", blobs.uploaded)
			}
			var doc contract.Document
			if err := json.Unmarshal(stored, &doc); err != nil {
				t.Fatal(err)
			}
			if doc.ID != resp.ID || len(doc.Pages) != 2 || doc.Pages[1].Text != "two" {
				t.Errorf("stored doc = %+v", doc)
			}
			if len(queue.sent) != 0 || len(blobs.uploaded) != 1 {
				t.Errorf("unexpected ocr path: sent=%v uploads=%d", queue.sent, len(blobs.uploaded))
			}
		})
	}
}

func TestMissingTextQueuesOCR(t *testing.T) {
	h, blobs, queue := newHandler(fakeExtractor{pages: []contract.Page{{Number: 1, Text: "one"}, {Number: 2, Text: " \n\t"}}})
	w, resp := serve(h, rawRequest(fakePDF))

	if w.Code != http.StatusAccepted || resp.Status != StatusQueued {
		t.Fatalf("got %d %s", w.Code, w.Body)
	}
	if got := blobs.uploaded["pdf-storage/"+resp.ID+".pdf"]; !bytes.Equal(got, fakePDF) {
		t.Errorf("pdf not stored, uploads: %v", blobs.uploaded)
	}
	want := contract.OCRRequest{ID: resp.ID, Blob: resp.ID + ".pdf"}
	if len(queue.sent) != 1 || queue.sent[0] != want {
		t.Errorf("sent = %v, want [%v]", queue.sent, want)
	}
}

func TestQueueFailureDeletesPDF(t *testing.T) {
	h, blobs, queue := newHandler(fakeExtractor{pages: []contract.Page{{Number: 1}}})
	queue.err = errors.New("bus down")
	w, _ := serve(h, rawRequest(fakePDF))

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("got %d, want 500", w.Code)
	}
	if len(blobs.deleted) != 1 || !strings.HasPrefix(blobs.deleted[0], "pdf-storage/") {
		t.Errorf("deleted = %v, want the uploaded pdf", blobs.deleted)
	}
}

func TestRejections(t *testing.T) {
	big := append([]byte("%PDF-1.7\n"), make([]byte, MaxPDFSize)...)
	tests := []struct {
		name string
		req  func(t *testing.T) *http.Request
		ex   Extractor
		want int
	}{
		{"too large raw", func(*testing.T) *http.Request { return rawRequest(big) }, nil, http.StatusRequestEntityTooLarge},
		{"too large multipart", func(t *testing.T) *http.Request { return multipartRequest(t, "file", big) }, nil, http.StatusRequestEntityTooLarge},
		{"not a pdf", func(*testing.T) *http.Request { return rawRequest([]byte("hello")) }, nil, http.StatusBadRequest},
		{"missing form field", func(t *testing.T) *http.Request { return multipartRequest(t, "other", fakePDF) }, nil, http.StatusBadRequest},
		{"wrong content type", func(*testing.T) *http.Request {
			r := rawRequest(fakePDF)
			r.Header.Set("Content-Type", "text/plain")
			return r
		}, nil, http.StatusUnsupportedMediaType},
		{"unreadable pdf", func(*testing.T) *http.Request { return rawRequest(fakePDF) },
			fakeExtractor{err: extract.ErrInvalidPDF}, http.StatusUnprocessableEntity},
		{"too many pages", func(*testing.T) *http.Request { return rawRequest(fakePDF) },
			fakeExtractor{err: extract.ErrTooManyPages}, http.StatusRequestEntityTooLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ex := tt.ex
			if ex == nil {
				ex = fakeExtractor{err: errors.New("extractor must not be called")}
			}
			h, blobs, queue := newHandler(ex)
			w, _ := serve(h, tt.req(t))
			if w.Code != tt.want {
				t.Fatalf("got %d %s, want %d", w.Code, w.Body, tt.want)
			}
			if len(blobs.uploaded) != 0 || len(queue.sent) != 0 {
				t.Errorf("side effects on rejection: uploads=%v sent=%v", blobs.uploaded, queue.sent)
			}
		})
	}
}
