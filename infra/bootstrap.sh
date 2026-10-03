#!/usr/bin/env bash
# One-time setup for the GitHub Actions deployments (PLAN.md, Step 2).
#
# Run by hand, logged in to both CLIs:
#   - az: Owner (or Contributor + User Access Administrator) on the
#     subscription, and allowed to register Entra apps.
#   - gh: admin on the repo.
#
#   az login && gh auth login
#   infra/bootstrap.sh
#
# Safe to re-run: every step checks what already exists.
#
# Overrides: REPO, LOCATION, AZURE_SUBSCRIPTION_ID (defaults to the current az
# subscription), PROD_REVIEWER (GitHub login; defaults to the gh user).
set -euo pipefail

REPO="${REPO:-spyrosblock/text-extraction}"
LOCATION="${LOCATION:-northeurope}"
ENVS=(staging prod)

PROVIDERS=(
  Microsoft.Storage
  Microsoft.ServiceBus
  Microsoft.ManagedIdentity
  Microsoft.Web
  Microsoft.App
  Microsoft.OperationalInsights
  Microsoft.Insights
  Microsoft.Network
)

# The only roles the deployment identity may assign or remove. Keep in sync
# with the `roles` map in modules/roles.bicep.
ASSIGNABLE_ROLES=(
  b7e6dc6d-f1e8-4753-8033-0f276bb0955b # Storage Blob Data Owner
  ba92f5b4-2d11-453d-a403-e96b0029c9fe # Storage Blob Data Contributor
  2a2b9908-6ea1-4ae2-8e65-a410df84e7d1 # Storage Blob Data Reader
  69a216fc-b8fb-44d8-bc22-1f3c2cd27a39 # Azure Service Bus Data Sender
  4f6d3b9b-027b-4f4c-9142-0e5a2a2247e0 # Azure Service Bus Data Receiver
  090c5cfd-751d-490a-894a-3ce6f1109419 # Azure Service Bus Data Owner
  3913510d-42f4-4e42-8a64-420c390055eb # Monitoring Metrics Publisher
)

CONTRIBUTOR=b24988ac-6180-42a0-ab88-20f7382dd24c
RBAC_ADMIN=f58310d9-a9f6-439a-9e8d-f62e7b41a168
OIDC_ISSUER=https://token.actions.githubusercontent.com

log() { printf '\n==> %s\n' "$*"; }

if [[ -n "${AZURE_SUBSCRIPTION_ID:-}" ]]; then
  az account set --subscription "$AZURE_SUBSCRIPTION_ID"
fi
SUBSCRIPTION_ID=$(az account show --query id -o tsv)
TENANT_ID=$(az account show --query tenantId -o tsv)
gh repo view "$REPO" --json name >/dev/null

# Role Based Access Control Administrator, limited to writing assignments of
# ASSIGNABLE_ROLES to service principals and deleting assignments of those
# roles (a deployment stack deletes assignments removed from Bicep).
roles_csv=$(printf '%s, ' "${ASSIGNABLE_ROLES[@]}")
roles_csv=${roles_csv%, }
RBAC_CONDITION="((!(ActionMatches{'Microsoft.Authorization/roleAssignments/write'})) OR (@Request[Microsoft.Authorization/roleAssignments:RoleDefinitionId] ForAnyOfAnyValues:GuidEquals {$roles_csv} AND @Request[Microsoft.Authorization/roleAssignments:PrincipalType] ForAnyOfAnyValues:StringEqualsIgnoreCase {'ServicePrincipal'})) AND ((!(ActionMatches{'Microsoft.Authorization/roleAssignments/delete'})) OR (@Resource[Microsoft.Authorization/roleAssignments:RoleDefinitionId] ForAnyOfAnyValues:GuidEquals {$roles_csv}))"

# --- 1. Resource providers -------------------------------------------------

log "Registering resource providers in subscription $SUBSCRIPTION_ID"
for ns in "${PROVIDERS[@]}"; do
  state=$(az provider show -n "$ns" --query registrationState -o tsv)
  if [[ "$state" == Registered ]]; then
    echo "$ns: registered"
  else
    echo "$ns: $state, registering"
    az provider register -n "$ns" --wait
  fi
done

# --- Helpers ---------------------------------------------------------------

ensure_app() { # display name -> appId
  local name=$1 app_id
  app_id=$(az ad app list --display-name "$name" --query '[0].appId' -o tsv)
  if [[ -z "$app_id" ]]; then
    app_id=$(az ad app create --display-name "$name" --sign-in-audience AzureADMyOrg --query appId -o tsv)
    echo "created app $name ($app_id)" >&2
  else
    echo "app $name exists ($app_id)" >&2
  fi
  echo "$app_id"
}

ensure_sp() { # appId -> service principal object id
  local app_id=$1 sp_id
  sp_id=$(az ad sp list --filter "appId eq '$app_id'" --query '[0].id' -o tsv)
  if [[ -z "$sp_id" ]]; then
    sp_id=$(az ad sp create --id "$app_id" --query id -o tsv)
    echo "created service principal $sp_id" >&2
  fi
  echo "$sp_id"
}

