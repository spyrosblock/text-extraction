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

	"github.com/knowledge/text-extraction/producer/internal/blobtest"
	"github.com/knowledge/text-extraction/producer/internal/contract"
	"github.com/knowledge/text-extraction/producer/internal/credits"
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

// startCredits is the balance of apiKey in newHandler.
const startCredits = 100

// apiKey is valid in every handler from newHandler.
var apiKey string

func newStore(b *blobtest.Fake) *credits.Store {
	return &credits.Store{Blobs: b, Container: "billing", KeyPrefix: "tx_test_", TextPageCredits: 1, OCRPageCredits: 5}
}

func newHandler(t *testing.T, ex Extractor) (*Handler, *blobtest.Fake, *fakeQueue) {
	t.Helper()
	b, q := &blobtest.Fake{}, &fakeQueue{}
	store := newStore(b)
	key, _, _, err := store.Create(context.Background(), "test", startCredits)
	if err != nil {
		t.Fatal(err)
	}
	apiKey = key
	return &Handler{Extractor: ex, Blobs: b, Queue: q, Credits: store, PDFContainer: "pdf-storage", TextContainer: "text-storage"}, b, q
}

func balance(t *testing.T, h *Handler) int64 {
	t.Helper()
	_, k, err := h.Credits.Lookup(context.Background(), apiKey)
	if err != nil {
		t.Fatal(err)
	}
	return k.Credits
}

// uploads returns the blobs written outside the billing container.
func uploads(b *blobtest.Fake) []string {
	var names []string
	for _, n := range b.Names() {
		if !strings.HasPrefix(n, "billing/") {
			names = append(names, n)
		}
	}
	return names
}

func rawRequest(body []byte) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/api/extract", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/pdf")
	r.Header.Set(APIKeyHeader, apiKey)
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
	r.Header.Set(APIKeyHeader, apiKey)
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

func TestTextLayerStoresText(t *testing.T) {
	for name, req := range map[string]func(t *testing.T) *http.Request{
		"raw":       func(*testing.T) *http.Request { return rawRequest(fakePDF) },
		"multipart": func(t *testing.T) *http.Request { return multipartRequest(t, "file", fakePDF) },
	} {
		t.Run(name, func(t *testing.T) {
			h, blobs, queue := newHandler(t, fakeExtractor{pages: textPages})
			w, resp := serve(h, req(t))

			if w.Code != http.StatusAccepted || resp.Status != StatusCompleted || resp.ID == "" {
				t.Fatalf("got %d %s", w.Code, w.Body)
			}
			if resp.Credits != 2 || resp.Balance != startCredits-2 || balance(t, h) != startCredits-2 {
				t.Errorf("credits = %d, balance = %d, want 2 charged", resp.Credits, resp.Balance)
			}
			stored := blobs.Data("text-storage", resp.ID+".json")
			if stored == nil {
				t.Fatalf("text not stored, blobs: %v", blobs.Names())
			}
			var doc contract.Document
			if err := json.Unmarshal(stored, &doc); err != nil {
				t.Fatal(err)
			}
			if doc.ID != resp.ID || len(doc.Pages) != 2 || doc.Pages[1].Text != "two" || doc.Pages[0].Confidence != contract.TextLayer || doc.Pages[1].Confidence != contract.TextLayer {
				t.Errorf("stored doc = %+v", doc)
			}
			if len(queue.sent) != 0 || len(uploads(blobs)) != 1 {
				t.Errorf("unexpected ocr path: sent=%v uploads=%v", queue.sent, uploads(blobs))
			}
		})
	}
}

func TestMissingTextQueuesOCR(t *testing.T) {
	h, blobs, queue := newHandler(t, fakeExtractor{pages: []contract.Page{{Number: 1, Text: "one"}, {Number: 2, Text: " \n\t"}}})
	w, resp := serve(h, rawRequest(fakePDF))

	if w.Code != http.StatusAccepted || resp.Status != StatusQueued {
		t.Fatalf("got %d %s", w.Code, w.Body)
	}
	if want := int64(1 + 5); resp.Credits != want || resp.Balance != startCredits-want {
		t.Errorf("credits = %d, balance = %d, want %d charged", resp.Credits, resp.Balance, want)
	}
	if got := blobs.Data("pdf-storage", resp.ID+".pdf"); !bytes.Equal(got, fakePDF) {
		t.Errorf("pdf not stored, blobs: %v", blobs.Names())
	}
	want := contract.OCRRequest{ID: resp.ID, Blob: resp.ID + ".pdf"}
	if len(queue.sent) != 1 || queue.sent[0] != want {
		t.Errorf("sent = %v, want [%v]", queue.sent, want)
	}
}

func TestQueueFailureDeletesPDF(t *testing.T) {
	h, blobs, queue := newHandler(t, fakeExtractor{pages: []contract.Page{{Number: 1}}})
	queue.err = errors.New("bus down")
	w, _ := serve(h, rawRequest(fakePDF))

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("got %d, want 500", w.Code)
	}
	if len(blobs.Deleted) != 1 || !strings.HasPrefix(blobs.Deleted[0], "pdf-storage/") {
		t.Errorf("deleted = %v, want the uploaded pdf", blobs.Deleted)
	}
	if got := balance(t, h); got != startCredits-5 {
		t.Errorf("balance = %d, want the charge kept", got)
	}
}

