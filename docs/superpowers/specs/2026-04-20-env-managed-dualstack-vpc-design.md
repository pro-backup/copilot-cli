# Env-Managed Dual-Stack VPC (IPv6 Foundation)

**Date**: 2026-04-20
**Tracking issue**: aws/copilot-cli#5339
**Status**: Design approved; ready for implementation plan

## Context

AWS charges $0.005/hour (~$3.60/month) per public IPv4 address since Feb 2024.
Copilot-CLI users in issue #5339 request IPv6 dual-stack VPCs to reduce
dependency on public IPv4 addresses and NAT Gateway data-processing fees.

Today the copilot-cli codebase contains zero IPv6 references. Every managed
VPC is IPv4-only. This design is the first of five sub-projects that together
deliver full IPv6 support:

1. **Env-managed dual-stack VPC** (this document) — VPC foundation
2. Workload-side IPv6 egress — tasks speak v6
3. Dual-stack ALB ingress — clients reach services over v6
4. Imported-VPC IPv6 support — opt-in for users who bring their own VPC
5. Upgrade/downgrade, v6-only, Service Connect over v6 — NAT removal and
   migration path for existing environments

Each sub-project gets its own spec, plan, and PR. This spec covers only #1.

## Goals

- Let users opt an environment into dual-stack networking via a single
  manifest field.
- Generate a CloudFormation environment stack that creates an Amazon-provided
  `/56` IPv6 CIDR on the VPC, dual-stack `/64` subnets, an egress-only
  internet gateway, and correct IPv6 default routes.
- Preserve behavior exactly for every environment that does not opt in.
- Establish stable interfaces (manifest field, template data, stack outputs,
  error sentinels) that sub-projects #2–#5 consume without modifying #1.

## Non-Goals

