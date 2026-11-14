---
name: text-extraction
description: Extracts the text of PDF files, scanned pages included (OCR), with the text-extraction API at {{SITE_URL}}. Use when the user wants the text or content of a PDF, or to OCR a scanned PDF. If the user has no API key yet, walks them through getting one.
---

# Text extraction

Two endpoints, called with `curl`:

- Submit a PDF: `POST {{PRODUCER_API}}/api/extract`
- Fetch its text: `POST {{CONSUMER_API}}/api/text`

Both need the user's API key in the `X-API-Key` header.

## 1. Get an API key

Use the key if the user has already given one in this conversation, or if the environment variable
`TEXTEXTRACT_API_KEY` is set. Otherwise, tell the user how to get one:

1. Open {{SITE_URL}}/login.html and enter your email address.
2. Click the sign-in link in the email (check spam if it doesn't arrive in a minute).
3. On the dashboard, choose **Create key** and copy the key. It is shown only once.
4. Paste the key here.

Tell them as well:

- Keys start with `tx_`.
- The first key of an email address comes with 100 free credits.
- More credits are bought with **Buy credits** next to the key on the dashboard.

Send the key only in the `X-API-Key` header, and only to the two hosts above. Don't repeat it back in
full; refer to it by its last 4 characters. Shell variables may not persist between your commands,
so put the key in each command if needed.

## 2. Cost and limits

- 1 credit per page with a text layer; 5 credits per page that needs OCR (scanned pages).
- Credits are charged when the PDF is submitted and are never refunded, even if it fails later.
- At most 90MB and 200 pages per PDF. PDFs only: other formats are rejected.
- Before sending a large PDF, tell the user it may cost up to 5 credits per page.

## 3. Submit the PDF

```sh
curl -sS -w '\n%{http_code}\n' -H 'X-API-Key: <key>' -H 'Content-Type: application/pdf' \
  --data-binary @document.pdf {{PRODUCER_API}}/api/extract
```

For a PDF at a URL, download it with `curl -sSLo document.pdf <url>` first.

A success is `202`:

```json
{"request_id": "<uuid>", "status": "completed", "credits": 3, "balance": 97}
```

`status` is `completed` when every page had a text layer, `queued` when some pages go to OCR.

## 4. Fetch the text

```sh
curl -sS -w '\n%{http_code}\n' -H 'X-API-Key: <key>' -H 'Content-Type: application/json' \
  -d '{"request_id": "<uuid>"}' {{CONSUMER_API}}/api/text
```

- `completed`: the text is ready right away.
- `queued`: `404` means OCR isn't finished. Retry about every 15 seconds, for up to about 10
  minutes. If it is still `404`, give the user the `request_id` so they can fetch it later.

The text is kept for one day, and only the key that submitted the PDF can fetch it.

A success is `200`:

```json
{"request_id": "<uuid>", "pages": [{"page": 1, "text": "...", "confidence": "-"}, {"page": 2, "text": "...", "confidence": 87.46}]}
```

## 5. Present the result

- Give the text in page order, unless the user asked for something else from it.
- `confidence` is `"-"` for text from the PDF's text layer. For an OCRed page it is a number from 0 to
  100. Point out OCRed pages below about 60 (the text may have errors), and pages with `0` and
  empty text (nothing could be read).
- Tell the user the credits charged and the balance left.

## Errors

| Status | Meaning and what to do |
| --- | --- |
| `400` | Not a PDF, or a malformed request. |
| `401` | The key is wrong or revoked. Ask for a new one from {{SITE_URL}}/dashboard.html. |
| `402` | Not enough credits. The body has `required` and `balance` (when a PDF was read). Tell the user to buy credits at {{SITE_URL}}/dashboard.html. |
| `404` | From `/api/text`: OCR not finished yet (retry), or the id is unknown, from another key or expired. |
| `413` | Over 90MB or 200 pages. Suggest splitting the PDF. |
| `415` | Wrong `Content-Type`. Send `application/pdf`. |
| `422` | The PDF can't be opened (damaged or encrypted). |
| `429` | The service's daily request limit is reached. `Retry-After` gives the seconds until it resets. |
| `500` | Server error. Credits may already be spent; tell the user. |
| `503` | Busy. Wait a few seconds and retry. |
