# producer

Azure Functions app (native Go worker, Flex Consumption) exposing `POST /api/extract`.

1. Reads the PDF (max 90MB) from the request:
   - raw body with `Content-Type: application/pdf` (or `application/octet-stream`), or
   - `multipart/form-data` with the PDF in the `file` field.
2. Extracts the text layer of every page with PDFium (WebAssembly, no cgo).
3. If every page has text → stores `{"id","pages":[{"page","text"}]}` as `<id>.json` in `text-storage`.
   Otherwise → stores the PDF as `<id>.pdf` in `pdf-storage` and sends `{"id","blob"}` to `pdf_queue`.
4. Responds `202 Accepted` with `{"id": "<uuid>", "status": "completed" | "queued"}`.

Errors: `400` (not a PDF / bad multipart), `413` (>90MB), `415` (content type), `422` (PDF can't be opened), `500`.

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
| `PDFIUM_WORKERS` | CPU count | concurrent PDFium instances |
| `CREATE_CONTAINERS` | `false` | create blob containers on startup (local dev) |

Role assignments for the function app identity: **Storage Blob Data Contributor** on the storage account
and **Azure Service Bus Data Sender** on the queue.

Blob container names can't contain underscores, so `pdf_storage`/`text_storage` from the spec
become `pdf-storage`/`text-storage`.

## Local development

Requires Go 1.26+, Docker and Azure Functions Core Tools 4.12+.

```sh
docker compose up -d            # from the repo root: Azurite + Service Bus emulator
cp local.settings.example.json local.settings.json
func start

curl -H 'Content-Type: application/pdf' --data-binary @doc.pdf http://localhost:7071/api/extract
curl -F file=@doc.pdf http://localhost:7071/api/extract
```

## Tests

```sh
go test ./...                                     # unit tests
go test -tags integration -run Integration .      # against the emulators
```

## Deploy

```sh
func azure functionapp publish <APP_NAME>
```