ensure_federated_credential() { # appId, credential name, subject
  local app_id=$1 name=$2 subject=$3 existing
  existing=$(az ad app federated-credential list --id "$app_id" --query "[?name=='$name'].subject | [0]" -o tsv)
  if [[ "$existing" == "$subject" ]]; then
    echo "federated credential $name: $subject"
    return
  fi
  if [[ -n "$existing" ]]; then
    az ad app federated-credential delete --id "$app_id" --federated-credential-id "$name"
  fi
  az ad app federated-credential create --id "$app_id" --parameters "$(jq -n \
    --arg name "$name" --arg issuer "$OIDC_ISSUER" --arg subject "$subject" \
    '{name: $name, issuer: $issuer, subject: $subject, audiences: ["api://AzureADTokenExchange"]}')" >/dev/null
  echo "federated credential $name: $subject (created)"
}

ensure_role() { # principal object id, role definition id, scope, [condition]
  local sp_id=$1 role=$2 scope=$3 condition=${4:-} existing
  existing=$(az role assignment list --assignee "$sp_id" --role "$role" --scope "$scope" \
    --query "[?scope=='$scope'] | [0].{id: id, condition: condition}" -o json)
  if [[ "$existing" != null && -n "$existing" ]]; then
    if [[ "$(jq -r '.condition // ""' <<<"$existing")" == "$condition" ]]; then
      echo "role $role: assigned"
      return
    fi
    echo "role $role: condition changed, re-assigning"
    az role assignment delete --ids "$(jq -r .id <<<"$existing")"
  fi
  local args=(--assignee-object-id "$sp_id" --assignee-principal-type ServicePrincipal
    --role "$role" --scope "$scope")
  if [[ -n "$condition" ]]; then
    args+=(--condition "$condition" --condition-version 2.0)
  fi
  az role assignment create "${args[@]}" >/dev/null
  echo "role $role: assigned (created)"
}

# --- 2-4. Resource groups, Entra apps, role assignments ----------------------

declare -A CLIENT_IDS
for env in "${ENVS[@]}"; do
  rg="rg-textextract-$env"

  log "[$env] Resource group $rg in $LOCATION"
  rg_id=$(az group create -n "$rg" -l "$LOCATION" --tags app=text-extraction "env=$env" --query id -o tsv)

  log "[$env] Entra app and federated credentials"
  app_id=$(ensure_app "gh-text-extraction-$env")
  sp_id=$(ensure_sp "$app_id")
  CLIENT_IDS[$env]=$app_id
  ensure_federated_credential "$app_id" "github-env-$env" "repo:$REPO:environment:$env"
  if [[ "$env" == staging ]]; then
    # PR jobs run without an environment; ci.yml uses this for what-if.
    ensure_federated_credential "$app_id" github-pull-request "repo:$REPO:pull_request"
  fi

  log "[$env] Role assignments on $rg"
  ensure_role "$sp_id" "$CONTRIBUTOR" "$rg_id"
  ensure_role "$sp_id" "$RBAC_ADMIN" "$rg_id" "$RBAC_CONDITION"
done

# --- 5. GitHub environments and variables ----------------------------------

reviewer_login="${PROD_REVIEWER:-$(gh api user --jq .login)}"
reviewer_id=$(gh api "users/$reviewer_login" --jq .id)

for env in "${ENVS[@]}"; do
  log "[$env] GitHub environment"
  if [[ "$env" == prod ]]; then
    jq -n --argjson id "$reviewer_id" '{reviewers: [{type: "User", id: $id}]}' |
      gh api -X PUT "repos/$REPO/environments/$env" --input - >/dev/null
    echo "required reviewer: $reviewer_login"
  else
    gh api -X PUT "repos/$REPO/environments/$env" >/dev/null
  fi

  gh variable set AZURE_CLIENT_ID --env "$env" -R "$REPO" --body "${CLIENT_IDS[$env]}"
  gh variable set AZURE_TENANT_ID --env "$env" -R "$REPO" --body "$TENANT_ID"
  gh variable set AZURE_SUBSCRIPTION_ID --env "$env" -R "$REPO" --body "$SUBSCRIPTION_ID"
  gh variable set AZURE_RG --env "$env" -R "$REPO" --body "rg-textextract-$env"
done

# Jobs without an environment (the PR what-if in ci.yml) only see repo-level
# variables. Point those at staging; environment variables override them.
log "Repository variables (staging, for PR what-if)"
gh variable set AZURE_CLIENT_ID -R "$REPO" --body "${CLIENT_IDS[staging]}"
gh variable set AZURE_TENANT_ID -R "$REPO" --body "$TENANT_ID"
gh variable set AZURE_SUBSCRIPTION_ID -R "$REPO" --body "$SUBSCRIPTION_ID"
gh variable set AZURE_RG -R "$REPO" --body rg-textextract-staging

log "Done"
for env in "${ENVS[@]}"; do
  echo "$env: AZURE_CLIENT_ID=${CLIENT_IDS[$env]} AZURE_RG=rg-textextract-$env"
done
