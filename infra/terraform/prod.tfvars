env                       = "prod"
resource_group_name       = "rg-textextract-prod-tf"
enable_private_networking = true
# Apart from the Bicep environments (10.20.0.0/22, 10.24.0.0/22).
vnet_address_prefix = "10.32.0.0/22"
admin_ip_rules      = []

# The infra deploy (staging-tf.yml/prod-tf.yml) sets TF_VAR_ocr_image to the
# image the job runs now; ocr_image defaults to the quickstart image.

log_daily_quota_gb     = 5
function_max_instances = 40
ocr_max_executions     = 10