func TestUploadFailureKeepsCharge(t *testing.T) {
	for name, pages := range map[string][]contract.Page{
		"text": textPages,
		"ocr":  {{Number: 1}},
	} {
		t.Run(name, func(t *testing.T) {
			h, blobs, queue := newHandler(t, fakeExtractor{pages: pages})
			blobs.UploadErr = map[string]error{"text-storage": errors.New("down"), "pdf-storage": errors.New("down")}
			w, _ := serve(h, rawRequest(fakePDF))

			if w.Code != http.StatusInternalServerError {
				t.Fatalf("got %d, want 500", w.Code)
			}
			if got, want := balance(t, h), startCredits-h.Credits.Cost(len(pages), extract.PagesWithoutText(pages)); got != want {
				t.Errorf("balance = %d, want %d", got, want)
			}
			if len(queue.sent) != 0 {
				t.Errorf("sent = %v", queue.sent)
			}
		})
	}
}

func TestRecordsJob(t *testing.T) {
	h, blobs, _ := newHandler(t, fakeExtractor{pages: []contract.Page{{Number: 1, Text: "one"}, {Number: 2}}})
	w, resp := serve(h, rawRequest(fakePDF))
	if w.Code != http.StatusAccepted {
		t.Fatalf("got %d %s", w.Code, w.Body)
	}
	var job credits.Job
	if err := json.Unmarshal(blobs.Data("billing", credits.JobBlob(resp.ID)), &job); err != nil {
		t.Fatalf("job blob: %v, blobs: %v", err, blobs.Names())
	}
	if job.KeyID != credits.KeyID(apiKey) || job.Pages != 2 || job.OCRPages != 1 || job.Credits != 6 || job.CreatedAt.IsZero() {
		t.Errorf("job = %+v", job)
	}
}

func TestAPIKeyAndCredits(t *testing.T) {
	tests := []struct {
		name    string
		key     func(h *Handler) string
		balance int64
		pages   int
		want    int
	}{
		{"no key", func(*Handler) string { return "" }, startCredits, 1, http.StatusUnauthorized},
		{"unknown key", func(*Handler) string { return apiKey + "x" }, startCredits, 1, http.StatusUnauthorized},
		{"no credits", func(*Handler) string { return apiKey }, 0, 1, http.StatusPaymentRequired},
		{"negative credits", func(*Handler) string { return apiKey }, -3, 1, http.StatusPaymentRequired},
		{"not enough credits", func(*Handler) string { return apiKey }, 2, 3, http.StatusPaymentRequired},
		{"exactly enough", func(*Handler) string { return apiKey }, 3, 3, http.StatusAccepted},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pages := make([]contract.Page, tt.pages)
			for i := range pages {
				pages[i] = contract.Page{Number: i + 1, Text: "x"}
			}
			h, blobs, queue := newHandler(t, fakeExtractor{pages: pages})
			if _, err := h.Credits.Set(context.Background(), credits.KeyID(apiKey), tt.balance); err != nil {
				t.Fatal(err)
			}
			r := rawRequest(fakePDF)
			r.Header.Set(APIKeyHeader, tt.key(h))
			w, _ := serve(h, r)
			if w.Code != tt.want {
				t.Fatalf("got %d %s, want %d", w.Code, w.Body, tt.want)
			}
			if tt.want == http.StatusAccepted {
				return
			}
			if got := balance(t, h); got != tt.balance {
				t.Errorf("balance = %d, want %d unchanged", got, tt.balance)
			}
			if len(uploads(blobs)) != 0 || len(queue.sent) != 0 {
				t.Errorf("side effects: uploads=%v sent=%v", uploads(blobs), queue.sent)
			}
			if tt.want == http.StatusPaymentRequired {
				var body creditsResponse
				json.Unmarshal(w.Body.Bytes(), &body)
				if body.Balance != tt.balance || tt.balance > 0 && body.Required != int64(tt.pages) {
					t.Errorf("402 body = %s", w.Body)
				}
			}
		})
	}
}

// The key and balance are checked before the body is read or the PDF parsed.
func TestRejectsBeforeReadingBody(t *testing.T) {
	for name, balance := range map[string]int64{"unknown key": startCredits, "no credits": 0} {
		t.Run(name, func(t *testing.T) {
			h, _, _ := newHandler(t, fakeExtractor{err: errors.New("extractor must not be called")})
			if _, err := h.Credits.Set(context.Background(), credits.KeyID(apiKey), balance); err != nil {
				t.Fatal(err)
			}
			body := &trackingReader{r: bytes.NewReader(fakePDF)}
			r := httptest.NewRequest(http.MethodPost, "/api/extract", body)
			r.Header.Set("Content-Type", "application/pdf")
			if name == "unknown key" {
				r.Header.Set(APIKeyHeader, "tx_test_unknown")
			} else {
				r.Header.Set(APIKeyHeader, apiKey)
			}
			w, _ := serve(h, r)
			if w.Code != http.StatusUnauthorized && w.Code != http.StatusPaymentRequired {
				t.Fatalf("got %d %s", w.Code, w.Body)
			}
			if body.read {
				t.Error("body was read")
			}
		})
	}
}

type trackingReader struct {
	r    *bytes.Reader
	read bool
}

func (t *trackingReader) Read(p []byte) (int, error) {
	t.read = true
	return t.r.Read(p)
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
			h, blobs, queue := newHandler(t, ex)
			w, _ := serve(h, tt.req(t))
			if w.Code != tt.want {
				t.Fatalf("got %d %s, want %d", w.Code, w.Body, tt.want)
			}
			if len(uploads(blobs)) != 0 || len(queue.sent) != 0 || balance(t, h) != startCredits {
				t.Errorf("side effects on rejection: uploads=%v sent=%v balance=%d", uploads(blobs), queue.sent, balance(t, h))
			}
		})
	}
}
