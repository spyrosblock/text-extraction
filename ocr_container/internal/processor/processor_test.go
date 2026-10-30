package processor

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/spyrosblock/text-extraction/ocr_container/internal/azure"
	"github.com/spyrosblock/text-extraction/ocr_container/internal/contract"
	"github.com/spyrosblock/text-extraction/ocr_container/internal/ocr"
	"github.com/spyrosblock/text-extraction/ocr_container/internal/pdf"
)

const id = "0b9c3c1e-6a53-4d3e-9d51-2f4c8f0a1b2c"

type fakeBlobs struct {
	mu        sync.Mutex
	blobs     map[string][]byte
	uploadErr error
}

func newBlobs(kv ...string) *fakeBlobs {
	b := &fakeBlobs{blobs: map[string][]byte{}}
	for i := 0; i < len(kv); i += 2 {
		b.blobs[kv[i]] = []byte(kv[i+1])
	}
	return b
}

func (b *fakeBlobs) Download(_ context.Context, c, n string) ([]byte, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	d, ok := b.blobs[c+"/"+n]
	if !ok {
		return nil, azure.ErrNotFound
	}
	return d, nil
}

func (b *fakeBlobs) Exists(_ context.Context, c, n string) (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.blobs[c+"/"+n]
	return ok, nil
}

func (b *fakeBlobs) Upload(_ context.Context, c, n string, d []byte, _ string) error {
	if b.uploadErr != nil {
		return b.uploadErr
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.blobs[c+"/"+n] = d
	return nil
}

func (b *fakeBlobs) Delete(_ context.Context, c, n string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.blobs, c+"/"+n)
	return nil
}

// fakeDoc pages: a text layer, or "img:<text>" for a page without one whose
// rendered image OCRs to <text>. "badtext" fails to read its text layer and
// "norender" fails to render.
type fakeDoc struct{ pages []string }

func (d *fakeDoc) PageCount() int { return len(d.pages) }
func (d *fakeDoc) Close()         {}
func (d *fakeDoc) Text(i int) (string, error) {
	switch {
	case d.pages[i] == "badtext":
		return "", errors.New("text layer broken")
	case strings.HasPrefix(d.pages[i], "img:"), d.pages[i] == "norender":
		return "", nil
	}
	return d.pages[i], nil
}
func (d *fakeDoc) Render(i, _ int) ([]byte, error) {
	if d.pages[i] == "norender" {
		return nil, errors.New("render failed")
	}
	return []byte(strings.TrimPrefix(d.pages[i], "img:")), nil
}

type fakeOpener struct{ doc *fakeDoc }

func (o fakeOpener) Open(_ context.Context, data []byte) (Document, error) {
	if string(data) != "pdf" {
		return nil, pdf.ErrInvalidPDF
	}
	return o.doc, nil
}

// fakeOCR returns the image bytes as text. "blurry" has low confidence,
// "fail" errors and "unavailable" fails as if tesseract couldn't start. Like
// Tesseract, it fails once ctx is cancelled.
type fakeOCR struct {
	calls, running, maxRunning atomic.Int32
}

func (o *fakeOCR) Recognize(ctx context.Context, img []byte) (ocr.Result, error) {
	o.calls.Add(1)
	if err := ctx.Err(); err != nil {
		return ocr.Result{}, err
	}
	n := o.running.Add(1)
	defer o.running.Add(-1)
	for {
		m := o.maxRunning.Load()
		if n <= m || o.maxRunning.CompareAndSwap(m, n) {
			break
		}
	}
	switch s := string(img); s {
	case "fail":
		return ocr.Result{}, errors.New("tesseract crashed")
	case "unavailable":
		return ocr.Result{}, ocr.ErrUnavailable
	case "blurry":
		return ocr.Result{Text: "b1urry", Confidence: 20, Words: 1}, nil
	default:
		return ocr.Result{Text: s, Confidence: 90, Words: 1}, nil
	}
}

func newProcessor(blobs *fakeBlobs, pages ...string) (*Processor, *fakeOCR) {
	o := &fakeOCR{}
	return &Processor{
		Blobs:         blobs,
		PDF:           fakeOpener{&fakeDoc{pages}},
		OCR:           o,
		PDFContainer:  "pdf-storage",
		TextContainer: "text-storage",
		Workers:       2,
		DPI:           300,
	}, o
}

var req = contract.OCRRequest{ID: id, Blob: contract.PDFBlobName(id)}

