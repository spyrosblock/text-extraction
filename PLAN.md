# Deploying text-extraction to Azure

## Context

The repo has three Go components that are ready to run locally:
- `producer` and `consumer`: Azure Functions, native Go worker, Flex Consumption.
- `ocr_container`: a Container Apps job triggered by KEDA.

They share one Storage account (`pdf-storage` and `text-storage`) and one Service Bus queue (`pdf_queue`). Today, deploying means hand-typed `az` and `func` commands in each README. There's no infrastructure as code and no CI/CD. The code is on GitHub at `spyrosblock/text-extraction` (public, so environment required reviewers work on the free plan).

The goal is repeatable **staging** and **prod** deployments:
- **Bicep** for the infrastructure.
- **GitHub Actions** with OIDC for the code.
- Managed identity everywhere, as SPEC.md requires.
- In both environments, **only the data Storage account** (`pdf-storage` and `text-storage`) is private.

**Decisions:**
- Bicep, GitHub Actions, staging and prod.
- Region: **northeurope** for both environments. westeurope rejects new resources for this subscription ("not accepting new customers").
- Staging and prod are built the same way: the data account's blob sits behind a private endpoint, and all three components join a VNet to reach it.
- Everything else stays public with identity-only access: the function host storage, Service Bus **Basic** and the HTTP APIs.
- The OCR image lives in a **public ghcr.io package** (`ghcr.io/spyrosblock/ocr-container`, already published and public). Pulling needs no credentials, and one tag goes to both environments. There's no ACR.

---

## Step 0: Spike (manual, before writing the Functions IaC)

The native Go worker on Flex is the biggest unknown. Before writing anything else:
1. Create a Flex app by hand in a scratch RG.
2. Find the `functionAppConfig.runtime` name and version the Go worker (`azure-functions-golang-worker`) needs.
3. Deploy with `func azure functionapp publish`, then with `func pack` plus zip/One Deploy.
4. Call `/api/extract` with `test1.pdf`, which is about 90 MB class. Check the request body limit and the 230 s HTTP timeout.

The findings decide the `runtime` block in `functionapp.bicep` and the deploy step in `functions.yml`.

---

## Step 1: Bicep (`infra/`)

```
infra/
  main.bicep                  # RG scope; params: env, location, enablePrivateNetworking, ocrImage, adminIpRules
  main.staging.bicepparam    # enablePrivateNetworking = true, VNet 10.20.0.0/22
  main.prod.bicepparam       # enablePrivateNetworking = true, VNet 10.24.0.0/22
  bootstrap.sh                # one-time setup (Step 2)
  modules/
    monitoring.bicep          # Log Analytics (daily cap param) + App Insights (Entra auth, local auth off)
    network.bicep             # VNet, subnets, blob private endpoint, private DNS zone
    storage-data.bicep        # pdf-storage, text-storage, lifecycle policy, network rules
    storage-host.bicep        # AzureWebJobsStorage + Flex deployment containers
    servicebus.bicep          # Basic namespace + pdf_queue
    identities.bicep          # 3 user-assigned identities
    functionapp.bicep         # Flex plan + app, used twice (producer, consumer)
    ocr-job.bicep             # Container Apps env + job (image from ghcr.io)
    roles.bicep               # all data-plane role assignments
```

Use Azure Verified Modules (`br/public:avm/res/...`) for Storage, Service Bus and Log Analytics where they fit. Write Flex and the Container Apps job by hand.

### Resources and settings
- **storage-data**:
  - Settings: StorageV2, `allowSharedKeyAccess: false`, `allowBlobPublicAccess: false`, TLS 1.2 minimum.
  - Containers: `pdf-storage` and `text-storage`.
  - Lifecycle policy: an `expire-extracted-text` rule defined inline, with `prefixMatch` built from the `textContainer` param (delete after 1 day).
  - Plus an `expire-abandoned-pdfs` rule: delete block blobs under `pdf-storage/` more than **7 days** after last modification. The OCR job deletes PDFs it processes, so this only catches PDFs whose message was dead-lettered or expired.
  - Networking (both envs): `publicNetworkAccess: Disabled`. If `adminIpRules` is set, use `Enabled` with `defaultAction: Deny` plus those IPs, so Storage Browser works from your machine.
