# A Flex Consumption plan and app running the native Go worker, with
# identity-based host storage, deployment storage and App Insights.
# Code is deployed separately (staging.yml/prod.yml, target functions);
# re-running this leaves the package in the deployment container alone.
#
# The app is an azapi resource: azurerm_function_app_flex_consumption doesn't
# accept the go runtime.
terraform {
  required_providers {
    azapi = {
      source = "Azure/azapi"
    }
  }
}

variable "resource_group_id" {
  type = string
}
variable "resource_group_name" {
  type = string
}
variable "location" {
  type = string
}
variable "name" {
  type = string
}
variable "plan_name" {
  type = string
}
variable "identity" {
  type = object({
    id        = string
    client_id = string
  })
}
variable "host_storage_name" {
  type = string
}
variable "host_storage_blob_endpoint" {
  type = string
}
variable "deployment_container_name" {
  type = string
}
variable "instance_memory_mb" {
  type = number
  validation {
    condition     = contains([512, 2048, 4096], var.instance_memory_mb)
    error_message = "instance_memory_mb must be 512, 2048 or 4096."
  }
}
variable "maximum_instance_count" {
  type = number
}
variable "http_per_instance_concurrency" {
  description = "Concurrent HTTP requests per instance; 0 = platform default."
  type        = number
  default     = 0
}
variable "app_insights_connection_string" {
  type = string
}
variable "subnet_id" {
  description = "Subnet for VNet integration; null = none."
  type        = string
  default     = null
}
variable "cors_allowed_origins" {
  description = "Origins allowed to call the app from a browser; empty = no CORS."
  type        = list(string)
  default     = []
}
variable "app_settings" {
  description = "App-specific settings, merged over the common ones."
  type        = map(string)
  default     = {}
}
variable "tags" {
  type = map(string)
}

locals {
  common_settings = {
    AzureWebJobsStorage__accountName          = var.host_storage_name
    AzureWebJobsStorage__credential           = "managedidentity"
    AzureWebJobsStorage__clientId             = var.identity.client_id
    APPLICATIONINSIGHTS_CONNECTION_STRING     = var.app_insights_connection_string
    APPLICATIONINSIGHTS_AUTHENTICATION_STRING = "ClientId=${var.identity.client_id};Authorization=AAD"
    AZURE_CLIENT_ID                           = var.identity.client_id
  }
}

resource "azurerm_service_plan" "this" {
  name                = var.plan_name
  resource_group_name = var.resource_group_name
  location            = var.location
  os_type             = "Linux"
  sku_name            = "FC1"
  tags                = var.tags
}

resource "azapi_resource" "app" {
  type      = "Microsoft.Web/sites@2024-11-01"
  name      = var.name
  parent_id = var.resource_group_id
  location  = var.location
  tags      = var.tags

  identity {
    type         = "UserAssigned"
    identity_ids = [var.identity.id]
  }

  # Leaves virtualNetworkSubnetId and triggers out of the request when null.
  ignore_null_property = true

  body = {
    kind = "functionapp,linux"
    properties = {
      serverFarmId           = azurerm_service_plan.this.id
      httpsOnly              = true
      virtualNetworkSubnetId = var.subnet_id
      functionAppConfig = {
        deployment = {
          storage = {
            type  = "blobContainer"
            value = "${var.host_storage_blob_endpoint}${var.deployment_container_name}"
            authentication = {
              type                           = "UserAssignedIdentity"
              userAssignedIdentityResourceId = var.identity.id
            }
          }
        }
        scaleAndConcurrency = {
          instanceMemoryMB     = var.instance_memory_mb
          maximumInstanceCount = var.maximum_instance_count
          triggers = var.http_per_instance_concurrency > 0 ? {
            http = { perInstanceConcurrency = var.http_per_instance_concurrency }
          } : null
        }
        # Native Go worker (azure-functions-golang-worker), per the Step 0 spike.
        runtime = {
          name    = "go"
          version = "1.0"
        }
      }
      # cors only when set, so apps without it see no change.
      siteConfig = merge({
        minTlsVersion = "1.2"
        ftpsState     = "Disabled"
        appSettings   = [for k, v in merge(local.common_settings, var.app_settings) : { name = k, value = v }]
      }, length(var.cors_allowed_origins) > 0 ? { cors = { allowedOrigins = var.cors_allowed_origins } } : {})
    }
  }

  response_export_values = ["properties.defaultHostName"]
}

# Dropping subnet_id doesn't detach the app: the null virtualNetworkSubnetId
# is left out of the PUT and Azure keeps the integration, whose
# legionservicelink then blocks deleting the subnet. This deletes the
# integration when subnet_id goes null or the app is destroyed. It references
# subnet_id, so terraform runs it before destroying the subnet.
resource "azapi_resource_action" "vnet_detach" {
  count       = var.subnet_id != null ? 1 : 0
  type        = "Microsoft.Web/sites/networkConfig@2024-11-01"
  resource_id = "${azapi_resource.app.id}/networkConfig/virtualNetwork"
  method      = "DELETE"
  when        = "destroy"
}

# Deployments authenticate with Entra ID (the deploy workflows use OIDC), so
# publishing credentials stay off. An update resource: these policies can't be
# deleted, they go with the app.
resource "azapi_update_resource" "basic_auth" {
  for_each  = toset(["scm", "ftp"])
  type      = "Microsoft.Web/sites/basicPublishingCredentialsPolicies@2024-11-01"
  name      = each.key
  parent_id = azapi_resource.app.id
  body = {
    properties = { allow = false }
  }
}

output "name" {
  value = azapi_resource.app.name
}

output "host_name" {
  value = azapi_resource.app.output.properties.defaultHostName
}
