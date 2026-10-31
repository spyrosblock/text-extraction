package main

import (
	"strings"
	"testing"
)

func setValidEnv(t *testing.T) {
	t.Helper()
	for k, v := range map[string]string{
		"STORAGE_CONNECTION_STRING": "UseDevelopmentStorage=true",
		"SITE_URL":                  "https://site.example",
		"EMAIL_MODE":                "log",
		"FREE_CREDITS_SECRET":       strings.Repeat("s", 32),
		"PACKS":                     `{"starter":{"credits":2000,"price_eur":10,"price_id":"price_a"},"pro":{"credits":12000,"price_eur":50,"price_id":"price_b"}}`,
		"PAYMENTS_TEST_MODE":        "true",
		"STRIPE_API_KEY":            "rk_test_abc",
		"STRIPE_WEBHOOK_SECRET":     "whsec_abc",
	} {
		t.Setenv(k, v)
	}
}

func TestLoadConfig(t *testing.T) {
	setValidEnv(t)
	c, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !c.PaymentsTestMode || c.Packs["pro"].PriceID != "price_b" || c.StripeWebhookSecret != "whsec_abc" || c.MaxCredits != 20000 {
		t.Errorf("config = %+v", c)
	}
}

func TestLoadConfigChecks(t *testing.T) {
	for name, c := range map[string]struct {
		env  map[string]string
		want string // "" = valid
	}{
		"no webhook secret":   {map[string]string{"STRIPE_WEBHOOK_SECRET": ""}, "STRIPE_WEBHOOK_SECRET is required"},
		"no test mode":        {map[string]string{"PAYMENTS_TEST_MODE": ""}, "PAYMENTS_TEST_MODE must be true or false"},
		"live key in test":    {map[string]string{"STRIPE_API_KEY": "rk_live_abc"}, "live-mode key but PAYMENTS_TEST_MODE is true"},
		"test key in live":    {map[string]string{"PAYMENTS_TEST_MODE": "false"}, "test-mode key but PAYMENTS_TEST_MODE is false"},
		"live key in live":    {map[string]string{"PAYMENTS_TEST_MODE": "false", "STRIPE_API_KEY": "rk_live_abc"}, ""},
		"secret key":          {map[string]string{"STRIPE_API_KEY": "sk_test_abc"}, ""},
		"publishable key":     {map[string]string{"STRIPE_API_KEY": "pk_test_abc"}, "must be a Stripe secret or restricted key"},
		"no packs":            {map[string]string{"PACKS": ""}, "PACKS is required"},
		"shared price":        {map[string]string{"PACKS": `{"a":{"credits":1,"price_id":"price_a"},"b":{"credits":2,"price_id":"price_a"}}`}, "share price_id price_a"},
		"no price id":         {map[string]string{"PACKS": `{"a":{"credits":1}}`}, `pack "a" needs a price_id`},
		"no price id, no key": {map[string]string{"PACKS": `{"a":{"credits":1}}`, "STRIPE_API_KEY": ""}, ""},
		"no key in test mode": {map[string]string{"STRIPE_API_KEY": ""}, ""},
		"no key in live mode": {map[string]string{"STRIPE_API_KEY": "", "PAYMENTS_TEST_MODE": "false"}, ""},
		"pack over the cap":   {map[string]string{"MAX_ACCOUNT_CREDITS": "10000"}, `pack "pro" has more credits than MAX_ACCOUNT_CREDITS (10000)`},
		"pack at the cap":     {map[string]string{"MAX_ACCOUNT_CREDITS": "12000"}, ""},
		"no cap":              {map[string]string{"MAX_ACCOUNT_CREDITS": "0"}, ""},
		"negative cap":        {map[string]string{"MAX_ACCOUNT_CREDITS": "-1"}, "MAX_ACCOUNT_CREDITS must be an integer ≥ 0"},
	} {
		t.Run(name, func(t *testing.T) {
			setValidEnv(t)
			for k, v := range c.env {
				t.Setenv(k, v)
			}
			_, err := loadConfig()
			if c.want == "" {
				if err != nil {
					t.Errorf("err = %v", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("err = %v, want %q", err, c.want)
			}
		})
	}
}
