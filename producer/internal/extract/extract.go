// Package extract reads the text layer of a PDF using PDFium compiled to
// WebAssembly (pure Go, no cgo).
package extract

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/klippa-app/go-pdfium"
	"github.com/klippa-app/go-pdfium/requests"
	"github.com/klippa-app/go-pdfium/webassembly"

	"github.com/knowledge/text-extraction/producer/internal/contract"
)

// ErrInvalidPDF is returned when the input cannot be opened as a PDF
// (corrupt, not a PDF, password protected, ...).
var ErrInvalidPDF = errors.New("invalid pdf")

// Extractor extracts per-page text from PDFs. It is safe for concurrent use;
// concurrency is bounded by the size of the underlying PDFium pool.
type Extractor struct {
	pool pdfium.Pool
}

// New starts a PDFium pool with up to workers concurrent instances.
func New(workers int) (*Extractor, error) {
	if workers < 1 {
		workers = 1
	}
	pool, err := webassembly.Init(webassembly.Config{
		MinIdle:  1,
		MaxIdle:  workers,
		MaxTotal: workers,
	})
	if err != nil {
		return nil, fmt.Errorf("init pdfium: %w", err)
	}
	return &Extractor{pool: pool}, nil
}

// Close shuts down the PDFium pool.
func (e *Extractor) Close() error { return e.pool.Close() }

// Extract returns the text layer of every page of pdf, in page order.
// Pages without a text layer are returned with an empty Text.
func (e *Extractor) Extract(ctx context.Context, pdf []byte) ([]contract.Page, error) {
	instance, err := e.pool.GetInstanceWithContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("get pdfium instance: %w", err)
	}
	defer instance.Close()

	doc, err := instance.OpenDocument(&requests.OpenDocument{File: &pdf})
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidPDF, err)
	}
	defer instance.FPDF_CloseDocument(&requests.FPDF_CloseDocument{Document: doc.Document})

	count, err := instance.FPDF_GetPageCount(&requests.FPDF_GetPageCount{Document: doc.Document})
	if err != nil {
		return nil, fmt.Errorf("%w: page count: %v", ErrInvalidPDF, err)
	}
	if count.PageCount == 0 {
		return nil, fmt.Errorf("%w: no pages", ErrInvalidPDF)
	}

	pages := make([]contract.Page, 0, count.PageCount)
	for i := range count.PageCount {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		text, err := instance.GetPageText(&requests.GetPageText{
			Page: requests.Page{ByIndex: &requests.PageByIndex{Document: doc.Document, Index: i}},
		})
		if err != nil {
			return nil, fmt.Errorf("page %d text: %w", i+1, err)
		}
		pages = append(pages, contract.Page{Number: i + 1, Text: text.Text})
	}
	return pages, nil
}

// AllPagesHaveText reports whether every page has non-whitespace text.
func AllPagesHaveText(pages []contract.Page) bool {
	for _, p := range pages {
		if strings.TrimSpace(p.Text) == "" {
			return false
		}
	}
	return true
}
