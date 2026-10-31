variable "env" {
  type = string
  validation {
    condition     = contains(["staging", "prod"], var.env)
    error_message = "env must be staging or prod."
  }
}

variable "resource_group_name" {
  description = "Existing resource group, created by infra/bootstrap."
  type        = string
}

variable "location" {
  description = "Defaults to the resource group's location."
  type        = string
  default     = null
}

variable "enable_private_networking" {
  description = "Data account blob behind a private endpoint, all three components in a VNet."
  type        = bool
  default     = true
}

variable "vnet_address_prefix" {
  description = "VNet address space (a /22). Keep environments apart so they can be peered."
  type        = string
  default     = "10.28.0.0/22"
}

variable "admin_ip_rules" {
  description = "Public IPs/CIDRs allowed through the data account firewall, for Storage Browser."
  type        = list(string)
  default     = []
}

variable "ocr_image" {
  description = "OCR job image. The infra deploy passes the deployed one (TF_VAR_ocr_image); the default is for the first deploy, before an OCR image has been pushed."
  type        = string
  default     = "mcr.microsoft.com/k8se/quickstart-jobs:latest"
}

variable "log_daily_quota_gb" {
  description = "Log Analytics daily cap in GB; -1 = no cap."
  type        = number
  default     = 1
}

variable "function_max_instances" {
  type    = number
  default = 20
}

variable "producer_http_concurrency" {
  description = "Concurrent requests per producer instance. Each holds up to a 90MB PDF plus PDFium's copy."
  type        = number
  default     = 4
}

variable "ocr_max_executions" {
  type    = number
  default = 10
}

variable "key_prefix" {
  description = "Start of every API key issued in this environment, e.g. tx_prod_."
  type        = string
  validation {
    condition     = can(regex("^[a-z0-9_]+_$", var.key_prefix))
    error_message = "key_prefix must be lowercase letters, digits and underscores, ending in _."
  }
}

variable "credits_per_text_page" {
  description = "Credits charged for a page with a text layer."
  type        = number
  default     = 1
  validation {
    condition     = var.credits_per_text_page >= 0 && floor(var.credits_per_text_page) == var.credits_per_text_page
    error_message = "credits_per_text_page must be a non-negative integer."
  }
}

variable "credits_per_ocr_page" {
  description = "Credits charged for a page that needs OCR."
  type        = number
  default     = 5
  validation {
    condition     = var.credits_per_ocr_page >= 0 && floor(var.credits_per_ocr_page) == var.credits_per_ocr_page
    error_message = "credits_per_ocr_page must be a non-negative integer."
  }
}

variable "credit_packs" {
  description = "Credit packs the website sells. The price is only displayed; credits always come from here."
  type = map(object({
    credits   = number
    price_eur = number
  }))
  default = {
    starter = { credits = 2000, price_eur = 10 }
    pro     = { credits = 12000, price_eur = 50 }
  }
  validation {
    condition     = alltrue([for p in values(var.credit_packs) : p.credits > 0 && floor(p.credits) == p.credits && p.price_eur >= 0])
    error_message = "Each pack needs a whole number of credits above 0 and a price_eur of at least 0."
  }
}

variable "free_credits_per_account" {
  description = "Free credits on the first key of each email address, once."
  type        = number
  default     = 100
  validation {
    condition     = var.free_credits_per_account >= 0 && floor(var.free_credits_per_account) == var.free_credits_per_account
    error_message = "free_credits_per_account must be a non-negative integer."
  }
}

variable "max_keys_per_account" {
  type    = number
  default = 2
  validation {
    condition     = var.max_keys_per_account >= 1 && floor(var.max_keys_per_account) == var.max_keys_per_account
    error_message = "max_keys_per_account must be an integer of at least 1."
  }
}

variable "mock_tokens_password" {
  description = "Needed for every mock purchase; empty = purchases off. The deploys set TF_VAR_mock_tokens_password from the GitHub environment secret MOCK_TOKENS_PASSWORD."
  type        = string
  default     = ""
  sensitive   = true
}
