# All data-plane role assignments, each scoped as narrowly as the code allows.
variable "data_storage_id" {
  type = string
}
variable "text_container_id" {
  type = string
}
variable "billing_container_id" {
  type = string
}
variable "host_storage_id" {
  type = string
}
variable "queue_id" {
  type = string
}
variable "app_insights_id" {
  type = string
}
variable "principal_ids" {
  description = "producer, consumer, ocr => principal id of the user-assigned identity."
  type = object({
    producer = string
    consumer = string
    ocr      = string
  })
}

# No API call, unlike azurerm_subscription: the deployment identity only has
# rights on its resource group.
data "azurerm_client_config" "current" {}

locals {
  # name => role definition id. infra/bootstrap lets the deployment identity
  # assign exactly these.
  roles = jsondecode(file("${path.module}/roles.json"))

  assignments = {
    # producer: writes text or PDFs, sends OCR requests, keeps the API keys
    # and their credits in the billing container.
    producer-data  = { principal = "producer", role = "blob_data_contributor", scope = var.data_storage_id }
    producer-queue = { principal = "producer", role = "service_bus_data_sender", scope = var.queue_id }

    # consumer: reads text, and API keys and job owners from billing.
    consumer-text    = { principal = "consumer", role = "blob_data_reader", scope = var.text_container_id }
    consumer-billing = { principal = "consumer", role = "blob_data_reader", scope = var.billing_container_id }

    # ocr: reads and deletes PDFs, writes text, receives from the queue. The
    # KEDA scaler reads the queue's message count, which needs Data Owner.
    ocr-data           = { principal = "ocr", role = "blob_data_contributor", scope = var.data_storage_id }
    ocr-queue-receiver = { principal = "ocr", role = "service_bus_data_receiver", scope = var.queue_id }
    ocr-queue-owner    = { principal = "ocr", role = "service_bus_data_owner", scope = var.queue_id }

    # Functions host storage and deployment packages.
    producer-host = { principal = "producer", role = "blob_data_owner", scope = var.host_storage_id }
    consumer-host = { principal = "consumer", role = "blob_data_owner", scope = var.host_storage_id }

    # Entra-authenticated telemetry.
    producer-telemetry = { principal = "producer", role = "monitoring_metrics_publisher", scope = var.app_insights_id }
    consumer-telemetry = { principal = "consumer", role = "monitoring_metrics_publisher", scope = var.app_insights_id }
    ocr-telemetry      = { principal = "ocr", role = "monitoring_metrics_publisher", scope = var.app_insights_id }
  }
}

resource "azurerm_role_assignment" "this" {
  for_each           = local.assignments
  scope              = each.value.scope
  role_definition_id = "/subscriptions/${data.azurerm_client_config.current.subscription_id}/providers/Microsoft.Authorization/roleDefinitions/${local.roles[each.value.role]}"
  principal_id       = var.principal_ids[each.value.principal]
  # infra/bootstrap only lets the deployment identity assign roles to service
  # principals, and the condition checks the request's principal type.
  principal_type = "ServicePrincipal"
}
