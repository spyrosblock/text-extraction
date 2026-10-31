// Command account is an Azure Functions (native Go worker) app behind the
// website: magic-link sign-in, the customer's API keys, and buying credit
// packs with Stripe Checkout (Managed Payments) and its webhook. Keys and
// balances live in the billing container, shared with the producer.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/azure/azure-functions-golang-worker/sdk"
	"github.com/azure/azure-functions-golang-worker/worker"
	"github.com/stripe/stripe-go/v87"

	"github.com/spyrosblock/text-extraction/account/internal/email"
	"github.com/spyrosblock/text-extraction/account/internal/handler"
	"github.com/spyrosblock/text-extraction/billing"
	"github.com/spyrosblock/text-extraction/billing/blobs"
)

const (
	sessionTTL    = 7 * 24 * time.Hour
	loginTokenTTL = 15 * time.Minute
)

// stripeTimeout bounds a single HTTP call to Stripe.
const stripeTimeout = 10 * time.Second

type config struct {
	// Managed identity (Azure).
	StorageAccountURL string // https://<account>.blob.core.windows.net
	// Connection string, only for the local emulator.
	StorageConnString string

	BillingContainer string
	CreateContainers bool

	KeyPrefix       string
	TextPageCredits int64
	OCRPageCredits  int64

	SiteURL     string
	EmailMode   string // acs or log
	ACSEndpoint string
	EmailSender string

	Packs             map[string]handler.Pack
	FreeCredits       int64
	FreeCreditsSecret string
	MaxKeys           int64
	MaxCredits        int64 // per account; 0: no cap

	StripeAPIKey        string // empty: checkout answers 503
	StripeWebhookSecret string
	PaymentsTestMode    bool
}

func loadConfig() (config, error) {
	c := config{
		StorageAccountURL:   os.Getenv("STORAGE_ACCOUNT_URL"),
		StorageConnString:   os.Getenv("STORAGE_CONNECTION_STRING"),
		BillingContainer:    envOr("BILLING_CONTAINER", "billing"),
		CreateContainers:    os.Getenv("CREATE_CONTAINERS") == "true",
		KeyPrefix:           envOr("KEY_PREFIX", "tx_dev_"),
		SiteURL:             os.Getenv("SITE_URL"),
		EmailMode:           envOr("EMAIL_MODE", "acs"),
		ACSEndpoint:         os.Getenv("ACS_ENDPOINT"),
		EmailSender:         os.Getenv("EMAIL_SENDER"),
		FreeCreditsSecret:   os.Getenv("FREE_CREDITS_SECRET"),
		StripeAPIKey:        os.Getenv("STRIPE_API_KEY"),
		StripeWebhookSecret: os.Getenv("STRIPE_WEBHOOK_SECRET"),
	}
	var errs []error
	for _, p := range []struct {
		key string
		def int64
		min int64
		dst *int64
	}{
		{"CREDITS_PER_TEXT_PAGE", 1, 0, &c.TextPageCredits},
		{"CREDITS_PER_OCR_PAGE", 5, 0, &c.OCRPageCredits},
		{"FREE_CREDITS", 100, 0, &c.FreeCredits},
		{"MAX_KEYS", 2, 1, &c.MaxKeys},
		{"MAX_ACCOUNT_CREDITS", 20000, 0, &c.MaxCredits},
	} {
		*p.dst = p.def
		if v := os.Getenv(p.key); v != "" {
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil || n < p.min {
				errs = append(errs, fmt.Errorf("%s must be an integer ≥ %d, got %q", p.key, p.min, v))
			}
			*p.dst = n
		}
	}
	if v := os.Getenv("PACKS"); v == "" {
		errs = append(errs, errors.New("PACKS is required"))
	} else if err := json.Unmarshal([]byte(v), &c.Packs); err != nil {
		errs = append(errs, fmt.Errorf("PACKS: %w", err))
	}
	priceIDs := map[string]string{}
	for name, p := range c.Packs {
		if name == "" || p.Credits <= 0 || p.PriceEUR < 0 {
			errs = append(errs, fmt.Errorf("PACKS: pack %q needs a name, credits > 0 and price_eur ≥ 0", name))
		}
		if c.MaxCredits > 0 && p.Credits > c.MaxCredits {
			errs = append(errs, fmt.Errorf("PACKS: pack %q has more credits than MAX_ACCOUNT_CREDITS (%d)", name, c.MaxCredits))
		}
		// Without an API key checkout is off, so a deploy before the Stripe
		// products exist can leave the price ids out.
		if p.PriceID == "" {
			if c.StripeAPIKey != "" {
				errs = append(errs, fmt.Errorf("PACKS: pack %q needs a price_id", name))
			}
			continue
		}
		if other, ok := priceIDs[p.PriceID]; ok {
			errs = append(errs, fmt.Errorf("PACKS: packs %q and %q share price_id %s", other, name, p.PriceID))
		}
		priceIDs[p.PriceID] = name
	}

	switch v := os.Getenv("PAYMENTS_TEST_MODE"); v {
	case "true", "false":
		c.PaymentsTestMode = v == "true"
	default:
		errs = append(errs, fmt.Errorf("PAYMENTS_TEST_MODE must be true or false, got %q", v))
	}
	if c.StripeWebhookSecret == "" {
		errs = append(errs, errors.New("STRIPE_WEBHOOK_SECRET is required"))
	}
	if c.StripeAPIKey != "" {
		if test, ok := stripeKeyTestMode(c.StripeAPIKey); !ok {
			errs = append(errs, errors.New("STRIPE_API_KEY must be a Stripe secret or restricted key (rk_test_…, rk_live_…)"))
		} else if test != c.PaymentsTestMode {
			errs = append(errs, fmt.Errorf("STRIPE_API_KEY is a %s key but PAYMENTS_TEST_MODE is %t", modeName(test), c.PaymentsTestMode))
		}
	}

	if c.StorageAccountURL == "" && c.StorageConnString == "" {
		errs = append(errs, errors.New("STORAGE_ACCOUNT_URL is required"))
	}
	if c.SiteURL == "" {
		errs = append(errs, errors.New("SITE_URL is required"))
	}
	switch c.EmailMode {
	case "acs":
		if c.ACSEndpoint == "" || c.EmailSender == "" {
			errs = append(errs, errors.New("EMAIL_MODE=acs needs ACS_ENDPOINT and EMAIL_SENDER"))
		}
	case "log":
	default:
		errs = append(errs, fmt.Errorf("EMAIL_MODE must be acs or log, got %q", c.EmailMode))
	}
	if len(c.FreeCreditsSecret) < billing.MinFreeCreditsSecret {
		errs = append(errs, fmt.Errorf("FREE_CREDITS_SECRET is required, at least %d bytes", billing.MinFreeCreditsSecret))
	}
	return c, errors.Join(errs...)
}

