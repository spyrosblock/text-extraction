// Container Apps environment (workload profiles, Consumption only) and the
// event-driven OCR job that KEDA starts while pdf_queue has messages.
param location string
param environmentName string
param jobName string
@description('Infrastructure subnet; empty = no VNet. It can only be set when the environment is created.')
param subnetId string = ''
param workspaceId string
@description('Current job image. The infra deploy passes the deployed one so infra runs don\'t roll back the OCR deploy.')
param image string
param identity {
  id: string
  clientId: string
}
param storageAccountUrl string
param serviceBusName string
param queueName string
param maxExecutions int
param tags object

resource env 'Microsoft.App/managedEnvironments@2025-01-01' = {
  name: environmentName
  location: location
  tags: tags
  properties: {
    workloadProfiles: [
      {
        name: 'Consumption'
        workloadProfileType: 'Consumption'
      }
    ]
    vnetConfiguration: empty(subnetId)
      ? null
      : {
          infrastructureSubnetId: subnetId
          // The job has no ingress, so nothing needs a public load balancer.
          internal: true
        }
    // Console logs go to Log Analytics through the diagnostic setting below
    // (the 'log-analytics' destination would need the workspace shared key).
    appLogsConfiguration: {
      destination: 'azure-monitor'
    }
  }
}

resource envDiagnostics 'Microsoft.Insights/diagnosticSettings@2021-05-01-preview' = {
  scope: env
  name: 'to-log-analytics'
  properties: {
    workspaceId: workspaceId
    logs: [
      { categoryGroup: 'allLogs', enabled: true }
    ]
  }
}

resource job 'Microsoft.App/jobs@2025-01-01' = {
  name: jobName
  location: location
  tags: tags
  identity: {
    type: 'UserAssigned'
    userAssignedIdentities: { '${identity.id}': {} }
  }
  properties: {
    environmentId: env.id
    workloadProfileName: 'Consumption'
    configuration: {
      triggerType: 'Event'
      // Above the time the largest PDF takes; an execution handles several
      // messages before it goes idle.
      replicaTimeout: 3600
      // Service Bus already retries failed messages.
      replicaRetryLimit: 0
      eventTriggerConfig: {
        parallelism: 1
        replicaCompletionCount: 1
        scale: {
          minExecutions: 0
          maxExecutions: maxExecutions
          pollingInterval: 30
          rules: [
            {
              name: 'pdf-queue'
              type: 'azure-servicebus'
              metadata: {
                queueName: queueName
                namespace: serviceBusName
                messageCount: '1'
              }
              identity: identity.id
            }
          ]
        }
      }
      // No registries block: the image is a public ghcr.io package.
    }
    template: {
      containers: [
        {
          name: 'ocr-container'
          image: image
          resources: {
            cpu: json('2')
            memory: '4Gi'
          }
          env: [
            { name: 'AZURE_CLIENT_ID', value: identity.clientId }
            { name: 'STORAGE_ACCOUNT_URL', value: storageAccountUrl }
            { name: 'SERVICEBUS_NAMESPACE', value: '${serviceBusName}.servicebus.windows.net' }
            { name: 'PDF_QUEUE_NAME', value: queueName }
            // runtime.NumCPU() sees the host's cores, not the 2 vCPU limit.
            { name: 'OCR_WORKERS', value: '2' }
          ]
        }
      ]
    }
  }
}

output environmentName string = env.name
output jobName string = job.name
