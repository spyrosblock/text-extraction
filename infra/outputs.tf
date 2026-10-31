# smoke.sh and the workflows read these from `terraform output -json`.
output "producerName" {
  value = module.producer.name
}

output "producerHostName" {
  value = module.producer.host_name
}

output "consumerName" {
  value = module.consumer.name
}

output "consumerHostName" {
  value = module.consumer.host_name
}

output "ocrJobName" {
  value = module.ocr_job.job_name
}

output "dataStorageName" {
  value = module.data_storage.name
}

output "serviceBusName" {
  value = module.service_bus.name
}

output "accountName" {
  value = module.account.name
}

output "accountHostName" {
  value = module.account.host_name
}

output "webName" {
  value = module.web.name
}

output "webHostName" {
  value = module.web.default_host_name
}

# The website's address: the custom domain when set.
output "siteUrl" {
  value = local.site_url
}

# Stripe test mode (staging) or live mode (prod).
output "testMode" {
  value = var.env != "prod"
}

# Paste into the Stripe webhook endpoint of this environment's mode.
output "accountWebhookUrl" {
  value = "https://${module.account.host_name}/api/webhooks/stripe"
}

# The DNS records of email_domain (domain TXT, SPF, DKIM, DKIM2); empty
# without one.
output "emailDnsRecords" {
  value = module.email.dns_records
}

# For the website's config.js (.github/scripts/build-web.sh): the packs
# without their Stripe prices, the VAT label and the contact address.
output "publicPacks" {
  value = { for name, p in var.credit_packs : name => { credits = p.credits, price_eur = p.price_eur } }
}

output "pricesIncludeVat" {
  value = var.prices_include_vat
}

output "contactEmail" {
  value = var.contact_email
}
