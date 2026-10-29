output "tfstate_account" {
  value = azurerm_storage_account.tfstate.name
}

output "client_ids" {
  value = { for env, app in azuread_application.deploy : env => app.client_id }
}
