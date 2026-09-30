// Package contract holds the payloads exchanged between producer,
// ocr_container and consumer. Keep it in sync across components.
package contract

// Page is the extracted text of a single PDF page. Number is 1-based.
type Page struct {
	Number int    `json:"page"`
	Text   string `json:"text"`
}

// Document is stored as "<id>.json" in the text_storage container and is
// what the consumer returns to the user.
type Document struct {
	ID    string `json:"id"`
	Pages []Page `json:"pages"`
}

// OCRRequest is the body of a message on pdf_queue. The PDF itself lives in
// the pdf_storage container under Blob.
type OCRRequest struct {
	ID   string `json:"id"`
	Blob string `json:"blob"`
}

// TextBlobName returns the text_storage blob name for a request id.
func TextBlobName(id string) string { return id + ".json" }

// PDFBlobName returns the pdf_storage blob name for a request id.
func PDFBlobName(id string) string { return id + ".pdf" }
