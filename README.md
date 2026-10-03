# text-extraction

Extracts the text of PDFs: [`producer`](producer) takes a PDF over HTTP, [`ocr_container`](ocr_container)
OCRs the pages without a text layer, and [`consumer`](consumer) returns the text. See [SPEC.md](SPEC.md).

## Deployment

Two environments, `staging` and `prod`, each in its own resource group (`rg-textextract-<env>`,
northeurope). The infrastructure is Bicep in [`infra/`](infra), deployed as a deployment stack, so
resources removed from Bicep are deleted in Azure. The apps use managed identities only; the data
storage account (`pdf-storage`, `text-storage`) is reachable only through a private endpoint in the
environment's VNet.

### Bootstrap (once)

[`infra/bootstrap.sh`](infra/bootstrap.sh), run by hand while logged in to `az` (subscription Owner,
allowed to register Entra apps) and `gh` (repo admin). It registers the resource providers, creates
the resource groups, an Entra app per environment with GitHub OIDC federated credentials and
scoped role assignments, and the GitHub environments and variables (`prod` gets a required
reviewer). It is safe to re-run.

### Workflows

| Workflow | Trigger | What it does |
| --- | --- | --- |
| `ci` | pull request | `go vet` and `go test` for each component, Bicep lint/build, and a what-if against staging posted to the PR |
| `staging` | manual, target `all`, `infra`, `functions` or `ocr` | Builds what the target needs (OCR image pushed to `ghcr.io/spyrosblock/ocr-container:<sha>`), deploys the stack, the OCR job image and the Function apps, then runs [`infra/smoke.sh`](infra/smoke.sh) |
| `prod` | manual, same targets, needs approval | Same deploy for prod. The OCR image is the tag `staging` pushed for the commit, so deploy a commit to staging first |

To smoke-test an environment by hand:

```sh
az login
infra/smoke.sh rg-textextract-staging
```
