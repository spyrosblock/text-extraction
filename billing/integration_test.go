//go:build integration

// Integration test against Azurite (docker compose up -d in the repo root).
// Run with: go test -tags integration -run Integration .
package billing

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spyrosblock/text-extraction/billing/blobs"
)

// The Go SDK doesn't understand UseDevelopmentStorage=true.
const storageConn = "DefaultEndpointsProtocol=http;AccountName=devstoreaccount1;AccountKey=Eby8vdM02xNOcqFlqUwJPLlmEtlCDXJ1OUzFT50uSRZ6IFsuFq2UVErCz4I6tq/K1SZFPTOtr/KBHBeksoGMGw==;BlobEndpoint=http://127.0.0.1:10000/devstoreaccount1;"

func TestIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	b, err := blobs.NewBlobs("", storageConn, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.EnsureContainer(ctx, "billing"); err != nil {
		t.Fatal(err)
	}
	prefix := "test/" + randomHex(8) + "/"
	defer func() {
		names, _ := b.List(context.WithoutCancel(ctx), "billing", prefix)
		for _, n := range names {
			b.Delete(context.WithoutCancel(ctx), "billing", n)
		}
	}()

	t.Run("conditional upload", func(t *testing.T) {
		name := prefix + "upload.json"
		if err := b.UploadIf(ctx, "billing", name, []byte("1"), "application/json", ""); err != nil {
			t.Fatal(err)
		}
		if err := b.UploadIf(ctx, "billing", name, []byte("2"), "application/json", ""); !errors.Is(err, blobs.ErrConditionNotMet) {
			t.Fatalf("create over existing blob: %v", err)
		}
		_, etag, err := b.Get(ctx, "billing", name)
		if err != nil {
			t.Fatal(err)
		}
		if err := b.UploadIf(ctx, "billing", name, []byte("3"), "application/json", etag); err != nil {
			t.Fatal(err)
		}
		if err := b.UploadIf(ctx, "billing", name, []byte("4"), "application/json", etag); !errors.Is(err, blobs.ErrConditionNotMet) {
			t.Fatalf("stale etag: %v", err)
		}
		if data, _, _ := b.Get(ctx, "billing", name); string(data) != "3" {
			t.Fatalf("data = %s", data)
		}
	})

	t.Run("conditional delete", func(t *testing.T) {
		name := prefix + "delete.json"
		if err := b.Upload(ctx, "billing", name, []byte("1"), "application/json"); err != nil {
			t.Fatal(err)
		}
		_, stale, _ := b.Get(ctx, "billing", name)
		if err := b.Upload(ctx, "billing", name, []byte("2"), "application/json"); err != nil {
			t.Fatal(err)
		}
		if err := b.DeleteIf(ctx, "billing", name, stale); !errors.Is(err, blobs.ErrConditionNotMet) {
			t.Fatalf("stale etag: %v", err)
		}
		_, etag, _ := b.Get(ctx, "billing", name)
		if err := b.DeleteIf(ctx, "billing", name, etag); err != nil {
			t.Fatal(err)
		}
		if err := b.DeleteIf(ctx, "billing", name, etag); !errors.Is(err, blobs.ErrConditionNotMet) {
			t.Fatalf("deleted blob: %v", err)
		}
	})

	// The fake serializes everything; Azurite checks the conditions for real.
	t.Run("login token consumed once", func(t *testing.T) {
		l := &LoginTokens{Blobs: b, Container: "billing", TTL: time.Minute}
		token, err := l.Create(ctx, "ann@example.com")
		if err != nil {
			t.Fatal(err)
		}
		defer b.Delete(context.WithoutCancel(ctx), "billing", loginTokenBlob(token))
		var won atomic.Int32
		var wg sync.WaitGroup
		for range 5 {
			wg.Go(func() {
				_, err := l.Consume(ctx, token)
				switch {
				case err == nil:
					won.Add(1)
				case !errors.Is(err, ErrInvalidToken):
					t.Error(err)
				}
			})
		}
		wg.Wait()
		if won.Load() != 1 {
			t.Fatalf("%d verifies won, want 1", won.Load())
		}
	})

	t.Run("free credits claimed once", func(t *testing.T) {
		a := &Accounts{Blobs: b, Container: "billing", FreeCreditsSecret: []byte(strings.Repeat(randomHex(8), 4))}
		defer a.ReleaseFreeCredits(context.WithoutCancel(ctx), "ann@example.com")
		var won atomic.Int32
		var wg sync.WaitGroup
		for range 5 {
			wg.Go(func() {
				ok, err := a.ClaimFreeCredits(ctx, "ann@example.com", KeyID(randomHex(8)))
				if err != nil {
					t.Error(err)
				}
				if ok {
					won.Add(1)
				}
			})
		}
		wg.Wait()
		if won.Load() != 1 {
			t.Fatalf("%d claims won, want 1", won.Load())
		}
	})
}