- **storage-host**: shared-key access off. One deployment blob container per function app. Stays public in every environment.
- **servicebus**:
  - **Basic** tier, `disableLocalAuth: true`. Basic is pay-per-operation (~$0.05 per million), so it costs effectively $0. The code uses only peek-lock receive, complete, abandon, dead-letter and lock renewal, which Basic supports, along with the dead-letter queue and TTL. Basic lacks topics, sessions, duplicate detection and transactions, none of which the code uses. Basic caps TTL at 14 days.
  - `pdf_queue` settings: lock `PT5M`, `maxDeliveryCount: 5`, as in `emulators/servicebus-config.json`.
  - **TTL `P1D` with `deadLetteringOnMessageExpiration: true`.** The emulator's `PT1H` would drop messages whenever the OCR job falls behind.
- **functionapp** (×2):
  - Plan and instances: Flex plan (FC1). Instance memory 4096 MB for the producer (PDFium on 90 MB PDFs) and 2048 MB for the consumer. `maximumInstanceCount` is a param.
  - Deployment storage uses user-assigned identity authentication.
  - App settings:
    - `STORAGE_ACCOUNT_URL`, `AZURE_CLIENT_ID`.
    - `SERVICEBUS_NAMESPACE` (producer only).
    - `AzureWebJobsStorage__accountName`, `__credential=managedidentity`, `__clientId`.
    - `APPLICATIONINSIGHTS_CONNECTION_STRING` plus `APPLICATIONINSIGHTS_AUTHENTICATION_STRING` (`ClientId=…;Authorization=AAD`).
  - `virtualNetworkSubnetId` set to that app's subnet.
- **ocr-job**:
  - Container Apps env: workload-profiles environment, Consumption profile only. `vnetConfiguration.infrastructureSubnetId` points at the env subnet. It has to be set at creation, because an environment can't be moved into a VNet later.
  - The job mirrors the `az containerapp job create` in `ocr_container/README.md`: event trigger, replica timeout 3600, retry limit 0, parallelism 1, 0–10 executions, polling 30 s, 2 CPU / 4 Gi.
  - Env vars: `AZURE_CLIENT_ID`, `STORAGE_ACCOUNT_URL`, `SERVICEBUS_NAMESPACE`.
  - Scale rule: `azure-servicebus` with `queueName=pdf_queue`, `namespace`, `messageCount=1` and `identity`.
  - No `registries` block, because the public ghcr image needs no pull credentials.
  - Image comes from the `ocrImage` param (see "code vs infra" below).
- **network** (both envs; separate address spaces so they could be peered later):
  - VNet `10.20.0.0/22` (staging), `10.24.0.0/22` (prod).
  - `snet-func-producer` /26 and `snet-func-consumer` /26, both delegated to `Microsoft.App/environments`, which Flex needs.
  - `snet-cae` /27, delegated to `Microsoft.App/environments`.
  - `snet-pe` /28.
  - One **blob private endpoint** on the data account, a `privatelink.blob.core.windows.net` zone linked to the VNet, and a DNS zone group on the endpoint.
- **roles** (scoped as narrowly as possible):
  - producer-id: Storage Blob Data Contributor (data account); Service Bus Data Sender (queue).
  - consumer-id: Storage Blob Data Reader (`text-storage` container).
  - ocr-id: Storage Blob Data Contributor (data account); Service Bus Data Receiver plus **Data Owner** on the queue (KEDA scaler).
  - The producer and consumer identities also get Storage Blob Data Owner on the host account. All three get Monitoring Metrics Publisher on App Insights.

### Code vs infra
- Re-running Bicep leaves Flex code alone, because the package lives in the deployment container.
- For the job image, `infra.yml` reads the current image with `az containerapp job show --query properties.template.containers[0].image` and passes it as `ocrImage`. On the first deploy, before any image has been pushed, it falls back to `mcr.microsoft.com/k8se/quickstart-jobs:latest`.

### Deploy command
`az stack group create --deny-settings-mode none --action-on-unmanage deleteResources` (a deployment stack), so resources removed from Bicep get deleted in Azure. Every PR also gets a what-if preview.

---

## Step 2: Bootstrap (`infra/bootstrap.sh`, run once by hand)

