# web

The website, served by an Azure Static Web App (Free tier): plain HTML, one `style.css` and one
`app.js`, no build step and no third-party scripts. `app.js` runs the code for the page named by
`<body data-page="...">`; it builds the DOM with `textContent` only.

| Page | Does |
| --- | --- |
| `index.html` | What the API does, prices, packs and a `curl` quick start |
| `login.html` | Email form; always answers "Check your inbox" for a well-formed address |
| `auth.html` | Reads `#token` from the magic link, drops it from the address, posts it to `/api/auth/verify` and keeps the session in `localStorage` |
| `dashboard.html` | The account's keys: create (the key is shown once), revoke, and the mock checkout; sign out, delete the account |
| `privacy.html` | What is stored, for how long, and how to delete it |

Every page shows the test-mode banner. The checkout creates its `order_id` with
`crypto.randomUUID()` when it opens and reuses it when a payment is retried. The test-mode password
is kept in memory for the open page only, never in `localStorage`.

Any `401` from the account app clears the session and goes to `login.html`.

## Generated at deploy

Not committed (`.gitignore`); the `staging`/`prod` workflows write them with
[`.github/scripts/build-web.sh`](../.github/scripts/build-web.sh), which also leaves `README.md` and
`config.local.js` out of the upload:

- `config.js`: `window.ACCOUNT_API`, `window.PRODUCER_API` and `window.CONSUMER_API`, the apps'
  `https://` hosts.
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
