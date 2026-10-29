#!/usr/bin/env bash
# One-time setup for the GitHub Actions deployments (PLAN.md, Step 2): applies
# the Terraform in infra/bootstrap with your own credentials.
#
# Run by hand, logged in to both CLIs:
#   - az: Owner (or Contributor + User Access Administrator) on the
#     subscription, and allowed to register Entra apps.
#   - gh: admin on the repo.
#
#   az login && gh auth login
#   infra/bootstrap.sh [terraform apply options, e.g. -var prod_reviewer=<login>]
#
# Safe to re-run. Targets the current az subscription; pick another with
# `az account set`. REPO overrides the repo (default: this checkout's).
set -euo pipefail

REPO="${REPO:-$(gh repo view --json nameWithOwner --jq .nameWithOwner)}"

ARM_SUBSCRIPTION_ID=$(az account show --query id -o tsv)
GITHUB_TOKEN=$(gh auth token)
# Subject prefix of the repo's OIDC tokens. Newer repos use immutable ids
# (repo:<owner>@<owner id>/<repo>@<repo id>) rather than repo:<owner>/<repo>.
TF_VAR_oidc_subject_prefix=$(gh api "repos/$REPO/actions/oidc/customization/sub" --jq ".sub_claim_prefix // \"repo:$REPO\"")
export ARM_SUBSCRIPTION_ID GITHUB_TOKEN TF_VAR_oidc_subject_prefix TF_VAR_repo="$REPO"

cd "$(dirname "$0")/bootstrap"
terraform init -input=false
terraform apply "$@"
