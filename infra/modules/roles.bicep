// All data-plane role assignments, each scoped as narrowly as the code allows.
param dataStorageName string
param textContainer string
param hostStorageName string
param serviceBusName string
param queueName string
param appInsightsName string
@description('User-assigned identity names. Assignment names derive from their resource ids, so what-if can resolve them.')
param identityNames {
  producer: string
  consumer: string
  ocr: string
}

var roles = {
  blobDataOwner: 'b7e6dc6d-f1e8-4753-8033-0f276bb0955b'
  blobDataContributor: 'ba92f5b4-2d11-453d-a403-e96b0029c9fe'
  blobDataReader: '2a2b9908-6ea1-4ae2-8e65-a410df84e7d1'
  serviceBusDataSender: '69a216fc-b8fb-44d8-bc22-1f3c2cd27a39'
  serviceBusDataReceiver: '4f6d3b9b-027b-4f4c-9142-0e5a2a2247e0'
  serviceBusDataOwner: '090c5cfd-751d-490a-894a-3ce6f1109419'
  monitoringMetricsPublisher: '3913510d-42f4-4e42-8a64-420c390055eb'
}

resource producer 'Microsoft.ManagedIdentity/userAssignedIdentities@2023-01-31' existing = {
  name: identityNames.producer
}

resource consumer 'Microsoft.ManagedIdentity/userAssignedIdentities@2023-01-31' existing = {
  name: identityNames.consumer
}

resource ocr 'Microsoft.ManagedIdentity/userAssignedIdentities@2023-01-31' existing = {
  name: identityNames.ocr
}

resource dataStorage 'Microsoft.Storage/storageAccounts@2024-01-01' existing = {
  name: dataStorageName

  resource blob 'blobServices' existing = {
    name: 'default'

    resource text 'containers' existing = {
      name: textContainer
    }
  }
}

resource hostStorage 'Microsoft.Storage/storageAccounts@2024-01-01' existing = {
  name: hostStorageName
}

resource serviceBus 'Microsoft.ServiceBus/namespaces@2024-01-01' existing = {
  name: serviceBusName

  resource queue 'queues' existing = {
    name: queueName
  }
}

resource appInsights 'Microsoft.Insights/components@2020-02-02' existing = {
  name: appInsightsName
}

func roleId(id string) string => subscriptionResourceId('Microsoft.Authorization/roleDefinitions', id)

// producer: writes text or PDFs, sends OCR requests.
resource producerData 'Microsoft.Authorization/roleAssignments@2022-04-01' = {
  scope: dataStorage
  name: guid(dataStorage.id, producer.id, roles.blobDataContributor)
  properties: {
    principalId: producer.properties.principalId
    principalType: 'ServicePrincipal'
    roleDefinitionId: roleId(roles.blobDataContributor)
  }
}

resource producerQueue 'Microsoft.Authorization/roleAssignments@2022-04-01' = {
  scope: serviceBus::queue
  name: guid(serviceBus::queue.id, producer.id, roles.serviceBusDataSender)
  properties: {
    principalId: producer.properties.principalId
    principalType: 'ServicePrincipal'
    roleDefinitionId: roleId(roles.serviceBusDataSender)
  }
}

// consumer: reads text only.
resource consumerText 'Microsoft.Authorization/roleAssignments@2022-04-01' = {
  scope: dataStorage::blob::text
  name: guid(dataStorage::blob::text.id, consumer.id, roles.blobDataReader)
  properties: {
    principalId: consumer.properties.principalId
    principalType: 'ServicePrincipal'
    roleDefinitionId: roleId(roles.blobDataReader)
  }
}

// ocr: reads and deletes PDFs, writes text, receives from the queue. The
// KEDA scaler reads the queue's message count, which needs Data Owner.
resource ocrData 'Microsoft.Authorization/roleAssignments@2022-04-01' = {
  scope: dataStorage
  name: guid(dataStorage.id, ocr.id, roles.blobDataContributor)
  properties: {
    principalId: ocr.properties.principalId
    principalType: 'ServicePrincipal'
    roleDefinitionId: roleId(roles.blobDataContributor)
  }
}

resource ocrQueueReceiver 'Microsoft.Authorization/roleAssignments@2022-04-01' = {
  scope: serviceBus::queue
  name: guid(serviceBus::queue.id, ocr.id, roles.serviceBusDataReceiver)
  properties: {
    principalId: ocr.properties.principalId
    principalType: 'ServicePrincipal'
    roleDefinitionId: roleId(roles.serviceBusDataReceiver)
  }
}

resource ocrQueueOwner 'Microsoft.Authorization/roleAssignments@2022-04-01' = {
  scope: serviceBus::queue
  name: guid(serviceBus::queue.id, ocr.id, roles.serviceBusDataOwner)
  properties: {
    principalId: ocr.properties.principalId
    principalType: 'ServicePrincipal'
    roleDefinitionId: roleId(roles.serviceBusDataOwner)
  }
}

// Functions host storage and deployment packages.
var hostUsers = [identityNames.producer, identityNames.consumer]

resource hostUser 'Microsoft.ManagedIdentity/userAssignedIdentities@2023-01-31' existing = [
  for n in hostUsers: { name: n }
]

resource hostOwner 'Microsoft.Authorization/roleAssignments@2022-04-01' = [
  for (n, i) in hostUsers: {
    scope: hostStorage
    name: guid(hostStorage.id, hostUser[i].id, roles.blobDataOwner)
    properties: {
      principalId: hostUser[i].properties.principalId
      principalType: 'ServicePrincipal'
      roleDefinitionId: roleId(roles.blobDataOwner)
    }
  }
]

// Entra-authenticated telemetry.
var telemetryUsers = [identityNames.producer, identityNames.consumer, identityNames.ocr]

resource telemetryUser 'Microsoft.ManagedIdentity/userAssignedIdentities@2023-01-31' existing = [
  for n in telemetryUsers: { name: n }
]

resource telemetry 'Microsoft.Authorization/roleAssignments@2022-04-01' = [
  for (n, i) in telemetryUsers: {
    scope: appInsights
    name: guid(appInsights.id, telemetryUser[i].id, roles.monitoringMetricsPublisher)
    properties: {
      principalId: telemetryUser[i].properties.principalId
      principalType: 'ServicePrincipal'
      roleDefinitionId: roleId(roles.monitoringMetricsPublisher)
    }
  }
]
