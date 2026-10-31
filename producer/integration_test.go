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
	"sync"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/messaging/azservicebus"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"

	"github.com/spyrosblock/text-extraction/billing"
	"github.com/spyrosblock/text-extraction/billing/blobs"
	"github.com/spyrosblock/text-extraction/producer/internal/azure"
	"github.com/spyrosblock/text-extraction/producer/internal/contract"
	"github.com/spyrosblock/text-extraction/producer/internal/extract"
	"github.com/spyrosblock/text-extraction/producer/internal/handler"
	"github.com/spyrosblock/text-extraction/producer/internal/pdftest"
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

	blobStore, err := blobs.NewBlobs("", storageConn, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []string{"pdf-storage", "text-storage", "billing"} {
		if err := blobStore.EnsureContainer(ctx, c); err != nil {
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

	store := &billing.Store{Blobs: blobStore, Container: "billing", KeyPrefix: "tx_test_", TextPageCredits: 1, OCRPageCredits: 5}
	key, keyID, _, err := store.Create(ctx, "integration", 100, "")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Delete(context.WithoutCancel(ctx), keyID)

	h := &handler.Handler{Extractor: extractor, Blobs: blobStore, Queue: queue, Credits: store, PDFContainer: "pdf-storage", TextContainer: "text-storage"}
	blobClient, _ := azblob.NewClientFromConnectionString(storageConn, nil)

	post := func(pdf []byte) handler.Response {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, "/api/extract", bytes.NewReader(pdf))
		r.Header.Set("Content-Type", "application/pdf")
		r.Header.Set(handler.APIKeyHeader, key)
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
		if resp.Status != handler.StatusCompleted || resp.Credits != 1 {
			t.Fatalf("resp = %+v", resp)
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
		if resp.Status != handler.StatusQueued || resp.Credits != 5 || resp.Balance != 100-1-5 {
			t.Fatalf("resp = %+v", resp)
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
	// The blob client's own conditional uploads and deletes are tested in
	// billing's integration test.

	// Parallel charges conflict on the key blob's ETag and are retried, so
	// none is lost. With 5 writers each needs at most 5 tries.
	t.Run("parallel charges", func(t *testing.T) {
		_, before, err := store.Lookup(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		for range 5 {
			wg.Go(func() {
				if _, err := store.Charge(ctx, keyID, 1); err != nil {
					t.Error(err)
				}
			})
		}
		wg.Wait()
		_, after, err := store.Lookup(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		if after.Credits != before.Credits-5 {
			t.Fatalf("balance %d -> %d, want 5 charged", before.Credits, after.Credits)
		}
	})
}
