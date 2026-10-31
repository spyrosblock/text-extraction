# text-extraction: one environment (staging or prod) in one resource group.
# Deployed by staging.yml and prod.yml into rg-textextract-<env>:
#   terraform plan -var-file=<env>.tfvars -out tfplan && terraform apply tfplan
data "azurerm_resource_group" "this" {
  name = var.resource_group_name
}

locals {
  location = coalesce(var.location, data.azurerm_resource_group.this.location)
  suffix   = substr(sha256(lower(data.azurerm_resource_group.this.id)), 0, 6)
  tags     = { app = "text-extraction", env = var.env, iac = "terraform" }

  pdf_container     = "pdf-storage"
  text_container    = "text-storage"
  billing_container = "billing"
  queue_name        = "pdf_queue"

  names = {
    workspace      = "log-textextract-${var.env}"
    app_insights   = "appi-textextract-${var.env}"
    data_storage   = "sttxdata${var.env}${local.suffix}"
    host_storage   = "sttxhost${var.env}${local.suffix}"
    service_bus    = "sb-textextract-${var.env}-${local.suffix}"
    vnet           = "vnet-textextract-${var.env}"
    producer       = "func-textextract-producer-${var.env}-${local.suffix}"
    consumer       = "func-textextract-consumer-${var.env}-${local.suffix}"
    account        = "func-textextract-account-${var.env}-${local.suffix}"
    web            = "stapp-textextract-${var.env}"
    acs            = "acs-textextract-${var.env}-${local.suffix}"
    email          = "ecs-textextract-${var.env}-${local.suffix}"
    ca_environment = "cae-textextract-${var.env}"
    ocr_job        = "caj-ocr-${var.env}"
    identities = {
      producer = "id-textextract-producer-${var.env}"
      consumer = "id-textextract-consumer-${var.env}"
      account  = "id-textextract-account-${var.env}"
      ocr      = "id-textextract-ocr-${var.env}"
    }
  }

  deployment_containers = {
    producer = "app-package-producer"
    consumer = "app-package-consumer"
    account  = "app-package-account"
  }
}

module "monitoring" {
  source              = "./modules/monitoring"
  resource_group_name = data.azurerm_resource_group.this.name
  location            = local.location
  workspace_name      = local.names.workspace
  app_insights_name   = local.names.app_insights
  daily_quota_gb      = var.log_daily_quota_gb
  tags                = local.tags
}

module "identities" {
  source              = "./modules/identities"
  resource_group_name = data.azurerm_resource_group.this.name
  location            = local.location
  names               = local.names.identities
  tags                = local.tags
}

module "data_storage" {
  source                    = "./modules/storage-data"
  resource_group_name       = data.azurerm_resource_group.this.name
  location                  = local.location
  name                      = local.names.data_storage
  pdf_container             = local.pdf_container
  text_container            = local.text_container
  billing_container         = local.billing_container
  admin_ip_rules            = var.admin_ip_rules
  enable_private_networking = var.enable_private_networking
  tags                      = local.tags
}

module "host_storage" {
  source                = "./modules/storage-host"
  resource_group_name   = data.azurerm_resource_group.this.name
  location              = local.location
  name                  = local.names.host_storage
  deployment_containers = values(local.deployment_containers)
  tags                  = local.tags
}

module "service_bus" {
  source              = "./modules/servicebus"
  resource_group_name = data.azurerm_resource_group.this.name
  location            = local.location
  name                = local.names.service_bus
  queue_name          = local.queue_name
  tags                = local.tags
}

module "network" {
  source                  = "./modules/network"
  count                   = var.enable_private_networking ? 1 : 0
  resource_group_name     = data.azurerm_resource_group.this.name
  location                = local.location
  vnet_name               = local.names.vnet
  address_prefix          = var.vnet_address_prefix
  data_storage_account_id = module.data_storage.id
  tags                    = local.tags
}

module "email" {
  source                     = "./modules/email"
  resource_group_name        = data.azurerm_resource_group.this.name
  communication_service_name = local.names.acs
  email_service_name         = local.names.email
  custom_domain              = var.email_domain
  custom_domain_verified     = var.email_domain_verified
  tags                       = local.tags
}

module "web" {
  source              = "./modules/web"
  resource_group_name = data.azurerm_resource_group.this.name
  name                = local.names.web
  custom_domain       = var.web_domain
  tags                = local.tags
}

module "roles" {
  source                   = "./modules/roles"
  data_storage_id          = module.data_storage.id
  text_container_id        = module.data_storage.container_ids[local.text_container]
  billing_container_id     = module.data_storage.container_ids[local.billing_container]
  host_storage_id          = module.host_storage.id
  queue_id                 = module.service_bus.queue_id
  app_insights_id          = module.monitoring.app_insights_id
  communication_service_id = module.email.communication_service_id
  principal_ids            = { for k, v in module.identities.identities : k => v.principal_id }
}

locals {
  storage_account_url = trimsuffix(module.data_storage.blob_endpoint, "/")
  subnet_ids          = one(module.network[*].subnet_ids)
}

