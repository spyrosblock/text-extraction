package pdf

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"github.com/knowledge/text-extraction/ocr_container/internal/ocr"
	"github.com/knowledge/text-extraction/ocr_container/internal/pdftest"
)

func open(t *testing.T, data []byte) *Document {
	t.Helper()
	e, err := New(1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	doc, err := e.Open(context.Background(), data)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(doc.Close)
	return doc
}

func TestText(t *testing.T) {
	doc := open(t, pdftest.Build("Hello page one", ""))
	if doc.PageCount() != 2 {
		t.Fatalf("pages = %d, want 2", doc.PageCount())
	}
	if text, _ := doc.Text(0); !strings.Contains(text, "Hello page one") {
		t.Errorf("page 1 text = %q", text)
	}
	if text, _ := doc.Text(1); strings.TrimSpace(text) != "" {
		t.Errorf("page 2 text = %q, want empty", text)
	}
}

func TestRender(t *testing.T) {
	doc := open(t, pdftest.Build("x"))
	img, err := doc.Render(0, 72)
	if err != nil {
		t.Fatal(err)
	}
	// US Letter at 72 DPI.
	header := fmt.Sprintf("P5\n%d %d\n255\n", 612, 792)
	if !bytes.HasPrefix(img, []byte(header)) || len(img) != len(header)+612*792 {
		t.Fatalf("got %d bytes starting %q", len(img), img[:min(len(img), 20)])
	}
}

func TestOpenInvalid(t *testing.T) {
	e, err := New(1)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if _, err := e.Open(context.Background(), []byte("%PDF-1.4 garbage")); !errors.Is(err, ErrInvalidPDF) {
		t.Fatalf("err = %v, want ErrInvalidPDF", err)
	}
}

// TestRenderOCR renders a page and reads it back with Tesseract. Skipped when
// tesseract is not installed (it is in the container image).
func TestRenderOCR(t *testing.T) {
	if _, err := exec.LookPath("tesseract"); err != nil {
		t.Skip("tesseract not installed")
	}
	doc := open(t, pdftest.Build("Hello OCR world"))
	img, err := doc.Render(0, 300)
	if err != nil {
		t.Fatal(err)
	}
	res, err := (&ocr.Tesseract{Languages: "eng+ell"}).Recognize(context.Background(), img)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "Hello OCR world") || res.Confidence < 60 {
		t.Fatalf("got %+v", res)
	}
}
