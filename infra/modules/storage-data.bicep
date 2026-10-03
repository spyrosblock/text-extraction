// Data account: pdf-storage (PDFs waiting for OCR) and text-storage
// (extracted text, deleted after a day). Identity-only, and private: blob is
// reached through the private endpoint in network.bicep.
param location string
param name string
param pdfContainer string
param textContainer string
@description('Public IPs or CIDRs allowed through the firewall (e.g. Storage Browser from an admin machine). Empty = public access disabled.')
param adminIpRules string[]
@description('false = public endpoint open to all networks (still identity-only).')
param enablePrivateNetworking bool
param tags object

// Expire extracted text after a day. Lifecycle runs about once a day, so the
// consumer also treats older blobs as gone (handler.Retention).
var lifecycleRules = [
  {
    enabled: true
    name: 'expire-extracted-text'
    type: 'Lifecycle'
    definition: {
      filters: {
        blobTypes: ['blockBlob']
        prefixMatch: ['${textContainer}/']
      }
      actions: {
        baseBlob: {
          delete: { daysAfterModificationGreaterThan: 1 }
        }
      }
    }
  }
]

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
    publicNetworkAccess: !enablePrivateNetworking || !empty(adminIpRules) ? 'Enabled' : 'Disabled'
    networkAcls: {
      bypass: 'None'
      defaultAction: enablePrivateNetworking ? 'Deny' : 'Allow'
      ipRules: [for ip in adminIpRules: { action: 'Allow', value: ip }]
    }
    blobServices: {
      // Soft delete would keep deleted PDFs and expired text recoverable.
      deleteRetentionPolicyEnabled: false
      containerDeleteRetentionPolicyEnabled: false
      containers: [
        { name: pdfContainer, publicAccess: 'None' }
        { name: textContainer, publicAccess: 'None' }
      ]
    }
    managementPolicyRules: lifecycleRules
    enableTelemetry: false
    tags: tags
  }
}

output id string = account.outputs.resourceId
output name string = account.outputs.name
output blobEndpoint string = account.outputs.primaryBlobEndpoint
