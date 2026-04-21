# IPv6 Support — Sub-Project Roadmap

**Tracking issue**: aws/copilot-cli#5339
**Umbrella branch**: `feature/ipv6-support`
**Date**: 2026-04-20

Full IPv6 dual-stack support in copilot-cli is decomposed into five sub-projects. Each has its own brainstorm → spec → plan → implement → merge cycle. Sub-projects branch from `feature/ipv6-support`, merge back into it, and the umbrella merges to `mainline` once all five land.

## Sub-project sequencing

```
#1  ─── #2 (requires #1) ─── #4 (requires #1 + #2) ─── #5 (requires #1-#4)
   └── #3 (requires #1, parallel to #2)
```

## Sub-projects

### #1 Env-managed dual-stack VPC (FOUNDATION) — **DONE**

**Branch**: `feature/ipv6-env-vpc` (merged into `feature/ipv6-support` as `a8a9ee7b`)
**Spec**: `docs/superpowers/specs/2026-04-20-env-managed-dualstack-vpc-design.md`
**Plan**: `docs/superpowers/plans/2026-04-20-env-managed-dualstack-vpc.md`

Adds opt-in `network.vpc.ipv6.enabled` on the env manifest. When on, the env CloudFormation stack provisions an Amazon-provided `/56` IPv6 CIDR, `/64` blocks on every subnet, an `EgressOnlyInternetGateway`, public and private `::/0` routes, and a widened `CreatePrivateRouteTables` gate so IPv6-with-no-NAT still has a route table.

**Stable interfaces shipped by #1** (consume in subsequent sub-projects):

- **Manifest field**: `network.vpc.ipv6.enabled` (`*ipv6VPCConfig` struct on `environmentVPCConfig`; accessor `IPv6Enabled()` at `internal/pkg/manifest/env.go`).
- **Template data**: `template.VPCConfig.IPv6Enabled bool` (`internal/pkg/template/env.go`).
- **Template helper**: `AddFunc(a, b int) int` in `internal/pkg/template/template_functions.go`, registered as `"add"` in the env template FuncMap.
- **Env template partials now receive ROOT context** (`.`); Managed fields are accessed as `.VPCConfig.Managed.*`. Both `vpc-resources.yml` and `nat-gateways.yml` follow this convention.
- **CFN stack outputs** (emitted only when IPv6 is on):
  - `IPv6Enabled` (string `"true"`)
  - `VpcIpv6CidrBlock`
  - `PublicSubnetsIpv6CidrBlocks` (CSV)
  - `PrivateSubnetsIpv6CidrBlocks` (CSV)
- **CFN condition**: `CreatePrivateRouteTables` (emitted only when IPv6 is on).
- **Go types**: `envStackOutputsGetter` interface in `internal/pkg/cli/interfaces.go`; `deployEnvOpts.newEnvDescriber` factory; `deployEnvOpts.mft` field.
- **Error sentinels**: `errIPv6WithImportedVPC` (`manifest` package); `errIPv6ToggleOnExistingEnv` (`cli` package).
- **Env stack golden fixture**: `internal/pkg/deploy/cloudformation/stack/testdata/environments/template-with-ipv6-enabled.yml`.

### #2 Workload-side IPv6 egress — SPEC APPROVED

**Depends on**: #1.
**Branch**: `feature/ipv6-task-egress` (off `feature/ipv6-support`)
**Spec**: `docs/superpowers/specs/2026-04-21-workload-ipv6-egress-design.md`

**Goal**: Fargate tasks in a dual-stack env get IPv6 addresses and can reach the IPv6 internet via the EgressOnlyIGW.

**Anticipated scope** (confirm during brainstorming):

- New optional field on service manifests (e.g. `network.assign_ipv6_address: true`) or automatic derivation from env's `IPv6Enabled` output.
- CFN service template: set `AwsvpcConfiguration.AssignIpv6Address: ENABLED` on the ECS task.
- Security group rules: add `::/0` IPv6 egress mirror of existing `0.0.0.0/0` rules; intra-env self-references already cover v6.
- Service stack reads env outputs (`IPv6Enabled`, `VpcIpv6CidrBlock`, etc.) via `Fn::ImportValue`.
- Fargate platform version check (1.4.0+).
- Tests: service manifest unit, service CFN render snapshots, E2E verifying a task pulls from ECR over v6 (dualstack endpoint).

