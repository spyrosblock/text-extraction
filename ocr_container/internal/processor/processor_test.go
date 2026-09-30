package processor

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/knowledge/text-extraction/ocr_container/internal/azure"
	"github.com/knowledge/text-extraction/ocr_container/internal/contract"
	"github.com/knowledge/text-extraction/ocr_container/internal/ocr"
	"github.com/knowledge/text-extraction/ocr_container/internal/pdf"
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
// rendered image OCRs to <text>.
type fakeDoc struct{ pages []string }

func (d *fakeDoc) PageCount() int { return len(d.pages) }
func (d *fakeDoc) Close()         {}
func (d *fakeDoc) Text(i int) (string, error) {
	if strings.HasPrefix(d.pages[i], "img:") {
		return "", nil
	}
	return d.pages[i], nil
}
func (d *fakeDoc) Render(i, _ int) ([]byte, error) {
	return []byte(strings.TrimPrefix(d.pages[i], "img:")), nil
}

type fakeOpener struct{ doc *fakeDoc }

func (o fakeOpener) Open(_ context.Context, data []byte) (Document, error) {
	if string(data) != "pdf" {
		return nil, pdf.ErrInvalidPDF
	}
	return o.doc, nil
}

// fakeOCR returns the image bytes as text. "blurry" has low confidence and
// "fail" errors.
type fakeOCR struct {
	calls, running, maxRunning atomic.Int32
}

func (o *fakeOCR) Recognize(_ context.Context, img []byte) (ocr.Result, error) {
	o.calls.Add(1)
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
		MinConfidence: 60,
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
	doc := storedDoc(t, blobs)
	want := []string{"layer one", "scanned two", "", "scanned four", "scanned five"}
	if doc.ID != id || len(doc.Pages) != len(want) {
		t.Fatalf("doc = %+v", doc)
	}
	for i, w := range want {
		if doc.Pages[i].Number != i+1 || doc.Pages[i].Text != w {
			t.Errorf("page %d = %+v, want %q", i+1, doc.Pages[i], w)
		}
	}
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

func TestProcessOCRError(t *testing.T) {
	blobs := newBlobs("pdf-storage/"+req.Blob, "pdf")
	p, _ := newProcessor(blobs, "img:ok", "img:fail", "img:ok")
	err := p.Process(context.Background(), req)
	if err == nil || errors.Is(err, ErrPermanent) {
		t.Fatalf("err = %v, want transient error", err)
	}
	if _, ok := blobs.blobs["pdf-storage/"+req.Blob]; !ok {
		t.Error("pdf deleted after failure")
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
