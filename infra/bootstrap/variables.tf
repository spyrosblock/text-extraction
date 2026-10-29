variable "repo" {
  description = "GitHub repository, <owner>/<name>."
  type        = string
}

variable "oidc_subject_prefix" {
  description = "Subject prefix of the repo's OIDC tokens (bootstrap.sh reads it; the github provider doesn't expose it)."
  type        = string
}

variable "location" {
  type    = string
  default = "northeurope"
}

variable "prod_reviewer" {
  description = "GitHub login of prod's required reviewer. Empty means the gh user."
  type        = string
  default     = ""
}

variable "tfstate_account" {
  description = "Name of the Terraform state storage account. Defaults to one derived from the subscription."
  type        = string
  default     = null
}
