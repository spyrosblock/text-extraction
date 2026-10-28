# Functions host storage (AzureWebJobsStorage) and the Flex Consumption
# deployment package containers. Public, identity-only.
variable "resource_group_name" {
  type = string
}
variable "location" {
  type = string
}
variable "name" {
  type = string
}
variable "deployment_containers" {
  type = list(string)
}
variable "tags" {
  type = map(string)
}

resource "azurerm_storage_account" "this" {
  name                            = var.name
  resource_group_name             = var.resource_group_name
  location                        = var.location
  account_kind                    = "StorageV2"
  account_tier                    = "Standard"
  account_replication_type        = "LRS"
  shared_access_key_enabled       = false
  default_to_oauth_authentication = true
  allow_nested_items_to_be_public = false
  min_tls_version                 = "TLS1_2"
  public_network_access           = "Enabled"

  network_rules {
    bypass         = ["AzureServices"]
    default_action = "Allow"
  }

  tags = var.tags
}

resource "azurerm_storage_container" "this" {
  for_each              = toset(var.deployment_containers)
  name                  = each.value
  storage_account_id    = azurerm_storage_account.this.id
  container_access_type = "private"
}

output "id" {
  value = azurerm_storage_account.this.id
}

output "name" {
  value = azurerm_storage_account.this.name
}

output "blob_endpoint" {
  value = azurerm_storage_account.this.primary_blob_endpoint
}
