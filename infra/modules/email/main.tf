# Azure Communication Services Email for the magic-link sign-in: a
# communication service, an email service with the Azure-managed domain
# (DoNotReply@<guid>.azurecomm.net, no DNS needed) and the link between them.
# The account app sends through the communication service's endpoint with its
# managed identity (modules/roles, account-email).
variable "resource_group_name" {
  type = string
}
variable "communication_service_name" {
  type = string
}
variable "email_service_name" {
  type = string
}
variable "tags" {
  type = map(string)
}

# Both are global resources; data_location is where their data is kept.
resource "azurerm_communication_service" "this" {
  name                = var.communication_service_name
  resource_group_name = var.resource_group_name
  data_location       = "Europe"
  tags                = var.tags
}

resource "azurerm_email_communication_service" "this" {
  name                = var.email_service_name
  resource_group_name = var.resource_group_name
  data_location       = "Europe"
  tags                = var.tags
}

# Low send limits; a custom domain (with DNS records) raises them.
resource "azurerm_email_communication_service_domain" "azure_managed" {
  name              = "AzureManagedDomain"
  email_service_id  = azurerm_email_communication_service.this.id
  domain_management = "AzureManaged"
  tags              = var.tags
}

resource "azurerm_communication_service_email_domain_association" "this" {
  communication_service_id = azurerm_communication_service.this.id
  email_service_domain_id  = azurerm_email_communication_service_domain.azure_managed.id
}

output "communication_service_id" {
  value = azurerm_communication_service.this.id
}

output "endpoint" {
  value = "https://${azurerm_communication_service.this.hostname}"
}

# The domain must be linked before the communication service sends from it.
output "sender" {
  value      = "DoNotReply@${azurerm_email_communication_service_domain.azure_managed.from_sender_domain}"
  depends_on = [azurerm_communication_service_email_domain_association.this]
}
