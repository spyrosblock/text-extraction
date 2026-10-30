//go:build integration

// Integration test against Azurite (docker compose up -d in the repo root).
// Run with: go test -tags integration -run Integration .
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/google/uuid"

	"github.com/spyrosblock/text-extraction/consumer/internal/azure"
	"github.com/spyrosblock/text-extraction/consumer/internal/contract"
	"github.com/spyrosblock/text-extraction/consumer/internal/handler"
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
	for _, c := range []string{"text-storage", "billing"} {
		if err := blobs.EnsureContainer(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	h := &handler.Handler{Blobs: blobs, TextContainer: "text-storage", BillingContainer: "billing"}
	client, _ := azblob.NewClientFromConnectionString(storageConn, nil)
	upload := func(container, name string, body []byte) {
		t.Helper()
		if _, err := client.UploadBuffer(ctx, container, name, body, nil); err != nil {
			t.Fatal(err)
		}
	}

	// A key as the producer stores it: keys/<sha256 of the key>.json.
	key := "tx_test_" + uuid.NewString()
	sum := sha256.Sum256([]byte(key))
	keyID := hex.EncodeToString(sum[:])
	upload("billing", "keys/"+keyID+".json", []byte(`{"name":"integration","credits":1}`))
	defer client.DeleteBlob(context.WithoutCancel(ctx), "billing", "keys/"+keyID+".json", nil)

	getAs := func(key, id string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/api/text", strings.NewReader(`{"request_id":"`+id+`"}`))
		r.Header.Set(handler.APIKeyHeader, key)
		h.ServeHTTP(w, r)
		return w
	}
	get := func(id string) *httptest.ResponseRecorder { return getAs(key, id) }

	id := uuid.NewString()
	body, _ := json.Marshal(contract.Document{ID: id, Pages: []contract.Page{{Number: 1, Text: "integration"}}})
	upload("text-storage", contract.TextBlobName(id), body)
	upload("billing", "jobs/"+id+".json", []byte(`{"keyId":"`+keyID+`"}`))

	t.Run("stored text returned", func(t *testing.T) {
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

	t.Run("unknown key", func(t *testing.T) {
		if w := getAs("tx_test_unknown", id); w.Code != http.StatusUnauthorized {
			t.Fatalf("got %d %s, want 401", w.Code, w.Body)
		}
	})
}
