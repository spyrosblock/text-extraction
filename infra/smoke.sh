#!/usr/bin/env bash
# End-to-end smoke test of one environment (PLAN.md, Step 3). Used by
# staging.yml; also run by hand:
#
#   az login
#   terraform -chdir=infra init -backend-config=... # for staging
#   infra/smoke.sh rg-textextract-staging
#
# 0. Creates an API key with 2000 credits and one with none through the
#    producer's admin endpoints; both are revoked on exit.
# 1. POSTs a PDF with a text layer to the producer: expects "completed", then
#    its text from the consumer, and one credit charged per page.
# 2. POSTs a scanned PDF: expects "queued", then the OCR job's text from the
#    consumer, and 5 credits charged per OCRed page. Needs the OCR image
#    deployed (target ocr), not the quickstart one.
# 3. Expects 401 from the producer without a key, 404 from the consumer for
#    another key's job, and 402 from the producer for the key without credits.
# 4. Account app and website (PLAN2_2, PLAN2_3): 401 from /api/me and
#    /api/checkout without a session and from /api/auth/verify for a random
#    token, 404 from the removed /api/purchases, 400 from
#    /api/webhooks/stripe for a wrong signature; the Static Web App serves
#    index.html with the CSP header, a config.js that names the account app,
#    and the terms, privacy and refunds pages, and index.html on the custom
#    domain when siteUrl is one. No email is sent and no checkout is created:
#    sign-in and payments are checked by hand.
#
# Reads the app names and host names from `terraform output -json` in
# infra/ (or from OUTPUTS, as staging.yml passes them), and the
# producer's master key from ARM, so az needs Contributor on the RG.
#
# Overrides: OUTPUTS, TEXT_PDF, SCANNED_PDF, TEXT_TIMEOUT, OCR_TIMEOUT
# (seconds), TEXT_PAGE_CREDITS and OCR_PAGE_CREDITS (the environment's
# prices, default 1 and 5).
set -euo pipefail

RG=${1:?usage: $0 <resource-group>}
ROOT=$(cd "$(dirname "$0")/.." && pwd)
TEXT_PDF=${TEXT_PDF:-$ROOT/samples/text.pdf}
SCANNED_PDF=${SCANNED_PDF:-$ROOT/samples/scanned.pdf}
TEXT_TIMEOUT=${TEXT_TIMEOUT:-60}
# KEDA polls every 30 s, then the execution pulls the image and OCRs every page.
OCR_TIMEOUT=${OCR_TIMEOUT:-1200}
TEXT_PAGE_CREDITS=${TEXT_PAGE_CREDITS:-1}
OCR_PAGE_CREDITS=${OCR_PAGE_CREDITS:-5}

log() { printf '\n==> %s\n' "$*"; }
fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }

outputs=${OUTPUTS:-$(terraform -chdir="$ROOT/infra" output -json)}
output() { jq -er ".$1.value" <<<"$outputs"; }
producer_name=$(output producerName)
producer_url="https://$(output producerHostName)/api/extract"
consumer_url="https://$(output consumerHostName)/api/text"
manage_url="https://$(output producerHostName)/api/manage"
account_host=$(output accountHostName)
account_url="https://$account_host/api"
web_url="https://$(output webHostName)"
site_url=$(output siteUrl)

mask() {
  if [[ -n "${GITHUB_ACTIONS:-}" ]]; then
    echo "::add-mask::$1"
  fi
}

# The admin endpoints use the master key. Right after a deploy the host may
# not have its keys yet.
for _ in {1..12}; do
  master=$(az functionapp keys list -g "$RG" -n "$producer_name" --query masterKey -o tsv 2>/dev/null) && [[ -n "$master" ]] && break
  sleep 10
done
[[ -n "${master:-}" ]] || fail "no master key for $producer_name"
mask "$master"

key_ids=()
revoke_keys() {
  local id
  for id in "${key_ids[@]}"; do
    curl -sS -o /dev/null --fail --max-time 60 -X DELETE -H "x-functions-key: $master" \
      "$manage_url/keys?key_id=$id" || echo "could not revoke key $id" >&2
  done
}
trap revoke_keys EXIT