func storedDoc(t *testing.T, b *fakeBlobs) contract.Document {
	t.Helper()
	raw, ok := b.blobs["text-storage/"+contract.TextBlobName(id)]
	if !ok {
		t.Fatal("text not stored")
	}
	var doc contract.Document
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func TestProcess(t *testing.T) {
	blobs := newBlobs("pdf-storage/"+req.Blob, "pdf")
	p, o := newProcessor(blobs, "layer one", "img:scanned two", "img:blurry", "img:scanned four", "img:scanned five")
	if err := p.Process(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	want := []contract.Page{
		page(1, "layer one", contract.TextLayer),
		page(2, "scanned two", 90),
		page(3, "b1urry", 20),
		page(4, "scanned four", 90),
		page(5, "scanned five", 90),
	}
	checkPages(t, storedDoc(t, blobs), want)
	if _, ok := blobs.blobs["pdf-storage/"+req.Blob]; ok {
		t.Error("pdf not deleted")
	}
	if o.calls.Load() != 4 {
		t.Errorf("ocr calls = %d, want 4", o.calls.Load())
	}
	if o.maxRunning.Load() > 2 {
		t.Errorf("max concurrent ocr = %d, want <= 2", o.maxRunning.Load())
	}
}

func page(n int, text string, conf contract.Confidence) contract.Page {
	return contract.Page{Number: n, Text: text, Confidence: conf}
}

func checkPages(t *testing.T, doc contract.Document, want []contract.Page) {
	t.Helper()
	if doc.ID != id || len(doc.Pages) != len(want) {
		t.Fatalf("doc = %+v", doc)
	}
	for i, w := range want {
		if doc.Pages[i] != w {
			t.Errorf("page %d = %+v, want %+v", i+1, doc.Pages[i], w)
		}
	}
}

func TestProcessPageErrors(t *testing.T) {
	blobs := newBlobs("pdf-storage/"+req.Blob, "pdf")
	p, _ := newProcessor(blobs, "img:one", "img:fail", "norender", "badtext", "img:five")
	if err := p.Process(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	want := []contract.Page{
		page(1, "one", 90),
		page(2, "", 0),
		page(3, "", 0),
		// The fake still renders a page whose text layer failed; PDFium
		// usually can't, which leaves it like page 3.
		page(4, "badtext", 90),
		page(5, "five", 90),
	}
	checkPages(t, storedDoc(t, blobs), want)
}

func TestProcessOCRUnavailable(t *testing.T) {
	blobs := newBlobs("pdf-storage/"+req.Blob, "pdf")
	p, _ := newProcessor(blobs, "img:one", "img:unavailable")
	err := p.Process(context.Background(), req)
	if !errors.Is(err, ocr.ErrUnavailable) || errors.Is(err, ErrPermanent) {
		t.Fatalf("err = %v, want transient ErrUnavailable", err)
	}
	if _, ok := blobs.blobs["text-storage/"+contract.TextBlobName(id)]; ok {
		t.Error("text stored although tesseract was unavailable")
	}
	if _, ok := blobs.blobs["pdf-storage/"+req.Blob]; !ok {
		t.Error("pdf deleted although tesseract was unavailable")
	}
}

func TestProcessCancelled(t *testing.T) {
	blobs := newBlobs("pdf-storage/"+req.Blob, "pdf")
	p, _ := newProcessor(blobs, "img:one", "img:two")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := p.Process(ctx, req); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if _, ok := blobs.blobs["text-storage/"+contract.TextBlobName(id)]; ok {
		t.Error("partial text stored after cancellation")
	}
	if _, ok := blobs.blobs["pdf-storage/"+req.Blob]; !ok {
		t.Error("pdf deleted after cancellation")
	}
}

func TestProcessUploadError(t *testing.T) {
	blobs := newBlobs("pdf-storage/"+req.Blob, "pdf")
	blobs.uploadErr = errors.New("503")
	p, _ := newProcessor(blobs, "text")
	if err := p.Process(context.Background(), req); err == nil || errors.Is(err, ErrPermanent) {
		t.Fatalf("err = %v, want transient error", err)
	}
	if _, ok := blobs.blobs["pdf-storage/"+req.Blob]; !ok {
		t.Error("pdf deleted although text was not stored")
	}
}

func TestProcessPermanent(t *testing.T) {
	tests := map[string]struct {
		req   contract.OCRRequest
		blobs *fakeBlobs
	}{
		"invalid id":     {contract.OCRRequest{ID: "../x", Blob: "../x.pdf"}, newBlobs()},
		"uppercase id":   {contract.OCRRequest{ID: strings.ToUpper(id), Blob: strings.ToUpper(id) + ".pdf"}, newBlobs()},
		"blob mismatch":  {contract.OCRRequest{ID: id, Blob: "other.pdf"}, newBlobs("pdf-storage/other.pdf", "pdf")},
		"missing pdf":    {req, newBlobs()},
		"unreadable pdf": {req, newBlobs("pdf-storage/"+req.Blob, "garbage")},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			p, _ := newProcessor(tt.blobs, "text")
			if err := p.Process(context.Background(), tt.req); !errors.Is(err, ErrPermanent) {
				t.Fatalf("err = %v, want ErrPermanent", err)
			}
		})
	}
}

func TestProcessAlreadyDone(t *testing.T) {
	blobs := newBlobs("text-storage/"+contract.TextBlobName(id), "{}")
	p, _ := newProcessor(blobs, "text")
	if err := p.Process(context.Background(), req); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
}
