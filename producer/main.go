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

	"github.com/knowledge/text-extraction/producer/internal/azure"
	"github.com/knowledge/text-extraction/producer/internal/extract"
	"github.com/knowledge/text-extraction/producer/internal/handler"
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
	Queue            string
	PDFiumWorkers    int
	CreateContainers bool
}

func loadConfig() (config, error) {
	c := config{
		StorageAccountURL:    os.Getenv("STORAGE_ACCOUNT_URL"),
		ServiceBusNamespace:  os.Getenv("SERVICEBUS_NAMESPACE"),
		StorageConnString:    os.Getenv("STORAGE_CONNECTION_STRING"),
		ServiceBusConnString: os.Getenv("SERVICEBUS_CONNECTION_STRING"),
		PDFContainer:         envOr("PDF_STORAGE_CONTAINER", "pdf-storage"),
		TextContainer:        envOr("TEXT_STORAGE_CONTAINER", "text-storage"),
		Queue:                envOr("PDF_QUEUE_NAME", "pdf_queue"),
		PDFiumWorkers:        runtime.NumCPU(),
		CreateContainers:     os.Getenv("CREATE_CONTAINERS") == "true",
	}
	if v := os.Getenv("PDFIUM_WORKERS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return c, fmt.Errorf("PDFIUM_WORKERS must be a positive integer, got %q", v)
		}
		c.PDFiumWorkers = n
	}

	var errs []error
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

	blobs, err := azure.NewBlobs(cfg.StorageAccountURL, cfg.StorageConnString, cred)
	if err != nil {
		return err
	}
	if cfg.CreateContainers {
		for _, c := range []string{cfg.PDFContainer, cfg.TextContainer} {
			if err := blobs.EnsureContainer(context.Background(), c); err != nil {
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

	h := &handler.Handler{
		Extractor:     extractor,
		Blobs:         blobs,
		Queue:         queue,
		PDFContainer:  cfg.PDFContainer,
		TextContainer: cfg.TextContainer,
	}

	app := sdk.FunctionApp()
	app.HTTP("extract", h.ServeHTTP,
		sdk.WithMethods("POST"),
		sdk.WithAuth("function"),
	)
	worker.Start(app, sdk.WithLifecycleHook(shutdown(func(ctx context.Context) error {
		return errors.Join(queue.Close(ctx), extractor.Close())
	})))
	return nil
}
