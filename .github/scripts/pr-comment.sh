#!/usr/bin/env bash
# Creates or updates a pull request comment from GitHub Actions, found by the
# marker it starts with, so each ci.yml job keeps its own comment.
#
#   .github/scripts/pr-comment.sh <pr number> <marker> <body file>
#
# Needs GH_TOKEN with pull-requests: write.
set -euo pipefail

pr=${1:?usage: $0 <pr> <marker> <body file>}
marker=${2:?}
body_file=${3:?}

body=$(printf '%s\n\n' "$marker"; cat "$body_file")
id=$(gh api --paginate "repos/$GITHUB_REPOSITORY/issues/$pr/comments" \
  --jq ".[] | select(.user.login == \"github-actions[bot]\" and (.body | startswith(\"$marker\"))) | .id" | head -n1)

if [[ -n "$id" ]]; then
  gh api -X PATCH "repos/$GITHUB_REPOSITORY/issues/comments/$id" -f body="$body" >/dev/null
  echo "updated comment $id"
else
  gh api "repos/$GITHUB_REPOSITORY/issues/$pr/comments" -f body="$body" >/dev/null
  echo "created comment"
fi
