// Package contract holds the payloads exchanged between producer,
// ocr_container and consumer. Keep it in sync across components.
//
// The billing blobs are shared by the producer and the consumer only: the
// API key id (hex sha256 of the key: credits.KeyID in the producer, keyID in
// the consumer's handler), keys/<key id>.json and jobs/<id>.json {"keyId"}.
// Keep those in sync too.
package contract

import (
	"fmt"
	"math"
	"strconv"
)

// Page is the extracted text of a single PDF page. Number is 1-based.
type Page struct {
	Number     int        `json:"page"`
	Text       string     `json:"text"`
	Confidence Confidence `json:"confidence"`
}

// Confidence is the mean word confidence (0-100) Tesseract had in a page's
// text; 0 when it found no words or the page couldn't be OCRed. TextLayer
// marks text taken from the PDF's text layer.
//
// It is encoded as a number rounded to 2 decimals, or "-" for TextLayer.
type Confidence float64

const TextLayer Confidence = -1

func (c Confidence) MarshalJSON() ([]byte, error) {
	if c == TextLayer {
		return []byte(`"-"`), nil
	}
	return strconv.AppendFloat(nil, math.Round(float64(c)*100)/100, 'f', -1, 64), nil
}

func (c *Confidence) UnmarshalJSON(b []byte) error {
	if string(b) == `"-"` {
		*c = TextLayer
		return nil
	}
	f, err := strconv.ParseFloat(string(b), 64)
	if err != nil {
		return fmt.Errorf("confidence must be a number or \"-\", got %s", b)
	}
	*c = Confidence(f)
	return nil
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
