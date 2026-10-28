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
# Safe to re-run: every step is idempotent.
#
# Targets the current az subscription; pick another with `az account set`.
#
# Overrides: REPO, LOCATION, PROD_REVIEWER (GitHub login; defaults to the gh
# user), TFSTATE_ACCOUNT (Terraform state storage account).
set -euo pipefail

################################################################
#                          CONSTANTS                           #
################################################################

REPO="${REPO:-spyrosblock/text-extraction}"
LOCATION="${LOCATION:-northeurope}"
# GitHub environments; each gets resource group rg-textextract-<env>, Entra
# app gh-text-extraction-<env> and a Terraform state container.
ENVS=(staging prod)
TFSTATE_RG=rg-textextract-tfstate

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
# with the `roles` map in modules/roles.
ASSIGNABLE_ROLES=(
  b7e6dc6d-f1e8-4753-8033-0f276bb0955b # Storage Blob Data Owner
  ba92f5b4-2d11-453d-a403-e96b0029c9fe # Storage Blob Data Contributor
  2a2b9908-6ea1-4ae2-8e65-a410df84e7d1 # Storage Blob Data Reader
  69a216fc-b8fb-44d8-bc22-1f3c2cd27a39 # Azure Service Bus Data Sender
  4f6d3b9b-027b-4f4c-9142-0e5a2a2247e0 # Azure Service Bus Data Receiver
  090c5cfd-751d-490a-894a-3ce6f1109419 # Azure Service Bus Data Owner
  3913510d-42f4-4e42-8a64-420c390055eb # Monitoring Metrics Publisher
)

# Role Based Access Control Administrator, limited to writing assignments of
# ASSIGNABLE_ROLES to service principals and deleting assignments of those
# roles (terraform deletes assignments removed from the code).
printf -v roles_csv '%s, ' "${ASSIGNABLE_ROLES[@]}"
roles_csv=${roles_csv%, }
RBAC_CONDITION="((!(ActionMatches{'Microsoft.Authorization/roleAssignments/write'})) OR (@Request[Microsoft.Authorization/roleAssignments:RoleDefinitionId] ForAnyOfAnyValues:GuidEquals {$roles_csv} AND @Request[Microsoft.Authorization/roleAssignments:PrincipalType] ForAnyOfAnyValues:StringEqualsIgnoreCase {'ServicePrincipal'})) AND ((!(ActionMatches{'Microsoft.Authorization/roleAssignments/delete'})) OR (@Resource[Microsoft.Authorization/roleAssignments:RoleDefinitionId] ForAnyOfAnyValues:GuidEquals {$roles_csv}))"


CONTRIBUTOR=b24988ac-6180-42a0-ab88-20f7382dd24c
BLOB_DATA_CONTRIBUTOR=ba92f5b4-2d11-453d-a403-e96b0029c9fe
RBAC_ADMIN=f58310d9-a9f6-439a-9e8d-f62e7b41a168

################################################################
#                          VARIABLES                           #
################################################################

SUBSCRIPTION_ID=$(az account show --query id -o tsv)
TENANT_ID=$(az account show --query tenantId -o tsv)
# Storage account names are global; derive a stable one per subscription.
TFSTATE_ACCOUNT=${TFSTATE_ACCOUNT:-sttxtfstate$(printf %s "$SUBSCRIPTION_ID" | sha256sum | cut -c1-8)}

# Subject prefix of the repo's OIDC tokens. Newer repos use immutable ids
# (repo:<owner>@<owner id>/<repo>@<repo id>) rather than repo:<owner>/<repo>.
SUB_PREFIX=$(gh api "repos/$REPO/actions/oidc/customization/sub" --jq ".sub_claim_prefix // \"repo:$REPO\"")

reviewer_login="${PROD_REVIEWER:-$(gh api user --jq .login)}"
reviewer_id=$(gh api "users/$reviewer_login" --jq .id)

################################################################
#                          FUNCTIONS                           #
################################################################

log() { printf '\n==> %s\n' "$*"; }

ensure_federated_credential() { # appId, credential name, subject
  local params
  params=$(jq -n --arg subject "$3" \
    '{issuer: "https://token.actions.githubusercontent.com", subject: $subject, audiences: ["api://AzureADTokenExchange"]}')
  az ad app federated-credential update --id "$1" --federated-credential-id "$2" --parameters "$params" 2>/dev/null ||
    az ad app federated-credential create --id "$1" --parameters "$(jq --arg name "$2" '.name = $name' <<<"$params")" >/dev/null
  echo "federated credential $2: $3"
}

assign_role() { # principal type, principal object id, role definition id, scope, [condition]
  local type=$1 principal=$2 role=$3 scope=$4 condition=${5:-} stale
  # az treats an existing assignment as success but leaves its condition alone,
  # so drop one whose condition differs first.
  if [[ -n "$condition" ]]; then
    stale=$(az role assignment list --assignee "$principal" --role "$role" --scope "$scope" \
      --query "[?scope=='$scope' && condition!=\`$(jq -n --arg c "$condition" '$c')\`].id" -o tsv)
    [[ -z "$stale" ]] || az role assignment delete --ids $stale
  fi
  az role assignment create --assignee-object-id "$principal" --assignee-principal-type "$type" \
    --role "$role" --scope "$scope" ${condition:+--condition "$condition" --condition-version 2.0} >/dev/null
  echo "role $role: assigned"
}

