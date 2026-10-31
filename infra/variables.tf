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
  description = "Credit packs the website sells. price_id is the pack's Stripe Price in this environment's mode (test in staging, live in prod); Stripe charges that Price's amount, and price_eur, only displayed, must equal it. Credits always come from here. The default has no price ids, for terraform validate; each tfvars file sets its own."
  type = map(object({
    credits   = number
    price_eur = number
    price_id  = optional(string, "")
  }))
  default = {
    starter = { credits = 2000, price_eur = 10 }
    pro     = { credits = 12000, price_eur = 50 }
  }
  validation {
    condition     = alltrue([for p in values(var.credit_packs) : p.credits > 0 && floor(p.credits) == p.credits && p.price_eur >= 0])
    error_message = "Each pack needs a whole number of credits above 0 and a price_eur of at least 0."
  }
  validation {
    condition = alltrue([for p in values(var.credit_packs) : p.price_id == "" || can(regex("^price_[A-Za-z0-9]+$", p.price_id))]) && (
      length(compact([for p in values(var.credit_packs) : p.price_id])) ==
      length(distinct(compact([for p in values(var.credit_packs) : p.price_id])))
    )
    error_message = "Each price_id must be a Stripe Price id (price_…), and no two packs can share one."
  }
  # The account app refuses to start with an API key but no price ids.
  validation {
    condition     = var.stripe_api_key == "" || alltrue([for p in values(var.credit_packs) : p.price_id != ""])
    error_message = "With stripe_api_key set, every pack in credit_packs needs its price_id."
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

variable "max_credits_per_account" {
  description = "Most credits an account can hold, summed over its keys; a checkout that would go over it is refused. 0: no cap."
  type        = number
  default     = 20000
  validation {
    condition     = var.max_credits_per_account >= 0 && floor(var.max_credits_per_account) == var.max_credits_per_account
    error_message = "max_credits_per_account must be a non-negative integer."
  }
  validation {
    condition     = var.max_credits_per_account == 0 || alltrue([for p in values(var.credit_packs) : p.credits <= var.max_credits_per_account])
    error_message = "Every pack in credit_packs must fit in max_credits_per_account."
  }
}

variable "prices_include_vat" {
  description = "Whether the Stripe Prices are tax-inclusive (tax_behavior = inclusive). Only changes how the website labels prices: \"incl. VAT\" or \"VAT added at checkout\"."
  type        = bool
  default     = true
}

# Stripe (PLAN2_3). The deploys set these from the GitHub environment
# secrets STRIPE_API_KEY and STRIPE_WEBHOOK_SECRET. The defaults let the PR
# plan job run without secrets; the account app refuses to start without a
# webhook secret, and without an API key checkout answers 503.
variable "stripe_api_key" {
  description = "Stripe restricted key with Checkout Sessions write and read: rk_test_… in staging, rk_live_… in prod."
  type        = string
  default     = ""
  sensitive   = true
  validation {
    condition     = var.stripe_api_key == "" || can(regex("^(rk|sk)_(test|live)_", var.stripe_api_key))
    error_message = "stripe_api_key must be a Stripe restricted key (rk_test_… or rk_live_…)."
  }
}

variable "stripe_webhook_secret" {
  description = "Signing secret (whsec_…) of the Stripe webhook endpoint at the accountWebhookUrl output."
  type        = string
  default     = ""
  sensitive   = true
}

variable "web_domain" {
  description = "Custom domain of the website, e.g. www.example.com; empty = only the default host. Its CNAME to the webHostName output must exist before the apply. Prod only."
  type        = string
  default     = ""
  validation {
    condition     = var.web_domain == "" || can(regex("^([a-z0-9]([a-z0-9-]*[a-z0-9])?\\.)+[a-z]{2,}$", var.web_domain))
    error_message = "web_domain must be a lowercase host name such as www.example.com, without https://."
  }
}

variable "email_domain" {
  description = "Customer-managed ACS email domain, e.g. example.com; empty = only the Azure-managed domain. Prod only. Its DNS records are in the emailDnsRecords output."
  type        = string
  default     = ""
  validation {
    condition     = var.email_domain == "" || can(regex("^([a-z0-9]([a-z0-9-]*[a-z0-9])?\\.)+[a-z]{2,}$", var.email_domain))
    error_message = "email_domain must be a lowercase domain such as example.com."
  }
}

variable "email_domain_verified" {
  description = "Set once Azure shows email_domain as verified (Domain, SPF, DKIM, DKIM2): the magic-link emails then come from DoNotReply@<email_domain>. False sends from the Azure-managed domain."
  type        = bool
  default     = false
}

variable "contact_email" {
  description = "Contact address shown in the website's footer and legal pages, e.g. support@example.com."
  type        = string
  default     = ""
}
