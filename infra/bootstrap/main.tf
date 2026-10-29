# One-time setup for the GitHub Actions deployments (PLAN.md, Step 2). Per
# environment: resource group rg-textextract-<env>, Entra app
# gh-text-extraction-<env> that GitHub signs in as over OIDC, its role
# assignments, a Terraform state container, and the GitHub environment and
# variables. Plus the shared Terraform state account.
data "azurerm_client_config" "current" {}

# Whoever runs this: owns the Entra apps and gets access to the state.
data "azuread_client_config" "current" {}

locals {
  owner      = split("/", var.repo)[0]
  repository = split("/", var.repo)[1]
  envs       = toset(["staging", "prod"])
  tags       = { app = "text-extraction" }

  subscription_id = data.azurerm_client_config.current.subscription_id
  # Storage account names are global; derive a stable one per subscription.
  tfstate_account = coalesce(var.tfstate_account, "sttxtfstate${substr(sha256(local.subscription_id), 0, 8)}")

  # The only roles the deployment identity may assign or remove: the ones
  # modules/roles assigns.
  assignable_roles = join(", ", values(jsondecode(file("${path.module}/../modules/roles/roles.json"))))
  # Role Based Access Control Administrator, limited to writing assignments of
  # those roles to service principals and deleting assignments of those roles
  # (terraform deletes assignments removed from the code).
  rbac_condition = join(" AND ", [
    "((!(ActionMatches{'Microsoft.Authorization/roleAssignments/write'})) OR (@Request[Microsoft.Authorization/roleAssignments:RoleDefinitionId] ForAnyOfAnyValues:GuidEquals {${local.assignable_roles}} AND @Request[Microsoft.Authorization/roleAssignments:PrincipalType] ForAnyOfAnyValues:StringEqualsIgnoreCase {'ServicePrincipal'}))",
    "((!(ActionMatches{'Microsoft.Authorization/roleAssignments/delete'})) OR (@Resource[Microsoft.Authorization/roleAssignments:RoleDefinitionId] ForAnyOfAnyValues:GuidEquals {${local.assignable_roles}}))",
  ])

  federated_credentials = merge(
    { for env in local.envs : "github-env-${env}" => { env = env, subject = "${var.oidc_subject_prefix}:environment:${env}" } },
    # PR jobs (the terraform plan in ci.yml) run without an environment, so
    # they need their own credential. They plan against staging.
    { github-pull-request = { env = "staging", subject = "${var.oidc_subject_prefix}:pull_request" } },
  )

  deploy_roles = merge([for env in local.envs : {
    # Terraform creates and manages the environment's resources...
    "${env}-contributor" = { env = env, role = "Contributor", scope = azurerm_resource_group.env[env].id, condition = null }
    # ...and their role assignments...
    "${env}-rbac-admin" = { env = env, role = "Role Based Access Control Administrator", scope = azurerm_resource_group.env[env].id, condition = local.rbac_condition }
    # ...and keeps its state in its own container.
    "${env}-tfstate" = { env = env, role = "Storage Blob Data Contributor", scope = "${azurerm_storage_account.tfstate.id}/blobServices/default/containers/${azurerm_storage_container.tfstate[env].name}", condition = null }
  }]...)

  # What the workflows read to sign in to Azure.
  azure_variables = { for env in local.envs : env => {
    AZURE_CLIENT_ID       = azuread_application.deploy[env].client_id
    AZURE_TENANT_ID       = data.azurerm_client_config.current.tenant_id
    AZURE_SUBSCRIPTION_ID = local.subscription_id
    AZURE_RG              = azurerm_resource_group.env[env].name
  } }
}

################################################################
#                      TERRAFORM STATE                         #
################################################################

# One account, a container per environment. Each deployment identity can only
# write its own container. Entra auth only, with blob versioning to recover an
# overwritten state.
resource "azurerm_resource_group" "tfstate" {
  name     = "rg-textextract-tfstate"
  location = var.location
  tags     = local.tags
}

resource "azurerm_storage_account" "tfstate" {
  name                            = local.tfstate_account
  resource_group_name             = azurerm_resource_group.tfstate.name
  location                        = azurerm_resource_group.tfstate.location
  account_tier                    = "Standard"
  account_replication_type        = "LRS"
  min_tls_version                 = "TLS1_2"
  allow_nested_items_to_be_public = false
  shared_access_key_enabled       = false
  tags                            = local.tags

  blob_properties {
    versioning_enabled = true
    delete_retention_policy {
      days = 7
    }
  }
}

resource "azurerm_storage_container" "tfstate" {
  for_each           = local.envs
  name               = each.key
  storage_account_id = azurerm_storage_account.tfstate.id
}

# Owner has no data access; let whoever runs this run terraform by hand.
resource "azurerm_role_assignment" "tfstate_caller" {
  scope                = azurerm_storage_account.tfstate.id
  role_definition_name = "Storage Blob Data Contributor"
  principal_id         = data.azuread_client_config.current.object_id
}

################################################################
#                  DEPLOYMENT ENVIRONMENTS                     #
################################################################

resource "azurerm_resource_group" "env" {
  for_each = local.envs
  name     = "rg-textextract-${each.key}"
  location = var.location
  tags     = merge(local.tags, { env = each.key })
}

resource "azuread_application" "deploy" {
  for_each         = local.envs
  display_name     = "gh-text-extraction-${each.key}"
  sign_in_audience = "AzureADMyOrg"
  owners           = [data.azuread_client_config.current.object_id]
}

resource "azuread_service_principal" "deploy" {
  for_each  = local.envs
  client_id = azuread_application.deploy[each.key].client_id
  owners    = [data.azuread_client_config.current.object_id]
}

resource "azuread_application_federated_identity_credential" "github" {
  for_each       = local.federated_credentials
  application_id = azuread_application.deploy[each.value.env].id
  display_name   = each.key
  issuer         = "https://token.actions.githubusercontent.com"
  subject        = each.value.subject
  audiences      = ["api://AzureADTokenExchange"]
}

resource "azurerm_role_assignment" "deploy" {
  for_each             = local.deploy_roles
  scope                = each.value.scope
  role_definition_name = each.value.role
  principal_id         = azuread_service_principal.deploy[each.value.env].object_id
  principal_type       = "ServicePrincipal"
  condition            = each.value.condition
  condition_version    = each.value.condition == null ? null : "2.0"
}

################################################################
#                          GITHUB                              #
################################################################

data "github_user" "prod_reviewer" {
  username = var.prod_reviewer
}

resource "github_repository_environment" "this" {
  for_each    = local.envs
  repository  = local.repository
  environment = each.key

  dynamic "reviewers" {
    for_each = each.key == "prod" ? [1] : []
    content {
      users = [tonumber(data.github_user.prod_reviewer.id)]
    }
  }
}

resource "github_actions_environment_variable" "azure" {
  for_each = merge([for env, vars in local.azure_variables : {
    for name, value in vars : "${env}/${name}" => { env = env, name = name, value = value }
  }]...)
  repository    = local.repository
  environment   = github_repository_environment.this[each.value.env].environment
  variable_name = each.value.name
  value         = each.value.value
}

# PR jobs only see repo-level variables; point them at staging. Environment
# variables override these.
resource "github_actions_variable" "repo" {
  for_each = merge(local.azure_variables["staging"], {
    TFSTATE_STORAGE_ACCOUNT = azurerm_storage_account.tfstate.name
  })
  repository    = local.repository
  variable_name = each.key
  value         = each.value
}
