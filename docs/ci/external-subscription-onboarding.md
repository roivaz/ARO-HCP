# External Subscription Onboarding

This document covers the procedure for onboarding a customer subscription that is **not managed by the ARO-HCP pipeline identity** — i.e. a subscription owned by a different team within the same Entra tenant.

For subscriptions where the ARO-HCP team has Owner access, see the standard [E2E Subscription Onboarding](e2e-subscription-onboarding.md) procedure.

## Background: What Gets Provisioned and Why

ARO-HCP E2E tests simulate the full lifecycle of a Hosted Control Plane cluster inside a customer subscription. To do that, several pieces of infrastructure must be in place before tests can run:

1. **CI Bot access (OpenShift Release Bot)** — The Prow CI system runs under a service principal that needs Contributor + limited RBAC Administrator access on the subscription so it can create resource groups, deploy ARM templates, and assign roles to per-cluster identities during test execution.

2. **Shared DEV identities** — In production, certain operations are performed by Azure First Party Application identities. In DEV, these are emulated by shared service principals (`aro-dev-first-party2`, `aro-dev-arm-helper2`, `aro-dev-msi-mock2`) that need custom role assignments on every customer subscription where tests run. The custom role definitions live in a central "home" subscription, but the *assignments* must exist in the target subscription.

3. **Pooled MSI mock principals** — Presubmit E2E jobs run in parallel and each needs its own MSI mock identity to distribute ARM throttling budget across different principals. A pool of service principals is pre-created in Entra, and each needs the same custom roles on the target subscription.

4. **Identity containers (MSI pools)** — Each E2E test slot gets a set of pre-provisioned resource groups containing managed identities. These are deployed via ARM deployment stacks and sized according to the slot catalog. The number of identity containers limits the number of AKS service clusters (which host the RP) that can be provisioned concurrently within the slot.

5. **Cleanup jobs** — Resource groups left behind by failed or timed-out tests are garbage-collected by periodic Prow jobs that delete expired groups.

All of the above is normally deployed automatically by the ARO-HCP pipeline for internally-managed subscriptions. For external subscriptions, the subscription-owning team performs steps 1–4 themselves using the same Bicep modules and tooling.

## How It Differs From Internal Onboarding

In a standard DEV subscription onboarding, the ARO-HCP pipeline identity (`aro-hcp-owners` group) has Owner access and can deploy RBAC, custom role assignments, and identity containers directly.

For an **external** subscription:

- The RBAC pipeline **skips** deployment into it (controlled by `unmanaged: true` in `config/config-dev-ci.yaml`)
- The `apply-identity-pool` command **skips** it by default (controlled by `identity_provisioning: unmanaged` in the slot catalog)
- The subscription-owning team is responsible for running the RBAC setup and identity-pool provisioning themselves, using the ARO-HCP Bicep modules and tooling

## Responsibility Split

| Step | Responsible Team | Description |
| :--- | :--------------- | :---------- |
| Slot catalog entry | ARO-HCP (approves PR) | Add the pool to `test/e2e-config/e2e-slots.yaml` with `identity_provisioning: unmanaged` |
| Boskos sync | ARO-HCP (approves PR) | Run `slot-manager sync-boskos-config` and merge the `openshift/release` PR |
| Config entry | ARO-HCP (approves PR) | Add subscription to `config/config-dev-ci.yaml` under `ci.dev.e2eSubscriptions` with `unmanaged: true` |
| Custom role `assignableScopes` | ARO-HCP (deploy) | Run `make dev-ci-e2e-subscription-rbac-local-run` — the external sub's ID appears in `assignableScopes` of the custom role definitions on the home subscription |
| Vault secret | ARO-HCP (manual) | Add `customer-<shard>-subscription-id` and `customer-<shard>-subscription-name` to the cluster profile secret |
| Release Bot grants | Subscription owner | Grant the CI bot the required roles on the subscription (Step 1 below) |
| Shared-principal RBAC | Subscription owner | Deploy the Bicep module targeting the subscription (Step 2 below) |
| Identity containers | Subscription owner | Run `apply-identity-pool --subscription` (Step 3 below) |
| Cleanup job | Subscription owner | Add a periodic cleanup job in `openshift/release` (Step 4 below) |

## Subscription Owner Steps

