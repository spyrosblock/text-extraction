# PLAN2: accounts, payments and a web dashboard

## Context

PLAN2_1 added API keys with per-key credit balances:
- Keys and balances live as JSON blobs in the private `billing` container.
- The producer charges each PDF from its key's balance.
- The consumer only returns a job's text to the key that submitted it.
- We issue keys and set their balances by hand with `infra/keys.sh`, which calls admin endpoints on the producer.

That is enough to bill a few customers by hand, but it can't be sold yet:
- **No customers or payments:** a customer can't sign up, buy credits or create their own keys.

The goal is to sell OCR as an API with **prepaid credits**:
1. A customer signs in on a small website with an emailed magic link.
2. They create API keys.
3. They buy credit packs for a key through Lemon Squeezy, which is the Merchant of Record and handles VAT and invoices.
4. Every PDF they submit is charged per page from that key's balance, as PLAN2_1 already does.

**Decisions:**
- **Payments:** Lemon Squeezy, with hosted checkout and webhooks. Staging uses a test-mode store; prod uses a live store.
- **Pricing:** a page with a text layer costs **1 credit**; a page that needs OCR costs **5 credits**. Both are app settings (`CREDITS_PER_TEXT_PAGE`, `CREDITS_PER_OCR_PAGE`).
  - The producer already extracts each page's text, so it knows the exact cost before any work is queued.
- **When credits are taken:** at submission. Credits are never refunded (see Known limits).
- **One key = one balance.** An account can own several keys, each with its own balance. A purchase tops up the key the customer picks.
- **Storage:** JSON blobs in the existing private `billing` container, updated with ETag (`If-Match`) checks, as in PLAN2_1. The account data goes there too. This reuses the blob private endpoint, so there is no new private endpoint, DNS zone or table.
- **Admin:** PLAN2_1's admin endpoints on the producer, which only the Azure **master key** can call (`sdk.WithAuth("admin")`), and `infra/keys.sh`. The account app has no admin endpoints.
- **ocr_container is unchanged.**
- **Login:** a magic-link email that we build ourselves, sent with Azure Communication Services (ACS) Email.
- **Web:** Azure Static Web Apps, Free tier, plain HTML/JS with no build step.
- **Infrastructure:** Terraform only (`infra/`).
- **Secrets:** no Key Vault in v1. The Lemon Squeezy secrets are GitHub environment secrets, passed as sensitive Terraform variables into app settings. They end up in the Terraform state, which is already Entra-only.

---

## Architecture

```
 web/ (Static Web App, Free, eastus2)          customers' scripts
   landing, pricing, login, dashboard               │  X-API-Key
          │ Bearer session                           ▼
          ▼                                  producer /api/extract ──▶ pdf_queue ──▶ ocr job
 func account (new)                          consumer /api/text                     (unchanged)
   /api/auth/*, /api/me, /api/keys,                 │ keys/, jobs/
   /api/checkout,                                   ▼
   /api/webhooks/lemonsqueezy  ◀── Lemon Squeezy   billing container (data account, private)
          │ keys/, accounts/, emails/, sessions/, …  ▲
          ├──────────────────────────────────────────┘
          └──▶ ACS Email (magic links)

 infra/keys.sh ──▶ producer admin endpoints (master key)
```

---

## Step 1: Data model (`billing` container)

PLAN2_1's two blob types stay as they are; the key blob gains two optional fields. The other blobs are new.

| Blob | Content | Written by |
|---|---|---|
| `keys/<keyId>.json` | `{"name","credits","createdAt","accountId","orders"}` | producer (charge), admin endpoints, account app |
| `jobs/<jobId>.json` | `{"keyId","pages","ocrPages","credits","createdAt"}` | producer, once per job (PLAN2_1) |
| `accounts/<accountId>.json` | `{"email","createdAt","signupUsed"}` | account app |
| `accounts/<accountId>/keys/<keyId>` | empty: an index of the account's keys | account app |
| `emails/<sha256(email)>.json` | `{"accountId"}` | account app |
| `sessions/<sha256(token)>.json` | `{"accountId","expiresAt"}` (7 days) | account app |
| `logintokens/<sha256(token)>.json` | `{"email","expiresAt"}` (15 min) | account app |
| `ratelimits/<email\|ip>/<value>/<bucket>.json` | `{"count"}` | account app |

**Rules:**
- **`keyId` is the hex sha256 of the key**, and the key itself is never stored (PLAN2_1).
- **New key fields:**
  - `accountId` is empty for keys issued with `keys.sh`.
  - `orders` maps a Lemon Squeezy order id to `{credits, debited}`. It makes webhooks idempotent (Step 4).
