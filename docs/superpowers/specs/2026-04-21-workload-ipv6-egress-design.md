# Workload-Side IPv6 Egress

**Date**: 2026-04-21
**Tracking issue**: aws/copilot-cli#5339
**Sub-project**: #2 of the [IPv6 roadmap](2026-04-20-ipv6-support-roadmap.md)
**Depends on**: #1 (env-managed dual-stack VPC) — merged at `a8a9ee7b`
**Branch**: `feature/ipv6-task-egress` (off `feature/ipv6-support`)
**Status**: Design approved; ready for implementation plan

## Context

Sub-project #1 gave environments an opt-in dual-stack VPC: an
Amazon-provided `/56` IPv6 CIDR, `/64` subnet blocks, an Egress-Only
Internet Gateway, and `::/0` default routes. No workload yet uses any
of it — Fargate tasks in an IPv6-enabled env still get IPv4 addresses
only. The env SG has no IPv6 egress rule, so even if a task somehow had
an IPv6 address, it could not reach the internet.

This sub-project puts the "dual" into "dual-stack" on the compute side:
Fargate tasks get a global IPv6 address on their ENI and can reach the
IPv6 internet via the EgressOnlyIGW from sub-project #1.

## Goals

- Fargate tasks in a dual-stack env receive a global IPv6 address.
- Tasks can reach IPv6 destinations on the public internet (via the
  EgressOnlyIGW for private subnets, via the IGW for public subnets).
- The behavior is automatic: if the env has `ipv6.enabled: true`, every
  Linux workload in it is dual-stack. No per-service manifest field.
- Every non-IPv6 env and every workload deployed into one renders
  byte-identical CloudFormation to today.

## Non-Goals

