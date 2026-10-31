# account

Azure Functions app (native Go worker, Flex Consumption) behind the website: magic-link sign-in,
the customer's API keys and **mock** purchases of credit packs. Keys and balances live in the
`billing` container, shared with the producer through the [`billing`](../billing) module, so a
key created here works with `/api/extract` and `/api/text` at once.

Payments are simulated (`PAYMENTS_MODE=mock`): no card is asked for and no money moves. Every
purchase must carry `MOCK_TOKENS_PASSWORD`, so only whoever holds it can add credits.

## Endpoints

Every endpoint is anonymous at the host level. "session" means `Authorization: Bearer <session>`
from `/api/auth/verify`; a missing, expired or unknown session, or one whose account was deleted,
gets `401`. Errors are `{"error": "..."}`.

| Endpoint | Auth | Does |
| --- | --- | --- |
| `POST /api/auth/start {"email"}` | none | Emails a sign-in link. Always `202`; `400` only for a malformed address. |
| `POST /api/auth/verify {"token"}` | none | `200 {"session","email"}`, creating the account on first sign-in; `401` for an unknown, used or expired token. |
| `POST /api/auth/logout` | session | Ends the session. `204`. |
| `GET /api/me` | session | `{"email","payments":"mock","prices":{"text_page","ocr_page"},"packs":{…},"max_keys","free_credits_available"}` |
| `DELETE /api/me` | session | Revokes the account's keys and deletes the account. `204`. |
| `GET /api/keys` | session | `[{"key_id","name","hint","credits","created_at"}]`, oldest first. |
| `POST /api/keys {"name"}` | session | `201 {"key","key_id","name","hint","credits"}`; the key is only shown here. `409` at `MAX_KEYS`. |
| `DELETE /api/keys?key_id=` | session | Revokes a key; its credits are lost. `204`, or `404`. |
| `POST /api/purchases {"pack","key_id","order_id","password"}` | session + password | `200 {"order_id","credits","balance"}`. `403` for a missing or wrong password, `429` after too many wrong ones. |

**Sign-in.** The link is `SITE_URL/auth.html#token=<token>`, valid 15 minutes and usable once
(the token blob is deleted with `If-Match`, so of two parallel verifies one wins). The token is in
the fragment, so it never reaches a server log or a `Referer`. `/api/auth/start` answers `202`
whether or not the address has an account, the email could be sent or a rate limit was hit (3 per
15 minutes per address, 20 per hour per IP), so it can't be used to probe accounts. Sessions last
7 days. Only the sha256 of tokens and session ids is stored, and addresses are logged as their
sha256 (`emailHash`).

**Free credits.** Each email address gets `FREE_CREDITS` once, on the first key its account
creates. A marker named after the HMAC of the address (keyed with `FREE_CREDITS_SECRET`) records
the grant. It is created with `If-None-Match: *`, so parallel key creations can't both get the
credits, and it is kept when the key is revoked or the account deleted.

**Ownership.** An account owns a key when the key is in its index (`accounts/<id>/keys/<key id>`)
and the key blob's `accountId` names it. Anything else, including keys issued with
`infra/keys.sh`, is `404`, so key ids can't be probed.

**Mock purchases.** Checked in this order:
1. `PAYMENTS_MODE` must be `mock` (otherwise `404`).
2. Password: an empty `MOCK_TOKENS_PASSWORD` turns purchases off (`403`). After 5 wrong passwords
   in 15 minutes for the account, or 20 in an hour from the IP, purchases get `429`, even with the
   right password. A wrong or missing password is `403` and counted. The password is never logged.
3. `pack` must be in `PACKS`, `order_id` a UUID (`400`), and the key the account's (`404`).
4. The pack's credits (from `PACKS`, never from the request) are added and the order recorded in
   the key's `orders`. A repeated `order_id` adds nothing and returns the current balance, so a
   retry is safe. There is no limit on purchases.

## Configuration

| Setting | Default | Notes |
| --- | --- | --- |
| `STORAGE_ACCOUNT_URL` | | `https://<account>.blob.core.windows.net`, managed identity |
| `AZURE_CLIENT_ID` | | set when using a user-assigned identity |
| `STORAGE_CONNECTION_STRING` | | local emulator only |
| `BILLING_CONTAINER` | `billing` | |
| `CREATE_CONTAINERS` | `false` | create the container on startup (local dev) |
| `KEY_PREFIX` | `tx_dev_` | same as the producer's |
| `CREDITS_PER_TEXT_PAGE`, `CREDITS_PER_OCR_PAGE` | `1`, `5` | shown by `/api/me`; the producer charges |
| `SITE_URL` | | required; the website the links point to |
| `EMAIL_MODE` | `acs` | `acs`, or `log` to log the link instead (local only) |
| `ACS_ENDPOINT` | | `https://<acs>.communication.azure.com`, for `acs` |
| `EMAIL_SENDER` | | `DoNotReply@<domain>`, for `acs` |
| `PAYMENTS_MODE` | `mock` | the only accepted value |
| `PACKS` | starter 2000 / €10, pro 12000 / €50 | JSON `{"<name>":{"credits":…,"price_eur":…}}` |
| `FREE_CREDITS` | `100` | per email address, once |
| `FREE_CREDITS_SECRET` | | required, at least 32 bytes; never change it (every address would get free credits again) |
| `MOCK_TOKENS_PASSWORD` | | empty = purchases off |
| `MAX_KEYS` | `2` | per account |

ACS Email is called with a managed-identity token for `https://communication.azure.com/.default`.

## Local development

Requires Go 1.26+, Docker and Azure Functions Core Tools 4.12+. It listens on port 7073, next to
the producer (7071) and the consumer (7072), and allows CORS from the local website on port 8000.

```sh
docker compose up -d            # from the repo root
cp local.settings.example.json local.settings.json
func start
```

With `EMAIL_MODE=log` the sign-in link is in the log (`email (not sent, EMAIL_MODE=log)`).

```sh
curl -X POST http://localhost:7073/api/auth/start -H 'Content-Type: application/json' -d '{"email": "me@example.com"}'
# open the logged link, or post its token:
curl -X POST http://localhost:7073/api/auth/verify -H 'Content-Type: application/json' -d '{"token": "<token>"}'
curl -X POST http://localhost:7073/api/keys -H "Authorization: Bearer <session>" \
  -H 'Content-Type: application/json' -d '{"name": "laptop"}'
curl -X POST http://localhost:7073/api/purchases -H "Authorization: Bearer <session>" -H 'Content-Type: application/json' \
  -d '{"pack": "starter", "key_id": "<key_id>", "order_id": "'"$(uuidgen)"'", "password": "local-test-password"}'
```

## Tests

```sh
go test ./...
```

The blob-level conditions (single-use tokens, one free-credit claim) are also tested against
Azurite in [`billing`](../billing): `go test -tags integration -run Integration .` there.

## Deploy

Deployed by the `staging` and `prod` GitHub Actions workflows, with the app and its settings
defined in [`infra/`](../infra). See [Deployment](../README.md#deployment).
