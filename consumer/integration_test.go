//go:build integration

// Integration test against Azurite (docker compose up -d in the repo root).
// Run with: go test -tags integration -run Integration .
package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/google/uuid"

	"github.com/knowledge/text-extraction/consumer/internal/azure"
	"github.com/knowledge/text-extraction/consumer/internal/contract"
	"github.com/knowledge/text-extraction/consumer/internal/handler"
)

// The Go SDK doesn't understand UseDevelopmentStorage=true.
const storageConn = "DefaultEndpointsProtocol=http;AccountName=devstoreaccount1;AccountKey=Eby8vdM02xNOcqFlqUwJPLlmEtlCDXJ1OUzFT50uSRZ6IFsuFq2UVErCz4I6tq/K1SZFPTOtr/KBHBeksoGMGw==;BlobEndpoint=http://127.0.0.1:10000/devstoreaccount1;"

func TestIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	blobs, err := azure.NewBlobs("", storageConn, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := blobs.EnsureContainer(ctx, "text-storage"); err != nil {
		t.Fatal(err)
	}
	h := &handler.Handler{Blobs: blobs, TextContainer: "text-storage"}

	get := func(id string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/text", strings.NewReader(`{"id":"`+id+`"}`)))
		return w
	}

	t.Run("stored text returned", func(t *testing.T) {
		id := uuid.NewString()
		body, _ := json.Marshal(contract.Document{ID: id, Pages: []contract.Page{{Number: 1, Text: "integration"}}})
		client, _ := azblob.NewClientFromConnectionString(storageConn, nil)
		if _, err := client.UploadBuffer(ctx, "text-storage", contract.TextBlobName(id), body, nil); err != nil {
			t.Fatal(err)
		}

		w := get(id)
		if w.Code != http.StatusOK {
			t.Fatalf("got %d %s", w.Code, w.Body)
		}
		var doc contract.Document
		if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
			t.Fatal(err)
		}
		if doc.ID != id || len(doc.Pages) != 1 || doc.Pages[0].Text != "integration" {
			t.Fatalf("doc = %+v", doc)
		}
	})

	t.Run("unknown id", func(t *testing.T) {
		if w := get(uuid.NewString()); w.Code != http.StatusNotFound {
			t.Fatalf("got %d %s, want 404", w.Code, w.Body)
		}
	})
}
