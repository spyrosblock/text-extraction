# Log Analytics workspace and a workspace-based Application Insights that
# only accepts Entra-authenticated telemetry.
variable "resource_group_name" {
  type = string
}
variable "location" {
  type = string
}
variable "workspace_name" {
  type = string
}
variable "app_insights_name" {
  type = string
}
variable "daily_quota_gb" {
  description = "Daily ingestion cap in GB; -1 means no cap."
  type        = number
}
variable "tags" {
  type = map(string)
}

resource "azurerm_log_analytics_workspace" "this" {
  name                = var.workspace_name
  resource_group_name = var.resource_group_name
  location            = var.location
  sku                 = "PerGB2018"
  retention_in_days   = 30
  daily_quota_gb      = var.daily_quota_gb
  tags                = var.tags
}

resource "azurerm_application_insights" "this" {
  name                         = var.app_insights_name
  resource_group_name          = var.resource_group_name
  location                     = var.location
  application_type             = "web"
  workspace_id                 = azurerm_log_analytics_workspace.this.id
  local_authentication_enabled = false
  tags                         = var.tags
}

output "workspace_id" {
  value = azurerm_log_analytics_workspace.this.id
}

output "app_insights_id" {
  value = azurerm_application_insights.this.id
}

# Local auth is off, so the connection string grants nothing on its own.
# Unmarking it keeps the function app bodies readable in plans.
output "app_insights_connection_string" {
  value = nonsensitive(azurerm_application_insights.this.connection_string)
}