- **Unknown fields must be kept.** The producer's `Charge` reads the key blob and writes it back, so the `Key` struct must carry the new fields. Otherwise a charge would erase them.
- **Balance changes** read the key blob and its ETag, then write it back with `If-Match`. On HTTP 412, retry up to 5 times with jitter, then return 503 (PLAN2_1). Purchases and order refunds use the same path.
- **Uniqueness and single use come from conditional writes:**
  - The `emails/` blob is created with `If-None-Match: *`, so it acts as a uniqueness lock.
  - A login token is deleted with `If-Match`, so only one verify can succeed.
  - Rate-limit counters are incremented with `If-Match`.
- **Email addresses are never used in a blob name directly.** The name uses the sha256 hash; IP addresses are used as they are.
- **Expiry uses lifecycle rules, not a timer.** These rules delete blobs a fixed number of days after they were last modified:

  | Prefix | Deleted after |
  |---|---|
  | `jobs/` | 1 day (PLAN2_1) |
  | `logintokens/` | 1 day |
  | `ratelimits/` | 1 day |
  | `sessions/` | 8 days |

  Lifecycle runs about once a day, so readers also check `expiresAt`.
- **The container is created by Terraform** (PLAN2_1). Apps create it only locally (`CREATE_CONTAINERS`).

---

## Step 2: Shared Go module `billing/`

Move `producer/internal/credits` into a new module, `github.com/spyrosblock/text-extraction/billing`. The producer and account apps use it via `replace … => ../billing`. `func pack` builds inside the full checkout, so the relative replace works. Check this once in CI.

- **Unchanged from PLAN2_1:** `Lookup`, `Charge`, `Create`, `Set`, `Add`, `Delete`, `List` and `Cost`, plus the small blob interface and its fake (`producer/internal/blobtest`).
- **Changes:**
  - `Create` takes an optional `accountId`.
  - `Key` gains `AccountID` and `Orders`.
  - New `CreditOrder(keyID, orderID, credits)` and `DebitOrder(keyID, orderID, debit)` change the balance and `orders` in the same `If-Match` write (Step 4).
- **Key format** (PLAN2_1): `tx_prod_` (prod) or `tx_stag_` (staging), followed by 32 random bytes in base62. The prefix comes from `KEY_PREFIX`. The full key is shown once.
- **Account helpers** in a separate file of the same module: accounts, emails, sessions, login tokens and rate limits, each built on the same blob interface.
- Unit tests use the existing blob fake. Integration tests run against Azurite blobs behind `//go:build integration`.

The consumer doesn't import `billing`. It keeps its own one-line key lookup (PLAN2_1). `ocr_container` doesn't import it either.

---

## Step 3: producer and consumer

PLAN2_1 already does the enforcement: `X-API-Key`, 401/402, the charge at submission, and the consumer's job-owner check. This plan only changes the following:
- **producer:** imports `billing` instead of `internal/credits`. The admin endpoints stay; `GET` and `keys.sh list` also show each key's `accountId`.
- **consumer:** no change. It reads `keys/<hash>.json` and ignores the new fields.

## Step 4: New function app `account/`

The same layout as producer and consumer: native Go worker, Flex, its own identity and subnet, 2048 MB.
- **Route parameters:** the preview worker doesn't reliably pass route parameters. Use query strings instead, e.g. `DELETE /api/keys?id=`.
- **HTTP methods:** the default is GET and POST only, so pass `sdk.WithMethods(...)` explicitly.
- **No Service Bus trigger and no timer.** Refunds aren't issued, and lifecycle rules handle expiry.

**Auth (magic link)**
- `POST /api/auth/start {email}`
  1. Normalize and validate the address.
  2. Apply rate limits: 3 per 15 min per email, 20 per hour per IP. The IP is the first entry of `X-Forwarded-For`.
  3. Store `logintokens/<sha256(token)>.json` with a 15-minute expiry. The token is 32 random bytes.
  4. Email `https://<site>/auth.html#token=<token>` via the ACS Email REST API (`emails:send`), using the managed identity.
  5. Always return **202**, so the response can't be used to check whether an email has an account.
  6. Locally, set `EMAIL_MODE=log` to log the link instead of sending it.
