// Package pdf reads the text layer of PDF pages and renders pages to images
// using PDFium compiled to WebAssembly (pure Go, no cgo).
package pdf

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"

	"github.com/klippa-app/go-pdfium"
	"github.com/klippa-app/go-pdfium/references"
	"github.com/klippa-app/go-pdfium/requests"
	"github.com/klippa-app/go-pdfium/webassembly"
)

// ErrInvalidPDF is returned when the input cannot be opened as a PDF
// (corrupt, not a PDF, password protected, ...).
var ErrInvalidPDF = errors.New("invalid pdf")

// Extractor opens PDFs. It is safe for concurrent use; concurrency is bounded
// by the size of the underlying PDFium pool.
type Extractor struct {
	pool pdfium.Pool
}

// New starts a PDFium pool with up to instances concurrent instances.
func New(instances int) (*Extractor, error) {
	if instances < 1 {
		instances = 1
	}
	pool, err := webassembly.Init(webassembly.Config{
		MinIdle:  1,
		MaxIdle:  instances,
		MaxTotal: instances,
	})
	if err != nil {
		return nil, fmt.Errorf("init pdfium: %w", err)
	}
	return &Extractor{pool: pool}, nil
}

// Close shuts down the PDFium pool.
func (e *Extractor) Close() error { return e.pool.Close() }

// Document is an open PDF. It holds a PDFium instance until closed and is not
// safe for concurrent use.
type Document struct {
	instance pdfium.Pdfium
	doc      references.FPDF_DOCUMENT
	pages    int
}

// Open opens pdf. The caller must Close the returned document.
func (e *Extractor) Open(ctx context.Context, pdf []byte) (*Document, error) {
	instance, err := e.pool.GetInstanceWithContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("get pdfium instance: %w", err)
	}
	doc, err := instance.OpenDocument(&requests.OpenDocument{File: &pdf})
	if err != nil {
		instance.Close()
		return nil, fmt.Errorf("%w: %v", ErrInvalidPDF, err)
	}
	d := &Document{instance: instance, doc: doc.Document}
	count, err := instance.FPDF_GetPageCount(&requests.FPDF_GetPageCount{Document: doc.Document})
	if err != nil {
		d.Close()
		return nil, fmt.Errorf("%w: page count: %v", ErrInvalidPDF, err)
	}
	if count.PageCount == 0 {
		d.Close()
		return nil, fmt.Errorf("%w: no pages", ErrInvalidPDF)
	}
	d.pages = count.PageCount
	return d, nil
}

// PageCount returns the number of pages.
func (d *Document) PageCount() int { return d.pages }

// Text returns the text layer of the page at 0-based index i, empty when the
// page has none.
func (d *Document) Text(i int) (string, error) {
	text, err := d.instance.GetPageText(&requests.GetPageText{Page: d.page(i)})
	if err != nil {
		return "", fmt.Errorf("page %d text: %w", i+1, err)
	}
	return text.Text, nil
}

// Render renders the page at 0-based index i in grayscale at dpi and returns
// it as a binary PGM image, which Tesseract reads without decoding overhead.
func (d *Document) Render(i, dpi int) ([]byte, error) {
	res, err := d.instance.RenderPageInDPI(&requests.RenderPageInDPI{
		Page:        d.page(i),
		DPI:         dpi,
		ImageFormat: requests.RenderImageFormatGrayscale,
	})
	if err != nil {
		return nil, fmt.Errorf("render page %d: %w", i+1, err)
	}
	// The pixel buffer lives in WebAssembly memory until Cleanup.
	defer res.Cleanup()
	img, ok := res.Result.RenderedImage.(*image.Gray)
	if !ok {
		return nil, fmt.Errorf("render page %d: unexpected image type %T", i+1, res.Result.RenderedImage)
	}
	return encodePGM(img), nil
}

// Close closes the document and returns its PDFium instance to the pool.
func (d *Document) Close() {
	d.instance.FPDF_CloseDocument(&requests.FPDF_CloseDocument{Document: d.doc})
	d.instance.Close()
}

func (d *Document) page(i int) requests.Page {
	return requests.Page{ByIndex: &requests.PageByIndex{Document: d.doc, Index: i}}
}

func encodePGM(img *image.Gray) []byte {
	w, h := img.Rect.Dx(), img.Rect.Dy()
	var b bytes.Buffer
	b.Grow(w*h + 32)
	fmt.Fprintf(&b, "P5\n%d %d\n255\n", w, h)
	for y := range h {
		off := y * img.Stride
		b.Write(img.Pix[off : off+w])
	}
	return b.Bytes()
}
