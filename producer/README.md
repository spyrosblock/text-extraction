# producer

Azure Functions app (native Go worker, Flex Consumption) exposing `POST /api/extract`, plus
admin endpoints for API keys and credits.

Every request needs an API key in `X-API-Key`. Each key has its own credit balance; a PDF is
charged per page when it is submitted: **1 credit** for a page with a text layer, **5 credits** for
a page that needs OCR (`CREDITS_PER_TEXT_PAGE`, `CREDITS_PER_OCR_PAGE`).

1. Checks the key: `401` if missing or unknown, `402` if its balance is ≤ 0. This happens before the
   PDF is read; nothing is charged yet.
2. Reads the PDF (max 90MB, 200 pages) from the request:
   - raw body with `Content-Type: application/pdf` (or `application/octet-stream`), or
   - `multipart/form-data` with the PDF in the `file` field.
3. Extracts the text layer of every page with PDFium (WebAssembly, no cgo).
4. Charges the PDF's cost to the key. If the balance is lower, responds `402`
   `{"error","required","balance"}` and does nothing else.
5. If every page has text → stores `{"id","pages":[{"page","text","confidence":"-"}]}` as `<id>.json` in `text-storage`.
   Otherwise → stores the PDF as `<id>.pdf` in `pdf-storage` and sends `{"id","blob"}` to `pdf_queue`.
6. Responds `202 Accepted` with `{"id": "<uuid>", "status": "completed" | "queued", "credits": <charged>, "balance": <left>}`.

Errors: `400` (not a PDF / bad multipart), `401` (missing or unknown key), `402` (not enough
credits), `413` (>90MB or >200 pages), `415` (content type), `422` (PDF can't be opened), `500`,
`503` (too many concurrent requests on one key; retry).

Credits are never refunded: if storing the text, uploading the PDF or queueing it fails after the
charge (`500`), or OCR fails later, the credits stay spent. An OCR page that comes back empty is
still charged.

## API keys and credits

Keys are issued by hand. They are stored in the `billing` container of the data account:

| Blob | Content |
| --- | --- |
| `keys/<id>.json` | `{"name","credits","createdAt"}`; `id` is the hex sha256 of the key, which itself is never stored |
| `jobs/<job id>.json` | `{"keyId","pages","ocrPages","credits","createdAt"}`, so the consumer only serves a job to its key; deleted after a day |

Balance changes read the key blob and write it back with its ETag (`If-Match`), retrying up to 5
times on a conflict, so parallel requests can't spend the same credits. Each charge also logs a
`charged` line (key id, job id, pages, ocrPages, credits, balance) to App Insights: that is the usage
history.

Keys look like `tx_prod_` / `tx_stag_` (`KEY_PREFIX`) followed by 32 random bytes in base62.

Admin endpoints, callable only with the app's **master key** (`x-functions-key`). The Functions host
reserves routes starting with `admin`, hence `manage`:

| Endpoint | Does |
| --- | --- |
| `POST /api/manage/keys {"name","credits"}` | Creates a key: `201 {"key","id","name","credits"}`. The key is only shown here. |
| `GET /api/manage/keys` | Lists `[{"id","name","credits","createdAt"}]`. |
| `PUT /api/manage/credits?id=<id> {"credits"}` | Sets the balance. |
| `POST /api/manage/credits?id=<id> {"delta"}` | Adds to the balance (takes from it if negative). |
| `DELETE /api/manage/keys?id=<id>` | Revokes the key (`204`). |

[`infra/keys.sh`](../infra/keys.sh) wraps them:

```sh
infra/keys.sh rg-textextract-prod create acme 10000   # prints the key once
infra/keys.sh rg-textextract-prod list
infra/keys.sh rg-textextract-prod add <id> 5000
infra/keys.sh rg-textextract-prod set <id> 0
infra/keys.sh rg-textextract-prod revoke <id>
```

## Configuration

| Setting | Default | Notes |
| --- | --- | --- |
| `STORAGE_ACCOUNT_URL` | | `https://<account>.blob.core.windows.net`, managed identity |
| `SERVICEBUS_NAMESPACE` | | `<ns>.servicebus.windows.net`, managed identity |
| `AZURE_CLIENT_ID` | | set when using a user-assigned identity |
| `STORAGE_CONNECTION_STRING` | | local emulator only |
| `SERVICEBUS_CONNECTION_STRING` | | local emulator only |
| `PDF_STORAGE_CONTAINER` | `pdf-storage` | |
| `TEXT_STORAGE_CONTAINER` | `text-storage` | |
| `PDF_QUEUE_NAME` | `pdf_queue` | |
| `BILLING_CONTAINER` | `billing` | API keys, balances and job owners |
| `KEY_PREFIX` | `tx_dev_` | start of new keys (`tx_stag_`, `tx_prod_` in Azure) |
| `CREDITS_PER_TEXT_PAGE` | `1` | |
| `CREDITS_PER_OCR_PAGE` | `5` | |
| `PDFIUM_WORKERS` | CPU count | concurrent PDFium instances |
| `CREATE_CONTAINERS` | `false` | create blob containers on startup (local dev) |

Role assignments for the function app identity: **Storage Blob Data Contributor** on the data storage
account and **Azure Service Bus Data Sender** on the queue
([`infra/modules/roles`](../infra/modules/roles/main.tf)).

Blob container names can't contain underscores, so `pdf_storage`/`text_storage` from the spec
become `pdf-storage`/`text-storage`.

## Local development

Requires Go 1.26+, Docker and Azure Functions Core Tools 4.12+.

```sh
docker compose up -d            # from the repo root: Azurite + Service Bus emulator
cp local.settings.example.json local.settings.json
func start

# Host keys aren't enforced locally, so the admin endpoints need none.
curl -H 'Content-Type: application/json' -d '{"name": "me", "credits": 1000}' http://localhost:7071/api/manage/keys
curl -H "X-API-Key: <key>" -H 'Content-Type: application/pdf' --data-binary @doc.pdf http://localhost:7071/api/extract
curl -H "X-API-Key: <key>" -F file=@doc.pdf http://localhost:7071/api/extract
```

## Tests

```sh
go test ./...                                     # unit tests
go test -tags integration -run Integration .      # against the emulators
```

## Deploy

Deployed by the `staging` and `prod` GitHub Actions workflows (target `functions` or `all`), with the
app and its settings defined in [`infra/`](../infra). See [Deployment](../README.md#deployment).

In Azure `/api/extract` is anonymous at the host level and checks `X-API-Key` itself; the admin
endpoints need the master key.