- `POST /api/auth/verify {token}`
  1. Read the token blob, check it hasn't expired, then delete it with `If-Match`. Only one parallel verify call can succeed.
  2. Find or create the account:
     - Create the `emails/` blob first with `If-None-Match: *`. If it already exists, use its `accountId`.
     - Then create `accounts/<accountId>.json`. Creating it again is harmless, so a retry after a crash is safe.
  3. Return an opaque session token. Only its sha256 hash is stored, in `sessions/`, with a 7-day expiry.
- `POST /api/auth/logout`: deletes the session.
- All dashboard endpoints require `Authorization: Bearer <session>`. A session whose account blob is gone gets 401.

**Dashboard API**
- `GET /api/me`: the email and prices.
- `GET /api/keys`: the account's keys (from its index), each with name, last 4 characters and balance.
- `POST /api/keys {name}`: creates a key with `billing.Create` and the account's id, and writes the index blob.
  - The first key an account creates starts with `SIGNUP_CREDITS` (default 50); later keys start at 0. Setting `signupUsed` on the account blob with `If-Match` makes this one-time.
  - The new key is returned once.
- `DELETE /api/keys?id=`: revokes a key the account owns. Its remaining credits are lost, and the dashboard says so before revoking.
- `DELETE /api/me`: deletes the account, its keys, its index and its email blob (GDPR). Its sessions stop working because the account blob is gone, and lifecycle removes them. Lemon Squeezy keeps its own order records as Merchant of Record.
- There is no usage history endpoint. Usage history is the producer's `charged` log lines in App Insights (PLAN2_1).

**Payments (Lemon Squeezy)**
- `POST /api/checkout {pack, keyId}`
  - `keyId` must be in the account's index; otherwise return 404.
  - `pack` is a name from the server-side `LS_PACKS` map (pack → variantId and credits). Client-sent amounts are never trusted.
  - Calls `POST https://api.lemonsqueezy.com/v1/checkouts` with `custom.account_id`, `custom.key_id`, a prefilled email and `redirect_url=<site>/dashboard.html?paid=1`.
  - Returns the checkout URL.
- `POST /api/webhooks/lemonsqueezy` (anonymous)
  1. Verify `X-Signature` = HMAC-SHA256 of the raw body using `LS_WEBHOOK_SECRET`, with a constant-time compare.
  2. Reject the request if `meta.test_mode` doesn't match the environment.
  3. `order_created` with status `paid`: look up the pack's credits by `first_order_item.variant_id` in `LS_PACKS`. Then `CreditOrder` the key in `custom.key_id`, if the key still exists and its `accountId` matches `custom.account_id`.
     - If the order id is already in `orders`, nothing changes.
     - If the key was revoked, log it and return 200. Support moves the credits to another key by hand with `keys.sh add`.
  4. `order_refunded`: compute `ceil(credits × refunded_amount / total)` and `DebitOrder` the difference from what that order already debited. The balance may go negative, which blocks new jobs with 402. A repeated webhook debits nothing more.
  5. Return 200 for duplicates.

**CORS:** `allowedOrigins` is the SWA's default hostname plus the custom domain.

## Step 5: `web/` (Static Web App)

Plain HTML, CSS and vanilla JS. There is no build step and no third-party scripts.
- `index.html`: landing page with pricing and an API quick start (`curl` examples).
- `login.html`: email form. It shows "check your inbox" whatever happens.
- `auth.html`: reads `#token`, calls `history.replaceState` to drop it from the URL, posts it to verify, saves the session in `localStorage`, then redirects to the dashboard.
- `dashboard.html`: API keys with each one's balance and buy-pack buttons, create (with show-once) and revoke, logout and delete account.
- `terms.html`, `privacy.html` and `refunds.html`: Lemon Squeezy needs these pages before it activates the store. You write the text. The refund page must say that used credits aren't refunded.
- **Generated at deploy:**
  - `config.js`: the account API base URL.
  - `staticwebapp.config.json`: a strict CSP (`default-src 'self'; connect-src 'self' https://<account host>`), plus `Referrer-Policy: no-referrer`, `X-Content-Type-Options: nosniff` and `frame-ancestors 'none'`.

---

## Step 6: Terraform (`infra/`)

**New resources in `infra/main.tf`**
- **Identity:** `account` added to `module.identities` and `local.deployment_containers`.
- **Account function app:** `module "account"` (functionapp), 2048 MB, subnet `account`.
  - Settings: `STORAGE_ACCOUNT_URL`, `BILLING_CONTAINER`, `KEY_PREFIX`, `CREDITS_PER_TEXT_PAGE`, `CREDITS_PER_OCR_PAGE`, `SIGNUP_CREDITS`, `SITE_URL`, `ACS_ENDPOINT`, `EMAIL_SENDER`, `LS_STORE_ID`, `LS_PACKS`, `LS_API_KEY` and `LS_WEBHOOK_SECRET`.
