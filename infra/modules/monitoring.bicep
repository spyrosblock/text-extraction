// Log Analytics workspace and a workspace-based Application Insights that
// only accepts Entra-authenticated telemetry.
param location string
param workspaceName string
param appInsightsName string
@description('Daily ingestion cap in GB, as a string; -1 means no cap.')
param dailyQuotaGb string
param tags object

module workspace 'br/public:avm/res/operational-insights/workspace:0.16.1' = {
  params: {
    name: workspaceName
    location: location
    skuName: 'PerGB2018'
    dataRetention: 30
    dailyQuotaGb: dailyQuotaGb
    enableTelemetry: false
    tags: tags
  }
}

resource appInsights 'Microsoft.Insights/components@2020-02-02' = {
  name: appInsightsName
  location: location
  kind: 'web'
  tags: tags
  properties: {
    Application_Type: 'web'
    WorkspaceResourceId: workspace.outputs.resourceId
    DisableLocalAuth: true
  }
}

output workspaceId string = workspace.outputs.resourceId
output appInsightsName string = appInsights.name
output appInsightsConnectionString string = appInsights.properties.ConnectionString