create_key() { # name, credits -> "key id"
  local resp
  # Retries cover the cold start and the package swap after a deploy.
  resp=$(curl -sS --fail-with-body --retry 6 --retry-delay 10 --retry-all-errors --max-time 60 \
    -X POST -H "x-functions-key: $master" -H 'Content-Type: application/json' \
    -d "{\"name\": \"$1\", \"credits\": $2}" "$manage_url/keys") || fail "create key: $resp"
  jq -r '"\(.key) \(.key_id)"' <<<"$resp"
}

extract() { # pdf, expected status -> response
  local pdf=$1 want=$2 resp status
  resp=$(curl -sS --fail-with-body --retry 6 --retry-delay 10 --retry-all-errors --max-time 300 \
    -H "X-API-Key: $key" -H 'Content-Type: application/pdf' \
    --data-binary "@$pdf" "$producer_url") || fail "POST $(basename "$pdf"): $resp"
  status=$(jq -r .status <<<"$resp")
  [[ "$status" == "$want" ]] || fail "$(basename "$pdf"): status $status, want $want ($resp)"
  echo "$(basename "$pdf"): $resp" >&2
  echo "$resp"
}

wait_text() { # id, timeout seconds -> document
  local id=$1 deadline=$((SECONDS + $2)) body code
  body=$(mktemp)
  while ((SECONDS < deadline)); do
    code=$(curl -sS -o "$body" -w '%{http_code}' --max-time 60 -X POST -H "X-API-Key: $key" \
      -H 'Content-Type: application/json' -d "{\"request_id\": \"$id\"}" "$consumer_url" || true)
    case "$code" in
      200)
        jq -e --arg id "$id" '.request_id == $id and ([.pages[].text | length] | add) > 0' "$body" >/dev/null ||
          fail "$id: unexpected document: $(head -c 500 "$body")"
        echo "$id: $(jq '.pages | length' "$body") pages, $(jq '[.pages[].text | length] | add' "$body") chars" >&2
        cat "$body"
        rm -f "$body"
        return
        ;;
      404) sleep 10 ;; # not written yet
      *) echo "$id: HTTP $code, retrying: $(head -c 200 "$body")" >&2; sleep 10 ;;
    esac
  done
  rm -f "$body"
  fail "$id: no text after $2 s"
}

check_credits() { # response, document
  local charged want
  charged=$(jq -r .credits <<<"$1")
  # Pages from the text layer have confidence "-", OCRed pages a number.
  want=$(jq --argjson text "$TEXT_PAGE_CREDITS" --argjson ocr "$OCR_PAGE_CREDITS" \
    '[.pages[] | if .confidence == "-" then $text else $ocr end] | add' <<<"$2")
  [[ "$charged" == "$want" ]] || fail "$(jq -r .request_id <<<"$1"): charged $charged credits, want $want"
}

http_code() { # curl args -> HTTP status
  curl -sS -o /dev/null -w '%{http_code}' --max-time 300 "$@" || true
}

warm_code() { # curl args -> HTTP status; retries while unreachable or 5xx (cold start, package swap)
  local code
  for _ in {1..12}; do
    code=$(http_code "$@")
    [[ "$code" != 000 && "$code" != 5* ]] && break
    sleep 10
  done
  echo "$code"
}

log "Keys: $manage_url/keys"
out=$(create_key smoke 2000)
read -r key key_id <<<"$out"
key_ids+=("$key_id")
mask "$key"
out=$(create_key smoke-empty 0)
read -r empty_key empty_key_id <<<"$out"
key_ids+=("$empty_key_id")
mask "$empty_key"

log "POST $producer_url"
text_resp=$(extract "$TEXT_PDF" completed)
ocr_resp=$(extract "$SCANNED_PDF" queued)
text_id=$(jq -r .request_id <<<"$text_resp")
ocr_id=$(jq -r .request_id <<<"$ocr_resp")
balance=$(jq -r .balance <<<"$ocr_resp")
want=$((2000 - $(jq -r .credits <<<"$text_resp") - $(jq -r .credits <<<"$ocr_resp")))
((balance == want)) || fail "balance $balance, want $want"

