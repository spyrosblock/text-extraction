# account

Azure Functions app (native Go worker, Flex Consumption) behind the website: magic-link sign-in,
the customer's API keys, and buying credit packs with Stripe Checkout and its webhook. Keys and
balances live in the `billing` container, shared with the producer through the
[`billing`](../billing) module, so a key created here works with `/api/extract` and `/api/text` at
once.

Payments use **Stripe Managed Payments**: Stripe, through Link, is the merchant of record. The
customer pays on Stripe's hosted Checkout page (no Stripe.js on our site); a signed webhook credits
the pack. Staging runs in Stripe test mode (`PAYMENTS_TEST_MODE=true`), prod in live mode.

## Endpoints

Every endpoint is anonymous at the host level. "session" means `Authorization: Bearer <session>`
from `/api/auth/verify`; a missing, expired or unknown session, or one whose account was deleted,
gets `401`. Errors are `{"error": "..."}`.

| Endpoint | Auth | Does |
| --- | --- | --- |
| `POST /api/auth/start {"email"}` | none | Emails a sign-in link. Always `202`; `400` only for a malformed address. |
| `POST /api/auth/verify {"token"}` | none | `200 {"session","email"}`, creating the account on first sign-in; `401` for an unknown, used or expired token. |
| `POST /api/auth/logout` | session | Ends the session. `204`. |
| `GET /api/me` | session | `{"email","test_mode","prices":{"text_page","ocr_page"},"packs":{"<name>":{"credits","price_eur"}},"max_keys","max_credits","free_credits_available"}` |
| `DELETE /api/me` | session | Revokes the account's keys and deletes the account. `204`. |
| `GET /api/keys` | session | `[{"key_id","name","hint","credits","created_at"}]`, oldest first. |
| `POST /api/keys {"name"}` | session | `201 {"key","key_id","name","hint","credits"}`; the key is only shown here. `409` at `MAX_KEYS`. |
| `DELETE /api/keys?key_id=` | session | Revokes a key; its credits are lost. `204`, or `404`. |
| `POST /api/checkout {"pack","key_id"}` | session | `200 {"url"}`, a Stripe Checkout page for the pack, to be credited to the key. `400` unknown pack, `404` not the account's key, `409` over `MAX_ACCOUNT_CREDITS`, `429` over 10 per hour, `502` Stripe error, `503` without `STRIPE_API_KEY`. |
| `POST /api/webhooks/stripe` | `Stripe-Signature` | Stripe's events: credits or debits the order's key. `400` for a bad signature or the wrong mode. |

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

