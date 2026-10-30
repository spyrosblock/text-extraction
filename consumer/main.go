// Command consumer is an Azure Functions (native Go worker) app that returns
// the text extracted by the producer or the ocr_container for a request id.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/azure/azure-functions-golang-worker/sdk"
	"github.com/azure/azure-functions-golang-worker/worker"

	"github.com/spyrosblock/text-extraction/consumer/internal/azure"
	"github.com/spyrosblock/text-extraction/consumer/internal/handler"
)

type config struct {
	// Managed identity (Azure).
	StorageAccountURL string // https://<account>.blob.core.windows.net
	// Connection string, only for the local emulator.
	StorageConnString string

	TextContainer    string
	BillingContainer string
	CreateContainers bool
}

func loadConfig() (config, error) {
	c := config{
		StorageAccountURL: os.Getenv("STORAGE_ACCOUNT_URL"),
		StorageConnString: os.Getenv("STORAGE_CONNECTION_STRING"),
		TextContainer:     envOr("TEXT_STORAGE_CONTAINER", "text-storage"),
		BillingContainer:  envOr("BILLING_CONTAINER", "billing"),
		CreateContainers:  os.Getenv("CREATE_CONTAINERS") == "true",
	}
	if c.StorageAccountURL == "" && c.StorageConnString == "" {
		return c, errors.New("STORAGE_ACCOUNT_URL is required")
	}
	return c, nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	if err := run(); err != nil {
		slog.Error("consumer startup failed", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	var cred azcore.TokenCredential
	if cfg.StorageConnString == "" {
		if cred, err = azure.Credential(); err != nil {
			return fmt.Errorf("azure credential: %w", err)
		}
	}

	blobs, err := azure.NewBlobs(cfg.StorageAccountURL, cfg.StorageConnString, cred)
	if err != nil {
		return err
	}
	if cfg.CreateContainers {
		for _, c := range []string{cfg.TextContainer, cfg.BillingContainer} {
			if err := blobs.EnsureContainer(context.Background(), c); err != nil {
				return err
			}
		}
	}

	h := &handler.Handler{Blobs: blobs, TextContainer: cfg.TextContainer, BillingContainer: cfg.BillingContainer}

	app := sdk.FunctionApp()
	// Callers authenticate with their API key (X-API-Key), checked by the
	// handler.
	app.HTTP("text", h.ServeHTTP,
		sdk.WithMethods("POST"),
		sdk.WithAuth("anonymous"),
		sdk.WithRoute("text"),
	)
	worker.Start(app)
	return nil
}
