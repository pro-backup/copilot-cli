# Dual-Stack Load Balancer Ingress

**Date**: 2026-04-21
**Tracking issue**: aws/copilot-cli#5339
**Sub-project**: #3 of the [IPv6 roadmap](2026-04-20-ipv6-support-roadmap.md)
**Depends on**: #1 (env-managed dual-stack VPC) — merged at `a8a9ee7b`
**Parallel to**: #2 (workload-side IPv6 egress) — merged at `26389938`
**Branch**: `feature/ipv6-lb-ingress` (off `feature/ipv6-support`)
**Status**: Design approved; ready for implementation plan

## Context

Sub-project #1 gave environments an opt-in dual-stack VPC. Sub-project #2
made Fargate tasks reach the IPv6 internet from inside that VPC. Neither
did anything about *inbound* v6 traffic. Today, in an IPv6-enabled env:

- The shared `PublicLoadBalancer` (env stack, `cf.yml:314`) omits
  `IpAddressType`, so CFN defaults it to `ipv4`. The ALB has no AAAA
  address.
- The public HTTP/HTTPS SGs allow `0.0.0.0/0` on 80/443 with no v6
  counterpart.
- Route 53 aliases emitted by `https-listener.yml` and `http-listener.yml`
  are Type A only. Clients doing happy-eyeballs resolve v4.

Sub-project #3 puts the "dual" into "dual-stack" on the ingress side: the
shared ALBs accept v6 clients, and services resolve over AAAA.

## Goals

1. In an env with `network.vpc.ipv6.enabled: true`, the shared
   **PublicLoadBalancer** and **InternalLoadBalancer** are dual-stack:
   `IpAddressType: dualstack`.
2. Their security groups accept matching traffic on IPv6: `CidrIpv6: ::/0`
   by default; user-supplied IPv6 CIDRs rendered as `CidrIpv6` when the
   `http.public.security_groups.ingress` list contains them.
3. LoadBalancedWebService aliases (default and user-supplied) get
   Route 53 AAAA records alongside the existing A records, so clients
   resolve services over v6.
4. Same for BackendService with internal ALB aliases, against the
   internal ALB's hosted zone.
5. Every env and every workload renders byte-identical CloudFormation
   when `ipv6.enabled` is off. Non-negotiable, same regression gate as
   #1 and #2.

## Non-Goals

- Per-workload NLB (`workloads/partials/cf/nlb.yml`). Different shape
  (per-workload, not per-env), own SG, deferred to a follow-up sub-project.
- CloudFront/CDN. CloudFront distributions are already dual-stack at the
  edge; origin fetches over v4 are fine. Orthogonal.
