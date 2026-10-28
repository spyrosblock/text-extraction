# Basic namespace (per-operation pricing) with pdf_queue. Entra auth only.
variable "resource_group_name" {
  type = string
}
variable "location" {
  type = string
}
variable "name" {
  type = string
}
variable "queue_name" {
  type = string
}
variable "tags" {
  type = map(string)
}

resource "azurerm_servicebus_namespace" "this" {
  name                          = var.name
  resource_group_name           = var.resource_group_name
  location                      = var.location
  sku                           = "Basic"
  local_auth_enabled            = false
  minimum_tls_version           = "1.2"
  public_network_access_enabled = true
  tags                          = var.tags
}

resource "azurerm_servicebus_queue" "this" {
  name               = var.queue_name
  namespace_id       = azurerm_servicebus_namespace.this.id
  lock_duration      = "PT5M"
  max_delivery_count = 5
  # The emulator's PT1H would drop messages whenever OCR falls behind.
  # Basic caps the TTL at 14 days.
  default_message_ttl                  = "P1D"
  dead_lettering_on_message_expiration = true
  requires_duplicate_detection         = false
  requires_session                     = false
  partitioning_enabled                 = false
}

output "id" {
  value = azurerm_servicebus_namespace.this.id
}

output "name" {
  value = azurerm_servicebus_namespace.this.name
}

output "queue_id" {
  value = azurerm_servicebus_queue.this.id
}
