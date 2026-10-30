# PLAN2_1: API keys and credits

## Context

Today every caller of `/api/extract` shares the app's function key (`producer/main.go:135`), and `/api/text` is anonymous (`consumer/main.go:85`). There is no way to tell callers apart or limit how much they use.

This plan adds **per-key credits**:
- Each customer gets an API key. Keys are issued **by hand** by us; there is no sign-up, website or payment.
- Each key has its own credit balance, also **set by hand**.
- Every PDF submitted with a key is charged per page from that key's balance.

**Decisions:**
- **Pricing:** a page with a text layer costs **1 credit**; a page that needs OCR costs **5 credits**. Both are app settings (`CREDITS_PER_TEXT_PAGE`, `CREDITS_PER_OCR_PAGE`).
  - The producer already extracts each page's text, so it knows the exact cost before any work is queued.
- **When credits are taken:** at submission.
- **One key = one balance.** A customer who needs two keys gets two balances.
- **Storage:** JSON blobs in a new private `billing` container in the existing data account, updated with ETag (`If-Match`) checks. This reuses the blob private endpoint and the producer's existing Blob Data Contributor role, so there is no new private endpoint, DNS zone, table or bootstrap change.
- **Admin:** a few endpoints on the producer app that only the Azure **master key** can call (`sdk.WithAuth("admin")`), plus a small script that wraps them. No new function app.
- **ocr_container is unchanged.**

---

## Step 1: Data model (`billing` container)

| Blob | Content | Written by |
|---|---|---|
| `keys/<sha256(key)>.json` | `{"name","credits","createdAt"}` | producer (charge), admin endpoints |
| `jobs/<jobId>.json` | `{"keyId","pages","ocrPages","credits","createdAt"}` | producer, once per job |

- **The key itself is never stored.** The blob name is the hex sha256 of the key; this hash is also the **key id** used by the admin endpoints.
- **Balance changes** read the key blob and its ETag, then write it back with `If-Match`. On HTTP 412, retry up to 5 times with jitter, then return 503.
- **Job blobs** record who owns a job so the consumer can check it. A lifecycle rule deletes them after a day, like the text blobs.
- **Usage history** is the `charged` log line written by the producer for each job (key id, job id, pages, ocrPages, credits, balance), queryable in App Insights.

## Step 2: Key format

- `tx_prod_` (prod) or `tx_stag_` (staging), followed by 32 random bytes in base62. The prefix comes from the `KEY_PREFIX` setting.
- The full key is returned once, when it is created.

## Step 3: producer

**New package `producer/internal/credits`**
- `Lookup(key) (Key, etag, error)`, `Charge(keyID, credits) (balance, error)`, `Create(name, credits) (key, keyID)`, `Set(keyID, credits)`, `Add(keyID, delta)`, `Delete(keyID)`, `List()`.
- `Cost(pages, ocrPages)` from the two price settings.
- Built on a small blob interface (get with ETag, conditional upload, delete, list), so unit tests use a fake like the existing handler tests. `producer/internal/azure` gains the matching `Blobs` methods.

**`/api/extract` (`handler.go`, `main.go`)**
- **Auth:** switch to `sdk.WithAuth("anonymous")`. Requests must send `X-API-Key`.
  - Missing or unknown key: **401**.
  - Balance ≤ 0: **402**.
  - Both checks happen **before** the 90 MB body is read and pdfium runs (today the body is read at `handler.go:77`). The page count isn't known yet, so this is only an early reject that saves work; nothing is charged.
- **Cost:** after extraction, `ocrPages` is the number of pages with no text, and the cost is `Cost(len(pages), ocrPages)`.
- **Charge:** `Charge` the cost, then write the job blob. If the balance is lower than the full cost, return **402** `{"error","required","balance"}` and do nothing else.
  - This is the check that counts. `Charge` checks `balance ≥ cost` on the blob it read and writes the new balance with `If-Match`. If a parallel request changed the balance in between, the write fails with 412 and the check is redone on the fresh balance, so two requests can't both spend the same credits.
- **Text-layer job:** upload the text as today. If the upload fails, return 500; the credits are not returned.
- **OCR job:** upload the PDF and send the message as today. If either fails, return 500; the credits are not returned. The existing delete-PDF-on-send-failure path stays.
- **Response:** adds `credits` (charged) and `balance`. The status codes for success stay as they are.
- **New settings:** `BILLING_CONTAINER` (default `billing`), `CREDITS_PER_TEXT_PAGE`, `CREDITS_PER_OCR_PAGE`, `KEY_PREFIX`. `CREATE_CONTAINERS` also creates `billing` locally.

**Admin endpoints** (`sdk.WithAuth("admin")`; query strings rather than route parameters, explicit `sdk.WithMethods`). They live under `/api/manage/` because the Functions host reserves routes that start with `admin`, so each one sets `sdk.WithRoute("manage/…")`.

