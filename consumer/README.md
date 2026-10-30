# consumer

Azure Functions app (native Go worker, Flex Consumption) exposing `POST /api/text`.

The request needs the API key that submitted the PDF in `X-API-Key`, and its body is the uuid
returned by the producer:

```json
{"request_id": "<uuid>"}
```

The consumer checks that the key exists (`keys/<sha256 of key>.json` in the `billing` container)
and that the job was submitted with it (`jobs/<id>.json`, written by the producer), then reads
`<id>.json` from `text-storage` and returns it as-is:

```json
{"request_id": "<uuid>", "pages": [{"page": 1, "text": "...", "confidence": "-"}, {"page": 2, "text": "...", "confidence": 87.46}]}
```

Every page of the PDF is listed. `confidence` is `"-"` when the text comes from the PDF's text
layer. For an OCRed page it is Tesseract's mean word confidence, a number from 0 to 100, and the
page carries whatever text was read, however low the confidence. `0` with empty text means no
words were found or the page couldn't be OCRed.

Responses: `200` with the document, `401` (missing or unknown key), `400` (body is not valid JSON
or `request_id` is not a uuid), `404` (no text for the id: unknown, submitted with another key, OCR not
finished yet, or expired), `500`. Another key's job is `404` rather than `403`, so ids can't be
probed.

The endpoint is anonymous at the host level (no function key) and checks `X-API-Key` itself. Only
the key that submitted a PDF can read its text, for a day. The key id is the hex sha256 of the key,
computed the same way as the producer's `credits.KeyID`; keep the two in sync.

## Retention

Text is kept for one day. A blob lifecycle rule on the data account deletes it; it is deployed
with the rest of the infrastructure
([`infra/modules/storage-data`](../infra/modules/storage-data/main.tf)).

Lifecycle policies run about once a day, so a blob can outlive its day by up to ~24h. The
consumer returns `404` for any blob last modified more than 24h ago, so the limit holds
either way.

## Configuration

| Setting | Default | Notes |
| --- | --- | --- |
| `STORAGE_ACCOUNT_URL` | | `https://<account>.blob.core.windows.net`, managed identity |
| `AZURE_CLIENT_ID` | | set when using a user-assigned identity |
| `STORAGE_CONNECTION_STRING` | | local emulator only |
| `TEXT_STORAGE_CONTAINER` | `text-storage` | |
| `BILLING_CONTAINER` | `billing` | API keys and job owners, written by the producer |
| `CREATE_CONTAINERS` | `false` | create the blob containers on startup (local dev) |

Role assignments for the function app identity: **Storage Blob Data Reader** on the `text-storage`
and `billing` containers ([`infra/modules/roles`](../infra/modules/roles/main.tf)).

## Local development

Requires Go 1.26+, Docker and Azure Functions Core Tools 4.12+. It listens on port 7072 so it
can run next to the producer (7071).

```sh
docker compose up -d            # from the repo root
cp local.settings.example.json local.settings.json
func start

curl -X POST http://localhost:7072/api/text -H "X-API-Key: <key>" \
  -H 'Content-Type: application/json' -d '{"request_id": "<id>"}'
```

## Tests

```sh
go test ./...                                     # unit tests
go test -tags integration -run Integration .      # against Azurite
```

## Deploy

Deployed by the `staging` and `prod` GitHub Actions workflows (target `functions` or `all`), with the
app and its settings defined in [`infra/`](../infra). See [Deployment](../README.md#deployment).
