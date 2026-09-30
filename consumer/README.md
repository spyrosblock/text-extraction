# consumer

Azure Functions app (native Go worker, Flex Consumption) exposing `POST /api/text`.

The request body is the uuid returned by the producer:

```json
{"id": "<uuid>"}
```

The consumer reads
`<id>.json` from `text-storage` and returns it as-is:

```json
{"id": "<uuid>", "pages": [{"page": 1, "text": "..."}, {"page": 2, "text": "..."}]}
```

Responses: `200` with the document, `400` (body is not valid JSON or id is not a uuid), `404` (no text for the id: unknown,
OCR not finished yet, or expired), `500`.

The endpoint is anonymous (no function key): the uuid is the access key. The producer generates
random v4 uuids (122 random bits from `crypto/rand`), so ids can't be guessed, and anyone holding
an id can read its text for a day.

## Retention

Text is kept for one day. Blob lifecycle management deletes it
([`infra/text-storage-lifecycle.json`](infra/text-storage-lifecycle.json)):

```sh
az storage account management-policy create \
  --account-name <ACCOUNT> --resource-group <RG> \
  --policy @infra/text-storage-lifecycle.json
```

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
| `CREATE_CONTAINERS` | `false` | create the blob container on startup (local dev) |

Role assignment for the function app identity: **Storage Blob Data Reader** on the `text-storage`
container.

## Local development

Requires Go 1.26+, Docker and Azure Functions Core Tools 4.12+. It listens on port 7072 so it
can run next to the producer (7071).

```sh
docker compose up -d            # from the repo root
cp local.settings.example.json local.settings.json
func start

curl -X POST http://localhost:7072/api/text \
  -H 'Content-Type: application/json' -d '{"id": "<id>"}'
```

## Tests

```sh
go test ./...                                     # unit tests
go test -tags integration -run Integration .      # against Azurite
```

## Deploy

```sh
func azure functionapp publish <APP_NAME>
```