set_vars() { # client id, resource group, [gh variable scope, e.g. --env staging]
  local client_id=$1 rg=$2
  shift 2
  gh variable set AZURE_CLIENT_ID -R "$REPO" "$@" --body "$client_id"
  gh variable set AZURE_TENANT_ID -R "$REPO" "$@" --body "$TENANT_ID"
  gh variable set AZURE_SUBSCRIPTION_ID -R "$REPO" "$@" --body "$SUBSCRIPTION_ID"
  gh variable set AZURE_RG -R "$REPO" "$@" --body "$rg"
}

################################################################
#                  REGISTER AZURE PROVIDERS                    #
################################################################

log "Registering resource providers in subscription $SUBSCRIPTION_ID"
for ns in "${PROVIDERS[@]}"; do
  az provider register -n "$ns" --wait
done

################################################################
#             CREATE AZURE STORAGE FOR TERRAFORM               #
################################################################

# One account, a container per environment. Each deployment identity can only
# write its own container. Entra auth only, with blob versioning to recover an
# overwritten state.
log "Terraform state: $TFSTATE_ACCOUNT in $TFSTATE_RG"
az group create -n "$TFSTATE_RG" -l "$LOCATION" --tags app=text-extraction >/dev/null
state_id=$(az storage account create -g "$TFSTATE_RG" -n "$TFSTATE_ACCOUNT" -l "$LOCATION" \
  --sku Standard_LRS --kind StorageV2 --min-tls-version TLS1_2 \
  --allow-blob-public-access false --allow-shared-key-access false \
  --tags app=text-extraction --query id -o tsv)
az storage account blob-service-properties update -g "$TFSTATE_RG" --account-name "$TFSTATE_ACCOUNT" \
  --enable-versioning true --enable-delete-retention true --delete-retention-days 7 >/dev/null
# Owner has no data access; let whoever runs this run terraform by hand.
if user_id=$(az ad signed-in-user show --query id -o tsv 2>/dev/null); then
  assign_role User "$user_id" "$BLOB_DATA_CONTRIBUTOR" "$state_id"
fi
gh variable set TFSTATE_STORAGE_ACCOUNT -R "$REPO" --body "$TFSTATE_ACCOUNT"

################################################################
#                SET UP DEPLOYMENT ENVIRONMENTS                #
################################################################

for env in "${ENVS[@]}"; do
  # Azure: setup resource group
  rg="rg-textextract-$env"
  log "[$env] Resource group $rg in $LOCATION"
  rg_id=$(az group create -n "$rg" -l "$LOCATION" --tags app=text-extraction "env=$env" --query id -o tsv)

  # Azure: app registration
  log "[$env] Entra app, federated credential and role assignments"
  app="gh-text-extraction-$env"
  app_id=$(az ad app list --display-name "$app" --query '[0].appId' -o tsv)
  [[ -n "$app_id" ]] || app_id=$(az ad app create --display-name "$app" --sign-in-audience AzureADMyOrg --query appId -o tsv)
  # Azure: service principal creation
  sp_id=$(az ad sp list --filter "appId eq '$app_id'" --query '[0].id' -o tsv)
  [[ -n "$sp_id" ]] || sp_id=$(az ad sp create --id "$app_id" --query id -o tsv)
  echo "app $app: client id $app_id, service principal $sp_id"
  # Azure: setup federated credentials for gha
  ensure_federated_credential "$app_id" "github-env-$env" "$SUB_PREFIX:environment:$env"
  # Azure: grant read/write permissions to the sp so that terraform can create and manage resources
  assign_role ServicePrincipal "$sp_id" "$CONTRIBUTOR" "$rg_id"
  # Azure: grant the permission to the sp to assign roles so that terraform can assign roles
  assign_role ServicePrincipal "$sp_id" "$RBAC_ADMIN" "$rg_id" "$RBAC_CONDITION"
  # Azure: create terraform state container and the sp permission to read/write it
  az storage container-rm create -g "$TFSTATE_RG" --storage-account "$TFSTATE_ACCOUNT" -n "$env" >/dev/null
  assign_role ServicePrincipal "$sp_id" "$BLOB_DATA_CONTRIBUTOR" "$state_id/blobServices/default/containers/$env"
  
  # Github: setup gh environments and gh environmental variables
  log "[$env] GitHub environment and variables"
  body='{}'
  if [[ "$env" == prod ]]; then
    body=$(jq -n --argjson id "$reviewer_id" '{reviewers: [{type: "User", id: $id}]}')
    echo "required reviewer: $reviewer_login"
  fi
  gh api -X PUT "repos/$REPO/environments/$env" --input - <<<"$body" >/dev/null
  set_vars "$app_id" "$rg" --env "$env"
  
  # Setup federated credentials for pr gha workflow
  if [[ "$env" == staging ]]; then
    # PR jobs (the terraform plan in ci.yml) run without an environment, so
    # they need their own federated credential and only see repo-level
    # variables. Point both at staging; environment variables override these.
    ensure_federated_credential "$app_id" github-pull-request "$SUB_PREFIX:pull_request"
    set_vars "$app_id" "$rg"
  fi
done

log "Done"