### Prerequisites

Register the required Azure resource providers (same as for any new subscription):

```sh
for ns in Microsoft.Compute Microsoft.Network Microsoft.ManagedIdentity \
          Microsoft.Storage Microsoft.KeyVault Microsoft.RedHatOpenShift \
          Microsoft.Quota; do
  az provider register --namespace "$ns" --subscription <subscription-id>
done
```

### Step 1: Grant the OpenShift Release Bot

The CI bot (`OpenShift Release Bot`, appId `38335e22-716a-4a21-bf20-15ab141823f0`, objectId `c209f8df-52ae-48fb-98ea-380f58b04652`) needs the following roles at **subscription scope**:

| Role | Condition |
| :--- | :-------- |
| Contributor | None |
| Role Based Access Control Administrator | ABAC condition preventing assignment of Owner, UAA, and RBAC Administrator roles |
| Azure Kubernetes Service RBAC Cluster Admin | None |

Deploy using the same Bicep module that manages CI bot RBAC for all environments:

```sh
SUBSCRIPTION_ID="<target-subscription-id>"
BOT_PRINCIPAL_ID="c209f8df-52ae-48fb-98ea-380f58b04652"

az deployment sub create \
  --subscription "${SUBSCRIPTION_ID}" \
  --location westus3 \
  --template-file dev-infrastructure/templates/ci-bot-rbac-subscription.bicep \
  --parameters \
    botPrincipalId="${BOT_PRINCIPAL_ID}" \
    isGlobalSubscription=false \
    grantAksRbac=true
```

This deploys Contributor, RBAC Administrator (with ABAC condition), and AKS RBAC Cluster Admin — identical to what the pipeline deploys for internally-managed subscriptions.

### Step 2: Deploy Shared-Principal RBAC Using the ARO-HCP Bicep Module

The same Bicep module used by the internal RBAC pipeline can be deployed directly against the target subscription. It grants the shared DEV identities the custom roles they need.

```sh
SUBSCRIPTION_ID="<your-subscription-id>"
HOME_SUBSCRIPTION_ID="1d3378d3-5a3f-4712-85a1-2485495dfc4b"

az deployment sub create \
  --subscription "${SUBSCRIPTION_ID}" \
  --location westus3 \
  --template-file dev-infrastructure/templates/e2e-subscription-rbac-assignment-subscription.bicep \
  --parameters \
    homeSubscriptionId="${HOME_SUBSCRIPTION_ID}" \
    firstPartyPrincipalId="47f69502-0065-4d9a-b19b-d403e183d2f4" \
    armHelperPrincipalId="ddeffa11-e3d9-487d-8fc9-9a9e26f64975" \
    miMockPrincipalId="d6b62dfa-87f5-49b3-bbcb-4a687c4faa96" \
    msiMockPoolPrincipals='[{"name":"aro-hcp-msi-mock-cs-sp-dev-0","principalId":"db27175c-5bd0-48b4-929a-41de9a53ffbf"},{"name":"aro-hcp-msi-mock-cs-sp-dev-1","principalId":"cd39c606-1f6a-4062-a5b9-497cd04c39fc"},{"name":"aro-hcp-msi-mock-cs-sp-dev-2","principalId":"3871b527-fb1e-4123-b38b-3cb2445a9fc8"},{"name":"aro-hcp-msi-mock-cs-sp-dev-3","principalId":"e92b9f76-b040-4cf6-a4dd-5c8bdc759e69"},{"name":"aro-hcp-msi-mock-cs-sp-dev-4","principalId":"3d8c36e1-ae7a-42ef-b7d1-ea4667708d30"},{"name":"aro-hcp-msi-mock-cs-sp-dev-5","principalId":"3015be55-a361-4a86-8439-0abc7860a4ef"},{"name":"aro-hcp-msi-mock-cs-sp-dev-6","principalId":"f8be0ede-39df-41cf-aed7-7f3626f23a5a"},{"name":"aro-hcp-msi-mock-cs-sp-dev-7","principalId":"07aa0e83-353e-444c-9f1d-d023ac3c5396"},{"name":"aro-hcp-msi-mock-cs-sp-dev-8","principalId":"0d25d885-2cd8-4972-b610-4d85881c1ec4"},{"name":"aro-hcp-msi-mock-cs-sp-dev-9","principalId":"5157a63c-87dc-4680-8f46-954b46399bdc"},{"name":"aro-hcp-msi-mock-cs-sp-dev-10","principalId":"5a34d2fd-f7db-460a-a272-334006a8a3b8"},{"name":"aro-hcp-msi-mock-cs-sp-dev-11","principalId":"fd35ac5f-6493-40cb-9131-d669a70114c3"},{"name":"aro-hcp-msi-mock-cs-sp-dev-12","principalId":"a76148d4-cb11-4b93-9363-54ba8239b0b1"},{"name":"aro-hcp-msi-mock-cs-sp-dev-13","principalId":"b117cf0f-0276-4486-9691-4f00f5a90e1b"},{"name":"aro-hcp-msi-mock-cs-sp-dev-14","principalId":"8d1ff6d0-aadd-4331-9171-9443ac5f4337"},{"name":"aro-hcp-msi-mock-cs-sp-dev-15","principalId":"3e3ad333-db6b-46df-b57d-6f0989dda8b2"},{"name":"aro-hcp-msi-mock-cs-sp-dev-16","principalId":"b26c2343-0863-4a55-bb7e-dfb2544999c1"},{"name":"aro-hcp-msi-mock-cs-sp-dev-17","principalId":"39eefc4f-5e24-491d-9f09-bcc0ad573bcf"},{"name":"aro-hcp-msi-mock-cs-sp-dev-18","principalId":"ccbf438f-08ec-4e2b-ae4e-219ce7dc3762"},{"name":"aro-hcp-msi-mock-cs-sp-dev-19","principalId":"12eafc4e-f869-4b62-8198-816b7d6d0876"}]'
```

