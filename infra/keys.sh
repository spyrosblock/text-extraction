#!/usr/bin/env bash
# Issues API keys and manages their credits through the producer's admin
# endpoints (/api/manage/keys, /api/manage/credits), which only the function
# app's master key can call. Needs az logged in with Contributor on the RG.
#
#   infra/keys.sh <rg> create <name> <credits>  # prints the key; it is shown only once
#   infra/keys.sh <rg> list
#   infra/keys.sh <rg> set <id> <credits>
#   infra/keys.sh <rg> add <id> <delta>         # a negative delta takes credits
#   infra/keys.sh <rg> revoke <id>
#
# <id> is the key id from create or list (the sha256 of the key).
set -euo pipefail

usage() { sed -n '6,10s/^#   //p' "$0" >&2; exit 2; }
fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }

(($# >= 2)) || usage
RG=$1 cmd=$2
shift 2

producer=$(az functionapp list -g "$RG" \
  --query "[?starts_with(name, 'func-textextract-producer-')] | [0].name" -o tsv)
[[ -n "$producer" ]] || fail "no producer function app in $RG"
host=$(az functionapp show -g "$RG" -n "$producer" --query defaultHostName -o tsv)
master=$(az functionapp keys list -g "$RG" -n "$producer" --query masterKey -o tsv)
[[ -n "$master" ]] || fail "no master key for $producer"

call() { # method, path, [json body]
  local args=(-sS --fail-with-body --max-time 60 -X "$1" -H "x-functions-key: $master")
  if (($# > 2)); then
    args+=(-H 'Content-Type: application/json' -d "$3")
  fi
  curl "${args[@]}" "https://$host/api/manage/$2"
}

int() { [[ "$1" =~ ^-?[0-9]+$ ]] || fail "not an integer: $1"; }

case "$cmd" in
  create)
    (($# == 2)) || usage
    int "$2"
    call POST keys "$(jq -nc --arg name "$1" --argjson credits "$2" '{name: $name, credits: $credits}')" | jq .
    ;;
  list)
    (($# == 0)) || usage
    call GET keys | jq .
    ;;
  set)
    (($# == 2)) || usage
    int "$2"
    call PUT "credits?id=$1" "{\"credits\": $2}" | jq .
    ;;
  add)
    (($# == 2)) || usage
    int "$2"
    call POST "credits?id=$1" "{\"delta\": $2}" | jq .
    ;;
  revoke)
    (($# == 1)) || usage
    call DELETE "keys?id=$1"
    echo "revoked $1"
    ;;
  *) usage ;;
esac
