// Command producer is an Azure Functions (native Go worker) app that accepts
// PDFs over HTTP, extracts their text layer and hands PDFs without a complete
// text layer to the ocr_container through Service Bus.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"strconv"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/azure/azure-functions-golang-worker/sdk"
	"github.com/azure/azure-functions-golang-worker/worker"

	"github.com/spyrosblock/text-extraction/billing"
	"github.com/spyrosblock/text-extraction/billing/blobs"
	"github.com/spyrosblock/text-extraction/producer/internal/azure"
	"github.com/spyrosblock/text-extraction/producer/internal/extract"
	"github.com/spyrosblock/text-extraction/producer/internal/handler"
)

type config struct {
	// Managed identity (Azure).
	StorageAccountURL   string // https://<account>.blob.core.windows.net
	ServiceBusNamespace string // <namespace>.servicebus.windows.net
	// Connection strings, only for the local emulators.
	StorageConnString    string
	ServiceBusConnString string

	PDFContainer     string
	TextContainer    string
	BillingContainer string
	Queue            string
	PDFiumWorkers    int
	CreateContainers bool

	KeyPrefix       string
	TextPageCredits int64
	OCRPageCredits  int64
}

func loadConfig() (config, error) {
	c := config{
		StorageAccountURL:    os.Getenv("STORAGE_ACCOUNT_URL"),
		ServiceBusNamespace:  os.Getenv("SERVICEBUS_NAMESPACE"),
		StorageConnString:    os.Getenv("STORAGE_CONNECTION_STRING"),
		ServiceBusConnString: os.Getenv("SERVICEBUS_CONNECTION_STRING"),
		PDFContainer:         envOr("PDF_STORAGE_CONTAINER", "pdf-storage"),
		TextContainer:        envOr("TEXT_STORAGE_CONTAINER", "text-storage"),
		BillingContainer:     envOr("BILLING_CONTAINER", "billing"),
		Queue:                envOr("PDF_QUEUE_NAME", "pdf_queue"),
		PDFiumWorkers:        runtime.NumCPU(),
		CreateContainers:     os.Getenv("CREATE_CONTAINERS") == "true",
		KeyPrefix:            envOr("KEY_PREFIX", "tx_dev_"),
	}
	var errs []error
	if v := os.Getenv("PDFIUM_WORKERS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			errs = append(errs, fmt.Errorf("PDFIUM_WORKERS must be a positive integer, got %q", v))
		}
		c.PDFiumWorkers = n
	}
	for _, p := range []struct {
		key string
		def int64
		dst *int64
	}{
		{"CREDITS_PER_TEXT_PAGE", 1, &c.TextPageCredits},
		{"CREDITS_PER_OCR_PAGE", 5, &c.OCRPageCredits},
	} {
		*p.dst = p.def
		if v := os.Getenv(p.key); v != "" {
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil || n < 0 {
				errs = append(errs, fmt.Errorf("%s must be a non-negative integer, got %q", p.key, v))
			}
			*p.dst = n
		}
	}

	if c.StorageAccountURL == "" && c.StorageConnString == "" {
		errs = append(errs, errors.New("STORAGE_ACCOUNT_URL is required"))
	}
	if c.ServiceBusNamespace == "" && c.ServiceBusConnString == "" {
		errs = append(errs, errors.New("SERVICEBUS_NAMESPACE is required"))
	}
	return c, errors.Join(errs...)
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// shutdown releases process-lifetime resources when the worker stops.
type shutdown func(ctx context.Context) error

func (shutdown) Start(context.Context) error          { return nil }
func (f shutdown) Shutdown(ctx context.Context) error { return f(ctx) }

func main() {
	if err := run(); err != nil {
		slog.Error("producer startup failed", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	var cred azcore.TokenCredential
	if cfg.StorageConnString == "" || cfg.ServiceBusConnString == "" {
		if cred, err = azure.Credential(); err != nil {
			return fmt.Errorf("azure credential: %w", err)
		}
	}

	blobClient, err := blobs.NewBlobs(cfg.StorageAccountURL, cfg.StorageConnString, cred)
	if err != nil {
		return err
	}
	if cfg.CreateContainers {
		for _, c := range []string{cfg.PDFContainer, cfg.TextContainer, cfg.BillingContainer} {
			if err := blobClient.EnsureContainer(context.Background(), c); err != nil {
				return err
			}
		}
	}

	queue, err := azure.NewQueue(cfg.ServiceBusNamespace, cfg.ServiceBusConnString, cfg.Queue, cred)
	if err != nil {
		return err
	}

	extractor, err := extract.New(cfg.PDFiumWorkers)
	if err != nil {
		return err
	}

	store := &billing.Store{
		Blobs:           blobClient,
		Container:       cfg.BillingContainer,
		KeyPrefix:       cfg.KeyPrefix,
		TextPageCredits: cfg.TextPageCredits,
		OCRPageCredits:  cfg.OCRPageCredits,
	}
	h := &handler.Handler{
		Extractor:     extractor,
		Blobs:         blobClient,
		Queue:         queue,
		Credits:       store,
		PDFContainer:  cfg.PDFContainer,
		TextContainer: cfg.TextContainer,
	}
	admin := &handler.Admin{Credits: store}

	app := sdk.FunctionApp()
	// Callers authenticate with their own API key (X-API-Key), checked by
	// the handler.
	app.HTTP("extract", h.ServeHTTP,
		sdk.WithMethods("POST"),
		sdk.WithAuth("anonymous"),
	)
	// Master key only.
	app.HTTP("manage-keys", admin.Keys,
		sdk.WithMethods("GET", "POST", "DELETE"),
		sdk.WithAuth("admin"),
		sdk.WithRoute("manage/keys"),
	)
	app.HTTP("manage-credits", admin.Balance,
		sdk.WithMethods("PUT", "POST"),
		sdk.WithAuth("admin"),
		sdk.WithRoute("manage/credits"),
	)
	worker.Start(app, sdk.WithLifecycleHook(shutdown(func(ctx context.Context) error {
		return errors.Join(queue.Close(ctx), extractor.Close())
	})))
	return nil
}
