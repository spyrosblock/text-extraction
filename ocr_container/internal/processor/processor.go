// Package processor turns one OCR request into a text document: it reads the
// PDF from pdf_storage, takes each page's text layer when present and runs
// OCR on the others in parallel, stores the result in text_storage and
// deletes the PDF.
package processor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"
	"golang.org/x/sync/errgroup"

	"github.com/knowledge/text-extraction/ocr_container/internal/azure"
	"github.com/knowledge/text-extraction/ocr_container/internal/contract"
	"github.com/knowledge/text-extraction/ocr_container/internal/ocr"
	"github.com/knowledge/text-extraction/ocr_container/internal/pdf"
)

// ErrPermanent marks failures that retrying won't fix (bad message, missing
// or unreadable PDF). The job dead-letters those messages instead of
// retrying them.
var ErrPermanent = errors.New("permanent failure")

type BlobStore interface {
	Download(ctx context.Context, container, name string) ([]byte, error)
	Exists(ctx context.Context, container, name string) (bool, error)
	Upload(ctx context.Context, container, name string, data []byte, contentType string) error
	Delete(ctx context.Context, container, name string) error
}

// Document is an open PDF. It is only used from one goroutine at a time.
type Document interface {
	PageCount() int
	Text(i int) (string, error)
	Render(i, dpi int) ([]byte, error)
	Close()
}

type Opener interface {
	Open(ctx context.Context, pdf []byte) (Document, error)
}

type OCR interface {
	Recognize(ctx context.Context, img []byte) (ocr.Result, error)
}

type Processor struct {
	Blobs         BlobStore
	PDF           Opener
	OCR           OCR
	PDFContainer  string
	TextContainer string
	// Workers is the number of pages OCRed at once.
	Workers int
	// DPI pages are rendered at before OCR.
	DPI int
	// MinConfidence is the mean word confidence (0-100) below which an OCRed
	// page is considered unreadable and left empty.
	MinConfidence float64
}

// Process handles one OCR request. It is idempotent: the text is uploaded
// before the PDF is deleted, and a request whose PDF is gone but whose text
// exists is treated as already done.
func (p *Processor) Process(ctx context.Context, req contract.OCRRequest) error {
	if err := validate(req); err != nil {
		return err
	}
	log := slog.With("id", req.ID)

	data, err := p.Blobs.Download(ctx, p.PDFContainer, req.Blob)
	if errors.Is(err, azure.ErrNotFound) {
		done, err := p.Blobs.Exists(ctx, p.TextContainer, contract.TextBlobName(req.ID))
		if err != nil {
			return err
		}
		if done {
			log.InfoContext(ctx, "already processed")
			return nil
		}
		return fmt.Errorf("%w: pdf %s not found", ErrPermanent, req.Blob)
	}
	if err != nil {
		return err
	}

	pages, stats, err := p.extract(ctx, data)
	if err != nil {
		return err
	}

	body, err := json.Marshal(contract.Document{ID: req.ID, Pages: pages})
	if err != nil {
		return err
	}
	if err := p.Blobs.Upload(ctx, p.TextContainer, contract.TextBlobName(req.ID), body, "application/json"); err != nil {
		return err
	}
	if err := p.Blobs.Delete(ctx, p.PDFContainer, req.Blob); err != nil {
		// The text is stored; a leftover PDF is only wasted space.
		log.WarnContext(ctx, "delete pdf", "error", err)
	}
	log.InfoContext(ctx, "text extracted",
		"pages", len(pages), "text_layer", stats.textLayer, "ocr", stats.ocr, "skipped", stats.skipped)
	return nil
}

type stats struct{ textLayer, ocr, skipped int }

type ocrJob struct {
	index int
	img   []byte
}

// extract returns the text of every page. Pages with a text layer use it;
// the rest are rendered one at a time (a PDFium document is single threaded)
// and handed to Workers goroutines running Tesseract.
func (p *Processor) extract(ctx context.Context, data []byte) ([]contract.Page, stats, error) {
	doc, err := p.PDF.Open(ctx, data)
	if errors.Is(err, pdf.ErrInvalidPDF) {
		return nil, stats{}, fmt.Errorf("%w: %v", ErrPermanent, err)
	}
	if err != nil {
		return nil, stats{}, err
	}
	defer doc.Close()

	n := doc.PageCount()
	pages := make([]contract.Page, n)
	var needOCR []int
	for i := range n {
		text, err := doc.Text(i)
		if err != nil {
			return nil, stats{}, err
		}
		pages[i] = contract.Page{Number: i + 1, Text: text}
		if strings.TrimSpace(text) == "" {
			needOCR = append(needOCR, i)
		}
	}
	st := stats{textLayer: n - len(needOCR)}
	if len(needOCR) == 0 {
		return pages, st, nil
	}

	workers := min(max(p.Workers, 1), len(needOCR))
	g, ctx := errgroup.WithContext(ctx)
	// Unbuffered so at most one rendered page waits on top of the ones being
	// OCRed; a 300 DPI A4 page is ~9MB.
	jobs := make(chan ocrJob)
	results := make([]ocr.Result, n)

	g.Go(func() error {
		defer close(jobs)
		for _, i := range needOCR {
			img, err := doc.Render(i, p.DPI)
			if err != nil {
				return err
			}
			select {
			case jobs <- ocrJob{i, img}:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	})
	for range workers {
		g.Go(func() error {
			for job := range jobs {
				res, err := p.OCR.Recognize(ctx, job.img)
				if err != nil {
					return fmt.Errorf("ocr page %d: %w", job.index+1, err)
				}
				// Each goroutine writes distinct indexes.
				results[job.index] = res
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, stats{}, err
	}

	for _, i := range needOCR {
		res := results[i]
		if res.Words == 0 || res.Confidence < p.MinConfidence {
			slog.DebugContext(ctx, "page unreadable, skipped", "page", i+1, "confidence", res.Confidence, "words", res.Words)
			st.skipped++
			continue
		}
		pages[i].Text = res.Text
		st.ocr++
	}
	return pages, st, nil
}

// validate only accepts requests the producer could have sent, so a message
// can't make the job read or delete arbitrary blobs.
func validate(req contract.OCRRequest) error {
	u, err := uuid.Parse(req.ID)
	if err != nil || u.String() != req.ID {
		return fmt.Errorf("%w: invalid id %q", ErrPermanent, req.ID)
	}
	if req.Blob != contract.PDFBlobName(req.ID) {
		return fmt.Errorf("%w: unexpected blob %q for id %s", ErrPermanent, req.Blob, req.ID)
	}
	return nil
}
