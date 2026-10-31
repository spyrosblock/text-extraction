env                       = "staging"
resource_group_name       = "rg-textextract-staging"
enable_private_networking = false
admin_ip_rules            = []
key_prefix                = "tx_stag_"

# The infra deploy (staging.yml/prod.yml) sets TF_VAR_ocr_image to the
# image the job runs now; ocr_image defaults to the quickstart image.

log_daily_quota_gb     = 1
function_max_instances = 10
ocr_max_executions     = 5

# Stripe test mode (PLAN2_3). Fill in the test-mode Price ids of the
# "OCR credits" product; price_eur must equal each Price's amount. Until
# they're set, leave STRIPE_API_KEY unset in the staging environment
# (checkout answers 503).
credit_packs = {
  starter = { credits = 2000, price_eur = 10, price_id = "" }
  pro     = { credits = 12000, price_eur = 50, price_id = "" }
}
prices_include_vat = true
contact_email      = ""
