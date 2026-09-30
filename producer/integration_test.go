//go:build integration

// Integration test against the local emulators (docker compose up -d in the
// repo root). Run with: go test -tags integration -run Integration .
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/messaging/azservicebus"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"

	"github.com/knowledge/text-extraction/producer/internal/azure"
	"github.com/knowledge/text-extraction/producer/internal/contract"
	"github.com/knowledge/text-extraction/producer/internal/extract"
	"github.com/knowledge/text-extraction/producer/internal/handler"
	"github.com/knowledge/text-extraction/producer/internal/pdftest"
)

const (
	// The Go SDK doesn't understand UseDevelopmentStorage=true.
	storageConn = "DefaultEndpointsProtocol=http;AccountName=devstoreaccount1;AccountKey=Eby8vdM02xNOcqFlqUwJPLlmEtlCDXJ1OUzFT50uSRZ6IFsuFq2UVErCz4I6tq/K1SZFPTOtr/KBHBeksoGMGw==;BlobEndpoint=http://127.0.0.1:10000/devstoreaccount1;"
	busConn     = "Endpoint=sb://localhost;SharedAccessKeyName=RootManageSharedAccessKey;SharedAccessKey=SAS_KEY_VALUE;UseDevelopmentEmulator=true;"
	queueName   = "pdf_queue"
)

func TestIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
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
	queue, err := azure.NewQueue("", busConn, queueName, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer queue.Close(ctx)
	extractor, err := extract.New(1)
	if err != nil {
		t.Fatal(err)
	}
	defer extractor.Close()

	h := &handler.Handler{Extractor: extractor, Blobs: blobs, Queue: queue, PDFContainer: "pdf-storage", TextContainer: "text-storage"}
	blobClient, _ := azblob.NewClientFromConnectionString(storageConn, nil)

	post := func(pdf []byte) handler.Response {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, "/api/extract", bytes.NewReader(pdf))
		r.Header.Set("Content-Type", "application/pdf")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusAccepted {
			t.Fatalf("got %d %s", w.Code, w.Body)
		}
		var resp handler.Response
		json.Unmarshal(w.Body.Bytes(), &resp)
		return resp
	}
	download := func(container, name string) []byte {
		t.Helper()
		var buf bytes.Buffer
		get, err := blobClient.DownloadStream(ctx, container, name, nil)
		if err != nil {
			t.Fatalf("download %s/%s: %v", container, name, err)
		}
		defer get.Body.Close()
		buf.ReadFrom(get.Body)
		return buf.Bytes()
	}

	t.Run("text layer stored", func(t *testing.T) {
		resp := post(pdftest.Build("integration text"))
		if resp.Status != handler.StatusCompleted {
			t.Fatalf("status = %s", resp.Status)
		}
		var doc contract.Document
		if err := json.Unmarshal(download("text-storage", contract.TextBlobName(resp.ID)), &doc); err != nil {
			t.Fatal(err)
		}
		if doc.ID != resp.ID || len(doc.Pages) != 1 {
			t.Fatalf("doc = %+v", doc)
		}
	})

	t.Run("scanned pdf queued", func(t *testing.T) {
		pdf := pdftest.Build("")
		resp := post(pdf)
		if resp.Status != handler.StatusQueued {
			t.Fatalf("status = %s", resp.Status)
		}
		if got := download("pdf-storage", contract.PDFBlobName(resp.ID)); !bytes.Equal(got, pdf) {
			t.Fatal("stored pdf differs")
		}

		client, err := azservicebus.NewClientFromConnectionString(busConn, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer client.Close(ctx)
		receiver, err := client.NewReceiverForQueue(queueName, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer receiver.Close(ctx)
		for {
			msgs, err := receiver.ReceiveMessages(ctx, 10, nil)
			if err != nil {
				t.Fatalf("no message for %s: %v", resp.ID, err)
			}
			for _, m := range msgs {
				receiver.CompleteMessage(ctx, m, nil)
				var req contract.OCRRequest
				json.Unmarshal(m.Body, &req)
				if req.ID == resp.ID {
					if req.Blob != contract.PDFBlobName(resp.ID) {
						t.Fatalf("message = %+v", req)
					}
					return
				}
			}
		}
	})
}
