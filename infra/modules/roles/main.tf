# All data-plane role assignments, each scoped as narrowly as the code allows.
variable "data_storage_id" {
  type = string
}
variable "text_container_id" {
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
  # Keep in sync with ASSIGNABLE_ROLES in bootstrap.sh.
  roles = {
    blob_data_owner              = "b7e6dc6d-f1e8-4753-8033-0f276bb0955b"
    blob_data_contributor        = "ba92f5b4-2d11-453d-a403-e96b0029c9fe"
    blob_data_reader             = "2a2b9908-6ea1-4ae2-8e65-a410df84e7d1"
    service_bus_data_sender      = "69a216fc-b8fb-44d8-bc22-1f3c2cd27a39"
    service_bus_data_receiver    = "4f6d3b9b-027b-4f4c-9142-0e5a2a2247e0"
    service_bus_data_owner       = "090c5cfd-751d-490a-894a-3ce6f1109419"
    monitoring_metrics_publisher = "3913510d-42f4-4e42-8a64-420c390055eb"
  }

  assignments = {
    # producer: writes text or PDFs, sends OCR requests.
    producer-data  = { principal = "producer", role = "blob_data_contributor", scope = var.data_storage_id }
    producer-queue = { principal = "producer", role = "service_bus_data_sender", scope = var.queue_id }

    # consumer: reads text only.
    consumer-text = { principal = "consumer", role = "blob_data_reader", scope = var.text_container_id }

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
  # bootstrap.sh only lets the deployment identity assign roles to service
  # principals, and the condition checks the request's principal type.
  principal_type = "ServicePrincipal"
}
