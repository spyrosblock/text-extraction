# One user-assigned identity per component.
variable "resource_group_name" {
  type = string
}
variable "location" {
  type = string
}
variable "names" {
  type = object({
    producer = string
    consumer = string
    ocr      = string
  })
}
variable "tags" {
  type = map(string)
}

resource "azurerm_user_assigned_identity" "this" {
  for_each            = var.names
  name                = each.value
  resource_group_name = var.resource_group_name
  location            = var.location
  tags                = var.tags
}

# producer, consumer, ocr => { id, principal_id, client_id }
output "identities" {
  value = {
    for k, v in azurerm_user_assigned_identity.this : k => {
      id           = v.id
      principal_id = v.principal_id
      client_id    = v.client_id
    }
  }
}