log "Text layer: POST $consumer_url"
doc=$(wait_text "$text_id" "$TEXT_TIMEOUT")
check_credits "$text_resp" "$doc"

log "Rejections"
code=$(http_code -H 'Content-Type: application/pdf' --data-binary "@$TEXT_PDF" "$producer_url")
[[ "$code" == 401 ]] || fail "producer without a key: HTTP $code, want 401"
code=$(http_code -X POST -H "X-API-Key: $empty_key" -H 'Content-Type: application/json' \
  -d "{\"request_id\": \"$text_id\"}" "$consumer_url")
[[ "$code" == 404 ]] || fail "consumer, another key's job: HTTP $code, want 404"
code=$(http_code -H "X-API-Key: $empty_key" -H 'Content-Type: application/pdf' --data-binary "@$TEXT_PDF" "$producer_url")
[[ "$code" == 402 ]] || fail "producer, key without credits: HTTP $code, want 402"
echo "401, 404 and 402 as expected"

log "Account app: $account_url"
code=$(warm_code "$account_url/me")
[[ "$code" == 401 ]] || fail "GET /api/me without a session: HTTP $code, want 401"
code=$(http_code -X POST -H 'Content-Type: application/json' \
  -d '{"pack": "starter", "key_id": "x"}' "$account_url/checkout")
[[ "$code" == 401 ]] || fail "POST /api/checkout without a session: HTTP $code, want 401"
code=$(http_code -X POST -H 'Content-Type: application/json' -d '{}' "$account_url/purchases")
[[ "$code" == 404 ]] || fail "POST /api/purchases (removed mock purchases): HTTP $code, want 404"
code=$(http_code -X POST -H 'Content-Type: application/json' \
  -H "Stripe-Signature: t=$(date +%s),v1=$(head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n')" \
  -d '{"id": "evt_smoke", "object": "event", "type": "checkout.session.completed"}' "$account_url/webhooks/stripe")
[[ "$code" == 400 ]] || fail "POST /api/webhooks/stripe with a wrong signature: HTTP $code, want 400"
code=$(http_code -X POST -H 'Content-Type: application/json' \
  -d "{\"token\": \"$(head -c 32 /dev/urandom | base64 | tr '+/' '-_' | tr -d '=')\"}" "$account_url/auth/verify")
[[ "$code" == 401 ]] || fail "POST /api/auth/verify with a random token: HTTP $code, want 401"
echo "401, 404 and 400 as expected"

log "Website: $web_url"
headers=$(curl -sS --fail -D - -o /dev/null --retry 6 --retry-delay 10 --retry-all-errors --max-time 60 \
  "$web_url/index.html") || fail "GET $web_url/index.html failed"
csp=$(tr -d '\r' <<<"$headers" | sed -n 's/^content-security-policy: *//Ip')
[[ "$csp" == *"default-src 'self'"* && "$csp" == *"connect-src 'self' https://$account_host"* ]] ||
  fail "index.html: CSP ${csp:-missing}, want default-src 'self' and connect-src to https://$account_host"
config=$(curl -sS --fail --max-time 60 "$web_url/config.js") || fail "GET $web_url/config.js failed"
grep -qF "https://$account_host" <<<"$config" || fail "config.js doesn't name https://$account_host: $config"
for page in terms privacy refunds; do
  curl -sS --fail -o /dev/null --max-time 60 "$web_url/$page.html" || fail "GET $web_url/$page.html failed"
done
if [[ "$site_url" != "$web_url" ]]; then
  curl -sS --fail -o /dev/null --retry 3 --retry-delay 10 --max-time 60 "$site_url/index.html" ||
    fail "GET $site_url/index.html (custom domain) failed"
  echo "custom domain $site_url serves index.html"
fi
echo "CSP, config.js and legal pages as expected"

log "OCR (up to $OCR_TIMEOUT s)"
doc=$(wait_text "$ocr_id" "$OCR_TIMEOUT")
check_credits "$ocr_resp" "$doc"

log "Smoke test passed"