- Task/ECS networking (deferred to #2).
- Load balancer `IpAddressType` (deferred to #3).
- Imported VPCs (deferred to #4); explicit validation error blocks this case.
- Toggling IPv6 on existing environments (deferred to #5); explicit
  validation error blocks this case.
- NAT Gateway removal or resizing (deferred to #5).
- IPv6-only subnets, DNS64/NAT64, Service Connect over v6 (deferred to #5).
- BYO IPv6 CIDR or IPAM pool integration (reserved fields in manifest,
  no implementation in v1).
- VPC endpoints for ECR/CloudWatch/S3 (orthogonal; not part of this spec
  series).

## Cost Framing

This sub-project on its own saves users $0. It is pure infrastructure. The
invoice drops only once sub-project #5 can remove the NAT Gateway. This
framing is important for user expectations and release notes.

## Manifest Schema

New optional struct `ipv6VPCConfig` on `environmentVPCConfig`:

```yaml
name: test
type: Environment

network:
  vpc:
    cidr: 10.0.0.0/16
    ipv6:
      enabled: true
    subnets:
      public:  [{cidr: 10.0.0.0/24, az: us-east-1a}, {cidr: 10.0.2.0/24, az: us-east-1b}]
      private: [{cidr: 10.0.1.0/24, az: us-east-1a}, {cidr: 10.0.3.0/24, az: us-east-1b}]
```

### Go model

```go
type environmentVPCConfig struct {
    ID      *string               `yaml:"id"`
    CIDR    *IPNet                `yaml:"cidr"`
    IPv6    *ipv6VPCConfig        `yaml:"ipv6,omitempty"`
    Subnets subnetsConfiguration  `yaml:"subnets"`
}

type ipv6VPCConfig struct {
    Enabled *bool `yaml:"enabled"`
    // Reserved for sub-project #1.x (not implemented in v1):
    // CIDR  *string  // BYOIP
    // Pool  *string  // IPAM pool id
}
```

### Back-compat

- `ipv6` key omitted → `IPv6 == nil` → template renders byte-identical to
  today's output. Enforced by a dedicated zero-value test.
- `ipv6.enabled: false` or absent → same as omitted.
- `ipv6.enabled: true` → all IPv6 template paths activate.

## Validation

All checks run in the command's `Validate()` phase before any AWS API call.

### Manifest-level (pure function, unit-testable)

| Condition | Result |
|-----------|--------|
| `vpc.ipv6.enabled: true` AND `vpc.id` set (imported VPC) | Firm error `ErrIPv6WithImportedVPC` |
| `vpc.ipv6.enabled: false` or omitted | No-op |
| `vpc.ipv6` present but `enabled` field missing | Treat as `enabled: false`, no error |

### Stack-state (requires describing current env stack)

| Condition | Result |
|-----------|--------|
| `env deploy` toggles `ipv6.enabled` false → true on existing stack | Firm error `ErrIPv6ToggleOnExistingEnv` |
| `env deploy` toggles `ipv6.enabled` true → false on existing stack | Firm error `ErrIPv6ToggleOnExistingEnv` |
| `env deploy` leaves `ipv6.enabled` unchanged | No-op |

Detection uses the new stack output `IPv6Enabled` (see Stack Outputs below).
For stacks deployed before sub-project #1 landed, the output is absent —
treated as `false`.

### Runtime failures

Inherit CloudFormation's native rollback. No custom handling needed.

### Out of scope for validation in v1

- Region support (every AWS region supports Amazon-provided IPv6).
- Account-level IPv6 readiness (AWS auto-enables).
- Subnet IPv6 readiness on imported VPCs (belongs to #4).

### User-facing error copy

```
Error: IPv6 cannot be enabled on an environment that imports a VPC. Support
for imported VPCs is tracked separately. Remove `network.vpc.id` or
`network.vpc.ipv6`.
```

```
Error: IPv6 cannot be toggled on an existing environment `test`. Create a
new environment with IPv6 enabled, or follow the upgrade path once it lands.
```

## CloudFormation Template Changes

All IPv6 resources live inside `{{- if .VPCConfig.IPv6Enabled }}` guards in
`internal/pkg/template/templates/environment/partials/vpc-resources.yml` and
`nat-gateways.yml`. When the flag is off, rendered CFN is byte-identical to
today.

### New resources

```yaml
VPCCidrBlockIPv6:
  Type: AWS::EC2::VPCCidrBlock
  Properties:
    VpcId: !Ref VPC
    AmazonProvidedIpv6CidrBlock: true

EgressOnlyInternetGateway:
  Type: AWS::EC2::EgressOnlyInternetGateway
  Properties:
    VpcId: !Ref VPC
```

### Modified subnets

The existing template generates subnets with a `range` loop over
`.PublicSubnetCIDRs` and `.PrivateSubnetCIDRs` (subnet count is user-
configurable, not fixed at 2+2). IPv6 CIDR assignment mirrors the same
pattern.

Each subnet carves one `/64` from the VPC's `/56`. A `/56` contains 256
`/64` blocks — we request the full 256 via `Fn::Cidr` and `Fn::Select` by
a computed index: `$ind` for public subnets, `publicCount + $ind` for
private subnets (guaranteeing disjoint blocks regardless of configured
subnet counts):

```yaml
{{- range $ind, $cidr := .PublicSubnetCIDRs }}
PublicSubnet{{inc $ind}}:
  Type: AWS::EC2::Subnet
  {{- if $.VPCConfig.IPv6Enabled }}
  DependsOn: VPCCidrBlockIPv6
  {{- end }}
  Properties:
    # existing v4 properties unchanged
    {{- if $.VPCConfig.IPv6Enabled }}
    Ipv6CidrBlock: !Select [{{$ind}}, !Cidr [!Select [0, !GetAtt VPC.Ipv6CidrBlocks], 256, 64]]
    AssignIpv6AddressOnCreation: true
    {{- end }}
{{- end }}

{{- $publicCount := len .PublicSubnetCIDRs }}
{{- range $ind, $cidr := .PrivateSubnetCIDRs }}
PrivateSubnet{{inc $ind}}:
  Type: AWS::EC2::Subnet
  {{- if $.VPCConfig.IPv6Enabled }}
  DependsOn: VPCCidrBlockIPv6
  {{- end }}
  Properties:
    # existing v4 properties unchanged
    {{- if $.VPCConfig.IPv6Enabled }}
    Ipv6CidrBlock: !Select [{{add $publicCount $ind}}, !Cidr [!Select [0, !GetAtt VPC.Ipv6CidrBlocks], 256, 64]]
    AssignIpv6AddressOnCreation: true
    {{- end }}
{{- end }}
```

This requires a new `add` template helper in
`internal/pkg/template/env.go`'s FuncMap (`"add": func(a, b int) int
{ return a + b }`). Adding it is scoped to this spec.

### IPv6 routes and private route tables

The existing `nat-gateways.yml` gates private route tables, their
associations, and the v4 default route on CFN `Condition: CreateNATGateways`.
When IPv6 is enabled but NAT is disabled (a real, intended combination for
the v6-only egress cost-saving case), we still need a private route table
to host the `::/0 → EgressOnlyIGW` route.

Introduce a new CFN condition that widens the gate without changing v4-only
behavior:

```yaml
CreatePrivateRouteTables: !Or
  - !Condition CreateNATGateways
  - !Equals [ "true", "true" ]   # when IPv6 is on, this clause is emitted
```

Implementation note: emit the `CreatePrivateRouteTables` condition in
`cf.yml` only when `IPv6Enabled` is true, and use `{{- if }}/{{- else }}`
branching at each resource's `Condition:` line in `nat-gateways.yml` so
IPv4-only envs render exactly today's output.

Gate changes in `nat-gateways.yml`:

| Resource | Current gate | New gate (when IPv6 on) |
|---|---|---|
| `PrivateRouteTable{N}` | `CreateNATGateways` | `CreatePrivateRouteTables` |
| `PrivateRouteTable{N}Association` | `CreateNATGateways` | `CreatePrivateRouteTables` |
| `PrivateRoute{N}` (v4 default → NAT) | `CreateNATGateways` | `CreateNATGateways` (unchanged) |
| `NatGateway{N}` / EIP | `CreateNATGateways` | `CreateNATGateways` (unchanged) |

Add IPv6 routes in the same `range $ind, $cidr := .PrivateSubnetCIDRs` loop:

```yaml
{{- if $.VPCConfig.IPv6Enabled }}
PrivateRouteIPv6{{inc $ind}}:
  Type: AWS::EC2::Route
  Properties:
    RouteTableId: !Ref PrivateRouteTable{{inc $ind}}
    DestinationIpv6CidrBlock: ::/0
    EgressOnlyInternetGatewayId: !Ref EgressOnlyInternetGateway
{{- end }}
```

Public subnets reuse the shared `PublicRouteTable` (already exists,
unconditional). Add the v6 default route once, not per-subnet:

```yaml
{{- if .VPCConfig.IPv6Enabled }}
DefaultPublicRouteIPv6:
  Type: AWS::EC2::Route
  Properties:
    RouteTableId: !Ref PublicRouteTable
    DestinationIpv6CidrBlock: ::/0
    GatewayId: !Ref InternetGateway
{{- end }}
```

### Environment security group

`EnvironmentSecurityGroupIngressFromSelf` in `cf.yml:259` already uses the
self-reference pattern (`SourceSecurityGroupId: !Ref EnvironmentSecurityGroup`),
which works for both IPv4 and IPv6 traffic without modification. No SG
refactor needed. Byte-identity for v4-only envs is preserved.

User-provided SG rules on `.VPCConfig.SecurityGroupConfig.Ingress` use
`CidrIp`; any IPv6 equivalents for user rules are a future sub-project's
concern, not #1's.

### Stack outputs (new)

- `IPv6Enabled` (string, emitted only when IPv6 on): gate for stack-state
  validation in future deploys.
- `VpcIpv6CidrBlock` (string, emitted only when IPv6 on): consumed by
  #2/#3/#5.
- `PublicSubnetsIpv6CidrBlocks` (comma-delimited): consumed by #2/#3.
- `PrivateSubnetsIpv6CidrBlocks` (comma-delimited): consumed by #2/#3.

## File Layout

| Path | Change | Approx. LOC |
|------|--------|-------------|
| `internal/pkg/manifest/env.go` | `ipv6VPCConfig` struct + field + accessor | +40 |
| `internal/pkg/manifest/validate_env.go` | Manifest-level validation | +30 |
| `internal/pkg/manifest/env_test.go` | Unit tests | +100 |
| `internal/pkg/manifest/validate_env_test.go` | Unit tests | +60 |
| `internal/pkg/template/templates/environment/partials/vpc-resources.yml` | Guarded IPv6 resources, subnet blocks, public v6 route | +70 |
| `internal/pkg/template/templates/environment/partials/nat-gateways.yml` | Widen route-table gates, add private v6 routes | +25 |
| `internal/pkg/template/templates/environment/cf.yml` | `CreatePrivateRouteTables` condition, new outputs | +20 |
| `internal/pkg/template/env.go` | Add `add` helper to FuncMap, `IPv6Enabled` on VPCConfig | +5 |
| `internal/pkg/template/env_test.go` | Unit test for `add` helper | +15 |
| `internal/pkg/deploy/cloudformation/stack/env.go` | Thread IPv6Enabled through vpcConfig() | +10 |
| `internal/pkg/deploy/cloudformation/stack/env_integration_test.go` | New golden test case + threaded-flag assertion | +50 |
| `internal/pkg/deploy/cloudformation/stack/testdata/environments/template-with-ipv6-enabled.yml` | New golden fixture | +~450 |
| `internal/pkg/cli/env_deploy.go` | Stack-state IPv6-toggle validation | +30 |
| `e2e/env_ipv6/env_ipv6_suite_test.go`, `env_ipv6_test.go` | New E2E scenarios | +200 |

Total: ~1,100 LOC. ~70% is tests and goldens. Fits in a single PR.

## Testing Strategy

Five layers. Additive only — no existing tests deleted.

### 1. Go unit tests

Covered in `internal/pkg/manifest/env_test.go` and `validate_env_test.go`:

- YAML round-trip for `ipv6.enabled: true`, `false`, omitted, and
  empty-map input.
- Table-driven validation cases matching the two tables under Validation.
- Zero-value guarantee: unmarshaling a manifest without `ipv6` produces
  `IPv6 == nil`, not a zero struct. Prevents silent template drift.

### 2. Env-stack integration tests (render goldens)

Golden-file comparison in
`internal/pkg/deploy/cloudformation/stack/env_integration_test.go`
(build tag `integration || localintegration`):

- New golden `testdata/environments/template-with-ipv6-enabled.yml` with
  full rendered stack.
- **Byte-identical regression**: all 7 existing env-stack goldens must
  produce output byte-identical to today when `IPv6Enabled` is false.
  This is the single most important test in the suite.

### 3. Local integration (`// +build localintegration`)

- `env init` with `ipv6.enabled: true` generates a manifest that validates
  and renders a template that parses as valid CloudFormation.
- `env init` with `ipv6.enabled: true` + `vpc.id` set returns
  `errIPv6WithImportedVPC`.

### 4. E2E (Ginkgo v2 in `e2e/`, nightly-gated)

New scenarios in `e2e/env_ipv6/`:

- **Scenario 1**: create an env with `ipv6.enabled: true` using copilot's
  default subnet layout. Assert the VPC has a `/56` IPv6 CIDR, every
  configured subnet has a `/64` block and `AssignIpv6AddressOnCreation:
  true`, an `EgressOnlyInternetGateway` exists, each private subnet's route
  table has `::/0 → EgressOnlyIGW`, the public route table has
  `::/0 → IGW`, and public/private subnet IPv6 CIDRs are disjoint.
- **Scenario 2**: create an env with `ipv6.enabled: true` AND no workloads
  needing NAT, to exercise the `CreatePrivateRouteTables` widening. Assert
  no NAT gateway is provisioned but private subnets still have a route
  table with the IPv6 default route.

**Not in #1**: end-to-end "task pulls ECR image over IPv6" verification —
that requires sub-project #2.

### 5. Custom resource tests (Jest)

None for #1. No Lambda logic touched. #4 and #5 will add these.

### Manual pre-release smoke (PR description, not automated)

One engineer:
1. Creates an env with IPv6 on.
2. Deploys a stock backend service.
3. Confirms healthy start.
4. Tears it down.

Catches anything that surfaces only with a real workload running.

### Approximate test counts

- Unit: ~25
- Render goldens: 8 (7 existing byte-identical + 1 new)
- Local integration: ~4
- E2E: 2
- Custom resource: 0

Fits in existing CI time budget.

## Stable Interfaces for Downstream Sub-Projects

Sub-projects #2, #3, #4, #5 consume these without modifying #1:

1. **Manifest field**: `network.vpc.ipv6.enabled` (documented in
   `site/content/docs/manifest/environment.en.md`).
2. **Template data field**: `VPCConfig.IPv6Enabled bool` — #2/#3 check this
   to gate their own template additions.
3. **Stack outputs**: `IPv6Enabled`, `VpcIpv6CidrBlock`,
   `PublicSubnetsIpv6CidrBlocks`, `PrivateSubnetsIpv6CidrBlocks` — consumed
   via `DescribeStacks` or existing copilot env-describe plumbing.
4. **Error sentinels**: `errIPv6WithImportedVPC` (removed by #4),
   `errIPv6ToggleOnExistingEnv` (removed by #5).

## Documentation

- `site/content/docs/manifest/environment.en.md` — new `ipv6` section with
  example and explicit note that this is foundation-only.
- CHANGELOG: "Add opt-in dual-stack VPC support for environments
  (foundation; task/LB support to follow)."

## Rollback Plan

If a bug is found post-release:

- `ipv6.enabled` defaults off; every user on any prior copilot version is
  unaffected.
- A patch release can force-disable the feature by adding a temporary
  validation error on `ipv6.enabled: true`. No stack changes needed —
  no production users depend on the feature yet, so no stack is already
  running with IPv6 on.

## Success Criteria

1. A user can add `ipv6.enabled: true` to a new env manifest and run
   `copilot env init` / `copilot env deploy` to completion.
2. The resulting VPC has an Amazon-provided `/56`, every configured subnet
   has a disjoint `/64` block with auto-assignment, an EgressOnlyIGW exists,
   and routing tables have correct `::/0` routes (public via IGW, private
   via EgressOnlyIGW), regardless of whether NAT Gateway is enabled.
3. All existing copilot unit, integration, and E2E tests pass with zero
   changes to their expected outputs.
4. Sub-project #2 can be started without touching any file owned by this
   spec, using only the four interfaces listed under Stable Interfaces.
