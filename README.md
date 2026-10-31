# text-extraction

Extracts the text of PDFs: [`producer`](producer) takes a PDF over HTTP, [`ocr_container`](ocr_container)
OCRs the pages without a text layer, and [`consumer`](consumer) returns the text. See [SPEC.md](SPEC.md).

Customers use the website in [`web/`](web) (a Static Web App) and its back end,
[`account`](account): they sign in with a magic link sent by email, create and revoke their API
keys, and buy credit packs on a Stripe Checkout page. Payments go through **Stripe Managed
Payments**: Stripe, through Link, is the merchant of record (VAT, receipts, refunds, buyers' payment
support, disputes); a Stripe webhook credits the pack to the key, and takes the credits back after
a refund or a lost dispute. Staging runs in Stripe test mode. Each email address's first key gets
100 free credits, once. An account can't buy past 20,000 credits, summed over its keys
(`max_credits_per_account`); a payment that would go over anyway (several opened at once) is
refunded automatically. Keys, balances, accounts and sessions are JSON blobs in the `billing`
container, through the shared Go module [`billing`](billing).

## Deployment

Two environments, `staging` and `prod`, each in its own resource group (`rg-textextract-<env>`,
northeurope). The infrastructure is Terraform in [`infra/`](infra) (azurerm, plus
azapi for the Function apps, since azurerm's Flex Consumption resource doesn't take the Go runtime).
Its state is in the storage account the bootstrap creates in `rg-textextract-tfstate`, one
container per environment. The apps use managed identities only. In prod the data storage account
(`pdf-storage`, `text-storage`, `billing`) is reachable only through a private endpoint in the environment's
VNet; staging has no VNet and keeps the account public (still identity-only).

Callers need an API key with credits: from the website's dashboard, or issued by an admin with
[`infra/keys.sh`](infra/keys.sh) (see [producer](producer/README.md#api-keys-and-credits)), which
can also set any key's balance.

The website (`stapp-textextract-<env>`, Free tier) is in eastus2: Static Web Apps isn't offered
in northeurope, and the region only holds metadata. In prod it is served on `www.<domain>`
(`web_domain`). The magic links are sent by Azure Communication Services Email: in prod from
`DoNotReply@<domain>` (`email_domain`), in staging from the Azure-managed domain
(`DoNotReply@<guid>.azurecomm.net`), which has low send limits and may land in spam.

### Bootstrap (once)

[`infra/bootstrap.sh`](infra/bootstrap.sh), run by hand while logged in to `az` (subscription Owner,
allowed to register Entra apps) and `gh` (repo admin). It applies the Terraform in
[`infra/bootstrap/`](infra/bootstrap) with your credentials: it registers the resource providers,
creates the resource groups, an Entra app per environment with GitHub OIDC federated credentials and
scoped role assignments, the Terraform state account, and the GitHub environments and variables
(`prod` gets a required reviewer). Its own state is local (`infra/bootstrap/terraform.tfstate`,
not committed), so keep that file. It is safe to re-run. The roles the deployment identity may
assign are those in [`infra/modules/roles/roles.json`](infra/modules/roles/roles.json); re-run the
bootstrap after adding one, or a resource provider.

### Payments (Stripe)

Each environment has its own Stripe mode: staging test mode, prod live mode. Per mode, in the
Stripe Dashboard:

1. **Product and prices.** One product "OCR credits" with tax code AIaaS or SaaS, business use, and
   one one-time EUR Price per pack. The Prices' `tax_behavior` (inclusive or exclusive) must match
   `prices_include_vat` in the tfvars, which only changes the site's "incl. VAT" / "VAT added at
   checkout" labels.
2. **Packs.** Put each Price id in `credit_packs` in `infra/<env>.tfvars`, with `price_eur` equal to
   the Price's amount (it's only displayed; Stripe charges the Price). To add a pack, add a Price
   and an entry there and run `infra` and `web`. The account app refuses to start when two packs
   share a Price, or when an API key is set and a pack has no Price.
3. **Restricted key** with Checkout Sessions write and read, and Refunds write (for orders over
   the account's credit cap; nothing else):
   `gh secret set STRIPE_API_KEY --env staging` (`rk_test_…`; `rk_live_…` for prod). Without it
   checkout answers 503 and everything else works.
4. **Webhook endpoint** at the `accountWebhookUrl` output
   (`https://<account host>/api/webhooks/stripe`), on API version **`2026-10-28.endive`** (the one
   `stripe-go` v87 pins; the app refuses events from another release train), for
   `checkout.session.completed`, `checkout.session.async_payment_succeeded`, `charge.refunded` and
   `charge.dispute.closed`. Its signing secret: `gh secret set STRIPE_WEBHOOK_SECRET --env staging`.
   The deploy fails before `terraform apply` without it, because the account app won't start.

The account app's host name doesn't change between deploys, so the webhook endpoint can be
created before the first deploy of this version. To rotate a secret, set it again and run `infra`.

Refunds are made in the Stripe Dashboard (or by Stripe itself); the `charge.refunded` webhook
takes the refunded share of the credits back. A payment for a key that was revoked during checkout
credits nothing and logs an error (`stripe order not credited`) with the order id: move the credits
with `infra/keys.sh add`, or refund the payment.

### Custom domains (prod)

- **Website:** create `www CNAME <webHostName output>` at the registrar, then set `web_domain`
  in `prod.tfvars` and run `infra` (the CNAME must exist first, or validation fails). Redirect the
  apex to `www` at the registrar. Magic links and checkout return URLs then use `siteUrl`;
  the default host keeps working.
- **Email:** set `email_domain` and run `infra`. Add the `emailDnsRecords` output (domain TXT, SPF,
  two DKIM CNAMEs) and `_dmarc.<domain> TXT "v=DMARC1; p=none; rua=mailto:…"` at the registrar, then
  start the verification (portal: Email Communication Service, Provision domains, Verify; or
  `az communication email domain initiate-verification` for `Domain`, `SPF`, `DKIM` and `DKIM2`).
  Once Azure shows it verified, set `email_domain_verified = true` and run `infra`: the domain is
  linked and the app sends from `DoNotReply@<domain>`. Setting it back to `false` returns to the
  Azure-managed domain.
- `contact_email` (tfvars) is the address in the site's footer and legal pages.

### Workflows

| Workflow | Trigger | What it does |
| --- | --- | --- |
| `ci` | pull request | `go vet` and `go test` for each component, `terraform fmt`/`validate`, and a `terraform plan` against staging posted to the PR |
| `staging` | manual, target `all`, `infra`, `functions`, `ocr`, `functions+ocr` or `web` | Builds what the target needs (OCR image pushed to `ghcr.io/spyrosblock/ocr-container:<sha>`), runs `terraform plan`/`apply`, deploys the OCR job image, the Function apps (producer, consumer, account) and the website (with `config.js` and its headers from [`.github/scripts/build-web.sh`](.github/scripts/build-web.sh)), then runs [`infra/smoke.sh`](infra/smoke.sh) |
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

The smoke test doesn't sign in (it would need an inbox) or pay. After a deploy that changes the
account app or the website, check by hand in staging: sign in from the email, open the link a
second time (expired or used), create a key, buy a pack with the test card `4242 4242 4242 4242`
(the credits appear within a minute), resend the event from the Stripe Dashboard (the balance
doesn't change), refund half then the rest (half the credits are taken back each time), and run the
home page's curl commands with the key.