// stripeKeyTestMode reports whether a Stripe secret or restricted key is a
// test-mode key; ok is false for anything else.
func stripeKeyTestMode(key string) (test, ok bool) {
	for _, p := range []string{"rk_", "sk_"} {
		if rest, found := strings.CutPrefix(key, p); found {
			switch {
			case strings.HasPrefix(rest, "test_"):
				return true, true
			case strings.HasPrefix(rest, "live_"):
				return false, true
			}
		}
	}
	return false, false
}

func modeName(test bool) string {
	if test {
		return "test-mode"
	}
	return "live-mode"
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	if err := run(); err != nil {
		slog.Error("account startup failed", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	// DefaultAzureCredential picks up the function app's managed identity
	// (AZURE_CLIENT_ID selects a user-assigned one) or a developer login.
	var cred azcore.TokenCredential
	if cfg.StorageConnString == "" || cfg.EmailMode == "acs" {
		if cred, err = azidentity.NewDefaultAzureCredential(nil); err != nil {
			return fmt.Errorf("azure credential: %w", err)
		}
	}

	blobClient, err := blobs.NewBlobs(cfg.StorageAccountURL, cfg.StorageConnString, cred)
	if err != nil {
		return err
	}
	if cfg.CreateContainers {
		if err := blobClient.EnsureContainer(context.Background(), cfg.BillingContainer); err != nil {
			return err
		}
	}

	var sender handler.Sender = email.Log{}
	if cfg.EmailMode == "acs" {
		sender = &email.ACS{Endpoint: cfg.ACSEndpoint, Sender: cfg.EmailSender, Cred: cred}
	}
	var stripeClient *stripe.Client
	if cfg.StripeAPIKey == "" {
		slog.Warn("STRIPE_API_KEY is empty: checkout is off")
	} else {
		backends := stripe.NewBackendsWithConfig(&stripe.BackendConfig{HTTPClient: &http.Client{Timeout: stripeTimeout}})
		stripeClient = stripe.NewClient(cfg.StripeAPIKey, stripe.WithBackends(backends))
	}

	accounts := &billing.Accounts{Blobs: blobClient, Container: cfg.BillingContainer, FreeCreditsSecret: []byte(cfg.FreeCreditsSecret)}
	h := &handler.Handler{
		Store: &billing.Store{
			Blobs:           blobClient,
			Container:       cfg.BillingContainer,
			KeyPrefix:       cfg.KeyPrefix,
			TextPageCredits: cfg.TextPageCredits,
			OCRPageCredits:  cfg.OCRPageCredits,
		},
		Accounts:      accounts,
		Sessions:      &billing.Sessions{Blobs: blobClient, Container: cfg.BillingContainer, Accounts: accounts, TTL: sessionTTL},
		LoginTokens:   &billing.LoginTokens{Blobs: blobClient, Container: cfg.BillingContainer, TTL: loginTokenTTL},
		Limits:        &billing.RateLimiter{Blobs: blobClient, Container: cfg.BillingContainer},
		Locks:         &billing.Locks{Blobs: blobClient, Container: cfg.BillingContainer},
		Email:         sender,
		SiteURL:       cfg.SiteURL,
		Packs:         cfg.Packs,
		FreeCredits:   cfg.FreeCredits,
		MaxKeys:       int(cfg.MaxKeys),
		MaxCredits:    cfg.MaxCredits,
		Stripe:        stripeClient,
		WebhookSecret: cfg.StripeWebhookSecret,
		TestMode:      cfg.PaymentsTestMode,
	}

	app := sdk.FunctionApp()
	// Anonymous at the host level: the website's callers sign in with a
	// session (Authorization: Bearer), and Stripe signs its webhook events;
	// the handlers check both.
	for _, f := range []struct {
		name, route string
		handler     sdk.HTTPHandler
		methods     []string
	}{
		{"auth-start", "auth/start", h.Start, []string{"POST"}},
		{"auth-verify", "auth/verify", h.Verify, []string{"POST"}},
		{"auth-logout", "auth/logout", h.Logout, []string{"POST"}},
		{"me", "me", h.Me, []string{"GET", "DELETE"}},
		{"keys", "keys", h.Keys, []string{"GET", "POST", "DELETE"}},
		{"checkout", "checkout", h.Checkout, []string{"POST"}},
		{"stripe-webhook", "webhooks/stripe", h.StripeWebhook, []string{"POST"}},
	} {
		app.HTTP(f.name, f.handler,
			sdk.WithMethods(f.methods...),
			sdk.WithAuth("anonymous"),
			sdk.WithRoute(f.route),
		)
	}
	worker.Start(app)
	return nil
}
