//go:build integration

// Integration test against the local emulators (docker compose up -d in the
// repo root) and a local tesseract. Run with:
// go test -tags integration -run Integration .
package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/messaging/azservicebus"
	"github.com/google/uuid"

	"github.com/knowledge/text-extraction/ocr_container/internal/azure"
	"github.com/knowledge/text-extraction/ocr_container/internal/contract"
	"github.com/knowledge/text-extraction/ocr_container/internal/job"
	"github.com/knowledge/text-extraction/ocr_container/internal/ocr"
	"github.com/knowledge/text-extraction/ocr_container/internal/pdf"
	"github.com/knowledge/text-extraction/ocr_container/internal/pdftest"
	"github.com/knowledge/text-extraction/ocr_container/internal/processor"
)

const (
	// The Go SDK doesn't understand UseDevelopmentStorage=true.
	storageConn = "DefaultEndpointsProtocol=http;AccountName=devstoreaccount1;AccountKey=Eby8vdM02xNOcqFlqUwJPLlmEtlCDXJ1OUzFT50uSRZ6IFsuFq2UVErCz4I6tq/K1SZFPTOtr/KBHBeksoGMGw==;BlobEndpoint=http://127.0.0.1:10000/devstoreaccount1;"
	busConn     = "Endpoint=sb://localhost;SharedAccessKeyName=RootManageSharedAccessKey;SharedAccessKey=SAS_KEY_VALUE;UseDevelopmentEmulator=true;"
	queueName   = "pdf_queue"
)

func TestIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	blobs, err := azure.NewBlobs("", storageConn, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []string{"pdf-storage", "text-storage"} {
		if err := blobs.EnsureContainer(ctx, c); err != nil {
			t.Fatal(err)
		}
	}

	// Queue a PDF the way the producer does: one page with a text layer and
	// one blank page, which OCR finds nothing on.
	id := uuid.NewString()
	req := contract.OCRRequest{ID: id, Blob: contract.PDFBlobName(id)}
	if err := blobs.Upload(ctx, "pdf-storage", req.Blob, pdftest.Build("integration text", ""), "application/pdf"); err != nil {
		t.Fatal(err)
	}
	client, err := azservicebus.NewClientFromConnectionString(busConn, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close(ctx)
	sender, err := client.NewSender(queueName, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sender.Close(ctx)
	body, _ := json.Marshal(req)
	if err := sender.SendMessage(ctx, &azservicebus.Message{Body: body, MessageID: &id}, nil); err != nil {
		t.Fatal(err)
	}

	receiver, err := client.NewReceiverForQueue(queueName, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer receiver.Close(ctx)
	extractor, err := pdf.New(1)
	if err != nil {
		t.Fatal(err)
	}
	defer extractor.Close()

	p := &processor.Processor{
		Blobs: blobs, PDF: opener{extractor}, OCR: &ocr.Tesseract{Languages: "eng+ell"},
		PDFContainer: "pdf-storage", TextContainer: "text-storage",
		Workers: 2, DPI: 150, MinConfidence: 60,
	}
	j := &job.Job{Receiver: receiver, Process: p.Process, IdleTimeout: 5 * time.Second, LockRenewInterval: time.Minute}
	if err := j.Run(ctx); err != nil {
		t.Fatal(err)
	}

	raw, err := blobs.Download(ctx, "text-storage", contract.TextBlobName(id))
	if err != nil {
		t.Fatal(err)
	}
	var doc contract.Document
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.ID != id || len(doc.Pages) != 2 || doc.Pages[0].Text != "integration text" || doc.Pages[1].Text != "" {
		t.Fatalf("doc = %+v", doc)
	}
	if ok, _ := blobs.Exists(ctx, "pdf-storage", req.Blob); ok {
		t.Fatal("pdf not deleted")
	}
}
