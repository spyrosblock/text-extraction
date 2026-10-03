# ocr_container

Azure Container Apps job (event-driven) that drains `pdf_queue`.

For each message `{"id","blob"}` sent by the producer it:

1. Downloads `<id>.pdf` from `pdf-storage`.
2. Reads the text layer of every page with PDFium (WebAssembly, no cgo).
3. Renders each page without a text layer to a 300 DPI grayscale image and runs Tesseract
   (`eng+ell`) on it. Pages are OCRed in parallel, `OCR_WORKERS` at a time.
4. Leaves a page's text empty when the OCR is unreadable: no words found, or mean word
   confidence below `OCR_MIN_CONFIDENCE`. The page stays in the output so page numbers line up.
5. Stores `{"id","pages":[{"page","text"}]}` as `<id>.json` in `text-storage` (the same format
   the producer writes), then deletes the PDF.

Each job execution takes messages one at a time and exits once the queue has been idle for
`IDLE_TIMEOUT`. KEDA starts more executions while messages are waiting.

### Failure handling

| Case | Result |
| --- | --- |
| Success | message completed |
| Transient error (storage, Tesseract crash, SIGTERM) | message abandoned; Service Bus redelivers it and dead-letters it after `MaxDeliveryCount` (5) |
| Bad message, id isn't a uuid, blob isn't `<id>.pdf`, PDF missing or unreadable | dead-lettered right away |
| PDF missing but `<id>.json` exists (earlier delivery finished) | completed |

A dead-lettered or expired message leaves its PDF in `pdf-storage`. A lifecycle rule
(`expire-abandoned-pdfs` in [`infra/modules/storage-data.bicep`](../infra/modules/storage-data.bicep))
deletes PDFs 7 days after they were last modified.

The message lock is renewed every `LOCK_RENEW_INTERVAL` so large PDFs can take longer than the
queue's 5 minute lock. If a renewal fails, processing stops and the message is abandoned.

## Configuration

| Setting | Default | Notes |
| --- | --- | --- |
| `STORAGE_ACCOUNT_URL` | | `https://<account>.blob.core.windows.net`, managed identity |
| `SERVICEBUS_NAMESPACE` | | `<ns>.servicebus.windows.net`, managed identity |
| `AZURE_CLIENT_ID` | | client id of the user-assigned identity |
| `STORAGE_CONNECTION_STRING` | | local emulator only |
| `SERVICEBUS_CONNECTION_STRING` | | local emulator only |
| `PDF_STORAGE_CONTAINER` | `pdf-storage` | |
| `TEXT_STORAGE_CONTAINER` | `text-storage` | |
| `PDF_QUEUE_NAME` | `pdf_queue` | |
| `OCR_WORKERS` | CPU count | pages OCRed in parallel |
| `OCR_LANGUAGES` | `eng+ell` | Tesseract `-l` |
| `OCR_DPI` | `300` | render resolution |
| `OCR_MIN_CONFIDENCE` | `60` | 0-100, pages below are skipped |
| `IDLE_TIMEOUT` | `30s` | exit after this long without messages; `0` never exits |
| `LOCK_RENEW_INTERVAL` | `2m` | keep below the queue lock duration |
| `MAX_MESSAGES` | `0` | stop after N messages; `0` = no limit |
| `CREATE_CONTAINERS` | `false` | create blob containers on startup (local dev) |

Role assignments for the job's identity: **Storage Blob Data Contributor** on the data storage
account, and **Azure Service Bus Data Receiver** plus **Azure Service Bus Data Owner** on the queue
([`infra/modules/roles.bicep`](../infra/modules/roles.bicep)). The KEDA scaler reads the queue
length, which needs Data Owner.

## Local development

`docker compose up -d --build` from the repo root starts the emulators and this container, set to
run continuously (`IDLE_TIMEOUT=0`). With the producer and consumer running (`func start`):

```sh
curl -H 'Content-Type: application/pdf' --data-binary @scanned.pdf http://localhost:7071/api/extract
# {"id":"<id>","status":"queued"}
docker compose logs -f ocr
curl -X POST http://localhost:7072/api/text -H 'Content-Type: application/json' -d '{"id": "<id>"}'
```

## Tests

```sh
go test ./...                                     # unit tests (OCR test skipped without tesseract)
go test -tags integration -run Integration .      # against the emulators, needs tesseract
```

Tesseract isn't needed on the host; the tests can run in a container:

```sh
docker compose stop ocr   # it would take the integration test's message
docker run --rm --network host -v "$PWD":/src -w /src golang:1.26-trixie sh -c \
  'apt-get update && apt-get install -y tesseract-ocr tesseract-ocr-eng tesseract-ocr-ell &&
   go test ./... && go test -tags integration -run Integration .'
```

## Deploy

The job is defined in [`infra/modules/ocr-job.bicep`](../infra/modules/ocr-job.bicep). The
`staging` workflow (target `ocr` or `all`) builds the image, pushes it to
`ghcr.io/spyrosblock/ocr-container:<commit sha>` and points the job at it; `prod` deploys the same
tag. See [Deployment](../README.md#deployment).

The replica retry limit is 0 because Service Bus already retries failed messages. Keep the replica
timeout (3600 s) above the time the largest PDF takes; an execution handles several messages before
it goes idle.
