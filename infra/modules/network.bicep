// VNet for the Flex apps and the Container Apps environment, plus the blob
// private endpoint of the data account and its private DNS zone.
param location string
param vnetName string
@description('A /22; the subnets are carved out of its first /24.')
param addressPrefix string
param dataStorageAccountId string
param tags object

// Flex Consumption VNet integration and workload-profile Container Apps
// environments both need the subnet delegated to Microsoft.App/environments.
var appDelegation = [
  {
    name: 'Microsoft.App.environments'
    properties: { serviceName: 'Microsoft.App/environments' }
  }
]

var subnets = {
  producer: { name: 'snet-func-producer', prefix: cidrSubnet(addressPrefix, 26, 0), delegations: appDelegation }
  consumer: { name: 'snet-func-consumer', prefix: cidrSubnet(addressPrefix, 26, 1), delegations: appDelegation }
  cae: { name: 'snet-cae', prefix: cidrSubnet(addressPrefix, 27, 4), delegations: appDelegation }
  pe: { name: 'snet-pe', prefix: cidrSubnet(addressPrefix, 28, 10), delegations: [] }
}

resource vnet 'Microsoft.Network/virtualNetworks@2024-05-01' = {
  name: vnetName
  location: location
  tags: tags
  properties: {
    addressSpace: { addressPrefixes: [addressPrefix] }
    subnets: [
      for s in items(subnets): {
        name: s.value.name
        properties: {
          addressPrefix: s.value.prefix
          delegations: s.value.delegations
          // Outbound to Service Bus, host storage, Entra ID, App Insights and
          // ghcr.io goes over the internet. New VNets default to private
          // subnets, so keep default outbound access on the app subnets.
          defaultOutboundAccess: s.key != 'pe'
        }
      }
    ]
  }
}

var blobZoneName = 'privatelink.blob.${environment().suffixes.storage}'

resource blobZone 'Microsoft.Network/privateDnsZones@2024-06-01' = {
  name: blobZoneName
  location: 'global'
  tags: tags
}

resource blobZoneLink 'Microsoft.Network/privateDnsZones/virtualNetworkLinks@2024-06-01' = {
  parent: blobZone
  name: vnet.name
  location: 'global'
  tags: tags
  properties: {
    virtualNetwork: { id: vnet.id }
    registrationEnabled: false
  }
}

resource blobPe 'Microsoft.Network/privateEndpoints@2024-05-01' = {
  name: 'pe-${last(split(dataStorageAccountId, '/'))}-blob'
  location: location
  tags: tags
  properties: {
    subnet: { id: '${vnet.id}/subnets/${subnets.pe.name}' }
    privateLinkServiceConnections: [
      {
        name: 'blob'
        properties: {
          privateLinkServiceId: dataStorageAccountId
          groupIds: ['blob']
        }
      }
    ]
  }
}

resource blobPeDns 'Microsoft.Network/privateEndpoints/privateDnsZoneGroups@2024-05-01' = {
  parent: blobPe
  name: 'default'
  properties: {
    privateDnsZoneConfigs: [
      {
        name: 'blob'
        properties: { privateDnsZoneId: blobZone.id }
      }
    ]
  }
}

output producerSubnetId string = '${vnet.id}/subnets/${subnets.producer.name}'
output consumerSubnetId string = '${vnet.id}/subnets/${subnets.consumer.name}'
output caeSubnetId string = '${vnet.id}/subnets/${subnets.cae.name}'
