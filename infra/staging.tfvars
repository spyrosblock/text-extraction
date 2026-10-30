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
