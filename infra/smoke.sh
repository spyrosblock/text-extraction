#!/usr/bin/env bash
# End-to-end smoke test of one environment (PLAN.md, Step 3). Used by
# functions.yml; also run by hand:
#
#   az login
#   infra/smoke.sh rg-textextract-staging
#
# 1. POSTs a PDF with a text layer to the producer: expects "completed", then
#    its text from the consumer.
# 2. POSTs a scanned PDF: expects "queued", then the OCR job's text from the
#    consumer. Needs the OCR image deployed (ocr.yml), not the quickstart one.
#
# Reads the app names and host names from the deployment stack's outputs and
# the producer's function key from ARM, so az needs Contributor on the RG.
#
# Overrides: STACK, TEXT_PDF, SCANNED_PDF, TEXT_TIMEOUT, OCR_TIMEOUT (seconds).
set -euo pipefail

RG=${1:?usage: $0 <resource-group>}
STACK=${STACK:-text-extraction}
ROOT=$(cd "$(dirname "$0")/.." && pwd)
TEXT_PDF=${TEXT_PDF:-$ROOT/test1.pdf}
SCANNED_PDF=${SCANNED_PDF:-$ROOT/test2.pdf}
TEXT_TIMEOUT=${TEXT_TIMEOUT:-60}
# KEDA polls every 30 s, then the execution pulls the image and OCRs every page.
OCR_TIMEOUT=${OCR_TIMEOUT:-1200}

log() { printf '\n==> %s\n' "$*"; }
fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }

outputs=$(az stack group show -g "$RG" -n "$STACK" --query outputs -o json)
output() { jq -er ".$1.value" <<<"$outputs"; }
producer_name=$(output producerName)
producer_url="https://$(output producerHostName)/api/extract"
consumer_url="https://$(output consumerHostName)/api/text"

# The producer uses function auth. Right after a deploy the host may not have
# its keys yet.
for _ in {1..12}; do
  key=$(az functionapp keys list -g "$RG" -n "$producer_name" --query functionKeys.default -o tsv 2>/dev/null) && [[ -n "$key" ]] && break
  sleep 10
done
[[ -n "${key:-}" ]] || fail "no function key for $producer_name"
if [[ -n "${GITHUB_ACTIONS:-}" ]]; then
  echo "::add-mask::$key"
fi

extract() { # pdf, expected status -> id
  local pdf=$1 want=$2 resp status
  # Retries cover the cold start and the package swap after a deploy.
  resp=$(curl -sS --fail-with-body --retry 6 --retry-delay 10 --retry-all-errors --max-time 300 \
    -H "x-functions-key: $key" -H 'Content-Type: application/pdf' \
    --data-binary "@$pdf" "$producer_url") || fail "POST $(basename "$pdf"): $resp"
  status=$(jq -r .status <<<"$resp")
  [[ "$status" == "$want" ]] || fail "$(basename "$pdf"): status $status, want $want ($resp)"
  echo "$(basename "$pdf"): $resp" >&2
  jq -r .id <<<"$resp"
}

wait_text() { # id, timeout seconds
  local id=$1 deadline=$((SECONDS + $2)) body code
  body=$(mktemp)
  while ((SECONDS < deadline)); do
    code=$(curl -sS -o "$body" -w '%{http_code}' --max-time 60 -X POST \
      -H 'Content-Type: application/json' -d "{\"id\": \"$id\"}" "$consumer_url" || true)
    case "$code" in
      200)
        jq -e --arg id "$id" '.id == $id and ([.pages[].text | length] | add) > 0' "$body" >/dev/null ||
          fail "$id: unexpected document: $(head -c 500 "$body")"
        echo "$id: $(jq '.pages | length' "$body") pages, $(jq '[.pages[].text | length] | add' "$body") chars"
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

log "POST $producer_url"
text_id=$(extract "$TEXT_PDF" completed)
ocr_id=$(extract "$SCANNED_PDF" queued)

log "Text layer: POST $consumer_url"
wait_text "$text_id" "$TEXT_TIMEOUT"

log "OCR (up to $OCR_TIMEOUT s)"
wait_text "$ocr_id" "$OCR_TIMEOUT"

log "Smoke test passed"
