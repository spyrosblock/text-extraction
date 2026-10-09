# text-extraction: one environment (staging or prod) in one resource group,
# the Terraform twin of ../main.bicep. Deployed by staging-tf.yml and
# prod-tf.yml into rg-textextract-<env>-tf:
#   terraform plan -var-file=<env>.tfvars -out tfplan && terraform apply tfplan
data "azurerm_resource_group" "this" {
  name = var.resource_group_name
}

locals {
  location = coalesce(var.location, data.azurerm_resource_group.this.location)
  suffix   = substr(sha256(lower(data.azurerm_resource_group.this.id)), 0, 6)
  tags     = { app = "text-extraction", env = var.env, iac = "terraform" }

  pdf_container  = "pdf-storage"
  text_container = "text-storage"
  queue_name     = "pdf_queue"

  names = {
    workspace      = "log-textextract-${var.env}"
    app_insights   = "appi-textextract-${var.env}"
    data_storage   = "sttxdata${var.env}${local.suffix}"
    host_storage   = "sttxhost${var.env}${local.suffix}"
    service_bus    = "sb-textextract-${var.env}-${local.suffix}"
    vnet           = "vnet-textextract-${var.env}"
    producer       = "func-textextract-producer-${var.env}-${local.suffix}"
    consumer       = "func-textextract-consumer-${var.env}-${local.suffix}"
    ca_environment = "cae-textextract-${var.env}"
    ocr_job        = "caj-ocr-${var.env}"
    identities = {
      producer = "id-textextract-producer-${var.env}"
      consumer = "id-textextract-consumer-${var.env}"
      ocr      = "id-textextract-ocr-${var.env}"
    }
  }

  deployment_containers = {
    producer = "app-package-producer"
    consumer = "app-package-consumer"
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

module "roles" {
  source            = "./modules/roles"
  data_storage_id   = module.data_storage.id
  text_container_id = module.data_storage.container_ids[local.text_container]
  host_storage_id   = module.host_storage.id
  queue_id          = module.service_bus.queue_id
  app_insights_id   = module.monitoring.app_insights_id
  principal_ids     = { for k, v in module.identities.identities : k => v.principal_id }
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
    PDF_QUEUE_NAME         = local.queue_name
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
