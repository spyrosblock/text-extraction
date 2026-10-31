# web

The website, served by an Azure Static Web App (Free tier): plain HTML, one `style.css` and one
`app.js`, no build step and no third-party scripts. `app.js` runs the code for the page named by
`<body data-page="...">`; it builds the DOM with `textContent` only.

| Page | Does |
| --- | --- |
| `index.html` | What the API does, prices, packs (from `config.js`) and a `curl` quick start |
| `login.html` | Email form; always answers "Check your inbox" for a well-formed address |
| `auth.html` | Reads `#token` from the magic link, drops it from the address, posts it to `/api/auth/verify` and keeps the session in `localStorage` |
| `dashboard.html` | The account's keys: create (the key is shown once), revoke, buy credits; sign out, delete the account |
| `terms.html` | Terms of service: prepaid credits that don't expire, Stripe/Link as merchant of record, acceptable use, no SLA, liability, Greek law |
| `privacy.html` | What is stored, for how long, how to delete it, and what Stripe/Link process |
| `refunds.html` | Refunds of unused credits within 14 days, what isn't refunded, what a refund or chargeback does to the key |

**Buying credits.** "Buy credits" on a key opens a dialog with the packs; packs that would take
the account's credits, summed over its keys, over `max_credits` (from `/api/me`) are disabled, with
a note naming the cap, and a `409` from the server shows its message. "Continue to payment"
posts `/api/checkout` and navigates to the Stripe Checkout URL it returns (a top-level navigation,
so the CSP needs no Stripe host). Stripe returns to `dashboard.html?paid=1`: the page says the
payment was received, reloads the keys every 5 seconds until a balance goes up (for up to a
minute), then drops `?paid=1` from the address.

**Test mode.** The banner ("pay with the test card 4242 4242 4242 4242") shows only in Stripe test
mode: on the dashboard from `/api/me`'s `test_mode`, on the other pages from `window.TEST_MODE`.
Prod pages have no banner.

**Legal text.** The terms, privacy and refunds pages are drafts to review before launch; the
operator's identity in `terms.html` is a highlighted placeholder (`.todo`). Every page's footer
links them and shows the contact address (`window.CONTACT_EMAIL`).

Any `401` from the account app clears the session and goes to `login.html`.

## Generated at deploy

Not committed (`.gitignore`); the `staging`/`prod` workflows write them with
[`.github/scripts/build-web.sh`](../.github/scripts/build-web.sh), which also leaves `README.md` and
`config.local.js` out of the upload:

- `config.js`: `window.ACCOUNT_API`, `window.PRODUCER_API` and `window.CONSUMER_API`, the apps'
  `https://` hosts; `window.TEST_MODE` (Stripe test mode, the `testMode` output); `window.PACKS`
  (the packs without their Stripe prices, `publicPacks`); `window.PRICES_INCLUDE_VAT` (the "incl.
  VAT" / "VAT added at checkout" label, `pricesIncludeVat`); `window.CONTACT_EMAIL`
  (`contactEmail`).
- `staticwebapp.config.json`: the CSP (`default-src 'self'`, `connect-src` limited to the account
  app, `frame-ancestors 'none'`), `Referrer-Policy: no-referrer` and `X-Content-Type-Options:
  nosniff`. The CSP allows no inline scripts or styles, so keep both in `app.js` and `style.css`.

## Local development

With the account app running on port 7073 (see [account](../account/README.md#local-development)):

```sh
cp config.local.js config.js
python3 -m http.server 8000 --bind 127.0.0.1
```

Open <http://localhost:8000> (or `127.0.0.1`); the account app's local CORS setting allows only
those two origins, so `http://0.0.0.0:8000` won't work. With `EMAIL_MODE=log` the sign-in link is in the account app's log.
