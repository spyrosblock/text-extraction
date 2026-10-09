# Container Apps environment (workload profiles, Consumption only) and the
# event-driven OCR job that KEDA starts while pdf_queue has messages.
variable "resource_group_name" {
  type = string
}
variable "location" {
  type = string
}
variable "environment_name" {
  type = string
}
variable "job_name" {
  type = string
}
variable "subnet_id" {
  description = "Infrastructure subnet; null = no VNet. It can only be set when the environment is created."
  type        = string
  default     = null
}
variable "workspace_id" {
  type = string
}
variable "image" {
  description = "Current job image. The infra deploy passes the deployed one so infra runs don't roll back the OCR deploy."
  type        = string
}
variable "identity" {
  type = object({
    id        = string
    client_id = string
  })
}
variable "storage_account_url" {
  type = string
}
variable "service_bus_name" {
  type = string
}
variable "queue_name" {
  type = string
}
variable "max_executions" {
  type = number
}
variable "tags" {
  type = map(string)
}

resource "azurerm_container_app_environment" "this" {
  name                     = var.environment_name
  resource_group_name      = var.resource_group_name
  location                 = var.location
  infrastructure_subnet_id = var.subnet_id
  # The job has no ingress, so nothing needs a public load balancer.
  internal_load_balancer_enabled = var.subnet_id != null
  # Console logs go to Log Analytics through the diagnostic setting below
  # (the log-analytics destination would need the workspace shared key).
  logs_destination = "azure-monitor"
  tags             = var.tags

  workload_profile {
    name                  = "Consumption"
    workload_profile_type = "Consumption"
  }

  lifecycle {
    # Azure names the platform-managed resource group (ME_...) when it isn't
    # set, and changing this attribute would replace the environment.
    ignore_changes = [infrastructure_resource_group_name]
  }
}

resource "azurerm_monitor_diagnostic_setting" "environment" {
  name                       = "to-log-analytics"
  target_resource_id         = azurerm_container_app_environment.this.id
  log_analytics_workspace_id = var.workspace_id

  enabled_log {
    category_group = "allLogs"
  }
}

resource "azurerm_container_app_job" "this" {
  name                         = var.job_name
  resource_group_name          = var.resource_group_name
  location                     = var.location
  container_app_environment_id = azurerm_container_app_environment.this.id
  workload_profile_name        = "Consumption"
  # Above the time the largest PDF takes; an execution handles several
  # messages before it goes idle.
  replica_timeout_in_seconds = 3600
  # Service Bus already retries failed messages.
  replica_retry_limit = 0
  tags                = var.tags

  identity {
    type         = "UserAssigned"
    identity_ids = [var.identity.id]
  }

  event_trigger_config {
    parallelism              = 1
    replica_completion_count = 1
    scale {
      min_executions              = 0
      max_executions              = var.max_executions
      polling_interval_in_seconds = 30
      rules {
        name             = "pdf-queue"
        custom_rule_type = "azure-servicebus"
        metadata = {
          queueName    = var.queue_name
          namespace    = var.service_bus_name
          messageCount = "1"
        }
        identity_id = var.identity.id
      }
    }
  }

  # No registry block: the image is a public ghcr.io package.
  template {
    container {
      name   = "ocr-container"
      image  = var.image
      cpu    = 2
      memory = "4Gi"

      dynamic "env" {
        for_each = {
          AZURE_CLIENT_ID      = var.identity.client_id
          STORAGE_ACCOUNT_URL  = var.storage_account_url
          SERVICEBUS_NAMESPACE = "${var.service_bus_name}.servicebus.windows.net"
          PDF_QUEUE_NAME       = var.queue_name
          # runtime.NumCPU() sees the host's cores, not the 2 vCPU limit.
          OCR_WORKERS = "2"
        }
        content {
          name  = env.key
          value = env.value
        }
      }
    }
  }
}

output "environment_name" {
  value = azurerm_container_app_environment.this.name
}

output "job_name" {
  value = azurerm_container_app_job.this.name
}
