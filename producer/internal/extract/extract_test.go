package extract

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/knowledge/text-extraction/producer/internal/pdftest"
)

func newExtractor(t *testing.T) *Extractor {
	t.Helper()
	e, err := New(1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	return e
}

func TestExtract(t *testing.T) {
	e := newExtractor(t)
	ctx := context.Background()

	t.Run("all pages have text", func(t *testing.T) {
		pages, err := e.Extract(ctx, pdftest.Build("Hello page one", "Second page"))
		if err != nil {
			t.Fatal(err)
		}
		if len(pages) != 2 {
			t.Fatalf("got %d pages, want 2", len(pages))
		}
		for i, want := range []string{"Hello page one", "Second page"} {
			if pages[i].Number != i+1 || !strings.Contains(pages[i].Text, want) {
				t.Errorf("page %d = %+v, want number %d containing %q", i, pages[i], i+1, want)
			}
		}
		if !AllPagesHaveText(pages) {
			t.Error("AllPagesHaveText = false, want true")
		}
	})

	t.Run("page without text layer", func(t *testing.T) {
		pages, err := e.Extract(ctx, pdftest.Build("Has text", ""))
		if err != nil {
			t.Fatal(err)
		}
		if len(pages) != 2 || strings.TrimSpace(pages[1].Text) != "" {
			t.Fatalf("got %+v, want 2 pages with an empty second page", pages)
		}
		if AllPagesHaveText(pages) {
			t.Error("AllPagesHaveText = true, want false")
		}
	})

	t.Run("invalid pdf", func(t *testing.T) {
		_, err := e.Extract(ctx, []byte("%PDF-1.4\nthis is not really a pdf"))
		if !errors.Is(err, ErrInvalidPDF) {
			t.Fatalf("err = %v, want ErrInvalidPDF", err)
		}
	})
}
