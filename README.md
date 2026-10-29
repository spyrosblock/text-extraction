# text-extraction

Extracts the text of PDFs: [`producer`](producer) takes a PDF over HTTP, [`ocr_container`](ocr_container)
OCRs the pages without a text layer, and [`consumer`](consumer) returns the text. See [SPEC.md](SPEC.md).

## Deployment

Two environments, `staging` and `prod`, each in its own resource group (`rg-textextract-<env>`,
northeurope). The infrastructure is Terraform in [`infra/`](infra) (azurerm, plus
azapi for the Function apps, since azurerm's Flex Consumption resource doesn't take the Go runtime).
Its state is in the storage account the bootstrap creates in `rg-textextract-tfstate`, one
container per environment. The apps use managed identities only; the data storage account
(`pdf-storage`, `text-storage`) is reachable only through a private endpoint in the environment's
VNet.

### Bootstrap (once)

[`infra/bootstrap.sh`](infra/bootstrap.sh), run by hand while logged in to `az` (subscription Owner,
allowed to register Entra apps) and `gh` (repo admin). It applies the Terraform in
[`infra/bootstrap/`](infra/bootstrap) with your credentials: it registers the resource providers,
creates the resource groups, an Entra app per environment with GitHub OIDC federated credentials and
scoped role assignments, the Terraform state account, and the GitHub environments and variables
(`prod` gets a required reviewer). Its own state is local (`infra/bootstrap/terraform.tfstate`,
not committed), so keep that file. It is safe to re-run. The roles the deployment identity may
assign are those in [`infra/modules/roles/roles.json`](infra/modules/roles/roles.json); re-run the
bootstrap after adding one.

### Workflows

| Workflow | Trigger | What it does |
| --- | --- | --- |
| `ci` | pull request | `go vet` and `go test` for each component, `terraform fmt`/`validate`, and a `terraform plan` against staging posted to the PR |
| `staging` | manual, target `all`, `infra`, `functions` or `ocr` | Builds what the target needs (OCR image pushed to `ghcr.io/spyrosblock/ocr-container:<sha>`), runs `terraform plan`/`apply`, deploys the OCR job image and the Function apps, then runs [`infra/smoke.sh`](infra/smoke.sh) |
| `prod` | manual, same targets, needs approval | Same deploy for prod. The OCR image is the tag `staging` pushed for the commit, so deploy a commit to staging first |
| `destroy` | manual, environment `staging` or `prod`, type its name to confirm (prod needs approval) | `terraform destroy`, then anything else left in the resource group. The resource group, Entra app, role assignments and (empty) state stay, so `staging`/`prod` can redeploy with target `all` |

To run Terraform and the smoke test by hand (the bootstrap gives its caller access to the state;
`TFSTATE_STORAGE_ACCOUNT` is a repo variable):

```sh
az login
export ARM_SUBSCRIPTION_ID=$(az account show --query id -o tsv)
cd infra
terraform init -backend-config=storage_account_name=<state account> -backend-config=container_name=staging
# Without ocr_image the plan resets the job to the quickstart image.
terraform plan -var-file=staging.tfvars -var ocr_image=$(az containerapp job show -g rg-textextract-staging \
  -n caj-ocr-staging --query 'properties.template.containers[0].image' -o tsv)
./smoke.sh rg-textextract-staging
```
