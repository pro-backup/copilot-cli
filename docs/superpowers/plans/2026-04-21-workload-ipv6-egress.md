# Workload-Side IPv6 Egress Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make Fargate tasks in a dual-stack env receive a global IPv6 address on their ENI and reach the IPv6 internet via the EgressOnlyIGW from sub-project #1.

**Architecture:** Env IPv6 state flows from `envManifest.Network.VPC.IPv6Enabled()` (shipped by #1) through each workload stack's config (`EnvManifest *manifest.Environment`) into a new `template.NetworkOpts.IPv6Enabled` bool. Templates gate a single `AssignIpv6Address: ENABLED` line on that bool and a new `.Platform.IsWindows` negation. The env's shared `EnvironmentSecurityGroup` gets an additive `AWS::EC2::SecurityGroupEgress` rule for `::/0` (preserves CFN-default IPv4-allow-all → byte-identity for v4-only envs). Warning emitted at deploy time when Windows workload + IPv6 env.

**Tech Stack:** Go 1.23, `mockgen` + `testify` for tests, CloudFormation YAML via `text/template`, Ginkgo v2 for E2E.

**Reference spec:** `docs/superpowers/specs/2026-04-21-workload-ipv6-egress-design.md`
**Base branch:** `feature/ipv6-task-egress` (already checked out off `feature/ipv6-support`)

---

## Task 1: Add `IsWindows()` helper to `RuntimePlatformOpts`

**Files:**
- Modify: `internal/pkg/template/workload.go` (after line 764, inside the `RuntimePlatformOpts` methods)
- Test: `internal/pkg/template/workload_test.go` (after the existing `TestRuntimePlatformOpts_IsDefault` block near line 223)

Context: `osFamiliesForPV100` at `internal/pkg/template/workload.go:118` already lists the four Windows OS family constants. We reuse it so the truth source stays single.

- [ ] **Step 1: Write the failing test**

Append to `internal/pkg/template/workload_test.go`:

```go
func TestRuntimePlatformOpts_IsWindows(t *testing.T) {
	testCases := map[string]struct {
		in     RuntimePlatformOpts
		wanted bool
	}{
		"empty platform is not Windows": {
			in:     RuntimePlatformOpts{},
			wanted: false,
		},
		"linux/amd64 is not Windows": {
			in:     RuntimePlatformOpts{OS: OSLinux, Arch: ArchX86},
			wanted: false,
		},
		"linux/arm64 is not Windows": {
			in:     RuntimePlatformOpts{OS: OSLinux, Arch: ArchARM64},
			wanted: false,
		},
		"windows_server_2019_full is Windows": {
			in:     RuntimePlatformOpts{OS: OSWindowsServer2019Full, Arch: ArchX86},
			wanted: true,
		},
		"windows_server_2019_core is Windows": {
			in:     RuntimePlatformOpts{OS: OSWindowsServer2019Core, Arch: ArchX86},
			wanted: true,
		},
		"windows_server_2022_full is Windows": {
			in:     RuntimePlatformOpts{OS: OSWindowsServer2022Full, Arch: ArchX86},
			wanted: true,
		},
		"windows_server_2022_core is Windows": {
			in:     RuntimePlatformOpts{OS: OSWindowsServer2022Core, Arch: ArchX86},
			wanted: true,
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.wanted, tc.in.IsWindows())
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./internal/pkg/template/ -run TestRuntimePlatformOpts_IsWindows -v
```

Expected: FAIL — `p.IsWindows undefined (type RuntimePlatformOpts has no field or method IsWindows)`.

- [ ] **Step 3: Implement `IsWindows()`**

Insert after `Version()` (around line 774) in `internal/pkg/template/workload.go`:

```go
// IsWindows returns true if the platform's OS family is one of the
// supported Windows Server variants.
func (p RuntimePlatformOpts) IsWindows() bool {
	for _, os := range osFamiliesForPV100 {
		if p.OS == os {
			return true
		}
	}
	return false
}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
go test ./internal/pkg/template/ -run TestRuntimePlatformOpts_IsWindows -v
```

Expected: PASS for all 7 subtests.

- [ ] **Step 5: Commit**

```bash
git add internal/pkg/template/workload.go internal/pkg/template/workload_test.go
git commit -m "Template: add RuntimePlatformOpts.IsWindows helper

Reuses the existing osFamiliesForPV100 OS-family list so the
definition of \"Windows workload\" stays single-sourced. Used by the
upcoming IPv6 egress work to gate AssignIpv6Address, which Windows
Fargate does not support."
```

---

## Task 2: Add `IPv6Enabled` field to `template.NetworkOpts`

**Files:**
- Modify: `internal/pkg/template/workload.go:703-710`
- Test: `internal/pkg/template/workload_test.go` (new top-level test)

- [ ] **Step 1: Write the failing test**

Append to `internal/pkg/template/workload_test.go`:

```go
func TestNetworkOpts_IPv6Enabled_Field(t *testing.T) {
	opts := NetworkOpts{IPv6Enabled: true}
	require.True(t, opts.IPv6Enabled, "NetworkOpts.IPv6Enabled must be a settable bool field")

	zero := NetworkOpts{}
	require.False(t, zero.IPv6Enabled, "zero-value NetworkOpts.IPv6Enabled must be false")
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./internal/pkg/template/ -run TestNetworkOpts_IPv6Enabled_Field -v
```

Expected: FAIL — `unknown field IPv6Enabled in struct literal of type NetworkOpts`.

- [ ] **Step 3: Add the field**

In `internal/pkg/template/workload.go`, extend the `NetworkOpts` struct:

```go
// NetworkOpts holds AWS networking configuration for the workloads.
type NetworkOpts struct {
	SecurityGroups []SecurityGroup
	AssignPublicIP string
	// SubnetsType and SubnetIDs are mutually exclusive. They won't be set together.
	SubnetsType              string
	SubnetIDs                []string
	DenyDefaultSecurityGroup bool
	// IPv6Enabled controls whether the rendered AwsvpcConfiguration sets
	// AssignIpv6Address: ENABLED. It is derived from the env manifest's
	// network.vpc.ipv6.enabled field at stack-construction time.
	IPv6Enabled bool
}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
go test ./internal/pkg/template/ -run TestNetworkOpts_IPv6Enabled_Field -v
```

Expected: PASS.

- [ ] **Step 5: Run the full template package tests**

```bash
go test ./internal/pkg/template/...
```

Expected: all existing tests still pass — adding a zero-valued bool field is byte-identity-preserving.

- [ ] **Step 6: Commit**

```bash
git add internal/pkg/template/workload.go internal/pkg/template/workload_test.go
git commit -m "Template: add NetworkOpts.IPv6Enabled field

Zero-valued bool addition; existing template render is byte-identical.
Populated by the workload stack layer from the env manifest in a
follow-up commit."
```

---

## Task 3: Thread `ipv6Enabled` through `convertNetworkConfig`

**Files:**
- Modify: `internal/pkg/deploy/cloudformation/stack/transformers.go:1057` (function signature + body)
- Modify call sites (5 total, `go build` will flag them):
  - `internal/pkg/deploy/cloudformation/stack/lb_web_svc.go:228`
  - `internal/pkg/deploy/cloudformation/stack/backend_svc.go:192`
  - `internal/pkg/deploy/cloudformation/stack/worker_svc.go:153`
  - `internal/pkg/deploy/cloudformation/stack/scheduled_job.go:192`
- Test: `internal/pkg/deploy/cloudformation/stack/transformers_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/pkg/deploy/cloudformation/stack/transformers_test.go`:

```go
func TestConvertNetworkConfig_IPv6Enabled(t *testing.T) {
	mft := manifest.NetworkConfig{}
	t.Run("ipv6Enabled=true is stamped onto NetworkOpts", func(t *testing.T) {
		got := convertNetworkConfig(mft, true)
		require.True(t, got.IPv6Enabled)
	})
	t.Run("ipv6Enabled=false leaves NetworkOpts.IPv6Enabled false", func(t *testing.T) {
		got := convertNetworkConfig(mft, false)
		require.False(t, got.IPv6Enabled)
	})
	t.Run("empty NetworkConfig with ipv6Enabled=true still sets IPv6Enabled", func(t *testing.T) {
		got := convertNetworkConfig(manifest.NetworkConfig{}, true)
		require.True(t, got.IPv6Enabled)
		// The empty-config branch also sets defaults — those are unchanged.
		require.Equal(t, template.EnablePublicIP, got.AssignPublicIP)
		require.Equal(t, template.PublicSubnetsPlacement, got.SubnetsType)
	})
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./internal/pkg/deploy/cloudformation/stack/ -run TestConvertNetworkConfig_IPv6Enabled -v
```

Expected: FAIL — compilation error, `too few arguments in call to convertNetworkConfig`.

- [ ] **Step 3: Update `convertNetworkConfig` signature and body**

In `internal/pkg/deploy/cloudformation/stack/transformers.go:1057`:

```go
func convertNetworkConfig(network manifest.NetworkConfig, ipv6Enabled bool) template.NetworkOpts {
	if network.IsEmpty() {
		return template.NetworkOpts{
			AssignPublicIP: template.EnablePublicIP,
			SubnetsType:    template.PublicSubnetsPlacement,
			IPv6Enabled:    ipv6Enabled,
		}
	}
	opts := template.NetworkOpts{
		AssignPublicIP: template.EnablePublicIP,
		SubnetsType:    template.PublicSubnetsPlacement,
		IPv6Enabled:    ipv6Enabled,
	}
	// ... rest of function unchanged ...
```

Leave everything after the `opts := ...` initializer untouched.

- [ ] **Step 4: Update the four ECS call sites to pass `false` for now**

Context: we pass `false` in this task; Task 4 replaces `false` with the real precomputed flag. Splitting keeps each commit tiny.

In `internal/pkg/deploy/cloudformation/stack/backend_svc.go:192`:

```go
Network:                 convertNetworkConfig(s.manifest.Network, false),
```

In `internal/pkg/deploy/cloudformation/stack/lb_web_svc.go:228`:

```go
Network:                 convertNetworkConfig(s.manifest.Network, false),
```

In `internal/pkg/deploy/cloudformation/stack/worker_svc.go:153`:

```go
Network:                  convertNetworkConfig(s.manifest.Network, false),
```

In `internal/pkg/deploy/cloudformation/stack/scheduled_job.go:192`:

```go
Network:                  convertNetworkConfig(j.manifest.Network, false),
```

- [ ] **Step 5: Run the full stack package tests**

```bash
go test ./internal/pkg/deploy/cloudformation/stack/...
```

Expected: all existing tests PASS. The new `TestConvertNetworkConfig_IPv6Enabled` subtests PASS. Zero-value `false` preserves byte-identity on existing goldens.

- [ ] **Step 6: Commit**

```bash
git add internal/pkg/deploy/cloudformation/stack/
git commit -m "Stack: thread ipv6Enabled through convertNetworkConfig

Signature-only change. Every call site passes false for now; the next
commit wires the real env-manifest value in per workload type."
```

---

## Task 4: Read `envManifest.IPv6Enabled()` on each ECS stack and pass it into `convertNetworkConfig`

**Files:**
- Modify: `internal/pkg/deploy/cloudformation/stack/backend_svc.go` (struct + constructor + `Template()`)
- Modify: `internal/pkg/deploy/cloudformation/stack/lb_web_svc.go`
- Modify: `internal/pkg/deploy/cloudformation/stack/worker_svc.go`
- Modify: `internal/pkg/deploy/cloudformation/stack/scheduled_job.go`
- Test: the existing `*_test.go` in the same package already exercise each constructor with `EnvManifest`; we extend one of the IPv6-off happy-path tests and add an IPv6-on case.

- [ ] **Step 1: Add a test helper for constructing IPv6-enabled env manifests**

Context: the `ipv6VPCConfig`, `environmentVPCConfig`, and `environmentNetworkConfig` types in `internal/pkg/manifest/env.go:117,121,133` are **unexported**, so tests outside the `manifest` package cannot construct them inline. The existing pattern (see `internal/pkg/deploy/cloudformation/stack/env_integration_test.go:272,497`) is to round-trip through YAML. We'll add a single shared helper in the stack package's test code once, then reuse it across all four stack-type tests.

Create `internal/pkg/deploy/cloudformation/stack/ipv6_test_helpers_test.go`:

```go
// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package stack

import (
	"testing"

	"github.com/aws/copilot-cli/internal/pkg/manifest"
	"github.com/stretchr/testify/require"
)

// mustEnvManifestWithIPv6 returns a *manifest.Environment whose
// network.vpc.ipv6.enabled field is set to the given value via a
// YAML round-trip (the IPv6 config struct is unexported).
func mustEnvManifestWithIPv6(t *testing.T, enabled bool) *manifest.Environment {
	t.Helper()
	var yml string
	if enabled {
		yml = `name: test
type: Environment
network:
  vpc:
    ipv6:
      enabled: true
`
	} else {
		yml = `name: test
type: Environment
`
	}
	env, err := manifest.UnmarshalEnvironment([]byte(yml))
	require.NoError(t, err)
	return env
}
```

- [ ] **Step 2: Write the failing test for BackendService**

Append to `internal/pkg/deploy/cloudformation/stack/backend_svc_test.go` (below the existing `TestBackendService_Template` cases):

```go
func TestBackendService_envIPv6EnabledPropagatesToNetworkOpts(t *testing.T) {
	conf := BackendServiceConfig{
		App:           &config.Application{Name: "mockApp"},
		EnvManifest:   mustEnvManifestWithIPv6(t, true),
		Manifest:      sampleBackendManifest(t), // existing helper; if absent, see note below
		RuntimeConfig: RuntimeConfig{Version: "v1.29.0"},
	}
	got, err := NewBackendService(conf)
	require.NoError(t, err)
	require.True(t, got.envIPv6Enabled, "BackendService.envIPv6Enabled must be set from envManifest.Network.VPC.IPv6Enabled()")
}
```

If `sampleBackendManifest` does not already exist in the test file, grep for the nearest existing BackendService constructor call — e.g. `grep -n "manifest.NewBackendService\|Manifest:\s*&manifest.BackendService" internal/pkg/deploy/cloudformation/stack/backend_svc_test.go` — and either:
- Extract its builder into `sampleBackendManifest(t)` as a new helper, or
- Inline the same builder literal into this test.

Either path is fine; pick whichever minimizes churn to existing tests.

- [ ] **Step 3: Run test to verify it fails**

```bash
go test ./internal/pkg/deploy/cloudformation/stack/ -run TestBackendService_envIPv6EnabledPropagatesToNetworkOpts -v
```

Expected: FAIL — `got.envIPv6Enabled undefined`.

- [ ] **Step 4: Add the `envIPv6Enabled` field to `BackendService`**

In `internal/pkg/deploy/cloudformation/stack/backend_svc.go:22`:

```go
type BackendService struct {
	*ecsWkld
	manifest        *manifest.BackendService
	httpsEnabled    bool
	albEnabled      bool
	envIPv6Enabled  bool
	importedALB     *elbv2.LoadBalancer

	parser backendSvcReadParser
}
```

- [ ] **Step 5: Populate the field in `NewBackendService`**

In the body of `NewBackendService` (around line 56+ in `backend_svc.go`), alongside the existing precomputed flags (`httpsEnabled`, `albEnabled`), add:

```go
envIPv6Enabled := conf.EnvManifest.Network.VPC.IPv6Enabled()
```

and stamp it into the returned struct:

```go
return &BackendService{
	ecsWkld:        /* ... existing ... */,
	manifest:       conf.Manifest,
	httpsEnabled:   httpsEnabled,
	albEnabled:     albEnabled,
	envIPv6Enabled: envIPv6Enabled,
	// ... etc ...
}, nil
```

- [ ] **Step 6: Pass the field into `convertNetworkConfig` during `Template()`**

In the `Template()` call site around line 192 of `backend_svc.go`:

```go
Network: convertNetworkConfig(s.manifest.Network, s.envIPv6Enabled),
```

- [ ] **Step 7: Run the new test to verify it passes**

```bash
go test ./internal/pkg/deploy/cloudformation/stack/ -run TestBackendService_envIPv6EnabledPropagatesToNetworkOpts -v
```

Expected: PASS.

- [ ] **Step 8: Apply the identical change to `LoadBalancedWebService` (`lb_web_svc.go`)**

- Add test `TestLoadBalancedWebService_envIPv6EnabledPropagatesToNetworkOpts` in `lb_web_svc_test.go`. Use `mustEnvManifestWithIPv6(t, true)` for the env; construct the LBWS config using the nearest existing LBWS test as the pattern (grep `LoadBalancedWebServiceConfig{` in that file).
- Add `envIPv6Enabled bool` field to the `LoadBalancedWebService` struct.
- In `NewLoadBalancedWebService`, alongside existing precomputed flags, set `envIPv6Enabled := conf.EnvManifest.Network.VPC.IPv6Enabled()` and stamp it into the returned struct.
- In the `Template()` call site at `lb_web_svc.go:228`, change `convertNetworkConfig(s.manifest.Network, false)` → `convertNetworkConfig(s.manifest.Network, s.envIPv6Enabled)`.
- Run the new test; expect PASS.

- [ ] **Step 9: Apply the identical change to `WorkerService` (`worker_svc.go`)**

- Add test `TestWorkerService_envIPv6EnabledPropagatesToNetworkOpts` in `worker_svc_test.go`. Mirror the Backend test (swap `NewBackendService` → `NewWorkerService`, `BackendServiceConfig` → `WorkerServiceConfig`).
- Add `envIPv6Enabled bool` field to the `WorkerService` struct.
- In `NewWorkerService`, set `envIPv6Enabled := conf.EnvManifest.Network.VPC.IPv6Enabled()` and stamp it.
- In the `Template()` call site at `worker_svc.go:153`, change the call to `convertNetworkConfig(s.manifest.Network, s.envIPv6Enabled)`.
- Run the new test; expect PASS.

- [ ] **Step 10: Apply the identical change to `ScheduledJob` (`scheduled_job.go`)**

- Add test `TestScheduledJob_envIPv6EnabledPropagatesToNetworkOpts` in `scheduled_job_test.go`. Mirror the Backend test (swap in `NewScheduledJob` / `ScheduledJobConfig`). **Note:** the receiver inside `Template()` is `j`, not `s`.
- Add `envIPv6Enabled bool` field to the `ScheduledJob` struct.
- In `NewScheduledJob`, set `envIPv6Enabled := conf.EnvManifest.Network.VPC.IPv6Enabled()` and stamp it.
- In the `Template()` call site at `scheduled_job.go:192`, change the call to `convertNetworkConfig(j.manifest.Network, j.envIPv6Enabled)`.
- Run the new test; expect PASS.

- [ ] **Step 11: Run the full stack package tests**

```bash
go test ./internal/pkg/deploy/cloudformation/stack/...
```

Expected: all tests pass. Existing goldens are unaffected (field is `false` for existing test fixtures whose `EnvManifest` has no IPv6).

- [ ] **Step 12: Commit**

```bash
git add internal/pkg/deploy/cloudformation/stack/
git commit -m "Stack: precompute envIPv6Enabled on each ECS stack

Each of BackendService, LoadBalancedWebService, WorkerService, and
ScheduledJob reads envManifest.Network.VPC.IPv6Enabled() in its
constructor, stores it as a struct field, and hands it to
convertNetworkConfig at Template() time. Mirrors the existing
precomputed-flag pattern (httpsEnabled, albEnabled)."
```

---

## Task 5: Render `AssignIpv6Address: ENABLED` on ECS service workloads

**Files:**
- Modify: `internal/pkg/template/templates/workloads/partials/cf/service-base-properties.yml`
- Test: `internal/pkg/template/template_integration_test.go` has rendering tests — we extend them.

Context: `service-base-properties.yml:91-115` contains the `AwsvpcConfiguration` block used by LBWS, BackendService, WorkerService. `ScheduledJob` has its own template handled in Task 6.

- [ ] **Step 1: Write the failing render test**

Add to `internal/pkg/template/template_integration_test.go` a new table entry in the existing service-rendering test. Locate `TestParsePartials_AwsvpcConfiguration` (or the nearest existing partial-rendering test — grep for `AwsvpcConfiguration` in this file) and add:

```go
{
	name: "IPv6 enabled, Linux workload: AssignIpv6Address is rendered",
	input: template.NetworkOpts{
		AssignPublicIP: template.EnablePublicIP,
		SubnetsType:    template.PublicSubnetsPlacement,
		IPv6Enabled:    true,
	},
	platform: template.RuntimePlatformOpts{OS: template.OSLinux, Arch: template.ArchX86},
	wantedSubstring: "AssignIpv6Address: ENABLED",
},
{
	name: "IPv6 enabled, Windows workload: AssignIpv6Address is NOT rendered",
	input: template.NetworkOpts{
		AssignPublicIP: template.EnablePublicIP,
		SubnetsType:    template.PublicSubnetsPlacement,
		IPv6Enabled:    true,
	},
	platform: template.RuntimePlatformOpts{OS: template.OSWindowsServer2019Full, Arch: template.ArchX86},
	wantedNotSubstring: "AssignIpv6Address",
},
{
	name: "IPv6 disabled: AssignIpv6Address is NOT rendered",
	input: template.NetworkOpts{
		AssignPublicIP: template.EnablePublicIP,
		SubnetsType:    template.PublicSubnetsPlacement,
		IPv6Enabled:    false,
	},
	platform: template.RuntimePlatformOpts{OS: template.OSLinux, Arch: template.ArchX86},
	wantedNotSubstring: "AssignIpv6Address",
},
```

If the existing test struct does not have `platform` / `wantedSubstring` / `wantedNotSubstring` fields, extend the struct with those; alternatively write a new standalone `TestParsePartials_AwsvpcConfiguration_IPv6` with the same shape. Prefer extending the existing test table — DRY.

- [ ] **Step 2: Run the test to verify it fails**

```bash
go test -tags=integration ./internal/pkg/template/ -run TestParsePartials -v
```

Expected: FAIL — rendered output does not contain `AssignIpv6Address: ENABLED`.

- [ ] **Step 3: Update the partial**

In `internal/pkg/template/templates/workloads/partials/cf/service-base-properties.yml`, locate the `AwsvpcConfiguration` block (around line 91). Insert the guarded line immediately after `AssignPublicIp`:

```yaml
NetworkConfiguration:
  AwsvpcConfiguration:
    AssignPublicIp: {{.Network.AssignPublicIP}}
    {{- if and .Network.IPv6Enabled (not .Platform.IsWindows) }}
    AssignIpv6Address: ENABLED
    {{- end }}
    Subnets:
      # ... existing ...
    SecurityGroups:
      # ... existing ...
```

Verify by eye: no trailing whitespace, consistent 4-space indentation for the property, `{{-` strips the newline above so v4-only envs produce the exact same byte sequence as before.

- [ ] **Step 4: Run the test to verify it passes**

```bash
go test -tags=integration ./internal/pkg/template/ -run TestParsePartials -v
```

Expected: PASS for all three new subtests.

- [ ] **Step 5: Run the full template render regression**

```bash
go test -tags=integration ./internal/pkg/template/...
```

Expected: every pre-existing golden render unchanged.

- [ ] **Step 6: Commit**

```bash
git add internal/pkg/template/templates/workloads/partials/cf/service-base-properties.yml internal/pkg/template/template_integration_test.go
git commit -m "Workload CFN: render AssignIpv6Address for IPv6-enabled Linux tasks

Single guarded line on the shared service-base-properties partial.
Gated on NetworkOpts.IPv6Enabled AND not Platform.IsWindows; v4-only
and Windows envs render byte-identically to today."
```

---

## Task 6: Render `AssignIpv6Address: ENABLED` on ScheduledJob

**Files:**
- Modify: `internal/pkg/template/templates/workloads/scheduled-job/cf.yml`
- Test: `internal/pkg/deploy/cloudformation/stack/scheduled_job_test.go`

Context: ScheduledJob uses `AWS::Events::Rule` with an ECS target. The `EcsParameters.NetworkConfiguration.AwsVpcConfiguration` block is the insertion point.

- [ ] **Step 1: Confirm the EventBridge Rule target schema accepts `AssignIpv6Address`**

```bash
grep -n "AwsVpcConfiguration\|NetworkConfiguration" internal/pkg/template/templates/workloads/scheduled-job/cf.yml
```

Locate the `AwsvpcConfiguration` block within `EcsParameters`. Document the line number as a comment in the commit message.

If `AssignIpv6Address` is not a recognized property on `AWS::Events::Rule.Target.EcsParameters.NetworkConfiguration.AwsVpcConfiguration` (verify against AWS CFN docs or the vendored CFN spec if one exists in this repo — `grep -l "AWS::Events::Rule" -r`), **stop**: fall back to the scope-reduction option from the spec (ECS services only; log Jobs for sub-project #5) and skip the rest of this task. Update `docs/superpowers/specs/2026-04-21-workload-ipv6-egress-design.md` accordingly and proceed to Task 7.

Current AWS documentation confirms `AssignIpv6Address` is supported on this path since 2023; the check above is a belt-and-suspenders guard.

- [ ] **Step 2: Write the failing render test**

Append to `internal/pkg/deploy/cloudformation/stack/scheduled_job_test.go` (near the existing `TestScheduledJob_Template` table):

```go
{
	name: "IPv6 env renders AssignIpv6Address on EcsParameters",
	envManifestWithIPv6: true, // new field; see struct change below
	wantedSubstring:     "AssignIpv6Address: ENABLED",
},
```

If the existing test struct does not support this assertion pattern, add a new standalone test:

```go
func TestScheduledJob_Template_IPv6Enabled_RendersAssignIpv6Address(t *testing.T) {
	enabled := true
	conf := ScheduledJobConfig{
		App: &config.Application{Name: "mockApp"},
		EnvManifest: &manifest.Environment{
			EnvironmentConfig: manifest.EnvironmentConfig{
				Network: manifest.EnvironmentNetworkConfig{
					VPC: manifest.EnvironmentVPCConfig{
						IPv6: &manifest.IPv6VPCConfig{Enabled: &enabled},
					},
				},
			},
		},
		Manifest:      sampleScheduledJobManifest(t),
		RuntimeConfig: RuntimeConfig{Version: "v1.29.0"},
	}
	job, err := NewScheduledJob(conf)
	require.NoError(t, err)
	tpl, err := job.Template()
	require.NoError(t, err)
	require.Contains(t, tpl, "AssignIpv6Address: ENABLED")
}
```

Use whichever scheduled-job manifest builder already exists in the test file (grep `func sample` or `manifest.NewScheduledJob` in the same file).

- [ ] **Step 3: Run the test to verify it fails**

```bash
go test ./internal/pkg/deploy/cloudformation/stack/ -run TestScheduledJob_Template_IPv6 -v
```

Expected: FAIL — template does not contain `AssignIpv6Address`.

- [ ] **Step 4: Update the scheduled-job template**

In `internal/pkg/template/templates/workloads/scheduled-job/cf.yml`, inside the ECS target's `AwsvpcConfiguration` block, add:

```yaml
AwsvpcConfiguration:
  AssignPublicIp: {{.Network.AssignPublicIP}}
  {{- if and .Network.IPv6Enabled (not .Platform.IsWindows) }}
  AssignIpv6Address: ENABLED
  {{- end }}
  Subnets:
    # ... existing ...
  SecurityGroups:
    # ... existing ...
```

(Jobs are Linux-only today, so the `IsWindows` guard is inert. Kept for consistency.)

- [ ] **Step 5: Run the test to verify it passes**

```bash
go test ./internal/pkg/deploy/cloudformation/stack/ -run TestScheduledJob_Template_IPv6 -v
```

Expected: PASS.

- [ ] **Step 6: Run the full stack package tests**

```bash
go test ./internal/pkg/deploy/cloudformation/stack/...
```

Expected: all tests pass, all existing scheduled-job goldens unchanged.

- [ ] **Step 7: Commit**

```bash
git add internal/pkg/template/templates/workloads/scheduled-job/cf.yml internal/pkg/deploy/cloudformation/stack/scheduled_job_test.go
git commit -m "ScheduledJob CFN: render AssignIpv6Address on IPv6-enabled envs

Inserts the same guarded line into the EventBridge Rule's
EcsParameters.AwsvpcConfiguration block. Byte-identical for
non-IPv6 envs."
```

---

## Task 7: Env stack — add `EnvironmentSecurityGroupEgressIPv6` resource

**Files:**
- Modify: `internal/pkg/template/templates/environment/cf.yml` (just after the `EnvironmentSecurityGroup` resource definition around line 215)
- Test/Golden: `internal/pkg/deploy/cloudformation/stack/testdata/environments/template-with-ipv6-enabled.yml` (extend the existing IPv6-on golden)

- [ ] **Step 1: Identify the insertion point and golden**

```bash
grep -n "EnvironmentSecurityGroup" internal/pkg/template/templates/environment/cf.yml
grep -n "EnvironmentSecurityGroup" internal/pkg/deploy/cloudformation/stack/testdata/environments/template-with-ipv6-enabled.yml
```

Note the line numbers.

- [ ] **Step 2: Add the expected resource to the golden**

In `template-with-ipv6-enabled.yml`, immediately after the `EnvironmentSecurityGroup:` resource block (copy format from surrounding resources exactly, including any `Metadata: 'aws:copilot:description':` line if used):

```yaml
  EnvironmentSecurityGroupEgressIPv6:
    Type: AWS::EC2::SecurityGroupEgress
    Properties:
      GroupId: !Ref EnvironmentSecurityGroup
      IpProtocol: -1
      CidrIpv6: ::/0
      Description: Allow all IPv6 outbound traffic
```

- [ ] **Step 3: Run the env golden test to verify it fails**

```bash
go test -tags=integration ./internal/pkg/deploy/cloudformation/stack/ -run TestEnvStack -v
```

Expected: FAIL — rendered template does not contain `EnvironmentSecurityGroupEgressIPv6`.

If the env golden test name differs, locate it:

```bash
grep -nE "template-with-ipv6-enabled|TestEnv" internal/pkg/deploy/cloudformation/stack/*_integration_test.go
```

- [ ] **Step 4: Add the guarded resource to the env template**

In `internal/pkg/template/templates/environment/cf.yml`, locate the `EnvironmentSecurityGroup:` resource block. Immediately after its closing brace / end of the block (still inside `Resources:`), add:

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

Match the existing indentation convention in this file (resources are indented 2 spaces inside `Resources:`).

- [ ] **Step 5: Run the env golden test to verify it passes**

```bash
go test -tags=integration ./internal/pkg/deploy/cloudformation/stack/ -run TestEnvStack -v
```

Expected: the IPv6-on golden now matches. All other env goldens (IPv6-off) MUST still be byte-identical — the `{{- if }}` guard strips the entire block including leading newline.

- [ ] **Step 6: Run the full env stack test suite**

```bash
go test -tags=integration ./internal/pkg/deploy/cloudformation/stack/...
```

Expected: every env golden unchanged except `template-with-ipv6-enabled.yml`.

- [ ] **Step 7: Commit**

```bash
git add internal/pkg/template/templates/environment/cf.yml internal/pkg/deploy/cloudformation/stack/testdata/environments/template-with-ipv6-enabled.yml
git commit -m "Env CFN: add standalone ::/0 IPv6 egress rule

Adds EnvironmentSecurityGroupEgressIPv6 as a standalone resource
(AWS::EC2::SecurityGroupEgress), not an inline SecurityGroupEgress on
the SG itself — this preserves the CFN-default IPv4-allow-all rule
and keeps every v4-only env byte-identical. Gated on VPCConfig.IPv6Enabled."
```

---

## Task 8: New workload goldens for IPv6-enabled envs (one per workload type)

**Files:**
- Create: `internal/pkg/deploy/cloudformation/stack/testdata/workloads/svc-ipv6-test.stack.yml`
- Create: `internal/pkg/deploy/cloudformation/stack/testdata/workloads/svc-ipv6-test.params.json`
- Create: `internal/pkg/deploy/cloudformation/stack/testdata/workloads/backend-ipv6-test.stack.yml` (+ params)
- Create: `internal/pkg/deploy/cloudformation/stack/testdata/workloads/worker-ipv6-test.stack.yml` (+ params)
- Create: `internal/pkg/deploy/cloudformation/stack/testdata/workloads/job-ipv6-test.stack.yml` (+ params)
- Modify: the corresponding `*_integration_test.go` files to register the IPv6 test case

Context: `ls internal/pkg/deploy/cloudformation/stack/testdata/workloads/` shows the convention — `<type>-<envname>.stack.yml` + `.params.json` pair per scenario. We add an `ipv6-test` scenario per workload type.

- [ ] **Step 1: Understand the existing integration test loop**

```bash
grep -n "svc-test.stack.yml\|backend\b" internal/pkg/deploy/cloudformation/stack/*_integration_test.go
```

Identify the table-driven integration test that iterates over the goldens (for LBWS: `lb_web_service_integration_test.go`, already referencing the existing `svc-test` fixture — the test will be a cousin of that).

- [ ] **Step 2: Write the failing test entry — LBWS first**

In `internal/pkg/deploy/cloudformation/stack/lb_web_service_integration_test.go`, add a new test case in the existing table (or, if the file has a single `func Test...`, add a second function with the IPv6 env scenario). Minimal sketch:

```go
{
	name:          "load balanced web service in ipv6 env",
	envManifest:   mustParseEnvManifestFile(t, "testdata/environments/env-ipv6-manifest.yml"),
	manifest:      "testdata/workloads/svc-manifest.yml",
	goldenStack:   "testdata/workloads/svc-ipv6-test.stack.yml",
	goldenParams:  "testdata/workloads/svc-ipv6-test.params.json",
},
```

If no `env-ipv6-manifest.yml` fixture exists, create a minimal one next to the existing env manifests:

```yaml
# internal/pkg/deploy/cloudformation/stack/testdata/environments/env-ipv6-manifest.yml
name: test
type: Environment
network:
  vpc:
    ipv6:
      enabled: true
```

If the existing test already uses a raw `manifest.Environment` struct rather than a YAML file, mirror that style — grep for `envConfig := &manifest.Environment{` in the existing LBWS integration test and set its IPv6 field directly.

- [ ] **Step 3: Run the test to verify it fails**

```bash
go test -tags=integration ./internal/pkg/deploy/cloudformation/stack/ -run TestLoadBalancedWebService -v
```

Expected: FAIL — golden file missing.

- [ ] **Step 4: Generate the golden**

The existing tests typically write the expected golden once and diff thereafter. Follow the per-test convention (e.g., a `-update` flag or a one-off `t.Log` of the rendered template) — grep for `UPDATE_GOLDEN` or `-update` in this package:

```bash
grep -rnE "UPDATE_GOLDEN|-update|update golden" internal/pkg/deploy/cloudformation/stack/
```

If a flag exists, run with it and the golden is populated. If not, print the rendered template with `t.Log` temporarily, copy into the new file, re-run to confirm the diff is clean.

- [ ] **Step 5: Verify the new golden contains `AssignIpv6Address: ENABLED`**

```bash
grep -c "AssignIpv6Address: ENABLED" internal/pkg/deploy/cloudformation/stack/testdata/workloads/svc-ipv6-test.stack.yml
```

Expected: 1 (or more, if the service has multiple containers with AwsvpcConfiguration blocks).

- [ ] **Step 6: Run the full integration suite**

```bash
go test -tags=integration ./internal/pkg/deploy/cloudformation/stack/...
```

Expected: new test passes; every pre-existing golden unchanged.

- [ ] **Step 7: Commit**

```bash
git add internal/pkg/deploy/cloudformation/stack/testdata/ internal/pkg/deploy/cloudformation/stack/lb_web_service_integration_test.go
git commit -m "Stack goldens: LBWS rendered in IPv6-enabled env

New golden svc-ipv6-test.stack.yml asserts AssignIpv6Address: ENABLED
appears on the AwsvpcConfiguration. All existing workload goldens
remain byte-identical."
```

- [ ] **Step 8: Apply the same pattern to BackendService**

- Test file to modify: `internal/pkg/deploy/cloudformation/stack/backend_svc_integration_test.go`
- New goldens to create: `testdata/workloads/backend-ipv6-test.stack.yml`, `testdata/workloads/backend-ipv6-test.params.json`
- Test case name: `"backend service in ipv6 env"`
- Follow Steps 2-7 above, substituting the correct manifest file and test function name. Commit separately: `git commit -m "Stack goldens: BackendService rendered in IPv6-enabled env"`.

- [ ] **Step 9: Apply the same pattern to WorkerService**

- Test file: `internal/pkg/deploy/cloudformation/stack/worker_svc_integration_test.go` (create if this file does not exist; use the existing non-integration test as the pattern).
- New goldens: `testdata/workloads/worker-ipv6-test.stack.yml`, `testdata/workloads/worker-ipv6-test.params.json`
- Test case name: `"worker service in ipv6 env"`
- Commit: `git commit -m "Stack goldens: WorkerService rendered in IPv6-enabled env"`.

- [ ] **Step 10: Apply the same pattern to ScheduledJob**

- Test file: `internal/pkg/deploy/cloudformation/stack/scheduled_job_integration_test.go` (create if missing)
- New goldens: `testdata/workloads/job-ipv6-test.stack.yml`, `testdata/workloads/job-ipv6-test.params.json`
- Test case name: `"scheduled job in ipv6 env"`
- Commit: `git commit -m "Stack goldens: ScheduledJob rendered in IPv6-enabled env"`.

Four commits total for this task, including the LBWS one above.

---

## Task 9: Windows + IPv6 warning at deploy time

**Files:**
- Modify: `internal/pkg/cli/deploy/workload.go` (insert warning emission in `newWorkloadDeployer` or a helper called during deploy)
- Test: `internal/pkg/cli/deploy/workload_test.go`

Context: `workloadDeployer.envConfig` is loaded at line 301 of `workload.go`. The workload manifest type is known (via `in.Mft`). The warning should fire once per deploy, on stderr, using `log.Warningf` (see `internal/pkg/cli/svc_deploy.go:250` for the existing idiom).

- [ ] **Step 1: Verify the `log.Warningf` output sink**

```bash
grep -n "DiagnosticWriter\|ErrWriter\|log.OutputWriter\|log.Terminal" internal/pkg/term/log/log.go
```

Read the file around any match to confirm the public variable that tests can swap to capture stderr output. Use that variable name (likely `log.DiagnosticWriter` or similar) in Step 2.

- [ ] **Step 2: Write the failing test**

Construct the IPv6-enabled env manifest via YAML round-trip (the `ipv6VPCConfig` type is unexported — inline struct literals do not compile outside the `manifest` package). Append to `internal/pkg/cli/deploy/workload_test.go`:

```go
func TestWarnIfWindowsInIPv6Env(t *testing.T) {
	envIPv6YAML := []byte(`name: test
type: Environment
network:
  vpc:
    ipv6:
      enabled: true
`)
	envV4YAML := []byte(`name: test
type: Environment
`)
	envIPv6, err := manifest.UnmarshalEnvironment(envIPv6YAML)
	require.NoError(t, err)
	envV4, err := manifest.UnmarshalEnvironment(envV4YAML)
	require.NoError(t, err)

	testCases := map[string]struct {
		env       *manifest.Environment
		isWindows bool
		wantWarn  bool
	}{
		"linux in ipv6 env → no warning": {env: envIPv6, isWindows: false, wantWarn: false},
		"windows in ipv6 env → warning":  {env: envIPv6, isWindows: true, wantWarn: true},
		"windows in v4 env → no warning": {env: envV4, isWindows: true, wantWarn: false},
		"linux in v4 env → no warning":   {env: envV4, isWindows: false, wantWarn: false},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			old := log.DiagnosticWriter // replace with the variable name confirmed in Step 1
			log.DiagnosticWriter = &buf
			defer func() { log.DiagnosticWriter = old }()

			warnIfWindowsInIPv6Env("myservice", tc.env, tc.isWindows)

			if tc.wantWarn {
				require.Contains(t, buf.String(), "IPv6 is not enabled for myservice")
				require.Contains(t, buf.String(), "Windows Fargate")
			} else {
				require.Empty(t, buf.String())
			}
		})
	}
}
```

- [ ] **Step 3: Run the test to verify it fails**

```bash
go test ./internal/pkg/cli/deploy/ -run TestWarnIfWindowsInIPv6Env -v
```

Expected: FAIL — `warnIfWindowsInIPv6Env undefined`.

- [ ] **Step 4: Add the helper and call site**

In `internal/pkg/cli/deploy/workload.go`, add below `newWorkloadDeployer`:

```go
// warnIfWindowsInIPv6Env emits a single yellow "Note:" warning to stderr
// when a Windows workload is being deployed into a dual-stack env.
// Windows Fargate does not support IPv6 task networking; the deploy
// proceeds without AssignIpv6Address on the task.
func warnIfWindowsInIPv6Env(name string, envMft *manifest.Environment, isWindows bool) {
	if envMft == nil || !envMft.Network.VPC.IPv6Enabled() {
		return
	}
	if !isWindows {
		return
	}
	log.Warningf("IPv6 is not enabled for %s: Windows Fargate does not support IPv6 task networking.\n", name)
}
```

Wire the call into the workload deployer construction, right after `envConfig` is populated (around line 301). Use a helper that the deployer can call once per deploy. Grep the existing deployer constructors to find a function that already has access to both `in.Name`, `envConfig`, and the workload manifest's platform:

```bash
grep -n "in.Mft\|platform" internal/pkg/cli/deploy/workload.go
```

The workload platform is on the manifest as `manifest.TaskConfig.Platform` (or equivalent); convert via existing helper `manifestIsWindows(mft)` if one exists, else use `template.RuntimePlatformOpts{OS: ..., Arch: ...}.IsWindows()` (the helper added in Task 1). A new tiny helper is acceptable here:

```go
// workloadIsWindows returns true when the applied workload manifest's
// platform OS is one of the supported Windows Server families.
func workloadIsWindows(mft interface{}) bool {
	type platformGetter interface {
		Platform() *manifest.PlatformArgsOrString
	}
	p, ok := mft.(platformGetter)
	if !ok || p.Platform() == nil {
		return false
	}
	osFam, _ := p.Platform().OSFamilyArch()
	return template.RuntimePlatformOpts{OS: osFam}.IsWindows()
}
```

**Verify interface method name:** `grep -n "OSFamilyArch\|OSFamily" internal/pkg/manifest/` to confirm the getter name and signature before pasting. Adjust as needed — the exact pre-existing accessor is what you call.

Call in the deployer's constructor (once per deploy):

```go
warnIfWindowsInIPv6Env(in.Name, envConfig, workloadIsWindows(in.Mft))
```

- [ ] **Step 5: Run the test to verify it passes**

```bash
go test ./internal/pkg/cli/deploy/ -run TestWarnIfWindowsInIPv6Env -v
```

Expected: PASS for all four subtests.

- [ ] **Step 6: Run the full deploy package tests**

```bash
go test ./internal/pkg/cli/deploy/...
```

Expected: all pass. No existing test exercised Windows + IPv6 before; our helper is additive.

- [ ] **Step 7: Commit**

```bash
git add internal/pkg/cli/deploy/workload.go internal/pkg/cli/deploy/workload_test.go
git commit -m "Deploy: warn when Windows workload is deployed into IPv6 env

Windows Fargate does not support IPv6 task networking. The deploy
proceeds (the task stays IPv4-only) and the user sees a single yellow
Note: at deploy time explaining why."
```

---

## Task 10: E2E suite — workload IPv6 reachability

**Files:**
- Create: `e2e/workload_ipv6/workload_ipv6_suite_test.go`
- Create: `e2e/workload_ipv6/workload_ipv6_test.go`
- Create: `e2e/workload_ipv6/testdata/manifests/env-manifest.yml`
- Create: `e2e/workload_ipv6/testdata/manifests/backend-manifest.yml`
- Create: `e2e/workload_ipv6/testdata/backend-probe/Dockerfile`
- Create: `e2e/workload_ipv6/testdata/backend-probe/entrypoint.sh`

Reference existing `e2e/env_ipv6/` for the suite-boilerplate pattern shipped by #1.

- [ ] **Step 1: Scaffold the suite**

Copy the structure of `e2e/env_ipv6/env_ipv6_suite_test.go` into `e2e/workload_ipv6/workload_ipv6_suite_test.go`; rename the Ginkgo spec runner and the app-name generator.

- [ ] **Step 2: Write the probe container**

`e2e/workload_ipv6/testdata/backend-probe/Dockerfile`:

```dockerfile
FROM public.ecr.aws/docker/library/alpine:3.20
RUN apk add --no-cache curl
COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh
ENTRYPOINT ["/entrypoint.sh"]
```

`e2e/workload_ipv6/testdata/backend-probe/entrypoint.sh`:

```sh
#!/bin/sh
set -eu
# Probe IPv6 egress via an AWS dualstack endpoint. Print a single marker
# line that the E2E polls for in CloudWatch Logs.
if curl -6 --max-time 15 --fail -s -o /dev/null "https://dualstack.s3.${AWS_DEFAULT_REGION}.amazonaws.com/"; then
  echo "IPV6_EGRESS_OK"
else
  echo "IPV6_EGRESS_FAIL"
fi
# Keep the task around so E2E can inspect the ENI.
sleep 3600
```

Mark executable on commit: `git update-index --chmod=+x e2e/workload_ipv6/testdata/backend-probe/entrypoint.sh`.

- [ ] **Step 3: Write the env + service manifests**

`e2e/workload_ipv6/testdata/manifests/env-manifest.yml`:

```yaml
name: test
type: Environment
network:
  vpc:
    ipv6:
      enabled: true
```

`e2e/workload_ipv6/testdata/manifests/backend-manifest.yml`:

```yaml
name: probe
type: Backend Service
image:
  build: e2e/workload_ipv6/testdata/backend-probe/Dockerfile
cpu: 256
memory: 512
count: 1
```

- [ ] **Step 4: Write the spec — Scenario 1 (Linux IPv6 reachability)**

`e2e/workload_ipv6/workload_ipv6_test.go` (sketch; match Ginkgo v2 style used in `e2e/env_ipv6`):

```go
var _ = Describe("workload_ipv6", Ordered, func() {
	BeforeAll(func() {
		// copilot app init, env init with testdata/manifests/env-manifest.yml, env deploy
	})
	AfterAll(func() {
		// copilot svc delete -y, env delete -y, app delete -y
	})

	It("deploys a BackendService into the IPv6 env", func() {
		// copilot svc init --name probe --svc-type "Backend Service"
		// copilot svc deploy --name probe --env test
		// Assert exit code 0.
	})

	It("the task ENI has a global IPv6 address", func() {
		// Use aws-sdk-go ECS.DescribeTasks → EC2.DescribeNetworkInterfaces
		// Assert at least one Ipv6Addresses entry on the task's ENI.
	})

	It("the env SG has a ::/0 IPv6 egress rule", func() {
		// EC2.DescribeSecurityGroups on the env SG (tag copilot-application=<app>)
		// Assert one IpPermissionsEgress entry with Ipv6Ranges = [::/0] and IpProtocol = "-1".
	})

	It("the task successfully reaches an AWS dualstack endpoint over IPv6", func() {
		// Poll CloudWatch Logs for the probe's log group for the line IPV6_EGRESS_OK.
		// Time out after 2 minutes.
	})
})
```

Consult `e2e/env_ipv6/env_ipv6_test.go` for the exact helper functions for copilot commands, CFN waiters, and teardown.

- [ ] **Step 5: Add Scenario 2 (non-IPv6 env is unaffected)**

Append a second `Describe` block (or a second Ordered group) that:

1. Deploys a v4-only env (default — no `ipv6` block in the env manifest).
2. Deploys the same probe service.
3. Asserts the rendered CFN stack does NOT contain `AssignIpv6Address` (describe the stack via CFN API, grep the template body).
4. Asserts the task ENI has no global IPv6 address.
5. Tears down.

- [ ] **Step 6: Run the E2E suite locally (optional dry run)**

These are nightly-gated; a local run requires real AWS credentials. If credentials are available:

```bash
cd e2e/workload_ipv6 && ginkgo -v
```

Expected: both scenarios pass. If no credentials, stop here and rely on CI nightly.

- [ ] **Step 7: Commit**

```bash
git add e2e/workload_ipv6/
git commit -m "E2E: workload IPv6 egress scenarios

Scenario 1: Linux BackendService in an IPv6 env gets a global IPv6
address on its task ENI and can curl -6 an AWS dualstack endpoint.
Scenario 2: v4-only env renders no AssignIpv6Address and gets no v6
address. Probe container is a minimal alpine + curl image."
```

---

## Task 11: Documentation & CHANGELOG

**Files:**
- Modify: `site/content/docs/manifest/environment.en.md` (extend the `ipv6` section shipped by #1)
- Modify: `CHANGELOG.md` (if present; if the repo uses `CHANGES.md` or similar, use that)

- [ ] **Step 1: Locate the IPv6 section added by #1**

```bash
grep -n "ipv6" site/content/docs/manifest/environment.en.md
```

- [ ] **Step 2: Extend the doc**

Append to the existing `ipv6.enabled` description:

```markdown
When `ipv6.enabled: true`, every **Linux** workload deployed into the
environment automatically receives a global IPv6 address on its Fargate
task ENI and can reach the IPv6 internet via the EgressOnlyIGW. No
per-service manifest field is required.

Windows Fargate does not support IPv6 task networking. Windows
workloads deployed into a dual-stack environment remain IPv4-only and
print a one-line notice at deploy time.
```

- [ ] **Step 3: Add a CHANGELOG entry**

```bash
ls CHANGELOG* CHANGES* 2>/dev/null
```

Append (use the repo's existing heading format — see prior entries):

```markdown
- Enable IPv6 task networking on workloads deployed into dual-stack
  environments. Fargate tasks now receive a global IPv6 address and can
  reach the IPv6 internet via the EgressOnlyIGW. Windows workloads
  remain IPv4 with a notice at deploy time.
```

- [ ] **Step 4: Commit**

```bash
git add site/content/docs/manifest/environment.en.md CHANGELOG.md
git commit -m "Docs: workload IPv6 egress notes and CHANGELOG entry"
```

---

## Task 12: Final verification & PR prep

- [ ] **Step 1: Run the full Go unit suite**

```bash
make run-unit-test
```

Expected: zero failures.

- [ ] **Step 2: Run the stack integration suite**

```bash
go test -tags=integration ./internal/pkg/deploy/cloudformation/stack/...
```

Expected: zero failures; new IPv6 goldens match; every pre-existing golden byte-identical.

- [ ] **Step 3: Run the race-check suite**

```bash
make test-race
```

Expected: zero failures.

- [ ] **Step 4: License-header check**

```bash
./scripts/license.sh .
```

Expected: zero missing headers. Any new `.go`, `.yml`, `.sh`, `.md`, or `Dockerfile` added in Tasks 1-11 must have the Apache 2.0 header if the scanner requires it (check `scripts/license.sh` behavior; some file types are exempt).

- [ ] **Step 5: Golangci-lint**

```bash
golangci-lint run ./...
```

Expected: zero new findings.

- [ ] **Step 6: Regenerate mocks (sanity check — expect no diff)**

```bash
make gen-mocks
git status
```

Expected: no changes. This sub-project adds no new interfaces.

- [ ] **Step 7: Update the roadmap status**

In `docs/superpowers/specs/2026-04-20-ipv6-support-roadmap.md`, flip sub-project #2's status:

```
### #2 Workload-side IPv6 egress — READY TO MERGE
```

and the status-table row:

```
| 2 | Workload IPv6 egress | ✅ READY TO MERGE | ✅ | ✅ | — |
```

(The plan column turns `✅` here; the merge column flips after the actual merge into `feature/ipv6-support`.)

- [ ] **Step 8: Commit the roadmap update**

```bash
git add docs/superpowers/specs/2026-04-20-ipv6-support-roadmap.md
git commit -m "Docs: sub-project #2 plan complete and implementation done"
```

- [ ] **Step 9: Summarize commits for PR description**

```bash
git log --oneline feature/ipv6-support..HEAD
```

Use the output as the basis for the PR description when opening a PR against `feature/ipv6-support`.

---

## Test Inventory (cross-reference)

- Unit: Task 1 (IsWindows 7 cases), Task 2 (NetworkOpts field 2 cases), Task 3 (convertNetworkConfig 3 cases), Task 4 (4 stack types × 1 case each = 4), Task 9 (Windows-warn 4 cases). **Total ≈ 20.**
- Render goldens: Task 5 (3 partial-render cases), Task 6 (1 scheduled-job), Task 7 (1 updated env golden), Task 8 (4 new workload goldens). **Total ≈ 9 new.**
- Local integration: Task 8's integration test cases (4 workload types). **Total ≈ 4.**
- E2E: Task 10 (2 scenarios).
- Custom resource (Jest): 0 (no Lambda touched).

Matches the spec's "Approximate test counts" section within rounding.
