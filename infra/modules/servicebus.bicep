// Basic namespace (per-operation pricing) with pdf_queue. Entra auth only.
param location string
param name string
param queueName string
param tags object

module namespace 'br/public:avm/res/service-bus/namespace:0.17.1' = {
  params: {
    name: name
    location: location
    skuObject: { name: 'Basic' }
    disableLocalAuth: true
    authorizationRules: []
    minimumTlsVersion: '1.2'
    publicNetworkAccess: 'Enabled'
    queues: [
      {
        name: queueName
        lockDuration: 'PT5M'
        maxDeliveryCount: 5
        // The emulator's PT1H would drop messages whenever OCR falls behind.
        // Basic caps the TTL at 14 days.
        defaultMessageTimeToLive: 'P1D'
        deadLetteringOnMessageExpiration: true
        requiresDuplicateDetection: false
        requiresSession: false
        enablePartitioning: false
      }
    ]
    enableTelemetry: false
    tags: tags
  }
}

output id string = namespace.outputs.resourceId
output name string = namespace.outputs.name
