// Package pdftest builds minimal PDFs for tests.
package pdftest

import (
	"bytes"
	"fmt"
	"strings"
)

// Build writes a minimal PDF with one page per entry of pages. A non-empty
// entry is drawn as Helvetica text; an empty entry yields a page with no text
// layer (like a scanned page).
func Build(pages ...string) []byte {
	var objs []string
	kids := make([]string, len(pages))
	// 1: catalog, 2: pages, 3: font, then page/content pairs.
	for i, text := range pages {
		pageObj, contentObj := 4+2*i, 5+2*i
		kids[i] = fmt.Sprintf("%d 0 R", pageObj)
		stream := ""
		if text != "" {
			stream = fmt.Sprintf("BT /F1 24 Tf 72 700 Td (%s) Tj ET", text)
		}
		objs = append(objs,
			fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 3 0 R >> >> /Contents %d 0 R >>", contentObj),
			fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream),
		)
	}
	objs = append([]string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), len(pages)),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}, objs...)

	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objs))
	for i, o := range objs {
		offsets[i] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for _, off := range offsets {
		fmt.Fprintf(&b, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, xref)
	return b.Bytes()
}
