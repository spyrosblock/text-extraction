using './main.bicep'

param env = 'staging'
param enablePrivateNetworking = true
param vnetAddressPrefix = '10.20.0.0/22'
param adminIpRules = []

// The infra deploy (staging.yml/prod.yml) sets OCR_IMAGE to the image the job runs now.
param ocrImage = readEnvironmentVariable('OCR_IMAGE', 'mcr.microsoft.com/k8se/quickstart-jobs:latest')

param logDailyQuotaGb = '1'
param functionMaxInstances = 10
param ocrMaxExecutions = 5