> **Note:** The custom role definitions (`dev-first-party-mock`, `dev-msi-mock`, `Azure Red Hat OpenShift KMS Plugin - Dev`) are defined in the home subscription and their `assignableScopes` must include the target subscription ID. This is handled by the ARO-HCP team when they add the subscription to `config-dev-ci.yaml` and re-deploy the `dev-ci` RBAC topology.

### Step 3: Provision Identity Containers

Use the `apply-identity-pool` command with the `--subscription` flag to target only the relevant pool:

```sh
go run ./test/cmd/aro-hcp-tests slot-manager apply-identity-pool \
  --environment dev \
  --subscription "Hypershift Managed Azure"
```

This deploys the MSI container deployment stacks into the target subscription based on the slot catalog configuration.

### Step 4: Add a Cleanup Job

Open a PR to `openshift/release` adding a periodic cleanup job for the subscription:

```yaml
- as: delete-expired-dev-ci-hypershift-resource-groups
  cron: 35 * * * *
  steps:
    env:
      CLEANUP_MODE: no-rp
      CUSTOMER_SUBSCRIPTION: Hypershift Managed Azure
    test:
    - ref: aro-hcp-deprovision-expired-resource-groups
```

## Maintenance

If the Bicep module `e2e-subscription-rbac-assignment-subscription.bicep` is updated (new principals, new roles), the subscription-owning team must re-run the deployment in Step 2.

Changes to the pooled MSI mock principal list (additions or removals in `config/config-dev-ci.yaml` → `msiMockPool.principals`) require re-running Step 2 with the updated `msiMockPoolPrincipals` parameter.

## Reference: Shared Principal IDs

| Identity | Principal ID | Purpose |
| :------- | :----------- | :------ |
| `aro-dev-first-party2` | `47f69502-0065-4d9a-b19b-d403e183d2f4` | First-party application mock |
| `aro-dev-arm-helper2` | `ddeffa11-e3d9-487d-8fc9-9a9e26f64975` | ARM helper (Contributor + RBAC Admin) |
| `aro-dev-msi-mock2` | `d6b62dfa-87f5-49b3-bbcb-4a687c4faa96` | MSI mock principal |
| OpenShift Release Bot | `c209f8df-52ae-48fb-98ea-380f58b04652` | CI bot (app: `38335e22-716a-4a21-bf20-15ab141823f0`) |

## See Also

- [E2E Subscription Onboarding](e2e-subscription-onboarding.md) — internal subscription procedure
- [Dev-CI Topology](dev-ci-topology.md)
- [CI Identity Leasing](identity-leasing.md)
