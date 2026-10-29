# State lives in the bootstrap's state account (infra/bootstrap), one container
# per environment, Entra auth only. The workflows pass the rest:
#   terraform init -backend-config=storage_account_name=<account> \
#     -backend-config=container_name=<staging|prod>
terraform {
  required_version = ">= 1.16"

  required_providers {
    azurerm = {
      source  = "hashicorp/azurerm"
      version = "~> 5.9"
    }
    azapi = {
      source  = "Azure/azapi"
      version = "~> 2.13"
    }
  }

  backend "azurerm" {
    key              = "text-extraction.tfstate"
    use_azuread_auth = true
  }
}

# The subscription comes from ARM_SUBSCRIPTION_ID.
provider "azurerm" {
  features {
    # Stop the provider reading storage accounts' queue and static-website
    # properties from the data plane on every refresh. Those calls would
    # fail from outside the VNet because the data account's endpoints are
    # private. Everything managed here goes through ARM (containers via
    # storage_account_id), so drift detection is unaffected;
    # queue_properties/static_website can't be set while this is off.
    storage {
      data_plane_available = false
    }
  }
  storage_use_azuread = true
  # The deployment identity only has rights on its resource group;
  # infra/bootstrap registers the providers.
  resource_provider_registrations = "none"
}

provider "azapi" {}
