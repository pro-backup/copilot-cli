# Imported-VPC IPv6 Support

**Date**: 2026-04-21
**Tracking issue**: aws/copilot-cli#5339
**Sub-project**: #4 of the [IPv6 roadmap](2026-04-20-ipv6-support-roadmap.md)
**Depends on**: #1 (env-managed dual-stack VPC), #2 (workload IPv6 egress), #3 (dual-stack LB ingress) — all merged on `feature/ipv6-support`
**Branch**: `feature/ipv6-imported-vpc` (off `feature/ipv6-support`)
**Status**: Design approved; ready for implementation plan

## Context

Sub-projects #1–#3 delivered full dual-stack networking on **managed** VPCs:
the env manifest's `network.vpc.ipv6.enabled: true` provisions a `/56`,
`/64` subnet blocks, an EgressOnlyIGW, dualstack ALBs, AAAA records, and
workload task ENIs with IPv6. All of that infrastructure was gated on
`not .VPCConfig.Imported`, and the manifest layer rejected the
imported-VPC + IPv6 combination outright with `errIPv6WithImportedVPC`
(`internal/pkg/manifest/validate_env.go:21`).

This sub-project removes that restriction. Users who bring their own VPC
with IPv6 pre-configured can now opt their copilot environment into
dual-stack networking, and workloads deployed into that environment
inherit the same IPv6 behavior #2 and #3 delivered on managed VPCs.

## Goals

1. Remove `errIPv6WithImportedVPC` from the manifest validator.
   `network.vpc.id: vpc-xxx` + `network.vpc.ipv6.enabled: true` is a
   valid manifest.
2. Add a pre-flight EC2-backed validator in the `cli` package that runs
   during `env init` and `env deploy` and hard-fails if the imported VPC
   or any imported subnet lacks an associated IPv6 CIDR block. The user
   sees a crisp, actionable error before any CloudFormation call is
   made.
3. End-to-end: a Linux workload deployed into an imported + IPv6 env
   gets a dualstack task ENI, and the shared copilot ALB serves
   dualstack with AAAA records — the same behavior #2 and #3 already
   deliver on managed VPCs.

## Non-Goals

- **No new manifest fields.** IPv6 subnet CIDRs are discovered via
  EC2 at validate time; the env template references subnets by ID, and
  Fargate/ALB derive v6 addresses automatically from subnet state.
- **Copilot does not create `EgressOnlyInternetGateway`, route tables,
  or IPv6 default routes on imported VPCs.** The user owns egress
  routing, consistent with how copilot already treats imported VPC IGWs
  and NAT gateways.
- **No routing audit in the validator.** We do not verify that `::/0`
  IPv6 routes exist or that an EgressOnlyIGW is attached. Valid
  topologies include transit gateways, peered egress VPCs, and
  IPv6-only-ingress setups — enumerating them all is infeasible. If
  routing is broken, the user's tasks fail fast post-deploy; that is
  the user's domain.
- **No workload-deploy re-verification.** Env-stack state is the source
  of truth once deployed. Copilot already does not re-verify NAT, IGW,
  or subnet reachability at `svc deploy`; we match that posture.
- **`errIPv6ToggleOnExistingEnv` stays in place.** Toggling IPv6 on an
  existing imported env is still blocked. #5 handles upgrade/downgrade.