**Branch name suggestion**: `feature/ipv6-task-egress`.

### #3 Dual-stack LB ingress — NOT STARTED

**Depends on**: #1. Can land in parallel with #2.

**Goal**: ALB (and optionally NLB) accept v6 clients. Route 53 gets AAAA records.

**Anticipated scope**:

- New optional field on public ALB config (e.g. `http.public.ip_address_type: dualstack` or auto-derived from env IPv6 flag).
- Set `IpAddressType: dualstack` on `AWS::ElasticLoadBalancingV2::LoadBalancer` in env CFN.
- Target groups over v6 for `target-type: ip` (already the default for Fargate).
- Route 53 AAAA records alongside existing A records when public DNS is managed.
- Tests: env render golden, E2E curl over v6.

**Branch name suggestion**: `feature/ipv6-lb-ingress`.

### #4 Imported-VPC IPv6 — NOT STARTED

**Depends on**: #1 + #2 merged.

**Goal**: Remove the `errIPv6WithImportedVPC` firm error; let users opt their imported VPC into IPv6.

**Anticipated scope**:

- Collect IPv6 CIDR info for imported VPCs (new manifest fields: `public_subnet_ipv6_cidrs`, `private_subnet_ipv6_cidrs`, or inferred via EC2 describe at validate time).
- Pre-deploy validation: imported VPC has `/56` association, subnets have `/64` blocks, account has Egress-Only IGW (or we create one).
- Thread through service and LB stacks (they already support IPv6 in #2 and #3).
- Tests: imported-VPC env golden, validation unit tests, E2E using a pre-created VPC.

**Branch name suggestion**: `feature/ipv6-imported-vpc`.

### #5 Upgrade/downgrade + v6-only + Service Connect + NAT removal — NOT STARTED

**Depends on**: #1–#4 merged. Most complex sub-project.

**Goal**: Enable the full cost-saving path. Remove `errIPv6ToggleOnExistingEnv`; allow toggling IPv6 on/off on an existing env with safe CFN upgrade semantics; remove NAT; add DNS64/NAT64; Service Connect over v6.

**Anticipated scope**:

- Upgrade path: add IPv6 to an existing env WITHOUT recreating subnets. Uses `AWS::EC2::VPCCidrBlock` no-interruption update + `AssignIpv6AddressOnCreation` no-interruption update.
- Downgrade path: safely remove IPv6 routes, associations, CIDR block.
- `v6_only` mode: IPv6-only subnets (no IPv4 CIDR), DNS64 on route tables, NAT64 via dualstack NAT gateway.
- Service Connect: confirm Cloud Map AAAA records are provisioned; Envoy proxy verifies v6 path.
- NAT removal: optional manifest flag to suppress NAT. Real cost savings materialize here.
- Extensive E2E: upgrade an existing env, roll tasks, verify, downgrade.

**Branch name suggestion**: `feature/ipv6-upgrade-v6-only`.

## Starting a new sub-project

When ready to start a sub-project, open a fresh Claude Code session and invoke:

```
/brainstorming Start sub-project #N from the IPv6 roadmap at
docs/superpowers/specs/2026-04-20-ipv6-support-roadmap.md. Branch from
feature/ipv6-support. Read the completed sub-project #1 spec and the
merged code for the Stable Interfaces listed in the roadmap.
```

The brainstorming skill will then walk through clarifying questions specific to that sub-project's scope, produce a dedicated spec in `docs/superpowers/specs/`, and hand off to `writing-plans` → `subagent-driven-development`.

## Status

| # | Sub-project | Status | Spec | Plan | Merged |
|---|---|---|---|---|---|
| 1 | Env dual-stack VPC | ✅ DONE | ✅ | ✅ | `a8a9ee7b` |
| 2 | Workload IPv6 egress | 📝 SPEC APPROVED | ✅ | — | — |
| 3 | Dual-stack LB ingress | ⏳ NOT STARTED | — | — | — |
| 4 | Imported-VPC IPv6 | ⏳ NOT STARTED | — | — | — |
| 5 | Upgrade/v6-only/NAT-removal | ⏳ NOT STARTED | — | — | — |
