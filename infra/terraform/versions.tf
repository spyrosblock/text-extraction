# State lives in the bootstrap's state account (bootstrap.sh), one container
# per environment, Entra auth only. The workflows pass the rest:
#   terraform init -backend-config=storage_account_name=<account> \
#     -backend-config=container_name=<staging-tf|prod-tf>
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
    # The data account's blob endpoint is private, so manage containers
    # through ARM only.
    storage {
      data_plane_available = false
    }
  }
  storage_use_azuread = true
  # The deployment identity only has rights on its resource group;
  # bootstrap.sh registers the providers.
  resource_provider_registrations = "none"
}

provider "azapi" {}
