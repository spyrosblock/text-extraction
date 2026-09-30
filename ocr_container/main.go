// Command ocr_container is an Azure Container Apps job that takes OCR
// requests from pdf_queue, extracts the text of the PDF from pdf_storage
// (text layer where present, Tesseract OCR elsewhere), stores it in
// text_storage and deletes the PDF.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"syscall"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"

	"github.com/knowledge/text-extraction/ocr_container/internal/azure"
	"github.com/knowledge/text-extraction/ocr_container/internal/job"
	"github.com/knowledge/text-extraction/ocr_container/internal/ocr"
	"github.com/knowledge/text-extraction/ocr_container/internal/pdf"
	"github.com/knowledge/text-extraction/ocr_container/internal/processor"
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
	CreateContainers bool

	OCRWorkers       int
	OCRLanguages     string
	OCRDPI           int
	OCRMinConfidence float64

	IdleTimeout       time.Duration
	LockRenewInterval time.Duration
	MaxMessages       int
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
		CreateContainers:     os.Getenv("CREATE_CONTAINERS") == "true",
		OCRLanguages:         envOr("OCR_LANGUAGES", "eng+ell"),
	}

	var errs []error
	collect := func(err error) {
		if err != nil {
			errs = append(errs, err)
		}
	}
	collect(envInt("OCR_WORKERS", runtime.NumCPU(), 1, &c.OCRWorkers))
	collect(envInt("OCR_DPI", 300, 72, &c.OCRDPI))
	collect(envInt("MAX_MESSAGES", 0, 0, &c.MaxMessages))
	collect(envDuration("IDLE_TIMEOUT", 30*time.Second, &c.IdleTimeout))
	collect(envDuration("LOCK_RENEW_INTERVAL", 2*time.Minute, &c.LockRenewInterval))
	c.OCRMinConfidence = 60
	if v := os.Getenv("OCR_MIN_CONFIDENCE"); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || f < 0 || f > 100 {
			collect(fmt.Errorf("OCR_MIN_CONFIDENCE must be between 0 and 100, got %q", v))
		}
		c.OCRMinConfidence = f
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

func envInt(key string, def, minimum int, dst *int) error {
	*dst = def
	v := os.Getenv(key)
	if v == "" {
		return nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < minimum {
		return fmt.Errorf("%s must be an integer >= %d, got %q", key, minimum, v)
	}
	*dst = n
	return nil
}

func envDuration(key string, def time.Duration, dst *time.Duration) error {
	*dst = def
	v := os.Getenv(key)
	if v == "" {
		return nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 {
		return fmt.Errorf("%s must be a non-negative duration like 30s, got %q", key, v)
	}
	*dst = d
	return nil
}

// opener adapts *pdf.Extractor to processor.Opener.
type opener struct{ *pdf.Extractor }

func (o opener) Open(ctx context.Context, data []byte) (processor.Document, error) {
	doc, err := o.Extractor.Open(ctx, data)
	if err != nil {
		return nil, err
	}
	return doc, nil
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	if err := run(); err != nil {
		slog.Error("ocr_container failed", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	// Container Apps sends SIGTERM before stopping a replica.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()

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
			if err := blobs.EnsureContainer(ctx, c); err != nil {
				return err
			}
		}
	}

	client, receiver, err := azure.NewReceiver(cfg.ServiceBusNamespace, cfg.ServiceBusConnString, cfg.Queue, cred)
	if err != nil {
		return err
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = receiver.Close(closeCtx)
		_ = client.Close(closeCtx)
	}()

	// One document is processed at a time; it holds one PDFium instance.
	extractor, err := pdf.New(1)
	if err != nil {
		return err
	}
	defer extractor.Close()

	p := &processor.Processor{
		Blobs:         blobs,
		PDF:           opener{extractor},
		OCR:           &ocr.Tesseract{Languages: cfg.OCRLanguages},
		PDFContainer:  cfg.PDFContainer,
		TextContainer: cfg.TextContainer,
		Workers:       cfg.OCRWorkers,
		DPI:           cfg.OCRDPI,
		MinConfidence: cfg.OCRMinConfidence,
	}
	j := &job.Job{
		Receiver:          receiver,
		Process:           p.Process,
		IdleTimeout:       cfg.IdleTimeout,
		LockRenewInterval: cfg.LockRenewInterval,
		MaxMessages:       cfg.MaxMessages,
	}
	slog.Info("ocr_container started", "queue", cfg.Queue, "workers", cfg.OCRWorkers, "languages", cfg.OCRLanguages)
	return j.Run(ctx)
}
