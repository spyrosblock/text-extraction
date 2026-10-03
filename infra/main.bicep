// text-extraction: one environment (staging or prod) in one resource group.
// Deployed as a deployment stack by staging.yml and prod.yml:
//   az stack group create -g <rg> -n text-extraction -f infra/main.bicep \
//     -p infra/main.<env>.bicepparam \
//     --deny-settings-mode none --action-on-unmanage deleteResources
targetScope = 'resourceGroup'

@allowed(['staging', 'prod'])
param env string
param location string = resourceGroup().location

@description('Data account blob behind a private endpoint, all three components in a VNet.')
param enablePrivateNetworking bool = true
@description('VNet address space (a /22). Keep environments apart so they can be peered.')
param vnetAddressPrefix string = '10.20.0.0/22'
@description('Public IPs/CIDRs allowed through the data account firewall, for Storage Browser.')
param adminIpRules string[] = []

@description('OCR job image. The infra deploy passes the deployed one; the default is for the first deploy, before an OCR image has been pushed.')
param ocrImage string = 'mcr.microsoft.com/k8se/quickstart-jobs:latest'

@description('Log Analytics daily cap in GB; -1 = no cap.')
param logDailyQuotaGb string = '1'
param functionMaxInstances int = 20
@description('Concurrent requests per producer instance. Each holds up to a 90MB PDF plus PDFium\'s copy.')
param producerHttpConcurrency int = 4
param ocrMaxExecutions int = 10

var suffix = take(uniqueString(resourceGroup().id), 6)
var tags = { app: 'text-extraction', env: env }

var pdfContainer = 'pdf-storage'
var textContainer = 'text-storage'
var queueName = 'pdf_queue'

var names = {
  workspace: 'log-textextract-${env}'
  appInsights: 'appi-textextract-${env}'
  dataStorage: 'sttxdata${env}${suffix}'
  hostStorage: 'sttxhost${env}${suffix}'
  serviceBus: 'sb-textextract-${env}-${suffix}'
  vnet: 'vnet-textextract-${env}'
  producer: 'func-textextract-producer-${env}-${suffix}'
  consumer: 'func-textextract-consumer-${env}-${suffix}'
  caEnvironment: 'cae-textextract-${env}'
  ocrJob: 'caj-ocr-${env}'
  identities: {
    producer: 'id-textextract-producer-${env}'
    consumer: 'id-textextract-consumer-${env}'
    ocr: 'id-textextract-ocr-${env}'
  }
}

var deploymentContainers = {
  producer: 'app-package-producer'
  consumer: 'app-package-consumer'
}

module monitoring 'modules/monitoring.bicep' = {
  params: {
    location: location
    workspaceName: names.workspace
    appInsightsName: names.appInsights
    dailyQuotaGb: logDailyQuotaGb
    tags: tags
  }
}

module identities 'modules/identities.bicep' = {
  params: {
    location: location
    names: names.identities
    tags: tags
  }
}

module dataStorage 'modules/storage-data.bicep' = {
  params: {
    location: location
    name: names.dataStorage
    pdfContainer: pdfContainer
    textContainer: textContainer
    adminIpRules: adminIpRules
    enablePrivateNetworking: enablePrivateNetworking
    tags: tags
  }
}

module hostStorage 'modules/storage-host.bicep' = {
  params: {
    location: location
    name: names.hostStorage
    deploymentContainers: [deploymentContainers.producer, deploymentContainers.consumer]
    tags: tags
  }
}

module serviceBus 'modules/servicebus.bicep' = {
  params: {
    location: location
    name: names.serviceBus
    queueName: queueName
    tags: tags
  }
}

module network 'modules/network.bicep' = if (enablePrivateNetworking) {
  params: {
    location: location
    vnetName: names.vnet
    addressPrefix: vnetAddressPrefix
    dataStorageAccountId: dataStorage.outputs.id
    tags: tags
  }
}

module roles 'modules/roles.bicep' = {
  params: {
    dataStorageName: dataStorage.outputs.name
    textContainer: textContainer
    hostStorageName: hostStorage.outputs.name
    serviceBusName: serviceBus.outputs.name
    queueName: queueName
    appInsightsName: monitoring.outputs.appInsightsName
    identityNames: names.identities
  }
  dependsOn: [identities]
}

var storageAccountUrl = 'https://${names.dataStorage}.blob.${environment().suffixes.storage}'

module producer 'modules/functionapp.bicep' = {
  params: {
    location: location
    name: names.producer
    planName: 'plan-${names.producer}'
    identity: identities.outputs.producer
    hostStorageName: hostStorage.outputs.name
    deploymentContainerName: deploymentContainers.producer
    // PDFium on PDFs up to 90MB.
    instanceMemoryMB: 4096
    maximumInstanceCount: functionMaxInstances
    httpPerInstanceConcurrency: producerHttpConcurrency
    appInsightsConnectionString: monitoring.outputs.appInsightsConnectionString
    subnetId: enablePrivateNetworking ? network!.outputs.producerSubnetId : ''
    appSettings: {
      STORAGE_ACCOUNT_URL: storageAccountUrl
      SERVICEBUS_NAMESPACE: '${serviceBus.outputs.name}.servicebus.windows.net'
      PDF_STORAGE_CONTAINER: pdfContainer
      TEXT_STORAGE_CONTAINER: textContainer
      PDF_QUEUE_NAME: queueName
    }
    tags: tags
  }
  dependsOn: [roles]
}

module consumer 'modules/functionapp.bicep' = {
  params: {
    location: location
    name: names.consumer
    planName: 'plan-${names.consumer}'
    identity: identities.outputs.consumer
    hostStorageName: hostStorage.outputs.name
    deploymentContainerName: deploymentContainers.consumer
    instanceMemoryMB: 2048
    maximumInstanceCount: functionMaxInstances
    appInsightsConnectionString: monitoring.outputs.appInsightsConnectionString
    subnetId: enablePrivateNetworking ? network!.outputs.consumerSubnetId : ''
    appSettings: {
      STORAGE_ACCOUNT_URL: storageAccountUrl
      TEXT_STORAGE_CONTAINER: textContainer
    }
    tags: tags
  }
  dependsOn: [roles]
}

module ocrJob 'modules/ocr-job.bicep' = {
  params: {
    location: location
    environmentName: names.caEnvironment
    jobName: names.ocrJob
    subnetId: enablePrivateNetworking ? network!.outputs.caeSubnetId : ''
    workspaceId: monitoring.outputs.workspaceId
    image: ocrImage
    identity: identities.outputs.ocr
    storageAccountUrl: storageAccountUrl
    serviceBusName: serviceBus.outputs.name
    queueName: queueName
    maxExecutions: ocrMaxExecutions
    tags: tags
  }
  dependsOn: [roles]
}

output producerName string = producer.outputs.name
output producerHostName string = producer.outputs.hostName
output consumerName string = consumer.outputs.name
output consumerHostName string = consumer.outputs.hostName
output ocrJobName string = ocrJob.outputs.jobName
output dataStorageName string = dataStorage.outputs.name
output serviceBusName string = serviceBus.outputs.name
