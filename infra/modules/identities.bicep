// One user-assigned identity per component.
param location string
param names {
  producer: string
  consumer: string
  ocr: string
}
param tags object

resource producer 'Microsoft.ManagedIdentity/userAssignedIdentities@2023-01-31' = {
  name: names.producer
  location: location
  tags: tags
}

resource consumer 'Microsoft.ManagedIdentity/userAssignedIdentities@2023-01-31' = {
  name: names.consumer
  location: location
  tags: tags
}

resource ocr 'Microsoft.ManagedIdentity/userAssignedIdentities@2023-01-31' = {
  name: names.ocr
  location: location
  tags: tags
}

output producer object = {
  id: producer.id
  principalId: producer.properties.principalId
  clientId: producer.properties.clientId
}
output consumer object = {
  id: consumer.id
  principalId: consumer.properties.principalId
  clientId: consumer.properties.clientId
}
output ocr object = {
  id: ocr.id
  principalId: ocr.properties.principalId
  clientId: ocr.properties.clientId
}
