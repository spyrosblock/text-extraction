# Data account: pdf-storage (PDFs waiting for OCR) and text-storage
# (extracted text, deleted after a day). Identity-only, and private: blob is
# reached through the private endpoint in the network module.
variable "resource_group_name" {
  type = string
}
variable "location" {
  type = string
}
variable "name" {
  type = string
}
variable "pdf_container" {
  type = string
}
variable "text_container" {
  type = string
}
variable "admin_ip_rules" {
  description = "Public IPs or CIDRs allowed through the firewall (e.g. Storage Browser from an admin machine). Empty = public access disabled."
  type        = list(string)
}
variable "enable_private_networking" {
  description = "false = public endpoint open to all networks (still identity-only)."
  type        = bool
}
variable "tags" {
  type = map(string)
}

resource "azurerm_storage_account" "this" {
  name                            = var.name
  resource_group_name             = var.resource_group_name
  location                        = var.location
  account_kind                    = "StorageV2"
  account_tier                    = "Standard"
  account_replication_type        = "LRS"
  shared_access_key_enabled       = false
  default_to_oauth_authentication = true
  allow_nested_items_to_be_public = false
  min_tls_version                 = "TLS1_2"
  public_network_access           = !var.enable_private_networking || length(var.admin_ip_rules) > 0 ? "Enabled" : "Disabled"

  network_rules {
    bypass         = ["None"]
    default_action = var.enable_private_networking ? "Deny" : "Allow"
    ip_rules       = var.admin_ip_rules
  }

  # No delete_retention_policy blocks: soft delete would keep deleted PDFs
  # and expired text recoverable.
  blob_properties {}

  tags = var.tags
}

resource "azurerm_storage_container" "this" {
  for_each              = toset([var.pdf_container, var.text_container])
  name                  = each.value
  storage_account_id    = azurerm_storage_account.this.id
  container_access_type = "private"
}

# Expire extracted text after a day. Lifecycle runs about once a day, so the
# consumer also treats older blobs as gone (handler.Retention).
# The OCR job deletes the PDFs it processes; the second rule only catches PDFs
# whose message was dead-lettered or expired.
resource "azurerm_storage_management_policy" "this" {
  storage_account_id = azurerm_storage_account.this.id

  rule {
    name    = "expire-extracted-text"
    enabled = true
    filters {
      blob_types   = ["blockBlob"]
      prefix_match = ["${var.text_container}/"]
    }
    actions {
      base_blob {
        delete_after_days_since_modification_greater_than = 1
      }
    }
  }

  rule {
    name    = "expire-abandoned-pdfs"
    enabled = true
    filters {
      blob_types   = ["blockBlob"]
      prefix_match = ["${var.pdf_container}/"]
    }
    actions {
      base_blob {
        delete_after_days_since_modification_greater_than = 7
      }
    }
  }
}

output "id" {
  value = azurerm_storage_account.this.id
}

output "name" {
  value = azurerm_storage_account.this.name
}

output "blob_endpoint" {
  value = azurerm_storage_account.this.primary_blob_endpoint
}

output "container_ids" {
  value = { for k, v in azurerm_storage_container.this : k => v.id }
}