- **Email:** a new `modules/email` containing `azurerm_communication_service` (data_location `Europe`), `azurerm_email_communication_service`, a domain and an association.
  - Staging uses `AzureManaged` (low send limits, fine for testing).
  - Prod uses `CustomerManaged` with `var.email_domain`. The SPF, DKIM and verification records are added by hand in your DNS.
- **Static Web App:** a new `modules/web` containing `azurerm_static_web_app`, Free tier, in **eastus2**. Static Web Apps isn't offered in northeurope, and westeurope rejects new resources for this subscription. The region only holds metadata; content is served globally.
  - Optional `azurerm_static_web_app_custom_domain` when `var.web_domain` is set.

**Changes to existing modules**
- **`modules/storage-data`:** lifecycle rules for `billing/logintokens/`, `billing/ratelimits/` and `billing/sessions/` (Step 1). The `billing` container and the `jobs/` rule already exist (PLAN2_1).
- **`modules/network`:** new subnet `snet-func-account` = `cidrsubnet(p,4,3)` (`x.x.0.192/26`, free). The app reaches the `billing` container through the existing blob private endpoint.
- **`modules/functionapp`:** new input `cors_allowed_origins` (default `[]`), written to `siteConfig.cors.allowedOrigins`.
- **`modules/roles`:**
  - Add `account` to the `principal_ids` object type.
  - Add assignments for `account`:
    - Blob Data Contributor on the `billing` container.
    - Blob Data Owner on its host storage.
    - Monitoring Metrics Publisher.
    - The ACS email sender role on the communication service.

**Variables and outputs**
- **`variables.tf` / tfvars:**
  - `email_domain`, `web_domain`, `lemonsqueezy_store_id`, `lemonsqueezy_packs` (map) and `signup_credits`.
  - `key_prefix` and `credits_per_*` already exist (PLAN2_1).
  - Sensitive: `lemonsqueezy_api_key` and `lemonsqueezy_webhook_secret`, defaulting to `""` so the PR plan job still works.
- **`outputs.tf`:** `accountName`, `accountHostName`, `staticWebAppName` and `staticWebAppHostName`.

**Bootstrap** (re-run `infra/bootstrap.sh` once before the first deploy)
- Add `Microsoft.Communication` to `resource_providers_to_register` (`infra/bootstrap/versions.tf`).
- Add the ACS email sender role's GUID to `modules/roles/roles.json`, so the deployment identity may assign it. Confirm that role's built-in name and GUID while implementing.
- No table roles are needed.

## Step 7: Workflows and smoke test

**`staging.yml` / `prod.yml`**
- Add `account` to the function build matrix (`:33`), the deploy steps (`:166-177`) and the "check deployed" loop (`:203`).
- Add `billing/go.sum` to the `setup-go` `cache-dependency-path`.
- New `web` target: generate `config.js` and `staticwebapp.config.json` from the Terraform outputs.
  - Get the deploy token with `az staticwebapp secrets list --query properties.apiKey`.
  - Then run `Azure/static-web-apps-deploy@v1` with `action: upload`, `app_location: web` and `skip_app_build: true`.
- Pass `TF_VAR_lemonsqueezy_api_key` and `TF_VAR_lemonsqueezy_webhook_secret` from the GitHub **environment secrets** `LEMONSQUEEZY_API_KEY` and `LEMONSQUEEZY_WEBHOOK_SECRET`.

**`ci.yml`:** add `billing` and `account` to the Go matrix.

**`infra/smoke.sh`:** PLAN2_1's checks stay unchanged: a key created with the producer's master key, then the charges, the 401/402/404 checks and the revoke in a `trap`. Add:
1. `GET /api/me` on the account app without a session returns 401.
2. The webhook with a wrong signature returns 401.
3. The Static Web App serves `index.html`.

---

## Launch checklist (outside the code)
1. Buy a domain.
   - Point `www`/apex at the SWA, and add the ACS SPF, DKIM and verification records.
   - Lemon Squeezy store activation and email deliverability both need it.
