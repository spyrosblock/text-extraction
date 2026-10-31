env                       = "prod"
resource_group_name       = "rg-textextract-prod"
enable_private_networking = true
vnet_address_prefix       = "10.32.0.0/22"
admin_ip_rules            = []
key_prefix                = "tx_prod_"

# The infra deploy (staging.yml/prod.yml) sets TF_VAR_ocr_image to the
# image the job runs now; ocr_image defaults to the quickstart image.

log_daily_quota_gb     = 5
function_max_instances = 40
ocr_max_executions     = 10

# Stripe live mode (PLAN2_3). Fill in the live-mode Price ids; price_eur
# must equal each Price's amount.
credit_packs = {
  starter = { credits = 2000, price_eur = 10, price_id = "" }
  pro     = { credits = 12000, price_eur = 50, price_id = "" }
}
prices_include_vat = true

# Custom domains (Launch checklist step 5). Set web_domain once the www CNAME
# points to the webHostName output. After the first apply with email_domain,
# add the emailDnsRecords output and a DMARC record at the registrar, verify
# the domain in Azure, then set email_domain_verified = true.
web_domain            = ""
email_domain          = ""
email_domain_verified = false
contact_email         = ""
