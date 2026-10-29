# Applied by hand with your own credentials (infra/bootstrap.sh), never by a
# workflow: it creates the identities and state the workflows depend on.
# State is local (terraform.tfstate here, gitignored): keep it.
terraform {
  required_version = ">= 1.16"

  required_providers {
    azurerm = {
      source  = "hashicorp/azurerm"
      version = "~> 5.9"
    }
    azuread = {
      source  = "hashicorp/azuread"
      version = "~> 3.10"
    }
    github = {
      source  = "integrations/github"
      version = "~> 6.13"
    }
  }
}

# Signs in with the az CLI; the subscription comes from ARM_SUBSCRIPTION_ID.
provider "azurerm" {
  features {
    # The state account has no shared keys and the caller's data access is
    # granted in the same apply, so manage containers through ARM only.
    storage {
      data_plane_available = false
    }
  }
  storage_use_azuread = true
  # What the main configuration (infra/) deploys; its identity can't
  # register providers.
  resource_provider_registrations = "none"
  resource_providers_to_register = [
    "Microsoft.Storage",
    "Microsoft.ServiceBus",
    "Microsoft.ManagedIdentity",
    "Microsoft.Web",
    "Microsoft.App",
    "Microsoft.OperationalInsights",
    "Microsoft.Insights",
    "Microsoft.Network",
  ]
}

provider "azuread" {}

# The token comes from GITHUB_TOKEN.
provider "github" {
  owner = local.owner
}