| Endpoint | Does |
|---|---|
| `POST /api/manage/keys {name, credits}` | Creates a key. Returns `{"key","id","name","credits"}`; the key is only shown here. |
| `GET /api/manage/keys` | Lists `{"id","name","credits","createdAt"}`. |
| `PUT /api/manage/credits?id= {credits}` | Sets the balance. |
| `POST /api/manage/credits?id= {delta}` | Adds to (or, if negative, takes from) the balance. |
| `DELETE /api/manage/keys?id=` | Revokes the key by deleting its blob. |

## Step 4: consumer

- Requires `X-API-Key`. It hashes the key and reads `keys/<hash>.json` (no ETag needed).
- Before reading the text, it reads `jobs/<id>.json` and checks that `keyId` matches.

| Case | Status |
|---|---|
| No or unknown key | 401 `{"error"}` |
| Job blob missing, or owned by another key | 404 `{"error"}` (404 rather than 403, so job ids can't be probed) |
| Everything else | Unchanged (200, or 404 while OCR runs or after expiry) |

- The consumer has no `credits` package, only a short key lookup in its handler. The hashing must match the producer's (the same one-liner; note it next to `contract.go`'s "keep it in sync").
- **New setting:** `BILLING_CONTAINER`.

## Step 5: Terraform (`infra/`)

- **`modules/storage-data`:**
  - Add a `billing` container (private).
  - Lifecycle rule `expire-job-records`: delete `billing/jobs/` after 1 day.
  - Output its container id.
- **`modules/roles`:** consumer gets Blob Data Reader on the `billing` container. The producer already has Blob Data Contributor on the whole data account.
- **`main.tf`:** add `BILLING_CONTAINER`, `CREDITS_PER_*` and `KEY_PREFIX` to the producer's app settings, and `BILLING_CONTAINER` to the consumer's.
- **`variables.tf` / tfvars:** `key_prefix` (`tx_stag_` in staging, `tx_prod_` in prod), `credits_per_text_page`, `credits_per_ocr_page`.

`bootstrap.sh` doesn't need to be re-run: no new providers or role types.

## Step 6: Admin script `infra/keys.sh`

Gets the producer's master key the same way `smoke.sh` gets the function key (`az functionapp keys list … --query masterKey`), then calls the admin endpoints:

```
infra/keys.sh <rg> create <name> <credits>
infra/keys.sh <rg> list
infra/keys.sh <rg> set <id> <credits>
infra/keys.sh <rg> add <id> <delta>
infra/keys.sh <rg> revoke <id>
```

## Step 7: Smoke test (`infra/smoke.sh`)

1. Get the producer's master key (existing retry loop), create a key with 2,000 credits, and revoke it in a `trap`.
2. Run `test1.pdf` with `X-API-Key`: expect `completed` and `credits` = its page count.
3. Run `test2.pdf`: expect `queued` and `credits` = 5 × its OCR page count, then poll the consumer with the key as today.
4. Negative checks:
   - The producer without a key returns 401.
   - The consumer asked for the job with a second fresh key returns 404.
   - A key with 0 credits gets 402 from the producer.

---

## Order of work
1. `producer/internal/credits` with unit tests, and the new `azure.Blobs` methods.
2. Producer enforcement and admin endpoints.
3. Consumer enforcement.
4. Terraform: container, lifecycle rule, role, settings.
5. `keys.sh`, `smoke.sh`, and README/SPEC updates (`X-API-Key`, 401/402, `credits`/`balance` in the response, admin endpoints).

## Verification
- **Unit tests** (`go test ./...` in `producer` and `consumer`):
  - Charge with enough and too few credits; the ETag retry on 412; an upload or send failure returns 500 and leaves the balance charged.
  - 401 and 402 happen before the body is read.
  - The consumer returns 401 for a bad key and 404 for another key's job.
  - Admin create returns a key whose hash is the blob name; set and add change the balance.
- **Local** (`docker compose up -d`, then `func start` in each app; host keys aren't enforced locally): create a key through the admin endpoint, submit `test1.pdf` and `test2.pdf` with curl, check the balance, and read the text back from the consumer.
- **Staging:** run `staging` with target `all`; `smoke.sh` must pass.
- **`terraform plan`** on a PR shows the new container, lifecycle rule change, role assignment and in-place app-setting updates only.

## Known limits (fine for manual issuance)
- Credits are never refunded: a request that fails after the charge (upload, send, or OCR) keeps its credits spent.
- An OCR page that comes back empty with confidence 0 (no words found, or the page could not be OCRed) is still charged 5 credits.
- Many parallel requests on one key contend on its ETag; the retries cover normal use.
- Job history beyond a day lives only in the App Insights logs.
