# VNet for the Flex apps and the Container Apps environment, plus the blob
# private endpoint of the data account and its private DNS zone.
variable "resource_group_name" {
  type = string
}
variable "location" {
  type = string
}
variable "vnet_name" {
  type = string
}
variable "address_prefix" {
  description = "A /22; the subnets are carved out of its first /24."
  type        = string
}
variable "data_storage_account_id" {
  type = string
}
variable "tags" {
  type = map(string)
}

locals {
  # Flex Consumption VNet integration and workload-profile Container Apps
  # environments both need the subnet delegated to Microsoft.App/environments.
  subnets = {
    producer = { name = "snet-func-producer", prefix = cidrsubnet(var.address_prefix, 4, 0), delegated = true }
    consumer = { name = "snet-func-consumer", prefix = cidrsubnet(var.address_prefix, 4, 1), delegated = true }
    cae      = { name = "snet-cae", prefix = cidrsubnet(var.address_prefix, 5, 4), delegated = true }
    pe       = { name = "snet-pe", prefix = cidrsubnet(var.address_prefix, 6, 10), delegated = false }
  }
}

resource "azurerm_virtual_network" "this" {
  name                = var.vnet_name
  resource_group_name = var.resource_group_name
  location            = var.location
  address_space       = [var.address_prefix]
  tags                = var.tags
}

resource "azurerm_subnet" "this" {
  for_each             = local.subnets
  name                 = each.value.name
  resource_group_name  = var.resource_group_name
  virtual_network_name = azurerm_virtual_network.this.name
  address_prefixes     = [each.value.prefix]
  # Outbound to Service Bus, host storage, Entra ID, App Insights and
  # ghcr.io goes over the internet. New VNets default to private subnets, so
  # keep default outbound access on the app subnets.
  default_outbound_access_enabled = each.key != "pe"

  dynamic "delegation" {
    for_each = each.value.delegated ? [1] : []
    content {
      name = "Microsoft.App.environments"
      service_delegation {
        name    = "Microsoft.App/environments"
        actions = ["Microsoft.Network/virtualNetworks/subnets/join/action"]
      }
    }
  }
}

resource "azurerm_private_dns_zone" "blob" {
  name                = "privatelink.blob.core.windows.net"
  resource_group_name = var.resource_group_name
  tags                = var.tags
}

resource "azurerm_private_dns_zone_virtual_network_link" "blob" {
  name                 = azurerm_virtual_network.this.name
  private_dns_zone_id  = azurerm_private_dns_zone.blob.id
  virtual_network_id   = azurerm_virtual_network.this.id
  registration_enabled = false
  tags                 = var.tags
}

resource "azurerm_private_endpoint" "blob" {
  name                = "pe-${basename(var.data_storage_account_id)}-blob"
  resource_group_name = var.resource_group_name
  location            = var.location
  subnet_id           = azurerm_subnet.this["pe"].id
  tags                = var.tags

  private_service_connection {
    name                           = "blob"
    private_connection_resource_id = var.data_storage_account_id
    subresource_names              = ["blob"]
    is_manual_connection           = false
  }

  private_dns_zone_group {
    name                 = "default"
    private_dns_zone_ids = [azurerm_private_dns_zone.blob.id]
  }
}

# producer, consumer, cae, pe => subnet id
output "subnet_ids" {
  value = { for k, v in azurerm_subnet.this : k => v.id }
}