2. In Lemon Squeezy, create a test-mode store for staging and a live store for prod.
   - Create the credit-pack products. The prices below are only examples to adjust: Starter 2,000 credits for €10, Pro 12,000 for €50.
   - Create a webhook to `https://<account host>/api/webhooks/lemonsqueezy` for `order_created` and `order_refunded`.
   - Put the store id and variant ids in tfvars, and the API key and signing secret in GitHub environment secrets.
3. Write the terms, privacy and refund pages.
4. Register the business as a sole proprietor (έναρξη εργασιών) before going live.

## Order of work
1. Move `producer/internal/credits` into the `billing/` module, add the new key fields and order methods, and add the account helpers, with unit and Azurite tests.
2. Terraform: lifecycle rules, subnet, roles, account app skeleton, email, SWA. Re-run bootstrap.
3. The `account` app's auth and keys endpoints.
4. Checkout and the webhook.
5. `web/` pages and the `web` workflow target.
6. smoke.sh and README/SPEC updates (sign-up, dashboard, purchases).

## Verification
- **Unit tests** (`go test ./...` in `billing`, `producer`, `consumer` and `account`):
  - PLAN2_1's tests still pass after the move.
  - A charge keeps `accountId` and `orders`.
  - `CreditOrder` and `DebitOrder`: a duplicate order is a no-op, a partial refund followed by a full refund debits only the difference, and the ETag retry works.
  - Signup credits apply to the first key only, including when two keys are created in parallel.
  - Two parallel verify calls for a new email create a single account.
  - Checkout and `DELETE /api/keys` return 404 for another account's key.
  - Webhook HMAC with valid and tampered signatures, and the `test_mode` mismatch.
  - Magic-link expiry and single use.
  - Rate limits.
- **Local** (`docker compose up -d`, then `func start` for each app):
  - With `EMAIL_MODE=log`: sign up, create a key in the dashboard, submit a text PDF and an OCR PDF with curl, and check the key's balance.
  - Post a signed sample `order_created` webhook twice and check that the key was credited once.
- **Staging:**
  - Run `staging` with target `all`; `smoke.sh` must pass.
  - Then do a manual purchase in the test-mode store with Lemon Squeezy's test card. Check that the credits appear on the chosen key, and that a refund from the Lemon Squeezy dashboard debits them.
- **`terraform plan`** on a PR shows only additions, plus in-place updates to the network module and the data account's lifecycle policy.

## Known limits
- Credits are never refunded: a request that fails after the charge (upload, send or OCR) keeps its credits spent (PLAN2_1).
- An OCR page that comes back empty with confidence 0 is still charged 5 credits (PLAN2_1).
- The consumer returns the same 404 for "pending", "failed" and "expired", so a customer can't tell a failed job from a slow one.
- Revoking a key, or deleting the account, forfeits its remaining credits. Credits can't be moved between keys from the dashboard; support can do it with `keys.sh`.
- Many parallel requests on one key contend on its ETag; the retries cover normal use (PLAN2_1).
- Job history beyond a day lives only in the App Insights logs, and the dashboard shows no usage table.

---

How it works:
- Credits: a page with a text layer costs 1 credit and a page that needs OCR costs 5. Each key has its own balance, charged when the PDF is submitted, as PLAN2_1 already does. Credits are never refunded.
- Login: a new account function app sends magic-link emails through Azure Communication Services Email, with rate limits and single-use tokens. The same app runs the dashboard API, the Lemon Squeezy checkout and its webhook.
- Keys: customers create and revoke their own keys in the dashboard. Each purchase tops up the key they pick. Support still uses `infra/keys.sh` and the producer's admin endpoints.
- Storage: everything lives as JSON blobs in the existing private `billing` container, with ETag checks and lifecycle rules. No tables and no new private endpoint.
- Website: plain HTML on a free Static Web App with a strict content security policy. It has landing/pricing, login, dashboard and the legal pages Lemon Squeezy requires.
- Terraform: adds the new app, its subnet and roles, email, the Static Web App and three lifecycle rules. You'll need to re-run infra/bootstrap.sh once for the Communication provider and the email sender role.
- Unchanged: the consumer, ocr_container, and the producer apart from the module move.

Before or during implementation, you'll need to sort out:
- A domain. Both Lemon Squeezy store approval and reliable email sending depend on it.
- The Static Web App region. It goes in eastus2: Static Web Apps isn't offered in northeurope, and westeurope rejects new resources on your subscription. The region only holds metadata; the pages are served worldwide.
- The email role. The built-in Azure role that lets the app send email still needs confirming, and is marked as such in the plan.
- Pack prices. €10 for 2,000 credits and €50 for 12,000 are only placeholders.
