# text-extraction

Extracts the text of PDFs: [`producer`](producer) takes a PDF over HTTP, [`ocr_container`](ocr_container)
OCRs the pages without a text layer, and [`consumer`](consumer) returns the text. See [SPEC.md](SPEC.md).

## Deployment

Two environments, `staging` and `prod`, each in its own resource group (`rg-textextract-<env>`,
northeurope). The infrastructure is Bicep in [`infra/`](infra), deployed as a deployment stack, so
resources removed from Bicep are deleted in Azure. The apps use managed identities only; the data
storage account (`pdf-storage`, `text-storage`) is reachable only through a private endpoint in the
environment's VNet.

The same infrastructure is also written in Terraform, in [`infra/terraform`](infra/terraform) (azurerm,
plus azapi for the Function apps, since azurerm's Flex Consumption resource doesn't take the Go
runtime). It deploys to two more environments, `staging-tf` and `prod-tf` (`rg-textextract-<env>-tf`),
so both can run side by side. Its state is in the storage account `bootstrap.sh` creates in
`rg-textextract-tfstate`, one container per environment.

### Bootstrap (once)

[`infra/bootstrap.sh`](infra/bootstrap.sh), run by hand while logged in to `az` (subscription Owner,
allowed to register Entra apps) and `gh` (repo admin). It registers the resource providers, creates
the resource groups, an Entra app per environment with GitHub OIDC federated credentials and
scoped role assignments, the Terraform state account, and the GitHub environments and variables
(`prod` and `prod-tf` get a required reviewer). It is safe to re-run.

### Workflows

| Workflow | Trigger | What it does |
| --- | --- | --- |
| `ci` | pull request | `go vet` and `go test` for each component, Bicep lint/build, and a what-if against staging posted to the PR |
| `staging` | manual, target `all`, `infra`, `functions` or `ocr` | Builds what the target needs (OCR image pushed to `ghcr.io/spyrosblock/ocr-container:<sha>`), deploys the stack, the OCR job image and the Function apps, then runs [`infra/smoke.sh`](infra/smoke.sh) |
| `prod` | manual, same targets, needs approval | Same deploy for prod. The OCR image is the tag `staging` pushed for the commit, so deploy a commit to staging first |
| `destroy` | manual, environment `staging` or `prod`, type its name to confirm (prod needs approval) | Deletes the stack and every resource it manages, then anything else left in the resource group. The resource group, Entra app and role assignments stay, so `staging`/`prod` can redeploy with target `all` |
| `staging-tf`, `prod-tf`, `destroy-tf` | as above | The same, with `terraform plan`/`apply`/`destroy` instead of the stack. `ci` also checks `terraform fmt`/`validate` and posts a plan against `staging-tf` to the PR |

To smoke-test an environment by hand:

```sh
az login
infra/smoke.sh rg-textextract-staging
```

To run Terraform by hand (`bootstrap.sh` gives its caller access to the state; `TFSTATE_STORAGE_ACCOUNT`
is a repo variable):

```sh
az login
export ARM_SUBSCRIPTION_ID=$(az account show --query id -o tsv)
cd infra/terraform
terraform init -backend-config=storage_account_name=<state account> -backend-config=container_name=staging-tf
# Without ocr_image the plan resets the job to the quickstart image.
terraform plan -var-file=staging.tfvars -var ocr_image=$(az containerapp job show -g rg-textextract-staging-tf \
  -n caj-ocr-staging --query 'properties.template.containers[0].image' -o tsv)
OUTPUTS=$(terraform output -json) ../smoke.sh rg-textextract-staging-tf
```
