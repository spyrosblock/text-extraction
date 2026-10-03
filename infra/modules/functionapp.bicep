// A Flex Consumption plan and app running the native Go worker, with
// identity-based host storage, deployment storage and App Insights.
// Code is deployed separately (staging.yml/prod.yml, target functions);
// re-running this leaves the package in the deployment container alone.
param location string
param name string
param planName string
param identity {
  id: string
  clientId: string
}
param hostStorageName string
param deploymentContainerName string
@allowed([512, 2048, 4096])
param instanceMemoryMB int
param maximumInstanceCount int
@description('Concurrent HTTP requests per instance; 0 = platform default.')
param httpPerInstanceConcurrency int = 0
param appInsightsConnectionString string
@description('Subnet for VNet integration; empty = none.')
param subnetId string = ''
@description('App-specific settings, merged over the common ones.')
param appSettings object = {}
param tags object

var blobSuffix = 'blob.${environment().suffixes.storage}'

var commonSettings = {
  AzureWebJobsStorage__accountName: hostStorageName
  AzureWebJobsStorage__credential: 'managedidentity'
  AzureWebJobsStorage__clientId: identity.clientId
  APPLICATIONINSIGHTS_CONNECTION_STRING: appInsightsConnectionString
  APPLICATIONINSIGHTS_AUTHENTICATION_STRING: 'ClientId=${identity.clientId};Authorization=AAD'
  AZURE_CLIENT_ID: identity.clientId
}

resource plan 'Microsoft.Web/serverfarms@2024-11-01' = {
  name: planName
  location: location
  tags: tags
  kind: 'functionapp'
  sku: {
    tier: 'FlexConsumption'
    name: 'FC1'
  }
  properties: {
    reserved: true
  }
}

resource app 'Microsoft.Web/sites@2024-11-01' = {
  name: name
  location: location
  tags: tags
  kind: 'functionapp,linux'
  identity: {
    type: 'UserAssigned'
    userAssignedIdentities: { '${identity.id}': {} }
  }
  properties: {
    serverFarmId: plan.id
    httpsOnly: true
    virtualNetworkSubnetId: empty(subnetId) ? null : subnetId
    functionAppConfig: {
      deployment: {
        storage: {
          type: 'blobContainer'
          value: 'https://${hostStorageName}.${blobSuffix}/${deploymentContainerName}'
          authentication: {
            type: 'UserAssignedIdentity'
            userAssignedIdentityResourceId: identity.id
          }
        }
      }
      scaleAndConcurrency: {
        instanceMemoryMB: instanceMemoryMB
        maximumInstanceCount: maximumInstanceCount
        triggers: httpPerInstanceConcurrency > 0
          ? { http: { perInstanceConcurrency: httpPerInstanceConcurrency } }
          : null
      }
      // Native Go worker (azure-functions-golang-worker), per the Step 0 spike.
      runtime: {
        name: 'go'
        version: '1.0'
      }
    }
    siteConfig: {
      minTlsVersion: '1.2'
      ftpsState: 'Disabled'
      appSettings: [for s in items(union(commonSettings, appSettings)): { name: s.key, value: s.value }]
    }
  }
}

// Deployments authenticate with Entra ID (the deploy workflows use OIDC), so
// publishing credentials stay off.
resource scmBasicAuth 'Microsoft.Web/sites/basicPublishingCredentialsPolicies@2024-11-01' = {
  parent: app
  name: 'scm'
  properties: { allow: false }
}

resource ftpBasicAuth 'Microsoft.Web/sites/basicPublishingCredentialsPolicies@2024-11-01' = {
  parent: app
  name: 'ftp'
  properties: { allow: false }
}

output name string = app.name
output hostName string = app.properties.defaultHostName