1. Register the resource providers the deployment needs (the subscription starts with none registered): `Microsoft.Storage`, `Microsoft.ServiceBus`, `Microsoft.ManagedIdentity`, `Microsoft.Web`, `Microsoft.App`, `Microsoft.OperationalInsights`, `Microsoft.Insights`, `Microsoft.Network`, with `az provider register -n <ns> --wait`.
2. Create `rg-textextract-staging` and `rg-textextract-prod` in **northeurope**.
3. Per env, create an Entra app with a federated credential for `repo:spyrosblock/text-extraction:environment:<env>`. Also add one for `pull_request` on staging, so PRs get what-if.
4. Per env, grant **Contributor** and **Role Based Access Control Administrator** on the RG. The second role gets a condition that allows assigning only the data-plane roles listed above.
5. Create the GitHub environments `staging` and `prod`. `prod` gets required reviewers. Set the environment variables `AZURE_CLIENT_ID`, `AZURE_TENANT_ID`, `AZURE_SUBSCRIPTION_ID` and `AZURE_RG`.

---

## Step 3: GitHub Actions (`.github/workflows/`)

| Workflow | Trigger | Steps |
|---|---|---|
| `ci.yml` | PR | Go matrix over `producer`, `consumer` and `ocr_container`: `go vet`, `go test ./...` (the ocr job installs `tesseract-ocr` with `-eng` and `-ell`). Then `az bicep lint`/build and what-if against staging, posted to the PR |
| `infra.yml` | push to `main`: `infra/**` | What-if, deploy the stack to staging, approval, deploy to prod |
| `functions.yml` | push to `main`: `producer/**`, `consumer/**` (matrix per app, with path filtering) | Build `GOOS=linux GOARCH=amd64` and package (per Step 0), upload the artifact, deploy to staging with `Azure/functions-action@v1` (`sku: flexconsumption`), smoke test, approval, deploy the **same artifact** to prod |
| `ocr.yml` | push to `main`: `ocr_container/**` | `docker/build-push-action` to `ghcr.io/<owner>/ocr-container:${{ github.sha }}` (`GITHUB_TOKEN` with `packages: write`, no extra secret), then `az containerapp job update --image` on staging, approval, then the **same tag** on prod |

**Smoke test** (`infra/smoke.sh`, used by `functions.yml` and run by hand):
1. POST `test1.pdf` to `https://<producer>/api/extract`.
2. Poll `POST https://<consumer>/api/text` with `{"id": …}` until it returns `200`, with a timeout.
3. Pass a scanned PDF to exercise the OCR path.

---

## Step 4: Docs
- Replace the "Deploy" section in `producer/README.md`, `consumer/README.md` and `ocr_container/README.md` with a pointer to the workflows and `infra/`. Keep the role-assignment notes, which now match `roles.bicep`.
- The `az storage account management-policy create` snippet in the consumer README becomes "applied by Bicep".
- Add a short "Deployment" section to the root `README.md`, covering bootstrap, workflows and environments. Do not touch `SPEC.md`.

---

## Cost (rough, list prices; confirm in the Azure pricing calculator)

| | Staging | Prod |
|---|---|---|
| Flex Consumption ×2 | usage (monthly free grant) | usage |
| Container Apps job | usage (free grant) | usage |
| Service Bus Basic | ~$0 (per-op) | ~$0 (per-op) |
| ghcr.io (public package) | free | free |
| Storage ×2 | cents to a few $ | cents to a few $ |
| Log Analytics / App Insights (listed under **Azure Monitor** in the calculator) | first 5 GB/month free per billing account, then ~$2.30/GB | same |
| Blob private endpoint plus DNS zone | **~$8** (+ $0.01/GB) | **~$8** (+ $0.01/GB) |
| VNet, subnets, VNet integration | free | free |

---

## Verification
1. `az bicep build infra/main.bicep` and lint are clean. What-if on the empty staging RG lists the expected resources.
2. Deploy staging.
   - No keys or connection strings appear in app settings.
   - `allowSharedKeyAccess=false` on both storage accounts.
   - `disableLocalAuth=true` on Service Bus.
3. `infra/smoke.sh` against staging:
   - The text-layer PDF returns `completed`, then its text.
   - A scanned PDF returns `queued`, a new entry shows up in `az containerapp job execution list`, then the text arrives, and `pdf-storage` ends up empty.
4. Send a malformed message to `pdf_queue`. It should land in the dead-letter queue.
5. Deploy prod.
   - The same smoke test passes, which proves Flex and the job reach blob through the private endpoint.
   - `curl https://<dataaccount>.blob.core.windows.net/text-storage?restype=container` from outside the VNet is refused.
   - `nslookup` of the blob host from inside resolves to a `10.20.x.x` address.
6. App Insights shows traces from the producer, the consumer and the OCR job.