**Checkout.** The pack must be in `PACKS` (`400`) and the key the account's (`404`), and the
account's credits, summed over all its keys, plus the pack's must not exceed `MAX_ACCOUNT_CREDITS`
(`409`, with a message naming the cap and the balance); then a rate limit of 10 per hour per
account (`429`), since each call creates a session at Stripe. Sessions opened in parallel all pass
this check, so the webhook checks the cap again when one is paid and refunds what no longer fits. The
Checkout Session (`mode=payment`, `managed_payments[enabled]=true`, the pack's `price_id`, quantity
1) carries `metadata` `account_id`, `key_id` and `pack`, `client_reference_id` = the key id,
`customer_email` = the account's address, returns to `SITE_URL/dashboard.html?paid=1` (or
`dashboard.html` on cancel) and expires after an hour. Tax, payment methods, invoices, receipts and
statement descriptors are left to Managed Payments. Each request gets a new idempotency key (the
SDK's retries reuse it). A Stripe error is logged with its type, code and request id.

**Webhook.** `webhook.ConstructEvent` checks the HMAC-SHA256 signature with
`STRIPE_WEBHOOK_SECRET`, a 5-minute timestamp tolerance, and that the event's API release train is
the SDK's (`2026-10-28.endive`, so create the endpoint on that version). `livemode` must be
`!PAYMENTS_TEST_MODE`. Then:

| Event | Does |
| --- | --- |
| `checkout.session.completed`, `checkout.session.async_payment_succeeded` | When `payment_status` is `paid`: credits the pack's credits (from `PACKS`, by `metadata.pack`) to `metadata.key_id` as order `payment_intent`, once (`billing.CreditOrder`). An `unpaid` session (a delayed payment method) is credited by its later `async_payment_succeeded`. With `MAX_ACCOUNT_CREDITS` set, a new order that would take the account's credits, summed over its keys, over the cap is recorded on the key as `overCap` with 0 credits (`billing.RefuseOrder`) and refunded in full (`POST /v1/refunds`, `metadata.reason` = `credit_cap`, idempotency key `overcap-<payment_intent>`; `charge_already_refunded` counts as done). The check and the credit run under a per-account lock blob (`locks/credits-<account id>.json`, 30 s, waited for up to 5 s, else `503`), so of parallel orders only those that fit are credited. A retried event keeps the order's first outcome. |
| `charge.refunded` | Finds the session by `payment_intent` (`GET /v1/checkout/sessions`) and debits `ceil(credits × amount_refunded / amount)` in all for the order (`billing.DebitOrder`): `amount_refunded` is cumulative, so a repeated or out-of-order event debits nothing more. An `overCap` order has nothing to debit. |
| `charge.dispute.closed` | When `status` is `lost`: the same lookup, then debits all the order's credits. Stripe fights disputes; we act on the result. |
| anything else | `200`, logged. |

The balance may go negative after a refund; the producer then answers `402` until the key is
topped up. Every credit and debit is logged as `stripe order` (account, key, order, event type,
pack, credits, balance).

Stripe retries an event without a 2xx for up to 3 days, so failures a retry can fix (blob
errors, the Stripe lookup, a key that kept changing) answer `500`/`503`. What a retry can't fix
answers `200` and logs an **error**: an unknown pack, a revoked key or an account that doesn't own
the key (`stripe order not credited`; the payer's email is logged so support can move the credits
with `infra/keys.sh add`, or refund), and a refund for an order the key doesn't have
(`stripe order not debited`).

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
| `PACKS` | | required; JSON `{"<name>":{"credits":…,"price_eur":…,"price_id":"price_…"}}`. `price_eur` is only displayed. No two packs may share a `price_id`; with `STRIPE_API_KEY` set, every pack needs one |
| `FREE_CREDITS` | `100` | per email address, once |
| `FREE_CREDITS_SECRET` | | required, at least 32 bytes; never change it (every address would get free credits again) |
| `MAX_KEYS` | `2` | per account |
| `MAX_ACCOUNT_CREDITS` | `20000` | most credits an account can hold, summed over its keys: checkout refuses a pack over it, the webhook refunds a paid one; `0`: no cap. Every pack must fit in it |
| `PAYMENTS_TEST_MODE` | | required, `true` or `false`: Stripe test or live mode |
| `STRIPE_API_KEY` | | restricted key, Checkout Sessions write and read, Refunds write; must match the mode (`rk_test_…`/`rk_live_…`). Empty: checkout answers `503` |
| `STRIPE_WEBHOOK_SECRET` | | required, `whsec_…` of the webhook endpoint |

ACS Email is called with a managed-identity token for `https://communication.azure.com/.default`.

## Local development

Requires Go 1.26+, Docker and Azure Functions Core Tools 4.12+. It listens on port 7073, next to
the producer (7071) and the consumer (7072), and allows CORS from the local website on port 8000
(`localhost` or `127.0.0.1`).

```sh
docker compose up -d            # from the repo root
cp local.settings.example.json local.settings.json
func start
```

To buy packs locally, set `STRIPE_API_KEY` to a test-mode restricted key and the packs' test-mode
`price_id`s in `local.settings.json`, and forward Stripe's events with the
[Stripe CLI](https://docs.stripe.com/stripe-cli):

```sh
stripe listen --forward-to localhost:7073/api/webhooks/stripe \
  --events checkout.session.completed,checkout.session.async_payment_succeeded,charge.refunded,charge.dispute.closed
```

Put the `whsec_…` it prints in `STRIPE_WEBHOOK_SECRET` and restart `func start`. Buy a pack from the
dashboard with the card `4242 4242 4242 4242` (Link passcode `000000` if asked); `stripe events
resend <evt id>` must not change the balance, and refunds from the Dashboard take credits back. The
CLI forwards events in the Stripe account's default API version; the app answers `400` to events
outside the `endive` release train (the log says which version arrived).

With `EMAIL_MODE=log` the sign-in link is in the log (`email (not sent, EMAIL_MODE=log)`).

```sh
curl -X POST http://localhost:7073/api/auth/start -H 'Content-Type: application/json' -d '{"email": "me@example.com"}'
# open the logged link, or post its token:
curl -X POST http://localhost:7073/api/auth/verify -H 'Content-Type: application/json' -d '{"token": "<token>"}'
curl -X POST http://localhost:7073/api/keys -H "Authorization: Bearer <session>" \
  -H 'Content-Type: application/json' -d '{"name": "laptop"}'
curl -X POST http://localhost:7073/api/checkout -H "Authorization: Bearer <session>" -H 'Content-Type: application/json' \
  -d '{"pack": "starter", "key_id": "<key_id>"}'
```

## Tests

```sh
go test ./...
```

The Stripe tests use a fake Stripe API (`httptest`, through the SDK's backend URL) and the events
in [`internal/handler/testdata`](internal/handler/testdata), signed in the test with
`webhook.GenerateTestSignedPayload`. Those bodies follow Stripe's event shape; replace them with
events captured in test mode (Dashboard, or `stripe events retrieve`) once the account exists,
keeping the `__KEY_ID__`/`__ACCOUNT_ID__` placeholders in the session metadata.

The blob-level conditions (single-use tokens, one free-credit claim) are also tested against
Azurite in [`billing`](../billing): `go test -tags integration -run Integration .` there.

## Deploy

Deployed by the `staging` and `prod` GitHub Actions workflows, with the app and its settings
defined in [`infra/`](../infra). See [Deployment](../README.md#deployment).
