# The website (web/): a Free Static Web App. The deploy workflows upload the
# files with the app's deployment token; there is no linked repository and no
# build. The region only holds metadata: the content is served globally.
variable "resource_group_name" {
  type = string
}
variable "name" {
  type = string
}
variable "location" {
  description = "Static Web Apps isn't offered in every region (not in northeurope)."
  type        = string
  default     = "eastus2"
}
variable "tags" {
  type = map(string)
}

resource "azurerm_static_web_app" "this" {
  name                = var.name
  resource_group_name = var.resource_group_name
  location            = var.location
  sku_tier            = "Free"
  sku_size            = "Free"
  # No staging copies of the site for pull requests.
  preview_environments_enabled = false
  tags                         = var.tags
}

output "name" {
  value = azurerm_static_web_app.this.name
}

output "default_host_name" {
  value = azurerm_static_web_app.this.default_host_name
}
