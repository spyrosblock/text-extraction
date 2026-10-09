env                       = "staging"
resource_group_name       = "rg-textextract-staging-tf"
enable_private_networking = true
# Apart from the Bicep environments (10.20.0.0/22, 10.24.0.0/22).
vnet_address_prefix = "10.28.0.0/22"
admin_ip_rules      = []

# The infra deploy (staging-tf.yml/prod-tf.yml) sets TF_VAR_ocr_image to the
# image the job runs now; ocr_image defaults to the quickstart image.

log_daily_quota_gb     = 1
function_max_instances = 10
ocr_max_executions     = 5