- **No template changes.** All IPv6 CFN resources in `vpc-resources.yml`
  and `nat-gateways.yml` are already gated on `not .VPCConfig.Imported`
  (from #1), so they correctly do not render for imported VPCs.
  `VPCConfig.IPv6Enabled` guards on dualstack ALB attributes and SG
  ingress (from #3) already fire identically for managed and imported.

## Design Decisions

Resolved during brainstorming and pin the rest of the design:

1. **Copilot consumes, does not mutate, imported VPC networking.** The
   user pre-provisions the `/56`, the `/64` subnet blocks, the
   EgressOnlyIGW (or equivalent), and the IPv6 default routes. Copilot
   sets only its own resources (ALB attributes, SG rules, task ENI
   config, Route 53 records).
2. **Validation is EC2-backed and hard-fails pre-deploy.** Checks
   (a) VPC has at least one IPv6 CIDR association in `associated`
   state; (b) every imported public and private subnet has at least one
   IPv6 CIDR in `associated` state. No routing audit. EC2 calls
   (`DescribeVpcs`, `DescribeSubnets`) are cheap and copilot's existing
   `env init` subnet-selection flow already requires the same
   permissions.
3. **Validator lives in the `cli` package, not `manifest`.** The
   manifest package must remain side-effect-free; EC2 describes belong
   alongside other CLI-layer validation (mirrors how
   `errIPv6ToggleOnExistingEnv` lives in `cli` because it needs stack
   state).
4. **E2E covers real-world integration with one Ginkgo scenario** backed
   by a helper CloudFormation stack that provisions a throw-away
   IPv6-ready VPC for the suite. Consistent with how other copilot E2Es
   that need external prerequisites manage them (`e2e/multi_env_app/`,
   others).
5. **No per-workload-deploy re-verification.** Explicit scope decision,
   matches copilot's existing posture on imported infra.
6. **Shared validator helper, not duplicated logic.** The same function
   is called from `env init` and `env deploy`, and #5's upgrade flow
   will reuse it.

## Architecture

One functional change: a new validator that gates the removal of
`errIPv6WithImportedVPC`. The template and stack-construction layers
are unchanged — #1/#2/#3's flag-driven plumbing already handles
imported + IPv6 correctly once the manifest-layer guard is lifted.

### Manifest layer

Delete these lines from `internal/pkg/manifest/validate_env.go`:

```go
// errIPv6WithImportedVPC is returned when a manifest enables IPv6 on an
// imported VPC. Imported-VPC IPv6 support is tracked as a follow-up
// sub-project (#4).
errIPv6WithImportedVPC = errors.New(
    `IPv6 cannot be enabled on an environment that imports a VPC. ` +
        `Remove "network.vpc.id" or "network.vpc.ipv6".`,
)
```

And in `environmentVPCConfig.validate()`:

```go
if cfg.imported() && (&cfg).IPv6Enabled() {
    return errIPv6WithImportedVPC
}
```

No other manifest changes. The `environmentVPCConfig.IPv6Enabled()`
accessor stays as-is — it is still the source of truth consumed by
#1/#2/#3 templates.

### CLI layer: new validator

New file `internal/pkg/cli/ipv6_imported_vpc_validator.go`:

```go
// validateImportedVPCIPv6Readiness returns nil if the imported VPC and every
// listed subnet has at least one associated IPv6 CIDR block. Called by
// env init and env deploy when the manifest sets both network.vpc.id and
// network.vpc.ipv6.enabled: true.
func validateImportedVPCIPv6Readiness(ec2Client vpcIPv6Describer, vpcID string, subnetIDs []string) error {
    // 1. DescribeVpcs for vpcID; require at least one Ipv6CidrBlockAssociationSet
    //    entry with state == "associated".
    // 2. DescribeSubnets for subnetIDs; for each subnet, require at least one
    //    Ipv6CidrBlockAssociationSet entry with state == "associated".
    // 3. Aggregate missing subnets into a single error message naming each.
}
```

Two new error sentinels, both in the `cli` package:

```go
errImportedVPCMissingIPv6 = errors.New(
    `the imported VPC does not have an IPv6 CIDR block associated. ` +
        `Associate an Amazon-provided /56 (or a BYOIP /56) on the VPC ` +
        `before enabling "network.vpc.ipv6.enabled".`,
)

errImportedSubnetsMissingIPv6 = errors.New(
    `one or more imported subnets do not have an IPv6 CIDR block. ` +
        `Each subnet must have a /64 block associated before enabling ` +
        `"network.vpc.ipv6.enabled".`,
)
```

The validator wraps these with `fmt.Errorf("%w: %s", ...)` to include
the offending VPC ID or subnet ID list in the user-facing message.

### EC2 client extension

`internal/pkg/aws/ec2/ec2.go` grows two thin additions:

- A new `DescribeVPC(vpcID string) (*VPC, error)` helper (or extension
  to an existing describe call) returning a struct that exposes
  `HasIPv6() bool`. Implementation reads
  `DescribeVpcsOutput.Vpcs[0].Ipv6CidrBlockAssociationSet` and checks
  for any entry with state `associated`.
- Extend the existing `Subnet` struct with an
  `IPv6CIDRBlocks []string` field populated from
  `DescribeSubnetsOutput.Subnets[*].Ipv6CidrBlockAssociationSet`
  entries in state `associated`. No new network call; the existing
  `ec2.subnets()` helper already invokes `DescribeSubnets`.

### Wiring into env init and env deploy

`env init` already holds an `ec2Client` field
(`internal/pkg/cli/env_init.go:169`), lazily initialized via
`ec2.New(o.sess)` before existing subnet-selection checks. `env deploy`
does **not** yet hold one; this spec plumbs a matching `ec2Client
ec2Client` field onto `deployEnvOpts`, initialized the same lazy way
the existing clients on that struct are
(`deployEnvOpts.newEnvDescriber`, etc.). Reusing the existing
`ec2Client` interface in `internal/pkg/cli/interfaces.go` keeps the
mock surface unchanged for other tests.

Two changes, one per command:

```go
// pseudocode in env_init.go Validate() / end of Ask()
if manifest.Network.VPC.imported() && manifest.Network.VPC.IPv6Enabled() {
    subnetIDs := collectImportedSubnetIDs(manifest)
    if err := validateImportedVPCIPv6Readiness(o.ec2Client, *manifest.Network.VPC.ID, subnetIDs); err != nil {
        return err
    }
}
```

Identical block in `env_deploy.go`. ~15 LOC per call site.

### Template and stack layers

**Zero changes.** This is the key property of the design and is
worth stating explicitly:

- `internal/pkg/template/templates/environment/cf.yml` — dualstack ALB
  attributes and IPv6 SG ingress resources are already gated on
  `VPCConfig.IPv6Enabled` (from #3). They fire identically for managed
  and imported VPCs.
- `internal/pkg/template/templates/environment/partials/vpc-resources.yml`
  and `nat-gateways.yml` — every IPv6 resource
  (`VPCCidrBlockIPv6`, `EgressOnlyInternetGateway`, v6 default routes,
  subnet `Ipv6CidrBlock` properties) is already wrapped in the
  top-level managed-VPC-only guard established before #1. They do not
  render on the imported path, correct by construction.
- Workload partials
  (`workloads/partials/cf/service-base-properties.yml`,
  `https-listener.yml`, `http-listener.yml`) — `NetworkOpts.IPv6Enabled`
  and `WorkloadOpts.IPv6Enabled` are already set from the same manifest
  accessor regardless of whether the VPC is imported.
- `internal/pkg/deploy/cloudformation/stack/env.go` and the four
  workload stack constructors — no change. The `envIPv6Enabled` field
  added by #2 already populates from
  `conf.EnvManifest.Network.VPC.IPv6Enabled()`.

The new env-stack golden added by this spec
(`template-with-ipv6-and-imported-vpc.yml`) asserts exactly this: the
only IPv6-related diff from the existing imported-VPC goldens is the
ALB `IpAddressType: dualstack` attribute and the four IPv6 SG ingress
resources — no managed-VPC IPv6 infrastructure leaks in.

## Validation

All checks run in the command's `Validate()` phase before any
CloudFormation call.

| Condition | Result |
|---|---|
| Imported VPC + `ipv6.enabled: true`, VPC and subnets all have IPv6 | No error. Proceed. |
| Imported VPC + `ipv6.enabled: true`, VPC missing IPv6 association | Firm error `errImportedVPCMissingIPv6` with the VPC ID |
| Imported VPC + `ipv6.enabled: true`, one or more subnets missing IPv6 | Firm error `errImportedSubnetsMissingIPv6` listing all offending subnet IDs |
| Managed VPC + `ipv6.enabled: true` | Unchanged. No EC2 call. |
| Any config, `ipv6.enabled` false or absent | No-op. |
| Imported VPC + `ipv6.enabled: true`, EC2 API failure | Error propagates; deploy aborts. |

### Out of scope for validation in v1

- Routing audit (EgressOnlyIGW existence, `::/0` route on private
  subnets' route tables).
- Workload-deploy re-verification — the env-stack state is trusted
  once deployed, matching copilot's existing posture on imported
  infrastructure.
- BYOIP IPv6 pool validation — the EC2 state check is family-agnostic
  about where the `/56` comes from.

### User-facing error copy

```
Error: the imported VPC vpc-0abc123def does not have an IPv6 CIDR block
associated. Associate an Amazon-provided /56 (or a BYOIP /56) on the
VPC before enabling "network.vpc.ipv6.enabled".
```

```
Error: the following imported subnets do not have an IPv6 CIDR block:
subnet-0abc123, subnet-0def456. Each subnet must have a /64 block
associated before enabling "network.vpc.ipv6.enabled".
```

## File Layout

| Path | Change | Approx. LOC |
|---|---|---|
| `internal/pkg/manifest/validate_env.go` | Delete `errIPv6WithImportedVPC` and its call site | −10 |
| `internal/pkg/manifest/validate_env_test.go` | Remove two test cases; add positive case for imported + IPv6 | −20 / +10 |
| `internal/pkg/cli/ipv6_imported_vpc_validator.go` | New validator + two error sentinels + small `vpcIPv6Describer` interface | +90 |
| `internal/pkg/cli/ipv6_imported_vpc_validator_test.go` | Table-driven unit tests with mocked EC2 | +150 |
| `internal/pkg/cli/env_init.go` | Validator call in Validate phase | +15 |
| `internal/pkg/cli/env_deploy.go` | Add `ec2Client ec2Client` field with lazy init; validator call in Validate phase | +30 |
| `internal/pkg/cli/env_init_test.go`, `env_deploy_test.go` | Call-site propagation tests | +60 |
| `internal/pkg/aws/ec2/ec2.go` | `DescribeVPC` helper or extension; `Subnet.IPv6CIDRBlocks` field | +30 |
| `internal/pkg/aws/ec2/ec2_test.go` | Unit tests for IPv6 field population and `HasIPv6` helper | +80 |
| `internal/pkg/aws/ec2/mocks/mock_ec2.go` | Regenerated | auto |
| `internal/pkg/deploy/cloudformation/stack/testdata/environments/template-with-ipv6-and-imported-vpc.yml` | New golden fixture | +~450 |
| `internal/pkg/deploy/cloudformation/stack/env_integration_test.go` | New golden case | +30 |
| `e2e/imported_vpc_ipv6/helper_stack.yml` | Prerequisite IPv6 VPC CFN stack (VPC + `/56` + 2 public + 2 private `/64` subnets + EgressOnlyIGW + routes) | +80 |
| `e2e/imported_vpc_ipv6/imported_vpc_ipv6_suite_test.go`, `imported_vpc_ipv6_test.go` | Ginkgo scenario + negative case | +280 |
| `site/content/docs/manifest/environment.en.md` | Remove "managed-VPC only" caveat from IPv6 section; document imported-VPC prerequisites | +20 |
| `CHANGELOG.md` | Entry | +3 |

Total: ~1,300 LOC. ~80% tests, goldens, and helper infra. Fits in a
single PR.

## Testing Strategy

Five layers; additive except for the two deleted manifest-layer test
cases. Every existing non-IPv6 test and every managed-IPv6 test from
#1–#3 stays unchanged.

### 1. Manifest unit tests

In `internal/pkg/manifest/validate_env_test.go`:

- **Remove** the test case at line 1110 asserting
  `errIPv6WithImportedVPC` fires for imported + IPv6.
- **Add** a positive case: imported VPC + `ipv6.enabled: true` parses
  and validates with no error. Keeps the manifest layer from re-adding
  the guard by accident.

### 2. CLI validator unit tests

Table-driven in `internal/pkg/cli/ipv6_imported_vpc_validator_test.go`,
with a mocked EC2 client:

| Case | Expected |
|---|---|
| VPC has IPv6, all subnets have IPv6 | no error |
| VPC missing IPv6 association | `errImportedVPCMissingIPv6`, message contains VPC ID |
| VPC has IPv6, one subnet missing | `errImportedSubnetsMissingIPv6`, message names the subnet |
| VPC has IPv6, multiple subnets missing | single error listing all offending subnets |
| VPC has disassociating (non-`associated` state) IPv6 block | treated as missing; `errImportedVPCMissingIPv6` |
| `DescribeVpcs` API error | error propagates |
| `DescribeSubnets` API error | error propagates |

Plus call-site propagation tests in `env_init_test.go` and
`env_deploy_test.go` that verify the validator is invoked when both
`network.vpc.id` and `network.vpc.ipv6.enabled` are set, and skipped
otherwise.

### 3. Env-stack render goldens

Build tag `integration || localintegration`. Golden-file comparison
in `internal/pkg/deploy/cloudformation/stack/env_integration_test.go`:

- **New**
  `testdata/environments/template-with-ipv6-and-imported-vpc.yml`
  exercising the imported + IPv6 path. Asserts:
  - The two shared ALBs have `IpAddressType: dualstack`.
  - The four standalone IPv6 SG ingress resources (from #3) are
    present.
  - **No** `VPCCidrBlockIPv6`, `EgressOnlyInternetGateway`,
    `DefaultPublicRouteIPv6`, `PrivateRouteIPv6N`, or subnet
    `Ipv6CidrBlock` properties appear. Copilot provisions zero
    managed-VPC IPv6 infrastructure.
- **Byte-identity regression**: every existing env-stack golden
  (managed-on, managed-off, imported-off, imported-with-internal-ALB,
  imported-with-custom-ingress) renders byte-identically. Non-negotiable,
  same gate as #1/#2/#3.

### 4. Local integration (`// +build localintegration`)

- Parse → validate (with mocked EC2) → render an imported + IPv6 env
  manifest; assert the rendered template is valid CFN YAML.
- Parse → validate an imported + IPv6 manifest against an EC2 mock
  that returns a v4-only VPC; assert `errImportedVPCMissingIPv6` fires
  with no CFN render attempted.

### 5. E2E (Ginkgo v2 in `e2e/imported_vpc_ipv6/`, nightly-gated)

**Helper infrastructure.** `BeforeSuite` deploys
`e2e/imported_vpc_ipv6/helper_stack.yml`, a CFN template that
provisions:

- A VPC with an Amazon-provided `/56`.
- Two public subnets with `/64` blocks, associated with an internet
  gateway.
- Two private subnets with `/64` blocks, associated with a route table
  that has `::/0 → EgressOnlyInternetGateway`.
- An `EgressOnlyInternetGateway` on the VPC.
- IPv4 CIDRs (`10.99.0.0/16`) so the env is truly dual-stack, not
  v6-only.

Stack outputs expose the VPC ID and subnet IDs for the test body.
`AfterSuite` first runs `copilot app delete` to tear down the copilot
env, then deletes the helper stack.

**Scenario 1 — imported + IPv6 env serves a dualstack workload:**

1. `copilot app init`.
2. `copilot env init` with `network.vpc.id`, subnet IDs, and
   `network.vpc.ipv6.enabled: true` pointed at the helper stack's
   outputs. The pre-flight validator passes.
3. `copilot env deploy`.
4. `copilot svc init` + `copilot svc deploy` a LoadBalancedWebService
   that runs `curl -6 --fail` against an AWS dualstack endpoint on
   startup and `sleep 3600`.
5. Assert via `DescribeLoadBalancers` that the shared public ALB has
   `IpAddressType: dualstack`.
6. Assert via `DescribeNetworkInterfaces` that the task ENI has a
   global IPv6 address.
7. `dig +short AAAA <svc-alias>` returns at least one AAAA record.
8. Poll CloudWatch Logs for the success marker from the startup
   `curl -6`.
9. Tear down.

**Scenario 2 — negative: validator rejects v4-only imported VPC:**

1. Deploy a tiny second helper stack (or reuse the `BeforeSuite` stack
   augmented with a second v4-only VPC) that provisions a plain
   IPv4-only VPC + two public + two private subnets. Capture its IDs.
2. Run `copilot env init` with that VPC ID and
   `network.vpc.ipv6.enabled: true`.
3. Assert the command exits non-zero with stderr containing the
   `errImportedVPCMissingIPv6` message and the VPC ID.
4. Assert no copilot env CFN stack was created (via `DescribeStacks`).
5. Tear down the v4-only helper VPC in `AfterSuite`.

### Custom resource (Jest) tests

None. No Lambda logic touched.

### Approximate test counts

- Manifest unit: 1 removed, 1 added (net zero).
- CLI validator unit: ~10 cases.
- Golden: 1 new, all existing unchanged.
- Local integration: ~2.
- E2E: 2 scenarios (1 positive, 1 negative).
- Custom resource: 0.

Fits in existing CI time budget.

## Stable Interfaces For Downstream Sub-Projects

Sub-project #5 (upgrade/v6-only/NAT removal) will consume:

- `validateImportedVPCIPv6Readiness` — the same helper is what #5's
  upgrade flow will call before attempting an in-place upgrade of an
  imported env.
- `errImportedVPCMissingIPv6`, `errImportedSubnetsMissingIPv6` — new
  sentinels in the `cli` package, stable for #5 assertions and reuse.

The EC2 `Subnet.IPv6CIDRBlocks` field and `DescribeVPC` helper are
general-purpose additions; #5 may reuse them for upgrade-path
subnet checks.

No new manifest fields, no new template-data fields, no new stack
outputs.

## Documentation

- `site/content/docs/manifest/environment.en.md` — extend the existing
  `ipv6` section to remove the "managed-VPC only" caveat and add a
  subsection on imported-VPC prerequisites:
  - VPC must have at least one IPv6 CIDR association.
  - Every imported public and private subnet must have at least one
    `/64` block.
  - Copilot does not manage an EgressOnlyIGW or IPv6 routing on the
    imported VPC. The user is responsible for egress configuration.
- `CHANGELOG.md`:
  > "Enable IPv6 dual-stack networking on environments that import an
  > existing VPC. Copilot validates that the imported VPC and every
  > imported subnet has an IPv6 CIDR block before deploying. The user
  > is responsible for pre-provisioning the EgressOnlyIGW and IPv6
  > routes on their VPC."

## Rollback Plan

- If a post-release bug surfaces, re-add the `errIPv6WithImportedVPC`
  guard in `manifest/validate_env.go`. Two-line patch. Users who had
  successfully deployed an imported + IPv6 env would have their next
  `env deploy` rejected; they would need to roll back to the prior
  copilot release, but no stack-state migration is required because
  the env stack itself is unchanged between IPv6-managed and
  IPv6-imported paths (same flag-driven template).
- The new validator helper is additive; removing its call sites (in
  `env_init.go` and `env_deploy.go`) is a mechanical revert.
- No production users yet depend on #4: `feature/ipv6-support` has
  not landed on `mainline`. The umbrella merges once #5 is in.

## Success Criteria

1. A user with a pre-provisioned IPv6-capable imported VPC can set
   `network.vpc.ipv6.enabled: true` in the env manifest, run
   `copilot env init` / `copilot env deploy`, and have the stack deploy
   successfully.
2. Workloads deployed into that environment get dualstack task ENIs,
   the shared ALB serves dualstack, and AAAA records resolve — the
   same end-to-end behavior #2 and #3 already deliver on managed VPCs.
3. A user with a v4-only imported VPC who sets `ipv6.enabled: true`
   gets a pre-flight error that names the missing VPC or subnet IPv6
   association; no CloudFormation call is attempted.
4. Every existing env-stack and workload-stack golden renders
   byte-identically. Regression suite unchanged.
5. Sub-project #5 can start without modifying any file owned by this
   spec, using only the shared validator helper and the two new
   error sentinels as its entry points into imported-VPC IPv6
   readiness checking.
