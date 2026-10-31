# Azure Communication Services Email for the magic-link sign-in: a
# communication service, an email service with the Azure-managed domain
# (DoNotReply@<guid>.azurecomm.net, no DNS needed) and the link between them.
# The account app sends through the communication service's endpoint with its
# managed identity (modules/roles, account-email).
#
# With custom_domain, a customer-managed domain is added too. Terraform
# doesn't verify it: add the dns_records at the registrar, start the
# verification (portal: Email Communication Service > Provision domains >
# Verify, or `az communication email domain initiate-verification` for
# Domain, SPF, DKIM and DKIM2), then set custom_domain_verified. Only then is
# it linked and used as the sender; the Azure-managed domain stays linked, so
# switching back is one variable.
variable "resource_group_name" {
  type = string
}
variable "communication_service_name" {
  type = string
}
variable "email_service_name" {
  type = string
}
variable "custom_domain" {
  description = "e.g. example.com; empty = none."
  type        = string
  default     = ""
}
variable "custom_domain_verified" {
  description = "Link the custom domain and send from DoNotReply@<custom_domain>. Set once Azure shows it verified."
  type        = bool
  default     = false
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

# Named after the domain, as ACS requires for customer-managed domains.
resource "azurerm_email_communication_service_domain" "custom" {
  count             = var.custom_domain != "" ? 1 : 0
  name              = var.custom_domain
  email_service_id  = azurerm_email_communication_service.this.id
  domain_management = "CustomerManaged"
  tags              = var.tags
}

# ACS links only verified domains.
resource "azurerm_communication_service_email_domain_association" "custom" {
  count                    = var.custom_domain != "" && var.custom_domain_verified ? 1 : 0
  communication_service_id = azurerm_communication_service.this.id
  email_service_domain_id  = azurerm_email_communication_service_domain.custom[0].id
}

locals {
  sender_domain = (length(azurerm_communication_service_email_domain_association.custom) > 0
    ? azurerm_email_communication_service_domain.custom[0].from_sender_domain
  : azurerm_email_communication_service_domain.azure_managed.from_sender_domain)
}

output "communication_service_id" {
  value = azurerm_communication_service.this.id
}

output "endpoint" {
  value = "https://${azurerm_communication_service.this.hostname}"
}

# The domain must be linked before the communication service sends from it.
output "sender" {
  value = "DoNotReply@${local.sender_domain}"
  depends_on = [
    azurerm_communication_service_email_domain_association.this,
    azurerm_communication_service_email_domain_association.custom,
  ]
}

# The custom domain's DNS records to add at the registrar: the domain's TXT,
# SPF (TXT) and two DKIM CNAMEs. ACS doesn't create a DMARC record; add
# _dmarc.<domain> TXT "v=DMARC1; p=none; rua=mailto:…" by hand.
output "dns_records" {
  value = flatten([
    for vr in try(azurerm_email_communication_service_domain.custom[0].verification_records, []) : [
      for purpose in ["domain", "spf", "dkim", "dkim2"] : [
        for r in vr[purpose] : { purpose = purpose, type = r.type, name = r.name, value = r.value, ttl = r.ttl }
      ]
    ]
  ])
}
