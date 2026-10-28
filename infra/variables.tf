variable "env" {
  type = string
  validation {
    condition     = contains(["staging", "prod"], var.env)
    error_message = "env must be staging or prod."
  }
}

variable "resource_group_name" {
  description = "Existing resource group, created by bootstrap.sh."
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