module "producer" {
  source                     = "./modules/functionapp"
  resource_group_id          = data.azurerm_resource_group.this.id
  resource_group_name        = data.azurerm_resource_group.this.name
  location                   = local.location
  name                       = local.names.producer
  plan_name                  = "plan-${local.names.producer}"
  identity                   = module.identities.identities.producer
  host_storage_name          = module.host_storage.name
  host_storage_blob_endpoint = module.host_storage.blob_endpoint
  deployment_container_name  = local.deployment_containers.producer
  # PDFium on PDFs up to 90MB.
  instance_memory_mb             = 4096
  maximum_instance_count         = var.function_max_instances
  http_per_instance_concurrency  = var.producer_http_concurrency
  app_insights_connection_string = module.monitoring.app_insights_connection_string
  subnet_id                      = try(local.subnet_ids.producer, null)
  app_settings = {
    STORAGE_ACCOUNT_URL    = local.storage_account_url
    SERVICEBUS_NAMESPACE   = "${module.service_bus.name}.servicebus.windows.net"
    PDF_STORAGE_CONTAINER  = local.pdf_container
    TEXT_STORAGE_CONTAINER = local.text_container
    BILLING_CONTAINER      = local.billing_container
    PDF_QUEUE_NAME         = local.queue_name
    KEY_PREFIX             = var.key_prefix
    CREDITS_PER_TEXT_PAGE  = tostring(var.credits_per_text_page)
    CREDITS_PER_OCR_PAGE   = tostring(var.credits_per_ocr_page)
  }
  tags       = local.tags
  depends_on = [module.roles]
}

module "consumer" {
  source                         = "./modules/functionapp"
  resource_group_id              = data.azurerm_resource_group.this.id
  resource_group_name            = data.azurerm_resource_group.this.name
  location                       = local.location
  name                           = local.names.consumer
  plan_name                      = "plan-${local.names.consumer}"
  identity                       = module.identities.identities.consumer
  host_storage_name              = module.host_storage.name
  host_storage_blob_endpoint     = module.host_storage.blob_endpoint
  deployment_container_name      = local.deployment_containers.consumer
  instance_memory_mb             = 2048
  maximum_instance_count         = var.function_max_instances
  app_insights_connection_string = module.monitoring.app_insights_connection_string
  subnet_id                      = try(local.subnet_ids.consumer, null)
  app_settings = {
    STORAGE_ACCOUNT_URL    = local.storage_account_url
    TEXT_STORAGE_CONTAINER = local.text_container
    BILLING_CONTAINER      = local.billing_container
  }
  tags       = local.tags
  depends_on = [module.roles]
}

# Hashes the email addresses in the billing container's freecredits/ markers
# (PLAN2_2). Kept in the state (Entra-only), so there's no secret to manage.
# Never replace it: every existing marker would stop matching and every
# address would get its free credits again. (No prevent_destroy: destroy.yml
# deletes the billing data, markers included, with it.)
resource "random_password" "free_credits" {
  length  = 64
  special = false
}

locals {
  default_site_url = "https://${module.web.default_host_name}"
  # Magic links and the checkout's return URLs go to the custom domain.
  site_url = var.web_domain != "" ? "https://${var.web_domain}" : local.default_site_url
}

# The website's back end: magic-link sign-in, self-service keys, and Stripe
# Checkout with its webhook.
module "account" {
  source                         = "./modules/functionapp"
  resource_group_id              = data.azurerm_resource_group.this.id
  resource_group_name            = data.azurerm_resource_group.this.name
  location                       = local.location
  name                           = local.names.account
  plan_name                      = "plan-${local.names.account}"
  identity                       = module.identities.identities.account
  host_storage_name              = module.host_storage.name
  host_storage_blob_endpoint     = module.host_storage.blob_endpoint
  deployment_container_name      = local.deployment_containers.account
  instance_memory_mb             = 2048
  maximum_instance_count         = var.function_max_instances
  app_insights_connection_string = module.monitoring.app_insights_connection_string
  subnet_id                      = try(local.subnet_ids.account, null)
  # Both hosts keep working when a custom domain is set.
  cors_allowed_origins = distinct([local.default_site_url, local.site_url])
  app_settings = {
    STORAGE_ACCOUNT_URL   = local.storage_account_url
    BILLING_CONTAINER     = local.billing_container
    KEY_PREFIX            = var.key_prefix
    CREDITS_PER_TEXT_PAGE = tostring(var.credits_per_text_page)
    CREDITS_PER_OCR_PAGE  = tostring(var.credits_per_ocr_page)
    SITE_URL              = local.site_url
    EMAIL_MODE            = "acs"
    ACS_ENDPOINT          = module.email.endpoint
    EMAIL_SENDER          = module.email.sender
    PACKS                 = jsonencode(var.credit_packs)
    FREE_CREDITS          = tostring(var.free_credits_per_account)
    MAX_KEYS              = tostring(var.max_keys_per_account)
    MAX_ACCOUNT_CREDITS   = tostring(var.max_credits_per_account)
    FREE_CREDITS_SECRET   = random_password.free_credits.result
    STRIPE_API_KEY        = var.stripe_api_key
    STRIPE_WEBHOOK_SECRET = var.stripe_webhook_secret
    PAYMENTS_TEST_MODE    = tostring(var.env != "prod")
  }
  tags       = local.tags
  depends_on = [module.roles]
}

module "ocr_job" {
  source              = "./modules/ocr-job"
  resource_group_name = data.azurerm_resource_group.this.name
  location            = local.location
  environment_name    = local.names.ca_environment
  job_name            = local.names.ocr_job
  subnet_id           = try(local.subnet_ids.cae, null)
  workspace_id        = module.monitoring.workspace_id
  image               = var.ocr_image
  identity            = module.identities.identities.ocr
  storage_account_url = local.storage_account_url
  service_bus_name    = module.service_bus.name
  queue_name          = local.queue_name
  max_executions      = var.ocr_max_executions
  tags                = local.tags
  depends_on          = [module.roles]
}