- Load balancer dual-stack ingress (deferred to #3).
- Imported VPCs (deferred to #4; #1 already blocks IPv6 on imported VPCs
  with `errIPv6WithImportedVPC`).
- Windows Fargate IPv6 support — AWS does not offer IPv6 task ENIs on
  Windows Fargate platform version 1.0.0. Windows workloads in an IPv6
  env remain IPv4-only with a warning.
- Toggling IPv6 on existing envs (#5); #1 already blocks this with
  `errIPv6ToggleOnExistingEnv`.
- VPC endpoints for ECR/CloudWatch over IPv6 (orthogonal, out of the
  spec series scope).
- New manifest fields. Sub-project #2 is zero-config at the workload
  level.
- ECR-pull-over-IPv6 verification. The E2E proves IPv6 egress works by
  hitting an AWS dualstack endpoint; proving image pulls specifically
  travel over IPv6 is a separate concern tracked with #5.

## Design Decisions

These were resolved during brainstorming and pin the rest of the design:

1. **Workload IPv6 is auto-derived**, not a manifest opt-in. If
   `env.network.vpc.ipv6.enabled: true`, every Linux workload in the env
   is dual-stack.
2. **Env IPv6 state is discovered at render time** via the
   `envStackOutputsGetter` interface that sub-project #1 already shipped
   for stack-state toggle validation. Service stacks do not use
   `Fn::ImportValue` for this — an output that may be absent would
   otherwise break every v4-only service deploy.
3. **All four workload types are in scope**: LoadBalancedWebService,
   BackendService, WorkerService, ScheduledJob. ECS services share one
   partial; jobs have an analogous EventBridge Rule target block. If
   implementation uncovers a CloudFormation schema gap for
   `AssignIpv6Address` on `AWS::Events::Rule.EcsParameters`, we fall
   back to ECS-services-only and log ScheduledJob for #5.
4. **Windows Fargate is unsupported**: warning + skip. The CLI emits a
   yellow `Warning:` line and deploys the workload v4-only.
5. **E2E verifies task-side IPv6 reachability** via `curl -6` from a
   test container against an AWS dualstack endpoint. Structural checks
   (ENI has a global IPv6, env SG has `::/0` egress) also included.
6. **Env SG IPv6 egress is additive**: a standalone
   `AWS::EC2::SecurityGroupEgress` resource, not an inline
   `SecurityGroupEgress` on the SG. Preserves the CFN default
   IPv4-allow-all behavior → byte-identity for v4-only envs.

## Architecture

Three changes, all gated on the env's `IPv6Enabled` state.

### Env stack: shared IPv6 egress rule

The `EnvironmentSecurityGroup` in
`internal/pkg/template/templates/environment/cf.yml:215` is the shared
SG every workload in the env attaches to. Today it has no explicit
`SecurityGroupEgress`, so CloudFormation applies its default "allow all
IPv4 outbound" rule. IPv6 has no corresponding default — we must add an
explicit rule.

Adding an inline `SecurityGroupEgress` block to the SG would replace
the CFN default IPv4 rule, breaking byte-identity for v4-only envs. A
standalone `AWS::EC2::SecurityGroupEgress` resource sidesteps this:
it is additive and does not suppress the default.

```yaml
{{- if .VPCConfig.IPv6Enabled }}
EnvironmentSecurityGroupEgressIPv6:
  Type: AWS::EC2::SecurityGroupEgress
  Properties:
    GroupId: !Ref EnvironmentSecurityGroup
    IpProtocol: -1
    CidrIpv6: ::/0
    Description: Allow all IPv6 outbound traffic
{{- end }}
```

One SG, shared by every workload. No per-workload SG changes needed.

### Workload stack: AssignIpv6Address

At `svc deploy` / `job deploy` render time, copilot calls
`envDescriber.Outputs()` (via the existing `envStackOutputsGetter`
interface shipped by #1) and checks for `IPv6Enabled == "true"`. The
result is stamped onto the template data struct as a new `IPv6Enabled`
bool on `template.NetworkOpts`.

The shared partial
`internal/pkg/template/templates/workloads/partials/cf/service-base-properties.yml`
— used by LBWS, BackendService, and WorkerService — grows a single
guarded line:

```yaml
NetworkConfiguration:
  AwsvpcConfiguration:
    AssignPublicIp: {{.Network.AssignPublicIP}}
    {{- if and .Network.IPv6Enabled (not .Platform.IsWindows) }}
    AssignIpv6Address: ENABLED
    {{- end }}
    Subnets: ...
    SecurityGroups: ...
```

`Platform.IsWindows` is a new helper on the existing
`RuntimePlatformOpts` struct (one-liner: returns true when OS family is
Windows). Pre-existing Windows detection lives in that struct, so we
extend it rather than duplicating logic.

The ScheduledJob template
`internal/pkg/template/templates/workloads/scheduled-job/cf.yml` has an
analogous block under `Targets[0].EcsParameters.NetworkConfiguration.AwsVpcConfiguration`;
the same `AssignIpv6Address: ENABLED` line goes there, guarded the same
way. Jobs are Linux-only today, so the `IsWindows` guard is inert but
kept for consistency and future-proofing.

### CLI: Windows warning

In the Ask/Validate phase of `svc deploy` and `job deploy`, after the
env describe has returned:

```
If envOutputs["IPv6Enabled"] == "true" AND workload platform is Windows:
    log.Warningf("IPv6 is not enabled for %s: Windows Fargate does not support IPv6 task networking.\n", workloadName)
```

The warning is printed once per deploy, to stderr (matches copilot's
existing convention for non-fatal diagnostics — see CLAUDE.md style
guide).

## Template Data

New field on `template.NetworkOpts` (in
`internal/pkg/template/workload.go`):

```go
type NetworkOpts struct {
    AssignPublicIP string
    SubnetsType    string
    SubnetIDs      []string
    SecurityGroups []string
    IPv6Enabled    bool   // NEW — derived from env stack describe at deploy time
}
```

The template-consuming code paths already pass `NetworkOpts` through.
Adding a field requires no new wiring in the template package.

New helper on `RuntimePlatformOpts` (in
`internal/pkg/template/workload.go`):

```go
func (p RuntimePlatformOpts) IsWindows() bool {
    // Returns true iff p.OS is one of the Windows family constants.
}
```

## Go Plumbing

### Populating `NetworkOpts.IPv6Enabled`

The service and job stack constructors in
`internal/pkg/deploy/cloudformation/stack/` build the
`template.NetworkOpts` struct. These constructors already receive the
env's `Environment` config and (via dependency injection) the env
describer.

One new method on the relevant stack type(s):

```go
func (s *svc) envIPv6Enabled() bool {
    outputs, err := s.envDescriber.Outputs()
    if err != nil {
        // Describe failure must not block v4 deploys. Return false.
        return false
    }
    return outputs["IPv6Enabled"] == "true"
}
```

Transient describe failures fall through to `IPv6Enabled=false`
(safe: the workload deploys v4-only, which is what it did before #2).

`NetworkOpts` is built once per stack render, so one describe call per
deploy is sufficient; no additional caching is needed.

### Dependency injection

Sub-project #1 added `envStackOutputsGetter` to
`internal/pkg/cli/interfaces.go` and the
`deployEnvOpts.newEnvDescriber` factory to plumb it through. We reuse
the same factory for `deploySvcOpts` / `deployJobOpts`. Existing CLI
deploy tests inject mocks for env describers; the mock generated by #1
(`mocks/mock_interfaces.go`) extends cleanly to this use.

## Validation

All checks run in the command's `Validate()` phase before any
CloudFormation call.

| Condition | Result |
|---|---|
| Linux workload in IPv6 env | IPv6 auto-enabled silently. No diagnostic. |
| Windows workload in IPv6 env | Yellow `Warning: IPv6 is not enabled for <svc>: Windows Fargate does not support IPv6 task networking.` Deploy proceeds without IPv6. |
| Workload in non-IPv6 env | No IPv6 rendered. Byte-identical to today. |
| Env describe fails / output absent | Treat as `IPv6Enabled=false`. Do not block the deploy. |

No new error sentinels. No firm failures added in #2 — the v4 fallback
path is always safe.

### Out of scope for validation

- Subnet-level IPv6 readiness on imported VPCs — belongs to #4.
- Region support — every AWS region with Fargate supports IPv6 task
  ENIs on platform version 1.4.0.
- Platform-version enforcement — Linux workloads default to `LATEST`
  (which resolves to 1.4.0+). If a user somehow specifies an older PV,
  CFN's native error ("AssignIpv6Address requires 1.4.0 or later") is
  the right UX; copilot does not need to pre-check.

## File Layout

| Path | Change | Approx. LOC |
|---|---|---|
| `internal/pkg/template/templates/environment/cf.yml` | `EnvironmentSecurityGroupEgressIPv6` resource, guarded | +12 |
| `internal/pkg/template/templates/workloads/partials/cf/service-base-properties.yml` | Guarded `AssignIpv6Address: ENABLED` | +3 |
| `internal/pkg/template/templates/workloads/scheduled-job/cf.yml` | Guarded `AssignIpv6Address: ENABLED` in `EcsParameters` | +3 |
| `internal/pkg/template/workload.go` | `IPv6Enabled` on `NetworkOpts`; `IsWindows()` on `RuntimePlatformOpts` | +10 |
| `internal/pkg/template/workload_test.go` | Unit tests for new helper and struct field | +30 |
| `internal/pkg/deploy/cloudformation/stack/lb_web_svc.go`, `backend_svc.go`, `worker_svc.go`, `job.go` | `envIPv6Enabled()` helper; populate `NetworkOpts.IPv6Enabled` | +60 (all four) |
| `internal/pkg/deploy/cloudformation/stack/*_test.go` | Unit tests for IPv6 flag propagation | +100 |
| `internal/pkg/deploy/cloudformation/stack/testdata/workloads/*-ipv6.yml` | 4 new golden fixtures (one per workload type) | +800 |
| `internal/pkg/deploy/cloudformation/stack/testdata/environments/template-with-ipv6-enabled.yml` | Extend existing golden to include the new egress rule | +10 |
| `internal/pkg/cli/svc_deploy.go`, `job_deploy.go` | Windows+IPv6 warning emission | +40 |
| `internal/pkg/cli/svc_deploy_test.go`, `job_deploy_test.go` | Unit tests for warning path | +60 |
| `e2e/workload_ipv6/workload_ipv6_suite_test.go`, `workload_ipv6_test.go` | New Ginkgo v2 scenario | +300 |
| `e2e/workload_ipv6/testdata/` | Minimal IPv6-probe container + manifest | +50 |

Total: ~1,500 LOC. ~85% is tests and goldens (four new workload
goldens dominate). Fits in a single PR.

## Testing Strategy

Five layers; additive only — no existing tests modified.

### 1. Go unit tests

- `template.NetworkOpts.IPv6Enabled=true` produces `AssignIpv6Address: ENABLED` on LBWS, BackendService, WorkerService, and ScheduledJob rendered templates.
- `IPv6Enabled=true` + `IsWindows=true` omits the line.
- `IPv6Enabled=false` omits the line. Regression guard.
- `RuntimePlatformOpts.IsWindows()` table-driven over all known OS-family constants.
- `svcStack.envIPv6Enabled()` returns `false` on describe error.
- `svcStack.envIPv6Enabled()` returns correct bool for `"true"`, `"false"`, missing, and unrelated output values.

### 2. Stack integration / golden tests

Golden-file comparison in
`internal/pkg/deploy/cloudformation/stack/*_integration_test.go`
(build tag `integration || localintegration`):

- New goldens: one `testdata/workloads/<type>-ipv6.yml` per workload
  type (LBWS, Backend, Worker, Job). Each is a full render of a stock
  manifest deployed into an IPv6 env.
- **Byte-identity regression**: every existing workload golden must
  render byte-identically to today when `NetworkOpts.IPv6Enabled=false`.
  Non-negotiable.
- Existing env-stack golden `template-with-ipv6-enabled.yml` updated
  to include the new `EnvironmentSecurityGroupEgressIPv6` resource.

### 3. Local integration (`// +build localintegration`)

- End-to-end manifest parse → env describe (mocked) → service stack
  render produces CFN that parses as valid YAML and includes the
  `AssignIpv6Address` property in the right place.

### 4. E2E (Ginkgo v2 in `e2e/workload_ipv6/`, nightly-gated)

**Scenario 1 — Linux task has working IPv6 egress**:

1. Deploy an env with `network.vpc.ipv6.enabled: true`.
2. Deploy a BackendService whose container image runs on start:
   `curl -6 --max-time 10 --fail https://dualstack.s3.<region>.amazonaws.com/ && echo IPV6_EGRESS_OK || echo IPV6_EGRESS_FAIL`
   Then `sleep 3600` to keep the task around for inspection.
3. Wait until the task is RUNNING.
4. Assert via EC2 `DescribeNetworkInterfaces` that the task ENI has
   at least one global IPv6 address (in the env's `/56`).
5. Assert via EC2 `DescribeSecurityGroups` that the env SG has an
   egress rule with `CidrIpv6: ::/0, IpProtocol: -1`.
6. Poll CloudWatch Logs for the container log group; assert the line
   `IPV6_EGRESS_OK` appears within 2 minutes.
7. Tear down.

**Scenario 2 — non-IPv6 env is unaffected**:

1. Deploy an env with IPv6 off (default).
2. Deploy the same BackendService.
3. Assert the rendered service template does NOT contain
   `AssignIpv6Address`.
4. Assert the task ENI has no global IPv6 address.
5. Tear down.

Windows-workload scenarios are not E2E-covered in #2; the warning path
is unit-tested and structurally trivial.

### 5. Custom resource (Jest)

None in #2. No Lambda logic touched.

### Approximate test counts

- Unit: ~30
- Render goldens: 4 new + all existing unchanged
- Local integration: ~4
- E2E: 2 scenarios
- Custom resource: 0

### Test container

The E2E needs a minimal container with `curl` that prints a known
marker line and then sleeps. The spec requires only that this image
exists and has no third-party dependencies; choice of base image
(reuse an existing copilot E2E image if one with curl exists, else
build from `alpine` with `apk add curl`) is left to the implementation
plan.

## Stable Interfaces For Downstream Sub-Projects

Sub-project #2 is a leaf: #3, #4, #5 do not consume new interfaces
from it. The small surface area #2 adds is internal:

- `template.NetworkOpts.IPv6Enabled` — only read by the workload
  templates inside this spec's scope.
- `template.RuntimePlatformOpts.IsWindows()` — convenient helper that
  future code may reuse, but not part of any downstream contract.

No new stack outputs, manifest fields, or error sentinels.

## Documentation

- `site/content/docs/manifest/environment.en.md` — expand the `ipv6`
  section: mention that every Linux workload in the env is dual-stack
  automatically, and Windows workloads stay IPv4-only with a warning.
- CHANGELOG: "Enable IPv6 task networking on workloads deployed into
  dual-stack environments. Fargate tasks now receive a global IPv6
  address and can reach the IPv6 internet via the EgressOnlyIGW.
  Windows workloads remain IPv4 with a notice at deploy time."

No new manifest documentation — there is no new manifest field.

## Rollback Plan

- No user-facing manifest surface means no migration concern.
- If a post-release bug surfaces, a patch can disable the feature by
  wrapping the workload template's `AssignIpv6Address` line in a
  build-time feature flag, or by unconditionally returning `false` from
  `envIPv6Enabled()`. Both are two-line changes.
- The env SG egress rule is additive; a patch can drop it by reverting
  the new `EnvironmentSecurityGroupEgressIPv6` resource.
- No production users can yet depend on the feature: sub-project #1
  has only merged to `feature/ipv6-support`, not `mainline`, so no
  released copilot version ships IPv6 support.

## Success Criteria

1. A Linux BackendService, LoadBalancedWebService, WorkerService, or
   ScheduledJob deployed into an env with `ipv6.enabled: true` has
   `AssignIpv6Address: ENABLED` on its ECS task network configuration,
   the task ENI gets a global IPv6 address, and the task can reach an
   AWS dualstack endpoint via `curl -6`.
2. A Windows workload deployed into the same env prints the documented
   warning, deploys successfully, and runs IPv4-only.
3. Every existing workload golden renders byte-identically when the
   target env does not have IPv6 enabled. Regression suite unchanged.
4. Sub-project #3 (dual-stack LB ingress) can start without modifying
   any file owned by this spec.
