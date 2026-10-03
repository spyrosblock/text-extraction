// Functions host storage (AzureWebJobsStorage) and the Flex Consumption
// deployment package containers. Public, identity-only.
param location string
param name string
param deploymentContainers string[]
param tags object

module account 'br/public:avm/res/storage/storage-account:0.33.1' = {
  params: {
    name: name
    location: location
    kind: 'StorageV2'
    skuName: 'Standard_LRS'
    allowSharedKeyAccess: false
    defaultToOAuthAuthentication: true
    allowBlobPublicAccess: false
    minimumTlsVersion: 'TLS1_2'
    publicNetworkAccess: 'Enabled'
    networkAcls: {
      bypass: 'AzureServices'
      defaultAction: 'Allow'
    }
    blobServices: {
      containers: [for c in deploymentContainers: { name: c, publicAccess: 'None' }]
    }
    enableTelemetry: false
    tags: tags
  }
}

output id string = account.outputs.resourceId
output name string = account.outputs.name
output blobEndpoint string = account.outputs.primaryBlobEndpoint