- Imported VPCs (blocked upstream by `errIPv6WithImportedVPC`, removed
  by #4).
- Toggling IPv6 on existing envs (blocked upstream by
  `errIPv6ToggleOnExistingEnv`, removed by #5).
- New manifest fields. Zero-config at both the env and workload level,
  consistent with #2.
- Static site and RDWS workloads. They don't use copilot's shared ALB.
- IPv6-only ingress (`IpAddressType: dualstack-without-public-ipv4` or
  equivalent). Deferred to #5 alongside the broader NAT-removal / v6-only
  work.
- Platform-version or region checks. Every region with ECS Fargate
  supports dual-stack ALB; we don't pre-check.

## Design Decisions

These were resolved during brainstorming and pin the rest of the design:

1. **LB scope: Public ALB + Internal ALB.** Both shared env-level LBs
   become dual-stack in lockstep. Per-workload NLB is explicitly deferred.
2. **Zero-config auto-derive.** If the env has `ipv6.enabled: true`,
   every eligible LB resource is dual-stack. No workload-level opt-in.
   Mirrors #2's decision. `errIPv6ToggleOnExistingEnv` ensures the env
   manifest value matches the deployed env state, so stacks never
   diverge.
3. **AAAA records emitted by default.** When the env is dual-stack,
   every A alias gets a matching AAAA alias against the same ALB target.
   Without AAAA, the ALB is dual-stack in name only — clients can't
   resolve v6.
4. **SG ingress for custom-CIDR users splits by family.** A new
   `isIPv6CIDR` template helper routes each user-provided CIDR to
   `CidrIp` or `CidrIpv6`. Users with existing v4-only lists see no
   change; users who later add v6 CIDRs get the right rule. Prefix
   lists already work family-agnostically in AWS.
5. **All new v6 SG ingress uses standalone `AWS::EC2::SecurityGroupIngress`
   resources**, same pattern #2 used for the env SG's v6 egress. This
   is what preserves byte-identity for v4-only envs.
6. **No new stack outputs, manifest fields, error sentinels, or CLI
   warnings.** The surface area is strictly internal.

## Architecture

Three changes, each gated on the env's IPv6 state. The env-stack edits
consume `VPCConfig.IPv6Enabled` (the flag shipped by #1). The
workload-stack edits consume a new `WorkloadOpts.IPv6Enabled` populated
at stack construction from the same env-manifest source that #2 uses.

### Env stack: dual-stack ALBs + v6 SG ingress

In `internal/pkg/template/templates/environment/cf.yml`:

- Add `IpAddressType: dualstack` to `PublicLoadBalancer` (`cf.yml:321`)
  and `InternalLoadBalancer` (`cf.yml:416`), each under a three-line
  `{{- if .VPCConfig.IPv6Enabled }}` guard.
- Add four standalone `AWS::EC2::SecurityGroupIngress` resources, each
  guarded by the IPv6 flag:
  - `PublicHTTPLoadBalancerSecurityGroupIngressIPv6` (port 80, condition
    `CreateALB`)
  - `PublicHTTPSLoadBalancerSecurityGroupIngressIPv6` (port 443,
    condition `ExportHTTPSListener`)
  - `InternalLoadBalancerSecurityGroupIngressFromHttpIPv6` (port 80,
    condition `CreateInternalALB`, additionally gated on `AllowVPCIngress`)
  - `InternalLoadBalancerSecurityGroupIngressFromHttpsIPv6` (port 443,
    condition `ExportInternalHTTPSListener`, additionally gated on
    `AllowVPCIngress`)
- In the existing `PublicALBSourceIPs` loop (custom-ingress path), split
  each CIDR by family using a new `isIPv6CIDR` helper and emit either
  `CidrIp:` or `CidrIpv6:` accordingly.

The two public-ALB SGs (HTTP, HTTPS) have two paths today:

- **Default** (no custom ingress): inline `CidrIp: 0.0.0.0/0` rule.
  Adding v6 inline would change every v4-only env's golden, so v6 goes
  in a standalone resource.
- **Custom ingress** (`PublicHTTPConfig.HasCustomIngress`): iterates
  `PublicALBSourceIPs` and emits `CidrIp` per entry. Users today can
  only have supplied v4 CIDRs (there was no IPv6 path to support v6),
  so splitting by family with `isIPv6CIDR` preserves byte-identity for
  every existing golden.

`CIDRPrefixListIDs` is already family-agnostic at the EC2 level and
needs no change.

### Workload stack: AAAA alias records

In `internal/pkg/template/templates/workloads/partials/cf/https-listener.yml`:

- **Default path** (no aliases): extend `LoadBalancerDNSAlias`'s
  `RecordSets` with a parallel AAAA entry, same `AliasTarget`
  (`EnvControllerAction.PublicLoadBalancerHostedZone` +
  `...PublicLoadBalancerDNSName`), guarded on `.IPv6Enabled`.
- **Aliases path**: inside the `range $alias := $aliases` loop, emit
  an AAAA record alongside the A record with the same alias target —
  for BackendService the target is the internal ALB; for LBWS it is
  the public ALB.

In `http-listener.yml`:

- `LoadBalancerInternalDNSAlias` (BackendService only) gets a parallel
  AAAA record under the same IPv6 guard, targeting the internal ALB.

A single `AWS::Route53::RecordSetGroup` can hold records of different
types against different names with one HostedZoneId, so this is a
straightforward additive change. Route 53 resolves AAAA against the
ALB's v6 addresses automatically because the ALB is dual-stack.

### What does not change

- Target groups (`DefaultHTTPTargetGroup`, per-workload `TargetGroup`).
  `TargetType: ip` continues to register IPv4 targets. ECS tasks in
  dual-stack subnets register their primary IPv4 with the ALB. This is
  the standard AWS-recommended pattern: edge is dual-stack, ALB-to-target
  stays v4. No new ALB-to-task v6 requirement.
- Listener rules (HTTP/HTTPS host-header, path-pattern). Family-agnostic
  at the ALB level.
- `EnvControllerAction` Lambda (`cf-custom-resources/lib/env-controller.js`).
  It proxies the env stack's existing outputs, which work unchanged for
  dual-stack ALBs.
- No new env stack outputs.

## Template Data

### `isIPv6CIDR` helper

In `internal/pkg/template/template_functions.go`:

```go
func isIPv6CIDR(cidr string) bool {
    ip, _, err := net.ParseCIDR(cidr)
    if err != nil {
        return false
    }
    return ip.To4() == nil
}
```

Registered in the env template FuncMap in `internal/pkg/template/env.go`
as `"isIPv6CIDR": isIPv6CIDR`.

### `IPv6Enabled` on `WorkloadOpts`

Add a top-level `IPv6Enabled bool` field to `template.WorkloadOpts`
(`internal/pkg/template/workload.go`) — the struct that all workload
templates already read, alongside existing top-level flags like
`ALBEnabled`, `ALBListener`, `WorkloadType`. Listener partials
reference `{{- if .IPv6Enabled }}`.

The `NetworkOpts.IPv6Enabled` field added by #2 stays where it is —
it's the task-networking flag consumed by the base-properties partial.
The two fields carry the same bool value (both derived from
`envManifest.Network.VPC.IPv6Enabled()`) but serve different partials
in different scopes; keeping them separate avoids coupling the
listener path to task-networking internals. Both are populated from
the same `envIPv6Enabled` field on the stack struct that #2 added.

## Go Plumbing

### Populating `IPv6Enabled` on workload template data

#2 already reads `conf.EnvManifest.Network.VPC.IPv6Enabled()` in the
LBWS and BackendService stack constructors and stores it on an
`envIPv6Enabled bool` field on the stack struct. #3 reuses that field
and stamps it onto the template-data struct in the stack's `Template()`
method, next to where `ALBListener`, `Network`, and peers are populated.

No new env-describe call, no new mocks, no new interfaces. The decision
from #2 — trust `errIPv6ToggleOnExistingEnv` to guarantee the env
manifest value matches the deployed env — carries through.

The WorkerService and ScheduledJob paths have no ALB, so no IPv6Enabled
stamp on their template data and no listener-partial AAAA changes.

### No new error sentinels, no new CLI validation

- `errIPv6WithImportedVPC` already blocks the imported-VPC case upstream.
- `errIPv6ToggleOnExistingEnv` already blocks toggle-on-existing upstream.
- No Windows-specific restriction for ingress (unlike #2's task-ENI
  restriction), so no CLI warning.

## Validation

All checks from #1 and #2 still fire. No new firm failures added. The
v4-fallback path is always safe — if for some reason the env is not
dual-stack, the workload stack renders with `IPv6Enabled=false` and
produces byte-identical output to today.

## File Layout

| Path | Change | Approx. LOC |
|---|---|---|
| `internal/pkg/template/templates/environment/cf.yml` | `IpAddressType: dualstack` on both ALBs; 4 standalone SG ingress resources; `isIPv6CIDR` split in custom-ingress block | +60 |
| `internal/pkg/template/templates/workloads/partials/cf/https-listener.yml` | Parallel AAAA RecordSets in default path and aliases path | +25 |
| `internal/pkg/template/templates/workloads/partials/cf/http-listener.yml` | Parallel AAAA in `LoadBalancerInternalDNSAlias` | +10 |
| `internal/pkg/template/template_functions.go` | `isIPv6CIDR` helper | +10 |
| `internal/pkg/template/env.go` | Register `isIPv6CIDR` in FuncMap | +1 |
| `internal/pkg/template/env_test.go` | Unit tests for `isIPv6CIDR` | +25 |
| `internal/pkg/template/workload.go` | `IPv6Enabled bool` field on `WorkloadOpts` | +3 |
| `internal/pkg/template/workload_test.go` | Unit tests for AAAA emission | +40 |
| `internal/pkg/deploy/cloudformation/stack/lb_web_svc.go` | Stamp `envIPv6Enabled` onto template data in `Template()` | +5 |
| `internal/pkg/deploy/cloudformation/stack/backend_svc.go` | Same | +5 |
| `internal/pkg/deploy/cloudformation/stack/*_test.go` | Constructor-propagation regression tests | +50 |
| `internal/pkg/deploy/cloudformation/stack/testdata/environments/template-with-ipv6-enabled.yml` | Extend with dual-stack ALB + SG ingress resources | +40 |
| `internal/pkg/deploy/cloudformation/stack/testdata/environments/template-with-ipv6-and-custom-ingress.yml` | New golden exercising `isIPv6CIDR` split | +~500 |
| `internal/pkg/deploy/cloudformation/stack/testdata/workloads/lb-web-svc-ipv6.yml` | New golden | +~700 |
| `internal/pkg/deploy/cloudformation/stack/testdata/workloads/backend-svc-ipv6.yml` | New golden | +~600 |
| `e2e/lb_ipv6/lb_ipv6_suite_test.go`, `lb_ipv6_test.go` | 3 Ginkgo scenarios | +350 |
| `e2e/lb_ipv6/testdata/` | Minimal test workload manifests + v6-capable client container | +80 |
| `site/content/docs/manifest/environment.en.md` | Extend the `ipv6` section with LB-ingress notes | +30 |
| `CHANGELOG.md` | Entry | +3 |

Total: ~2,550 LOC. ~90% is tests and goldens. Fits in a single PR.

## Testing Strategy

Five layers; additive only — no existing tests modified.

### 1. Go unit tests

- `isIPv6CIDR` helper: table-driven over v4 CIDRs (`10.0.0.0/8`,
  `0.0.0.0/0`), v6 CIDRs (`::/0`, `2001:db8::/32`), bare IPs (not
  CIDRs), empty strings, garbage. In `internal/pkg/template/env_test.go`.
- `WorkloadOpts.IPv6Enabled=true` on a LBWS-shaped render produces
  AAAA records in both the default and the aliases path of
  `https-listener.yml`.
- `WorkloadOpts.IPv6Enabled=true` with `WorkloadType="Backend Service"`
  produces AAAA records in `LoadBalancerInternalDNSAlias` and any
  internal-ALB alias paths.
- `WorkloadOpts.IPv6Enabled=false` omits every AAAA record.
  Regression guard.
- LBWS and BackendService stack constructors populate `envIPv6Enabled`
  correctly from `conf.EnvManifest.Network.VPC.IPv6Enabled()` —
  covered by the integration goldens rather than a separate mock test.

### 2. Env-stack render goldens

Build tag `integration || localintegration`. Golden-file comparison
in `internal/pkg/deploy/cloudformation/stack/env_integration_test.go`:

- **Extend** `testdata/environments/template-with-ipv6-enabled.yml`
  to include `IpAddressType: dualstack` on both ALBs and the four
  new standalone `SecurityGroupIngress` resources.
- **New** `testdata/environments/template-with-ipv6-and-custom-ingress.yml`
  exercising the `isIPv6CIDR` split with a mix of v4 and v6 CIDRs in
  `PublicALBSourceIPs`.
- **Byte-identity regression**: every other existing env-stack golden
  must render byte-identically. Non-negotiable.

### 3. Workload-stack render goldens

Same build tag. In
`internal/pkg/deploy/cloudformation/stack/*_integration_test.go`:

- **New** `testdata/workloads/lb-web-svc-ipv6.yml`: LBWS with default
  alias and a user-supplied alias list, into an IPv6 env. Asserts
  parallel AAAA next to A in the default `LoadBalancerDNSAlias` and
  in the per-hosted-zone aliases RecordSetGroups.
- **New** `testdata/workloads/backend-svc-ipv6.yml`: BackendService
  with internal-ALB alias, into an IPv6 env. Asserts
  `LoadBalancerInternalDNSAlias` has both A and AAAA records.
- **Byte-identity regression**: every existing LBWS and BackendService
  golden renders byte-identically when the env is v4-only. The four
  `-ipv6.yml` goldens added by #2 are extended with AAAA records only
  if they use aliases.

### 4. Local integration (`// +build localintegration`)

- Parse → render → CFN-yaml-valid check for the full env stack with
  `ipv6.enabled: true`, including the dual-stack ALBs and the four new
  SG ingress rules.
- Parse → render a LBWS with a custom alias into an IPv6 env; assert
  both A and AAAA records parse as valid CFN and have matching alias
  targets.

### 5. E2E (Ginkgo v2 in `e2e/lb_ipv6/`, nightly-gated)

**Scenario 1 — Public dual-stack ALB resolves and serves over v6:**

1. Deploy an env with `network.vpc.ipv6.enabled: true` in a
   domain-managed app (with a hosted zone copilot can write to).
2. Deploy a stock LoadBalancedWebService.
3. Assert via EC2 `DescribeLoadBalancers` that the public ALB has
   `IpAddressType: dualstack`.
4. Assert via EC2 `DescribeSecurityGroups` that the public-HTTP SG
   has a `::/0` ingress rule on port 80 (and HTTPS SG has one on
   port 443 if HTTPS is enabled).
5. Resolve the service's alias with `dig +short AAAA <alias>`; assert
   at least one AAAA answer.
6. From a v6-capable client container, `curl -6 --max-time 20 --fail
   http://<alias>/` succeeds with the expected payload. Also run
   `curl -4` and assert it still succeeds.
7. Tear down.

**Scenario 2 — Internal ALB gets v6 ingress with `AllowVPCIngress`:**

1. Deploy an env with `ipv6.enabled: true` and an internal ALB with
   `allow_vpc_ingress: true`.
2. Deploy a BackendService with `http.path: "/"`.
3. Assert the internal ALB has `IpAddressType: dualstack`.
4. Assert the internal-LB SG has both `::/0` and `0.0.0.0/0` ingress
   rules on port 80.
5. From a task in the same VPC, `curl -6 http://<internal-alias>/`
   succeeds.
6. Tear down.

**Scenario 3 — v4-only env is unaffected (structural):**

1. Deploy an env with `ipv6.enabled: false` (default) and a LBWS.
2. Assert the public ALB has no `IpAddressType: dualstack` via
   `DescribeLoadBalancers` (AWS returns `ipv4`).
3. Assert no AAAA record exists for the service alias.
4. Tear down.

### 6. Custom resource tests (Jest)

None. No Lambda logic touched. `env-controller.js` returns whatever
outputs the env stack has; we added none.

### Approximate test counts

- Unit: ~20
- Render goldens: 2 new workload + 1 new env variant + extended
  existing IPv6 env golden
- Local integration: ~3
- E2E: 3 scenarios
- Custom resource: 0

Fits in existing CI time budget.

## Stable Interfaces For Downstream Sub-Projects

#3 is a leaf like #2 — #4 and #5 do not consume new interfaces from
it. Internal surface added:

- `template.WorkloadOpts.IPv6Enabled` — only read by the listener
  partials in this spec's scope.
- `isIPv6CIDR` template helper — convenient, not a downstream contract.

No new stack outputs, no new manifest fields, no new error sentinels.

## Documentation

- `site/content/docs/manifest/environment.en.md` — extend the existing
  `ipv6` section (from #1/#2) with: "When enabled, the shared public
  and internal Application Load Balancers run in `dualstack` mode and
  LoadBalancedWebService / BackendService aliases get AAAA records
  alongside A records, so clients reach the service over IPv6."
- `CHANGELOG.md`: "Route internet IPv6 traffic to services in dual-stack
  environments. Shared ALBs run in `dualstack` mode, security groups
  accept IPv6 ingress, and Route 53 AAAA records are emitted alongside
  A records for every LoadBalancedWebService and BackendService alias."

No new manifest documentation — no new manifest field.

## Rollback Plan

- No user-facing manifest surface, no migration concern.
- If a post-release bug surfaces, a patch can disable the feature by
  unconditionally rendering `IPv6Enabled=false` on workload template
  data, or by reverting the `{{- if .VPCConfig.IPv6Enabled }}` guards
  in `environment/cf.yml`. Both are mechanical reverts.
- The SG ingress rules and AAAA records are additive; reverts don't
  affect running workloads' v4 reachability.
- No production users yet depend on the feature: #1, #2, and (after
  this) #3 have only merged to `feature/ipv6-support`, not `mainline`.
  The umbrella branch merges to `mainline` once #5 lands.

## Success Criteria

1. A LoadBalancedWebService deployed into an env with `ipv6.enabled:
   true` resolves both A and AAAA over its configured alias, and
   clients can reach it over either protocol.
2. A BackendService with the internal ALB in the same env resolves
   both A and AAAA for its internal alias; v6-capable tasks in the
   same VPC can reach it over IPv6.
3. User-supplied ingress CIDRs are emitted as `CidrIp` (v4) or
   `CidrIpv6` (v6) based on the CIDR's family, with no change to
   existing v4-only behavior.
4. Every existing env-stack and workload-stack golden renders
   byte-identically when the env has IPv6 off. Regression suite
   unchanged.
5. Sub-project #4 (imported-VPC IPv6) can start without modifying
   any file owned by this spec.
