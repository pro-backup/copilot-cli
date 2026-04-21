# Dual-Stack LB Ingress Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the shared Public and Internal ALBs dual-stack (v4 + v6) when the env has `network.vpc.ipv6.enabled: true`, open security-group ingress for IPv6 clients, and emit Route 53 AAAA records alongside existing A records so LoadBalancedWebService and BackendService clients can reach services over IPv6.

**Architecture:** Env IPv6 state continues to flow from `envManifest.Network.VPC.IPv6Enabled()` (shipped by #1) through each workload stack's `envIPv6Enabled bool` field (shipped by #2). We stamp that bool onto a new top-level `template.WorkloadOpts.IPv6Enabled` so the ALB/listener partials can gate AAAA emission. Env template reads the existing `VPCConfig.IPv6Enabled` flag; three SG-ingress resources and an `IpAddressType: dualstack` property are gated on it. A new `isIPv6CIDR` template helper lets the custom-ingress path route user-supplied CIDRs to `CidrIp` (v4) or `CidrIpv6` (v6). Every change is additive — v4-only envs and their workloads render byte-identical CloudFormation.

**Tech Stack:** Go 1.23, `mockgen` + `testify`, CloudFormation YAML via `text/template`, Ginkgo v2 for E2E.

**Reference spec:** `docs/superpowers/specs/2026-04-21-ipv6-lb-ingress-design.md`
**Base branch:** `feature/ipv6-support` (parent — where this plan lives). Implementation branch: `feature/ipv6-lb-ingress` (created in Task 1).

---

## Task 1: Create implementation branch

**Files:** none (git operation).

- [ ] **Step 1: Verify clean tree on parent branch**

```bash
git status
git branch --show-current
```

Expected: clean tree, `feature/ipv6-support` checked out.

- [ ] **Step 2: Create and switch to the implementation branch**

```bash
git checkout -b feature/ipv6-lb-ingress
```

Expected output: `Switched to a new branch 'feature/ipv6-lb-ingress'`.

- [ ] **Step 3: Confirm branch**

```bash
git branch --show-current
```

Expected output: `feature/ipv6-lb-ingress`.

No commit needed — branch creation is the change.

---

## Task 2: Add `IsIPv6CIDR` template helper

**Files:**
- Modify: `internal/pkg/template/template_functions.go` (append after the existing `AddFunc`, currently at line 109)
- Test: `internal/pkg/template/template_functions_test.go` (append at end of file) — OR `internal/pkg/template/env_test.go` if that file is the canonical home for FuncMap helper tests.

Context: #1 added `AddFunc` to `template_functions.go:109`. The env FuncMap is registered in `internal/pkg/template/env.go:294` (`withEnvParsingFuncs`). We follow the same two-step pattern (define function, register in FuncMap) — registration in Task 3.

- [ ] **Step 1: Locate the helper test file**

```bash
ls internal/pkg/template/template_functions_test.go 2>/dev/null || ls internal/pkg/template/env_test.go
```

If `template_functions_test.go` exists, use it. Otherwise use `env_test.go`. The rest of this task assumes `env_test.go` (the file where `AddFunc` tests live).

- [ ] **Step 2: Write the failing test**

Append to `internal/pkg/template/env_test.go`:

```go
func TestIsIPv6CIDR(t *testing.T) {
	testCases := map[string]struct {
		in     string
		wanted bool
	}{
		"IPv4 CIDR /32":        {in: "10.0.0.0/32", wanted: false},
		"IPv4 CIDR /8":         {in: "10.0.0.0/8", wanted: false},
		"IPv4 default route":   {in: "0.0.0.0/0", wanted: false},
		"IPv6 CIDR /128":       {in: "2001:db8::1/128", wanted: true},
		"IPv6 CIDR /32":        {in: "2001:db8::/32", wanted: true},
		"IPv6 default route":   {in: "::/0", wanted: true},
		"IPv4-mapped IPv6":     {in: "::ffff:10.0.0.0/104", wanted: false},
		"bare IPv4 no mask":    {in: "10.0.0.0", wanted: false},
		"bare IPv6 no mask":    {in: "2001:db8::", wanted: false},
		"empty string":         {in: "", wanted: false},
		"garbage":              {in: "not-a-cidr", wanted: false},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.wanted, IsIPv6CIDR(tc.in))
		})
	}
}
```

Note: `::ffff:10.0.0.0/104` is intentionally classified as IPv4 — Go's `net.IP.To4()` returns non-nil for IPv4-mapped IPv6 addresses, and that's the right semantic for SG rules (an IPv4-mapped address belongs in `CidrIp`, not `CidrIpv6`).

- [ ] **Step 3: Run the test and confirm compile failure**

```bash
go test ./internal/pkg/template/ -run TestIsIPv6CIDR -v
```

Expected: FAIL — `undefined: IsIPv6CIDR`.

- [ ] **Step 4: Implement `IsIPv6CIDR`**

Append to `internal/pkg/template/template_functions.go` (after `AddFunc` at line 111). Also add `"net"` to the import block at the top of the file if not already present.

```go
// IsIPv6CIDR reports whether cidr is a valid IPv6 CIDR block (for use as
// AWS::EC2::SecurityGroupIngress.CidrIpv6). Returns false for v4 CIDRs,
// v4-mapped v6, bare IPs, or malformed input.
func IsIPv6CIDR(cidr string) bool {
	ip, _, err := net.ParseCIDR(cidr)
	if err != nil {
		return false
	}
	return ip.To4() == nil
}
```

- [ ] **Step 5: Run the test and confirm pass**

```bash
go test ./internal/pkg/template/ -run TestIsIPv6CIDR -v
```

Expected: PASS for all 11 cases.

- [ ] **Step 6: Commit**

```bash
git add internal/pkg/template/template_functions.go internal/pkg/template/env_test.go
git commit -m "$(cat <<'EOF'
Template: add IsIPv6CIDR helper

Used by the env template to route user-supplied CIDRs in
http.public.security_groups.ingress to CidrIp (v4) or CidrIpv6 (v6).

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: Register `IsIPv6CIDR` in the env template FuncMap

**Files:**
- Modify: `internal/pkg/template/env.go:294-305` (the `withEnvParsingFuncs` function)
- Test: covered indirectly by Task 9's golden test, but we add a dedicated "the helper is callable from a rendered template" unit test here to catch registration mistakes early.

- [ ] **Step 1: Write the failing test**

Append to `internal/pkg/template/env_test.go`:

```go
func TestEnvTemplate_IsIPv6CIDR_IsRegistered(t *testing.T) {
	// Smoke test: the env template FuncMap must expose `isIPv6CIDR` so
	// partials can call it. If this test fails, the custom-ingress
	// CidrIp/CidrIpv6 split in cf.yml will silently render wrong.
	tpl := New()
	// Use ParseEnv with a minimal data to trigger FuncMap registration,
	// but we want to confirm the function map contains our name. A cheap
	// check: create a standalone template and try to Parse a literal that
	// calls the function.
	inner := `{{ if isIPv6CIDR "::/0" }}yes{{ else }}no{{ end }}`
	parser, err := tpl.parser(inner, withEnvParsingFuncs())
	require.NoError(t, err, "env FuncMap must expose isIPv6CIDR")
	var buf bytes.Buffer
	require.NoError(t, parser.Execute(&buf, nil))
	require.Equal(t, "yes", buf.String())
}
```

If the `parser` helper is unexported or the name differs, fall back to this simpler form:

```go
func TestEnvTemplate_IsIPv6CIDR_IsRegisteredInFuncMap(t *testing.T) {
	// Directly inspect the option's returned FuncMap.
	fm := map[string]any{}
	tt := gotemplate.New("probe").Funcs(map[string]any{}) // shadow import below
	tt = withEnvParsingFuncs()(tt)
	// We cannot introspect the FuncMap directly; instead parse a literal
	// calling the function and verify it doesn't error.
	_, err := tt.Parse(`{{ isIPv6CIDR "::/0" }}`)
	require.NoError(t, err, "env FuncMap must expose isIPv6CIDR")
	_ = fm
}
```

Use whichever variant compiles against the existing helpers in the test file. The canonical form — the one #1 used for its `add` helper — lives near `TestAddFunc` in `env_test.go`; copy that idiom exactly.

- [ ] **Step 2: Run test, confirm it fails**

```bash
go test ./internal/pkg/template/ -run TestEnvTemplate_IsIPv6CIDR -v
```

Expected: FAIL — the template parser rejects `isIPv6CIDR` as an unknown function.

- [ ] **Step 3: Register the helper in `withEnvParsingFuncs`**

Edit `internal/pkg/template/env.go:294-305`:

```go
func withEnvParsingFuncs() ParseOption {
	return func(t *template.Template) *template.Template {
		return t.Funcs(map[string]interface{}{
			"inc":               IncFunc,
			"add":               AddFunc,
			"isIPv6CIDR":        IsIPv6CIDR,
			"fmtSlice":          FmtSliceFunc,
			"quote":             strconv.Quote,
			"truncate":          truncate,
			"bucketNameFromURL": bucketNameFromURL,
			"logicalIDSafe":     StripNonAlphaNumFunc,
		})
	}
}
```

- [ ] **Step 4: Run test, confirm pass**

```bash
go test ./internal/pkg/template/ -run TestEnvTemplate_IsIPv6CIDR -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/pkg/template/env.go internal/pkg/template/env_test.go
git commit -m "$(cat <<'EOF'
Template: register isIPv6CIDR in env FuncMap

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 4: Add `IPv6Enabled` field to `WorkloadOpts`

**Files:**
- Modify: `internal/pkg/template/workload.go:802-877` (the `WorkloadOpts` struct)
- Test: `internal/pkg/template/workload_test.go`

Context: `WorkloadOpts` at `workload.go:802` is the data struct every workload template consumes. `NetworkOpts.IPv6Enabled` (shipped by #2) gates the task-ENI `AssignIpv6Address`. The new top-level `WorkloadOpts.IPv6Enabled` gates AAAA record emission in the listener partials. The two fields carry the same value but serve different partials in different scopes — keeping them separate avoids coupling the listener path to NetworkOpts internals.

- [ ] **Step 1: Write the failing test**

Append to `internal/pkg/template/workload_test.go`:

```go
func TestWorkloadOpts_IPv6EnabledField(t *testing.T) {
	// Regression guard: the field exists on WorkloadOpts at the top level
	// (not nested under Network). Listener partials reference `.IPv6Enabled`.
	opts := WorkloadOpts{IPv6Enabled: true}
	require.True(t, opts.IPv6Enabled)
	opts.IPv6Enabled = false
	require.False(t, opts.IPv6Enabled)
}
```

- [ ] **Step 2: Run test, confirm compile failure**

```bash
go test ./internal/pkg/template/ -run TestWorkloadOpts_IPv6EnabledField -v
```

Expected: FAIL — `unknown field IPv6Enabled in struct literal of type WorkloadOpts`.

- [ ] **Step 3: Add the field**

Edit `internal/pkg/template/workload.go` in the `WorkloadOpts` struct (currently ends around line 877). Add the new field in the "Additional options that are common between **all** workload templates" block, alongside `ALBEnabled` around line 835:

```go
	// Additional options that are common between **all** workload templates.
	// ... existing fields ...
	ALBEnabled               bool
	IPv6Enabled              bool // Set when the env has network.vpc.ipv6.enabled: true. Gates AAAA alias records in https-listener.yml and http-listener.yml.
	CredentialsParameter     string
	PermissionsBoundary      string
```

- [ ] **Step 4: Run test, confirm pass**

```bash
go test ./internal/pkg/template/ -run TestWorkloadOpts_IPv6EnabledField -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/pkg/template/workload.go internal/pkg/template/workload_test.go
git commit -m "$(cat <<'EOF'
Template: add WorkloadOpts.IPv6Enabled field

Gates AAAA alias records in the LBWS/BackendService listener partials.
Separate from NetworkOpts.IPv6Enabled (which gates task-ENI
AssignIpv6Address) to avoid coupling the listener path to NetworkOpts.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 5: Populate `WorkloadOpts.IPv6Enabled` in `LoadBalancedWebService.Template()`

**Files:**
- Modify: `internal/pkg/deploy/cloudformation/stack/lb_web_svc.go` (around line 230 — where `WorkloadOpts` is constructed in `Template()`)
- Test: `internal/pkg/deploy/cloudformation/stack/lb_web_svc_test.go`

Context: `LoadBalancedWebService.envIPv6Enabled` already holds the value (`lb_web_svc.go:37`, populated at `lb_web_svc.go:119`). We pass it to `WorkloadOpts.IPv6Enabled` next to where `ALBEnabled: !s.manifest.HTTPOrBool.Disabled()` is set (around `lb_web_svc.go:237`).

This task writes a test that stays RED until Task 11 lands the AAAA template change. That's the #2 convention (see `TestEnvStack_IPv6ThreadedFromManifest` in `env_integration_test.go:487-521`, added by sub-project #1 and made green by Task 5 of #2's plan). The test proves the wiring works end-to-end when both the Go change (here) and the template change (Task 11) are in place.

- [ ] **Step 1: Write the failing test**

Append to `internal/pkg/deploy/cloudformation/stack/lb_web_svc_test.go`:

```go
// TestLoadBalancedWebService_IPv6PropagatesToListenerTemplate proves the
// WorkloadOpts.IPv6Enabled flag reaches the listener template. INTENTIONALLY
// FAILS until Task 11 lands the AAAA RecordSet in https-listener.yml.
func TestLoadBalancedWebService_IPv6PropagatesToListenerTemplate(t *testing.T) {
	conf := LoadBalancedWebServiceConfig{
		App:                &config.Application{Name: "mockApp"},
		EnvManifest:        mustEnvManifestWithIPv6(t, true),
		ArtifactBucketName: "mockBucket",
		Manifest: manifest.NewLoadBalancedWebService(&manifest.LoadBalancedWebServiceProps{
			WorkloadProps: &manifest.WorkloadProps{
				Name:       "frontend",
				Dockerfile: testDockerfile,
			},
			Path: "/",
			Port: 8080,
		}),
		RuntimeConfig: RuntimeConfig{
			Version:   "v1.29.0",
			Region:    "us-west-2",
			AccountID: "123456789012",
		},
	}
	stk, err := NewLoadBalancedWebService(conf)
	require.NoError(t, err)
	tpl, err := stk.Template()
	require.NoError(t, err)
	// Proof: the flag reached the partial. Only AAAA blocks appear when
	// .IPv6Enabled is true in the listener partial.
	require.Contains(t, tpl, "Type: AAAA",
		"LBWS in IPv6 env must emit AAAA alias records (lands in Task 11)")
}
```

- [ ] **Step 2: Run test — confirm it fails with the intentional message**

```bash
go test ./internal/pkg/deploy/cloudformation/stack/ -run TestLoadBalancedWebService_IPv6PropagatesToListenerTemplate -v
```

Expected: FAIL — `Type: AAAA` not present in rendered template.

- [ ] **Step 3: Wire `IPv6Enabled` into the WorkloadOpts literal**

Edit `internal/pkg/deploy/cloudformation/stack/lb_web_svc.go:230` (inside the `s.parser.ParseLoadBalancedWebService(template.WorkloadOpts{...})` literal). Add `IPv6Enabled: s.envIPv6Enabled,` on its own line, grouped with the other ALB fields around `ALBEnabled`:

```go
		// ALB configs.
		ALBEnabled:  !s.manifest.HTTPOrBool.Disabled(),
		IPv6Enabled: s.envIPv6Enabled,
		GracePeriod: s.convertGracePeriod(),
		ALBListener: albListenerConfig,
		ImportedALB: importedALBConfig,
```

- [ ] **Step 4: Re-run the test; it still fails (the listener template has no AAAA yet)**

```bash
go test ./internal/pkg/deploy/cloudformation/stack/ -run TestLoadBalancedWebService_IPv6PropagatesToListenerTemplate -v
```

Expected: still FAIL — that's fine; Task 11 is what turns it green. The wiring change in Step 3 is proven when Task 11's single template change makes this test pass without any further Go modifications.

- [ ] **Step 5: Commit the wiring**

```bash
git add internal/pkg/deploy/cloudformation/stack/lb_web_svc.go internal/pkg/deploy/cloudformation/stack/lb_web_svc_test.go
git commit -m "$(cat <<'EOF'
Stack: thread envIPv6Enabled into LBWS WorkloadOpts

Also adds an end-to-end test that remains RED until Task 11 lands the
AAAA RecordSet in https-listener.yml — mirrors the #2 convention from
TestEnvStack_IPv6ThreadedFromManifest.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 6: Populate `WorkloadOpts.IPv6Enabled` in `BackendService.Template()`

**Files:**
- Modify: `internal/pkg/deploy/cloudformation/stack/backend_svc.go:194` (inside the `s.parser.ParseBackendService(template.WorkloadOpts{...})` literal)
- Test: `internal/pkg/deploy/cloudformation/stack/backend_svc_test.go`

Context: BackendService has its own `envIPv6Enabled` field at `backend_svc.go:28`, populated in the constructor at `backend_svc.go:87`. Same pattern as LBWS, but with `s.albEnabled` instead of `!s.manifest.HTTPOrBool.Disabled()`.

- [ ] **Step 1: Write the failing test**

Append to `internal/pkg/deploy/cloudformation/stack/backend_svc_test.go`:

```go
// TestBackendService_IPv6PropagatesToListenerTemplate proves the
// WorkloadOpts.IPv6Enabled flag reaches the BackendService listener
// template. INTENTIONALLY FAILS until Task 12 lands the AAAA record in
// http-listener.yml.
func TestBackendService_IPv6PropagatesToListenerTemplate(t *testing.T) {
	conf := BackendServiceConfig{
		App:                &config.Application{Name: "mockApp"},
		EnvManifest:        mustEnvManifestWithIPv6(t, true),
		ArtifactBucketName: "mockBucket",
		Manifest: manifest.NewBackendService(manifest.BackendServiceProps{
			WorkloadProps: manifest.WorkloadProps{
				Name:       "api",
				Dockerfile: testDockerfile,
			},
			Port: 8080,
			HealthCheck: manifest.ContainerHealthCheck{
				Command: []string{"CMD-SHELL", "curl -f http://localhost/ || exit 1"},
			},
			Path: aws.String("/"), // Activates internal ALB path with AAAA alias.
		}),
		RuntimeConfig: RuntimeConfig{
			Version:   "v1.29.0",
			Region:    "us-west-2",
			AccountID: "123456789012",
		},
	}
	stk, err := NewBackendService(conf)
	require.NoError(t, err)
	tpl, err := stk.Template()
	require.NoError(t, err)
	require.Contains(t, tpl, "Type: AAAA",
		"BackendService in IPv6 env with internal ALB must emit AAAA alias (lands in Task 12)")
}
```

If `manifest.BackendServiceProps` does not have a `Path *string` field, adapt to whatever the codebase uses to enable the internal ALB (search: `grep -rn "Path\s*\*string" internal/pkg/manifest/backend_svc.go`). The goal is to construct a BackendService whose HTTP config activates the internal ALB path in `http-listener.yml`.

- [ ] **Step 2: Run test, confirm it fails**

```bash
go test ./internal/pkg/deploy/cloudformation/stack/ -run TestBackendService_IPv6PropagatesToListenerTemplate -v
```

Expected: FAIL — `Type: AAAA` not present.

- [ ] **Step 3: Wire `IPv6Enabled` into the WorkloadOpts literal**

Edit `internal/pkg/deploy/cloudformation/stack/backend_svc.go` in the `s.parser.ParseBackendService(template.WorkloadOpts{...})` literal (currently around line 164-220). Add `IPv6Enabled: s.envIPv6Enabled,` in the ALB configs block around line 201:

```go
		// ALB configs.
		ALBEnabled:  s.albEnabled,
		IPv6Enabled: s.envIPv6Enabled,
		GracePeriod: s.convertGracePeriod(),
		ALBListener: albListenerConfig,
		ImportedALB: importedALBConfig,
```

- [ ] **Step 4: Run test, confirm it still fails (partial landed, Task 12 completes it)**

```bash
go test ./internal/pkg/deploy/cloudformation/stack/ -run TestBackendService_IPv6PropagatesToListenerTemplate -v
```

Expected: still FAIL — Task 12 will turn it green.

- [ ] **Step 5: Commit**

```bash
git add internal/pkg/deploy/cloudformation/stack/backend_svc.go internal/pkg/deploy/cloudformation/stack/backend_svc_test.go
git commit -m "$(cat <<'EOF'
Stack: thread envIPv6Enabled into BackendService WorkloadOpts

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 7: Env CFN — add `IpAddressType: dualstack` to Public and Internal ALBs

**Files:**
- Modify: `internal/pkg/template/templates/environment/cf.yml:314-346` (PublicLoadBalancer) and `cf.yml:412-427` (InternalLoadBalancer)
- Test: `internal/pkg/deploy/cloudformation/stack/env_integration_test.go` — add a one-liner `Contains` assertion to `TestEnvStack_IPv6ThreadedFromManifest` (which already exists per #1), OR add a new proof test.

Context: Both ALB resources are of type `AWS::ElasticLoadBalancingV2::LoadBalancer`. Adding `IpAddressType: dualstack` under a `{{- if .VPCConfig.IPv6Enabled }}` guard preserves byte-identity for v4-only envs (CFN default is `ipv4` when the property is absent).

- [ ] **Step 1: Write a failing proof test**

Append to `internal/pkg/deploy/cloudformation/stack/env_integration_test.go` (bottom of file):

```go
// TestEnvStack_IPv6ALBsAreDualstack proves the PublicLoadBalancer and
// InternalLoadBalancer get IpAddressType: dualstack when the env is
// IPv6-enabled. Intentionally fails until Task 7 lands.
func TestEnvStack_IPv6ALBsAreDualstack(t *testing.T) {
	rawMft := `name: test
type: Environment
network:
  vpc:
    ipv6:
      enabled: true
`
	var mft manifest.Environment
	require.NoError(t, yaml.Unmarshal([]byte(rawMft), &mft))
	envCfg := &stack.EnvConfig{
		Version: "1.x",
		App: deploy.AppInformation{
			AccountPrincipalARN: "arn:aws:iam::000000000:root",
			Name:                "demo",
		},
		Name:                 "test",
		ArtifactBucketARN:    "arn:aws:s3:::mockbucket",
		ArtifactBucketKeyARN: "arn:aws:kms:us-west-2:000000000:key/1234abcd-12ab-34cd-56ef-1234567890ab",
		Mft:                  &mft,
		RawMft:               rawMft,
	}
	envStack, err := stack.NewEnvStackConfig(envCfg)
	require.NoError(t, err)
	body, err := envStack.Template()
	require.NoError(t, err)
	// At least twice: once on PublicLoadBalancer, once on InternalLoadBalancer.
	require.GreaterOrEqual(t, strings.Count(body, "IpAddressType: dualstack"), 2,
		"expected IpAddressType: dualstack on both PublicLoadBalancer and InternalLoadBalancer")
}
```

Make sure `strings` is in the import block. If it is not, add it.

- [ ] **Step 2: Run test, confirm it fails**

```bash
go test -tags localintegration ./internal/pkg/deploy/cloudformation/stack/ -run TestEnvStack_IPv6ALBsAreDualstack -v
```

Expected: FAIL — current render has zero `IpAddressType: dualstack` occurrences.

- [ ] **Step 3: Edit PublicLoadBalancer**

In `internal/pkg/template/templates/environment/cf.yml` (current line ~321), add the guard after `Scheme: internet-facing`:

```yaml
      Scheme: internet-facing
      {{- if .VPCConfig.IPv6Enabled }}
      IpAddressType: dualstack
      {{- end }}
      SecurityGroups:
```

- [ ] **Step 4: Edit InternalLoadBalancer**

In the same file at line ~416, add the guard after `Scheme: internal`:

```yaml
      Scheme: internal
      {{- if .VPCConfig.IPv6Enabled }}
      IpAddressType: dualstack
      {{- end }}
      SecurityGroups: [ !GetAtt InternalLoadBalancerSecurityGroup.GroupId ]
```

- [ ] **Step 5: Run test, confirm pass**

```bash
go test -tags localintegration ./internal/pkg/deploy/cloudformation/stack/ -run TestEnvStack_IPv6ALBsAreDualstack -v
```

Expected: PASS.

- [ ] **Step 6: Run the full env integration test suite to catch regressions**

```bash
go test -tags localintegration ./internal/pkg/deploy/cloudformation/stack/ -run TestEnvStack_Template -v
```

Expected: the `"ipv6 dual-stack enabled"` case FAILS because the golden does not yet have `IpAddressType: dualstack` on its ALB blocks. Task 13 updates the golden. All other cases (v4-only envs) PASS — proves byte-identity.

If any v4-only case fails, the guard is wrong — debug before moving on.

- [ ] **Step 7: Commit**

```bash
git add internal/pkg/template/templates/environment/cf.yml internal/pkg/deploy/cloudformation/stack/env_integration_test.go
git commit -m "$(cat <<'EOF'
Env CFN: set IpAddressType: dualstack on both ALBs when IPv6 is enabled

Preserves byte-identity for v4-only envs (CFN default is ipv4 when the
property is absent).

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 8: Env CFN — add standalone IPv6 SG ingress for Public ALB (default path)

**Files:**
- Modify: `internal/pkg/template/templates/environment/cf.yml` (immediately after the `PublicHTTPSLoadBalancerSecurityGroup` block that ends around line 191)
- Test: `env_integration_test.go`

Context: The two public-ALB SGs today have an inline `CidrIp: 0.0.0.0/0` on 80/443 in the `{{- else }}` branch of `HasCustomIngress` (cf.yml:139-145, 177-183). Adding v6 inline would mutate every existing env golden. Instead, we emit two standalone `AWS::EC2::SecurityGroupIngress` resources, gated on `.VPCConfig.IPv6Enabled` AND the same CFN Condition attributes as their v4 counterparts.

- [ ] **Step 1: Write the failing test**

Append to `env_integration_test.go`:

```go
// TestEnvStack_IPv6_PublicALBHasV6SGIngress proves the standalone
// SecurityGroupIngress resources are emitted when IPv6 is on. Fails
// until Task 8 lands.
func TestEnvStack_IPv6_PublicALBHasV6SGIngress(t *testing.T) {
	rawMft := `name: test
type: Environment
network:
  vpc:
    ipv6:
      enabled: true
`
	var mft manifest.Environment
	require.NoError(t, yaml.Unmarshal([]byte(rawMft), &mft))
	envStack, err := stack.NewEnvStackConfig(&stack.EnvConfig{
		Version:              "1.x",
		App:                  deploy.AppInformation{AccountPrincipalARN: "arn:aws:iam::000000000:root", Name: "demo"},
		Name:                 "test",
		ArtifactBucketARN:    "arn:aws:s3:::mockbucket",
		ArtifactBucketKeyARN: "arn:aws:kms:us-west-2:000000000:key/1234abcd-12ab-34cd-56ef-1234567890ab",
		Mft:                  &mft,
		RawMft:               rawMft,
	})
	require.NoError(t, err)
	body, err := envStack.Template()
	require.NoError(t, err)
	require.Contains(t, body, "PublicHTTPLoadBalancerSecurityGroupIngressIPv6:")
	require.Contains(t, body, "PublicHTTPSLoadBalancerSecurityGroupIngressIPv6:")
	require.Contains(t, body, "CidrIpv6: ::/0")
}
```

- [ ] **Step 2: Run test, confirm it fails**

```bash
go test -tags localintegration ./internal/pkg/deploy/cloudformation/stack/ -run TestEnvStack_IPv6_PublicALBHasV6SGIngress -v
```

Expected: FAIL.

- [ ] **Step 3: Add the two new resources**

In `internal/pkg/template/templates/environment/cf.yml`, immediately after the `PublicHTTPSLoadBalancerSecurityGroup` block (which ends around line 191 — right before `InternalLoadBalancerSecurityGroup:` at line 192):

```yaml
{{- if .VPCConfig.IPv6Enabled }}
  PublicHTTPLoadBalancerSecurityGroupIngressIPv6:
    Metadata:
      'aws:copilot:description': 'An IPv6 inbound rule to the public HTTP load balancer security group for port 80'
    Condition: CreateALB
    Type: AWS::EC2::SecurityGroupIngress
    Properties:
      GroupId: !Ref PublicHTTPLoadBalancerSecurityGroup
      IpProtocol: tcp
      FromPort: 80
      ToPort: 80
      CidrIpv6: ::/0
      Description: Allow from anyone over IPv6 on port 80
  PublicHTTPSLoadBalancerSecurityGroupIngressIPv6:
    Metadata:
      'aws:copilot:description': 'An IPv6 inbound rule to the public HTTPS load balancer security group for port 443'
    Condition: ExportHTTPSListener
    Type: AWS::EC2::SecurityGroupIngress
    Properties:
      GroupId: !Ref PublicHTTPSLoadBalancerSecurityGroup
      IpProtocol: tcp
      FromPort: 443
      ToPort: 443
      CidrIpv6: ::/0
      Description: Allow from anyone over IPv6 on port 443
{{- end }}
```

- [ ] **Step 4: Run the test, confirm pass**

```bash
go test -tags localintegration ./internal/pkg/deploy/cloudformation/stack/ -run TestEnvStack_IPv6_PublicALBHasV6SGIngress -v
```

Expected: PASS.

- [ ] **Step 5: Re-run full env integration to confirm v4-only byte-identity**

```bash
go test -tags localintegration ./internal/pkg/deploy/cloudformation/stack/ -run TestEnvStack_Template -v
```

Expected: v4-only cases still PASS (the new resources are inside `{{- if .VPCConfig.IPv6Enabled }}` so v4-only renders are unchanged). The `"ipv6 dual-stack enabled"` case continues to FAIL until Task 13 updates its golden.

- [ ] **Step 6: Commit**

```bash
git add internal/pkg/template/templates/environment/cf.yml internal/pkg/deploy/cloudformation/stack/env_integration_test.go
git commit -m "$(cat <<'EOF'
Env CFN: add ::/0 IPv6 ingress for the public ALB SGs

Standalone AWS::EC2::SecurityGroupIngress resources (not inline) so
v4-only envs render byte-identically. Conditioned on CreateALB and
ExportHTTPSListener to match their v4 counterparts.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 9: Env CFN — family-aware `CidrIp` / `CidrIpv6` in custom-ingress path

**Files:**
- Modify: `internal/pkg/template/templates/environment/cf.yml:123-138` (the HTTP SG custom-ingress block) and `cf.yml:161-176` (the HTTPS SG custom-ingress block)
- Test: `env_integration_test.go`

Context: Today, the `{{- if .PublicHTTPConfig.HasCustomIngress}}` branch iterates `PublicALBSourceIPs` and emits `CidrIp: {{$cidr}}`. If the user supplies a v6 CIDR, that's invalid CFN (CidrIp is v4-only). The `isIPv6CIDR` helper (registered in Task 3) lets us branch per entry.

- [ ] **Step 1: Write the failing test**

Append to `env_integration_test.go`:

```go
// TestEnvStack_IPv6_CustomIngressSplitsByFamily proves the custom-ingress
// block routes v4 CIDRs to CidrIp and v6 CIDRs to CidrIpv6. Fails until
// Task 9 lands.
func TestEnvStack_IPv6_CustomIngressSplitsByFamily(t *testing.T) {
	rawMft := `name: test
type: Environment
http:
  public:
    certificates:
      - cert-1
    security_groups:
      ingress:
        restrict_to:
          cdn: false
network:
  vpc:
    ipv6:
      enabled: true
`
	var mft manifest.Environment
	require.NoError(t, yaml.Unmarshal([]byte(rawMft), &mft))
	envStack, err := stack.NewEnvStackConfig(&stack.EnvConfig{
		Version:              "1.x",
		App:                  deploy.AppInformation{AccountPrincipalARN: "arn:aws:iam::000000000:root", Name: "demo"},
		Name:                 "test",
		PublicALBSourceIPs:   []string{"1.1.1.1/32", "2001:db8::/32"},
		ArtifactBucketARN:    "arn:aws:s3:::mockbucket",
		ArtifactBucketKeyARN: "arn:aws:kms:us-west-2:000000000:key/1234abcd-12ab-34cd-56ef-1234567890ab",
		Mft:                  &mft,
		RawMft:               rawMft,
	})
	require.NoError(t, err)
	body, err := envStack.Template()
	require.NoError(t, err)
	require.Contains(t, body, "CidrIp: 1.1.1.1/32")
	require.Contains(t, body, "CidrIpv6: 2001:db8::/32")
}
```

If the manifest surface required to activate `HasCustomIngress` differs, adjust — the goal is to trigger the custom-ingress branch with a mix of v4 and v6 CIDRs.

- [ ] **Step 2: Run test, confirm it fails**

```bash
go test -tags localintegration ./internal/pkg/deploy/cloudformation/stack/ -run TestEnvStack_IPv6_CustomIngressSplitsByFamily -v
```

Expected: FAIL — `CidrIpv6: 2001:db8::/32` not in render (today it's emitted as `CidrIp: 2001:db8::/32` which is invalid CFN).

- [ ] **Step 3: Edit the HTTP SG custom-ingress loop**

In `internal/pkg/template/templates/environment/cf.yml` around line 132-138:

```yaml
        {{- range $cidr := .PublicHTTPConfig.PublicALBSourceIPs }}
        {{- if isIPv6CIDR $cidr }}
        - CidrIpv6: {{$cidr}}
          Description: Allow ingress from source IPv6 {{$cidr}} on port 80
          FromPort: 80
          IpProtocol: tcp
          ToPort: 80
        {{- else }}
        - CidrIp: {{$cidr}}
          Description: Allow ingress from source IP {{$cidr}} on port 80
          FromPort: 80
          IpProtocol: tcp
          ToPort: 80
        {{- end }}
        {{- end }}
```

- [ ] **Step 4: Edit the HTTPS SG custom-ingress loop**

Mirror change around line 170-176:

```yaml
        {{- range $cidr := .PublicHTTPConfig.PublicALBSourceIPs }}
        {{- if isIPv6CIDR $cidr }}
        - CidrIpv6: {{$cidr}}
          Description: Allow ingress from source IPv6 {{$cidr}} on port 443
          FromPort: 443
          IpProtocol: tcp
          ToPort: 443
        {{- else }}
        - CidrIp: {{$cidr}}
          Description: Allow ingress from source IP {{$cidr}} on port 443
          FromPort: 443
          IpProtocol: tcp
          ToPort: 443
        {{- end }}
        {{- end }}
```

- [ ] **Step 5: Run test, confirm pass**

```bash
go test -tags localintegration ./internal/pkg/deploy/cloudformation/stack/ -run TestEnvStack_IPv6_CustomIngressSplitsByFamily -v
```

Expected: PASS.

- [ ] **Step 6: Confirm v4-only byte-identity — critical regression gate**

```bash
go test -tags localintegration ./internal/pkg/deploy/cloudformation/stack/ -run TestEnvStack_Template -v
```

Expected: every v4-only case passes. The template-with-cloudfront-observability test case (which uses `PublicALBSourceIPs: []string{"1.1.1.1", "2.2.2.2"}`) must still pass byte-identically — `isIPv6CIDR` returns false for both, so both CIDRs still render as `CidrIp:` exactly as before.

Note: `1.1.1.1` and `2.2.2.2` are IPs without masks. `net.ParseCIDR("1.1.1.1")` returns an error, so `IsIPv6CIDR` returns `false`, and the else branch is taken — correct. But verify by re-running the regression case explicitly:

```bash
go test -tags localintegration ./internal/pkg/deploy/cloudformation/stack/ -run TestEnvStack_Template/generate_template_with_default_access_logs -v
```

- [ ] **Step 7: Commit**

```bash
git add internal/pkg/template/templates/environment/cf.yml internal/pkg/deploy/cloudformation/stack/env_integration_test.go
git commit -m "$(cat <<'EOF'
Env CFN: route custom ingress CIDRs to CidrIp/CidrIpv6 by family

User-supplied v6 CIDRs in http.public.security_groups.ingress now render
as CidrIpv6; v4 CIDRs continue to render as CidrIp.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 10: Env CFN — IPv6 SG ingress for Internal ALB (when AllowVPCIngress)

**Files:**
- Modify: `internal/pkg/template/templates/environment/cf.yml` — inside the existing `{{- if .VPCConfig.AllowVPCIngress }}` block at around line 288, immediately after `InternalLoadBalancerSecurityGroupIngressFromHttps` at line 301.
- Test: `env_integration_test.go`

Context: The existing `InternalLoadBalancerSecurityGroupIngressFromHttp` / `FromHttps` rules (cf.yml:289-313) use `CidrIp: 0.0.0.0/0` and are gated on `{{- if .VPCConfig.AllowVPCIngress }}`. The v6 equivalents add `CidrIpv6: ::/0` and are additionally gated on `.VPCConfig.IPv6Enabled`.

- [ ] **Step 1: Write the failing test**

Append to `env_integration_test.go`:

```go
// TestEnvStack_IPv6_InternalALBHasV6Ingress proves the internal ALB SG
// has v6 ingress when both IPv6 and AllowVPCIngress are on. Fails until
// Task 10 lands.
func TestEnvStack_IPv6_InternalALBHasV6Ingress(t *testing.T) {
	rawMft := `name: test
type: Environment
http:
  private:
    security_groups:
      ingress:
        from_vpc: true
    certificates:
      - cert-1
network:
  vpc:
    ipv6:
      enabled: true
`
	var mft manifest.Environment
	require.NoError(t, yaml.Unmarshal([]byte(rawMft), &mft))
	envStack, err := stack.NewEnvStackConfig(&stack.EnvConfig{
		Version:              "1.x",
		App:                  deploy.AppInformation{AccountPrincipalARN: "arn:aws:iam::000000000:root", Name: "demo"},
		Name:                 "test",
		ArtifactBucketARN:    "arn:aws:s3:::mockbucket",
		ArtifactBucketKeyARN: "arn:aws:kms:us-west-2:000000000:key/1234abcd-12ab-34cd-56ef-1234567890ab",
		Mft:                  &mft,
		RawMft:               rawMft,
	})
	require.NoError(t, err)
	body, err := envStack.Template()
	require.NoError(t, err)
	require.Contains(t, body, "InternalLoadBalancerSecurityGroupIngressFromHttpIPv6:")
	require.Contains(t, body, "InternalLoadBalancerSecurityGroupIngressFromHttpsIPv6:")
	require.Contains(t, body, "Allow from within the VPC over IPv6")
}
```

If `from_vpc: true` doesn't map to `AllowVPCIngress`, search for the actual manifest field that sets `.VPCConfig.AllowVPCIngress`:

```bash
grep -rn "AllowVPCIngress" internal/pkg/manifest/ internal/pkg/deploy/cloudformation/stack/
```

- [ ] **Step 2: Run test, confirm it fails**

```bash
go test -tags localintegration ./internal/pkg/deploy/cloudformation/stack/ -run TestEnvStack_IPv6_InternalALBHasV6Ingress -v
```

Expected: FAIL.

- [ ] **Step 3: Edit cf.yml**

In `internal/pkg/template/templates/environment/cf.yml`, inside the existing `{{- if .VPCConfig.AllowVPCIngress }}` block (around line 288), immediately after the `InternalLoadBalancerSecurityGroupIngressFromHttps` resource (ending at line 312), add:

```yaml
{{- if .VPCConfig.IPv6Enabled }}
  InternalLoadBalancerSecurityGroupIngressFromHttpIPv6:
    Metadata:
      'aws:copilot:description': 'An IPv6 inbound rule to the internal load balancer security group for port 80 within the VPC'
    Type: AWS::EC2::SecurityGroupIngress
    Condition: CreateInternalALB
    Properties:
      Description: Allow from within the VPC over IPv6 on port 80
      CidrIpv6: ::/0
      FromPort: 80
      ToPort: 80
      IpProtocol: tcp
      GroupId: !Ref InternalLoadBalancerSecurityGroup
  InternalLoadBalancerSecurityGroupIngressFromHttpsIPv6:
    Metadata:
      'aws:copilot:description': 'An IPv6 inbound rule to the internal load balancer security group for port 443 within the VPC'
    Type: AWS::EC2::SecurityGroupIngress
    Condition: ExportInternalHTTPSListener
    Properties:
      Description: Allow from within the VPC over IPv6 on port 443
      CidrIpv6: ::/0
      FromPort: 443
      ToPort: 443
      IpProtocol: tcp
      GroupId: !Ref InternalLoadBalancerSecurityGroup
{{- end }}
```

The outer `{{- if .VPCConfig.AllowVPCIngress }}` already wraps this block; the inner guard is only `IPv6Enabled`.

- [ ] **Step 4: Run test, confirm pass**

```bash
go test -tags localintegration ./internal/pkg/deploy/cloudformation/stack/ -run TestEnvStack_IPv6_InternalALBHasV6Ingress -v
```

Expected: PASS.

- [ ] **Step 5: Byte-identity regression**

```bash
go test -tags localintegration ./internal/pkg/deploy/cloudformation/stack/ -run TestEnvStack_Template -v
```

Expected: all v4-only and v4-only-with-custom-security-group cases still pass.

- [ ] **Step 6: Commit**

```bash
git add internal/pkg/template/templates/environment/cf.yml internal/pkg/deploy/cloudformation/stack/env_integration_test.go
git commit -m "$(cat <<'EOF'
Env CFN: add ::/0 IPv6 ingress for the internal ALB SG

Emitted only when both network.vpc.ipv6.enabled: true AND
http.private.security_groups.ingress.from_vpc: true are set. Mirrors
the existing v4 FromHttp/FromHttps rules.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 11: Workload CFN — AAAA RecordSets in `https-listener.yml`

**Files:**
- Modify: `internal/pkg/template/templates/workloads/partials/cf/https-listener.yml` (lines 1-46)
- Test: `TestLoadBalancedWebService_IPv6PropagatesToListenerTemplate` (written in Task 5, currently failing).

Context: Two RecordSet emissions: the default (no aliases) path at lines 2-22, and the aliases loop at lines 24-45. The outer scope `.` is `WorkloadOpts`, so `{{- if .IPv6Enabled }}` references the new top-level field.

- [ ] **Step 1: Confirm Task 5's test still fails**

```bash
go test ./internal/pkg/deploy/cloudformation/stack/ -run TestLoadBalancedWebService_IPv6PropagatesToListenerTemplate -v
```

Expected: FAIL (carries from Task 5).

- [ ] **Step 2: Edit the default path**

In `internal/pkg/template/templates/workloads/partials/cf/https-listener.yml`, extend the `RecordSets` list inside `LoadBalancerDNSAlias` (ends at line 22):

```yaml
{{- if not .ALBListener.Aliases}}
LoadBalancerDNSAlias:
  Metadata:
    'aws:copilot:description': 'The default alias record for the application load balancer'
  Type: AWS::Route53::RecordSetGroup
  Properties:
    HostedZoneId:
      Fn::ImportValue:
        !Sub "${AppName}-${EnvName}-HostedZone"
    Comment: !Sub "LoadBalancer alias for service ${WorkloadName}"
    RecordSets:
    - Name:
        !Join
          - '.'
          - - !Ref WorkloadName
            - Fn::ImportValue:
                !Sub "${AppName}-${EnvName}-SubDomain"
            - ""
      Type: A
      AliasTarget:
        HostedZoneId: !GetAtt EnvControllerAction.PublicLoadBalancerHostedZone
        DNSName: !GetAtt EnvControllerAction.PublicLoadBalancerDNSName
    {{- if .IPv6Enabled }}
    - Name:
        !Join
          - '.'
          - - !Ref WorkloadName
            - Fn::ImportValue:
                !Sub "${AppName}-${EnvName}-SubDomain"
            - ""
      Type: AAAA
      AliasTarget:
        HostedZoneId: !GetAtt EnvControllerAction.PublicLoadBalancerHostedZone
        DNSName: !GetAtt EnvControllerAction.PublicLoadBalancerDNSName
    {{- end }}
{{- else}}
```

- [ ] **Step 3: Edit the aliases path**

In the `range $alias := $aliases` loop (around lines 33-44), add an AAAA entry after the existing A entry:

```yaml
    {{- range $alias := $aliases}}
      - Name: {{quote $alias}}
        Type: A
        AliasTarget:
          {{- if eq $.WorkloadType "Backend Service"}}
          HostedZoneId: !GetAtt EnvControllerAction.InternalLoadBalancerHostedZone
          DNSName: !GetAtt EnvControllerAction.InternalLoadBalancerDNSName
          {{- else}}
          HostedZoneId: !GetAtt EnvControllerAction.PublicLoadBalancerHostedZone
          DNSName: !GetAtt EnvControllerAction.PublicLoadBalancerDNSName
          {{- end}}
      {{- if $.IPv6Enabled }}
      - Name: {{quote $alias}}
        Type: AAAA
        AliasTarget:
          {{- if eq $.WorkloadType "Backend Service"}}
          HostedZoneId: !GetAtt EnvControllerAction.InternalLoadBalancerHostedZone
          DNSName: !GetAtt EnvControllerAction.InternalLoadBalancerDNSName
          {{- else}}
          HostedZoneId: !GetAtt EnvControllerAction.PublicLoadBalancerHostedZone
          DNSName: !GetAtt EnvControllerAction.PublicLoadBalancerDNSName
          {{- end}}
      {{- end}}
    {{- end}}
```

Note the dollar prefix: `$.IPv6Enabled` and `$.WorkloadType` — inside the nested `range`, `.` rebinds to `$alias`, so we reach the outer `WorkloadOpts` via `$`.

- [ ] **Step 4: Run Task 5's test, confirm pass**

```bash
go test ./internal/pkg/deploy/cloudformation/stack/ -run TestLoadBalancedWebService_IPv6PropagatesToListenerTemplate -v
```

Expected: PASS.

- [ ] **Step 5: Run the full LBWS unit suite to catch regressions**

```bash
go test ./internal/pkg/deploy/cloudformation/stack/ -run TestLoadBalancedWebService -v
```

Expected: all previously-passing tests still pass. Any existing LBWS test that builds an IPv6-disabled env (the default) must render the same A-only records as before — byte-identity gate.

- [ ] **Step 6: Commit**

```bash
git add internal/pkg/template/templates/workloads/partials/cf/https-listener.yml internal/pkg/deploy/cloudformation/stack/lb_web_svc_test.go
git commit -m "$(cat <<'EOF'
Workload CFN: emit AAAA alias records alongside A for LBWS / Backend Service

Gated on WorkloadOpts.IPv6Enabled, which is true only when the env has
network.vpc.ipv6.enabled: true. Covers both the default alias path and
the user-supplied-aliases path.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 12: Workload CFN — AAAA RecordSet in `http-listener.yml` (BackendService)

**Files:**
- Modify: `internal/pkg/template/templates/workloads/partials/cf/http-listener.yml` (lines 1-18)
- Test: `TestBackendService_IPv6PropagatesToListenerTemplate` (written in Task 6, currently failing).

Context: The first 18 lines of `http-listener.yml` emit `LoadBalancerInternalDNSAlias` for BackendService. Same pattern: add an AAAA entry to the `RecordSets` list under `{{- if .IPv6Enabled }}`.

- [ ] **Step 1: Confirm Task 6's test still fails**

```bash
go test ./internal/pkg/deploy/cloudformation/stack/ -run TestBackendService_IPv6PropagatesToListenerTemplate -v
```

Expected: FAIL.

- [ ] **Step 2: Edit http-listener.yml**

In `internal/pkg/template/templates/workloads/partials/cf/http-listener.yml`, extend the `RecordSets` list:

```yaml
{{- if eq $.WorkloadType "Backend Service"}}
LoadBalancerInternalDNSAlias:
  Metadata:
    'aws:copilot:description': 'Alias for {{.WorkloadName}}.{{.EnvName}}.{{.AppName}}.internal to the internal load balancer'
  Type: AWS::Route53::RecordSetGroup
  Properties:
    Comment: !Sub "Load balancer alias for service ${WorkloadName}"
    HostedZoneId: !GetAtt EnvControllerAction.InternalWorkloadsHostedZone
    RecordSets:
      - Type: A
        AliasTarget:
          HostedZoneId: !GetAtt EnvControllerAction.InternalLoadBalancerHostedZone
          DNSName: !GetAtt EnvControllerAction.InternalLoadBalancerDNSName
        Name: !Join
          - '.'
          - - !Ref WorkloadName
            - !GetAtt EnvControllerAction.InternalWorkloadsHostedZoneName
      {{- if .IPv6Enabled }}
      - Type: AAAA
        AliasTarget:
          HostedZoneId: !GetAtt EnvControllerAction.InternalLoadBalancerHostedZone
          DNSName: !GetAtt EnvControllerAction.InternalLoadBalancerDNSName
        Name: !Join
          - '.'
          - - !Ref WorkloadName
            - !GetAtt EnvControllerAction.InternalWorkloadsHostedZoneName
      {{- end }}
{{- end}}
```

- [ ] **Step 3: Run Task 6's test, confirm pass**

```bash
go test ./internal/pkg/deploy/cloudformation/stack/ -run TestBackendService_IPv6PropagatesToListenerTemplate -v
```

Expected: PASS.

- [ ] **Step 4: Run the full BackendService unit suite**

```bash
go test ./internal/pkg/deploy/cloudformation/stack/ -run TestBackendService -v
```

Expected: existing tests still pass.

- [ ] **Step 5: Commit**

```bash
git add internal/pkg/template/templates/workloads/partials/cf/http-listener.yml internal/pkg/deploy/cloudformation/stack/backend_svc_test.go
git commit -m "$(cat <<'EOF'
Workload CFN: emit AAAA record for BackendService internal alias

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 13: Update the `template-with-ipv6-enabled.yml` env golden

**Files:**
- Modify: `internal/pkg/deploy/cloudformation/stack/testdata/environments/template-with-ipv6-enabled.yml`
- Test: the existing `TestEnvStack_Template/"ipv6 dual-stack enabled"` case.

Context: Tasks 7 and 8 added CFN output that doesn't yet appear in the golden. Task 7 adds `IpAddressType: dualstack` to both ALB blocks; Task 8 adds two `SecurityGroupIngress` resources. Task 10's additions don't apply here because this test case doesn't set `AllowVPCIngress`. Task 9's CIDR split doesn't apply because this case has no `PublicALBSourceIPs`.

**Do NOT regenerate the golden automatically — diff manually to confirm only the expected three additions.**

- [ ] **Step 1: Run the failing test to see the concrete diff**

```bash
go test -tags localintegration ./internal/pkg/deploy/cloudformation/stack/ -run 'TestEnvStack_Template$/ipv6_dual-stack_enabled' -v 2>&1 | tee /tmp/ipv6-golden-diff.txt
```

Expected: FAIL. The test output shows the diff between the current golden and the actual render. Confirm the only three diffs are:
1. `PublicLoadBalancer.Properties.IpAddressType: dualstack` (new key)
2. `InternalLoadBalancer.Properties.IpAddressType: dualstack` (new key)
3. Two new resources: `PublicHTTPLoadBalancerSecurityGroupIngressIPv6`, `PublicHTTPSLoadBalancerSecurityGroupIngressIPv6`

If any other diff appears, stop and debug. That indicates one of Tasks 7-9 introduced an unexpected change.

- [ ] **Step 2: Edit the golden — add IpAddressType to PublicLoadBalancer**

Find the `PublicLoadBalancer:` block in `internal/pkg/deploy/cloudformation/stack/testdata/environments/template-with-ipv6-enabled.yml` (around line 1236). Insert `IpAddressType: dualstack` alphabetically in the `Properties:` block. The serializer emits keys alphabetically within `Properties`, so the expected position is between `LoadBalancerAttributes` and `Scheme`:

```yaml
    PublicLoadBalancer:
        Condition: CreateALB
        Metadata:
            aws:copilot:description: An Application Load Balancer to distribute public traffic to your services
        Properties:
            IpAddressType: dualstack
            LoadBalancerAttributes:
                - Key: access_logs.s3.enabled
                  Value: false
            Scheme: internet-facing
            SecurityGroups:
```

Verify alphabetical position against the actual rendered template (`I` comes before `L`, and `L` before `S` — so `IpAddressType` is first in the `Properties` block).

- [ ] **Step 3: Edit the golden — add IpAddressType to InternalLoadBalancer**

Find `InternalLoadBalancer:` (around line 974). Insert `IpAddressType: dualstack` alphabetically.

- [ ] **Step 4: Edit the golden — add the two new SG ingress resources**

Resources in this golden are already alphabetized by logical ID. Add two new resources immediately before `PublicLoadBalancer:` (alphabetical position: `PublicHTTPLoadBalancerSecurityGroup`, `PublicHTTPLoadBalancerSecurityGroupIngressIPv6`, `PublicHTTPSLoadBalancerSecurityGroup`, `PublicHTTPSLoadBalancerSecurityGroupIngressIPv6`, `PublicLoadBalancer`).

Example block:

```yaml
    PublicHTTPLoadBalancerSecurityGroupIngressIPv6:
        Condition: CreateALB
        Metadata:
            aws:copilot:description: An IPv6 inbound rule to the public HTTP load balancer security group for port 80
        Properties:
            CidrIpv6: ::/0
            Description: Allow from anyone over IPv6 on port 80
            FromPort: 80
            GroupId: PublicHTTPLoadBalancerSecurityGroup
            IpProtocol: tcp
            ToPort: 80
        Type: AWS::EC2::SecurityGroupIngress
    PublicHTTPSLoadBalancerSecurityGroupIngressIPv6:
        Condition: ExportHTTPSListener
        Metadata:
            aws:copilot:description: An IPv6 inbound rule to the public HTTPS load balancer security group for port 443
        Properties:
            CidrIpv6: ::/0
            Description: Allow from anyone over IPv6 on port 443
            FromPort: 443
            GroupId: PublicHTTPSLoadBalancerSecurityGroup
            IpProtocol: tcp
            ToPort: 443
        Type: AWS::EC2::SecurityGroupIngress
```

The exact YAML formatting must match how the test serializer canonicalizes — note the 4-space indentation for resource keys, 8-space for nested keys, and the `!Ref`-style references unquoted (`GroupId: PublicHTTPLoadBalancerSecurityGroup` without the `!Ref` prefix because the test does a YAML-parse-then-map-compare, stripping intrinsic function syntax).

**Risk:** the exact serialization is sensitive. The safest path is:

1. Run the test with a temporary "write actual output to disk" hack:

```go
// Temporarily add this before `compareStackTemplate(t, wantedObj, actualObj)` in the test loop:
if tc.wantedFileName == "template-with-ipv6-enabled.yml" {
    require.NoError(t, os.WriteFile("/tmp/actual-ipv6.yml", []byte(actual), 0644))
}
```

2. Manually diff `/tmp/actual-ipv6.yml` against `testdata/environments/template-with-ipv6-enabled.yml`.

3. Copy only the three expected additions into the golden, preserving the serializer's style.

4. Remove the hack and commit only the golden change.

- [ ] **Step 5: Run the test, confirm pass**

```bash
go test -tags localintegration ./internal/pkg/deploy/cloudformation/stack/ -run 'TestEnvStack_Template$' -v
```

Expected: all cases PASS, including `"ipv6 dual-stack enabled"`.

- [ ] **Step 6: Run the full integration suite for sanity**

```bash
go test -tags localintegration ./internal/pkg/deploy/cloudformation/stack/ -v 2>&1 | tail -40
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/pkg/deploy/cloudformation/stack/testdata/environments/template-with-ipv6-enabled.yml
git commit -m "$(cat <<'EOF'
Env golden: reflect dualstack ALBs and IPv6 SG ingress in ipv6 fixture

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 14: Add `template-with-ipv6-and-custom-ingress.yml` env golden

**Files:**
- Create: `internal/pkg/deploy/cloudformation/stack/testdata/environments/template-with-ipv6-and-custom-ingress.yml`
- Modify: `internal/pkg/deploy/cloudformation/stack/env_integration_test.go` — add a new `testCases` entry

Context: A new test case that exercises (a) `isIPv6CIDR`-based CidrIp/CidrIpv6 splitting, AND (b) the internal ALB v6 ingress rules under `AllowVPCIngress`. This is the single-file integration proof.

- [ ] **Step 1: Add the test case**

In the `testCases` map inside `TestEnvStack_Template`, add:

```go
"ipv6 with custom ingress and internal alb vpc ingress": {
    input: func() *stack.EnvConfig {
        rawMft := `name: test
type: Environment
http:
  public:
    certificates:
      - cert-1
    security_groups:
      ingress:
        restrict_to:
          cdn: false
  private:
    security_groups:
      ingress:
        from_vpc: true
    certificates:
      - cert-2
network:
  vpc:
    ipv6:
      enabled: true
`
        var mft manifest.Environment
        err := yaml.Unmarshal([]byte(rawMft), &mft)
        require.NoError(t, err)
        return &stack.EnvConfig{
            Version:              "1.x",
            App:                  deploy.AppInformation{AccountPrincipalARN: "arn:aws:iam::000000000:root", Name: "demo"},
            Name:                 "test",
            PublicALBSourceIPs:   []string{"1.1.1.1/32", "2001:db8::/32"},
            ArtifactBucketARN:    "arn:aws:s3:::mockbucket",
            ArtifactBucketKeyARN: "arn:aws:kms:us-west-2:000000000:key/1234abcd-12ab-34cd-56ef-1234567890ab",
            Mft:                  &mft,
            RawMft:               rawMft,
        }
    }(),
    wantedFileName: "template-with-ipv6-and-custom-ingress.yml",
},
```

If the manifest shape for custom ingress differs (check the existing `template-with-cloudfront-observability` case — it uses `security_groups.ingress.restrict_to.cdn: true` with `cdn.certificate` set), mirror that structure exactly. The goal is to activate `HasCustomIngress=true` AND `AllowVPCIngress=true` simultaneously.

- [ ] **Step 2: Run the test without a golden file to capture the actual render**

```bash
go test -tags localintegration ./internal/pkg/deploy/cloudformation/stack/ -run 'TestEnvStack_Template$/ipv6_with_custom_ingress' -v
```

Expected: FAIL with "open ...testdata/environments/template-with-ipv6-and-custom-ingress.yml: no such file or directory".

- [ ] **Step 3: Generate the golden by adding the temporary write hack**

In `env_integration_test.go` inside the test-case body (inside the `t.Run`), immediately before `compareStackTemplate(t, wantedObj, actualObj)`:

```go
if tc.wantedFileName == "template-with-ipv6-and-custom-ingress.yml" {
    require.NoError(t, os.WriteFile(filepath.Join("testdata", "environments", tc.wantedFileName), []byte(actual), 0644))
    t.Skip("generated golden — remove this hack and re-run")
}
```

- [ ] **Step 4: Run once to write the file, remove the hack**

```bash
go test -tags localintegration ./internal/pkg/deploy/cloudformation/stack/ -run 'TestEnvStack_Template$/ipv6_with_custom_ingress' -v
```

Then delete the temporary hack from the test file, and re-read the generated fixture to verify:

```bash
grep -nE "CidrIp: 1.1.1.1/32|CidrIpv6: 2001:db8::/32|InternalLoadBalancerSecurityGroupIngressFromHttpIPv6|PublicHTTPLoadBalancerSecurityGroupIngressIPv6" \
  internal/pkg/deploy/cloudformation/stack/testdata/environments/template-with-ipv6-and-custom-ingress.yml
```

Expected: all four patterns present.

- [ ] **Step 5: Run the test for real (no hack), confirm pass**

```bash
go test -tags localintegration ./internal/pkg/deploy/cloudformation/stack/ -run 'TestEnvStack_Template$' -v
```

Expected: all cases PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/pkg/deploy/cloudformation/stack/testdata/environments/template-with-ipv6-and-custom-ingress.yml internal/pkg/deploy/cloudformation/stack/env_integration_test.go
git commit -m "$(cat <<'EOF'
Env golden: add ipv6 + custom ingress + internal ALB vpc-ingress fixture

Exercises CidrIp/CidrIpv6 family split and internal ALB v6 SG ingress
in a single integration test case.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 15: Add LBWS and BackendService workload goldens for IPv6

**Files:**
- Create: `internal/pkg/deploy/cloudformation/stack/testdata/workloads/svc-ipv6-test.stack.yml`
- Create: `internal/pkg/deploy/cloudformation/stack/testdata/workloads/svc-ipv6-test.params.json`
- Create: `internal/pkg/deploy/cloudformation/stack/testdata/workloads/backend/svc-ipv6-test.stack.yml`
- Modify: `internal/pkg/deploy/cloudformation/stack/lb_web_service_integration_test.go` — add an IPv6 env test case
- Modify: `internal/pkg/deploy/cloudformation/stack/backend_svc_integration_test.go` — add an IPv6 env test case

Context: Existing LBWS golden tests iterate over a small `testCases` map and load `svc-manifest.yml`. We add a new case that passes an IPv6-enabled `EnvManifest` to the stack constructor. Backend goldens live in `testdata/workloads/backend/` — list that directory for the naming pattern.

- [ ] **Step 1: Inspect the existing LBWS integration test loop**

```bash
grep -n "EnvManifest\|envName\b" internal/pkg/deploy/cloudformation/stack/lb_web_service_integration_test.go | head -30
```

Note how `envConfig` is constructed at lines 119-125 (no IPv6). We'll build a new `envConfig` with `network.vpc.ipv6.enabled: true` for the new case.

- [ ] **Step 2: Add the LBWS test case**

In `TestLoadBalancedWebService_TemplateInteg`, extend the `testCases` map with a new entry. Build `envConfig` using `mustEnvManifestWithIPv6(true)` (the helper from `ipv6_test_helpers_test.go`) — but note: that helper lives in `package stack`, while the integration test is in `package stack_test`. Copy the helper body inline, OR export the helper by moving its definition to a file without `_test` suffix restrictions. Simplest: inline YAML.

```go
"ipv6 env": {
    envName:       "test",
    svcStackPath:  "svc-ipv6-test.stack.yml",
    svcParamsPath: "svc-ipv6-test.params.json",
    envIPv6:       true,
},
```

Also extend the struct literal of `testCases` values to include the new `envIPv6 bool` field, and in the loop body branch on it:

```go
envConfig := &manifest.Environment{
    Workload: manifest.Workload{Name: &tc.envName},
}
envConfig.HTTPConfig.Public.Certificates = []string{"mockCertARN"}
if tc.envIPv6 {
    // Set network.vpc.ipv6.enabled via YAML round-trip so the unexported
    // ipv6VPCConfig struct is populated.
    rawEnv := `name: test
type: Environment
network:
  vpc:
    ipv6:
      enabled: true
http:
  public:
    certificates:
      - mockCertARN
`
    require.NoError(t, yaml.Unmarshal([]byte(rawEnv), envConfig))
}
```

If `manifest.Environment` does not unmarshal into the pre-initialized struct cleanly, use the exported unmarshaller:

```go
parsed, err := manifest.UnmarshalEnvironment([]byte(rawEnv))
require.NoError(t, err)
envConfig = parsed
```

- [ ] **Step 3: Generate the golden via the same "temporary write hack" pattern**

Inside the test-case loop, immediately before the `compareStackTemplate` call:

```go
if tc.svcStackPath == "svc-ipv6-test.stack.yml" {
    require.NoError(t, os.WriteFile(filepath.Join("testdata", "workloads", tc.svcStackPath), actualBytes, 0644))
    t.Skip("generated golden — remove this hack and re-run")
}
```

Run:

```bash
go test -tags integration ./internal/pkg/deploy/cloudformation/stack/ -run 'TestLoadBalancedWebService_TemplateInteg$/CF_Template_should_be_equal/ipv6_env' -v
```

Then remove the hack.

- [ ] **Step 4: Generate the params.json equivalently**

Same pattern — look at how the existing test writes params (around `serializer.SerializedParameters()` below line 168). Mirror the hack.

- [ ] **Step 5: Grep the generated golden for expected AAAA records**

```bash
grep -nE "Type: AAAA|Type: A$" \
  internal/pkg/deploy/cloudformation/stack/testdata/workloads/svc-ipv6-test.stack.yml
```

Expected: at least one `Type: AAAA` alongside `Type: A` in a `RecordSetGroup`.

- [ ] **Step 6: Repeat for BackendService**

Do the same in `backend_svc_integration_test.go` — add an IPv6 test case with envIPv6=true, generate `testdata/workloads/backend/svc-ipv6-test.stack.yml`, confirm AAAA emission.

- [ ] **Step 7: Run the full suite for sanity**

```bash
go test -tags integration ./internal/pkg/deploy/cloudformation/stack/ -run TestLoadBalancedWebService_TemplateInteg -v
go test -tags integration ./internal/pkg/deploy/cloudformation/stack/ -run TestBackendService_TemplateInteg -v
```

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/pkg/deploy/cloudformation/stack/testdata/workloads/svc-ipv6-test.stack.yml \
        internal/pkg/deploy/cloudformation/stack/testdata/workloads/svc-ipv6-test.params.json \
        internal/pkg/deploy/cloudformation/stack/testdata/workloads/backend/svc-ipv6-test.stack.yml \
        internal/pkg/deploy/cloudformation/stack/lb_web_service_integration_test.go \
        internal/pkg/deploy/cloudformation/stack/backend_svc_integration_test.go
git commit -m "$(cat <<'EOF'
Stack integration: workload goldens for LBWS + BackendService in IPv6 envs

Each new golden asserts parallel AAAA RecordSets next to existing A
records in https-listener / http-listener partials. Non-IPv6 goldens
remain byte-identical.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 16: E2E skeleton for dual-stack LB ingress

**Files:**
- Create: `e2e/lb_ipv6/lb_ipv6_suite_test.go`
- Create: `e2e/lb_ipv6/lb_ipv6_test.go`

Context: Sub-project #2 established the convention that E2E skeletons ship with fully-documented `Skip(...)` stubs — they describe the setup and AWS SDK assertions but don't run until the `e2e/internal/client` package grows describe helpers. Mirror that convention.

- [ ] **Step 1: Create the suite file**

Create `e2e/lb_ipv6/lb_ipv6_suite_test.go`:

```go
// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package lb_ipv6_test

import (
	"testing"

	"github.com/aws/copilot-cli/e2e/internal/client"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var (
	cli     *client.CLI
	appName string
)

func TestLBIPv6(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "LB IPv6 Suite")
}

var _ = BeforeSuite(func() {
	var err error
	cli, err = client.NewCLI()
	Expect(err).NotTo(HaveOccurred())
	appName = "e2e-lb-ipv6"
	_, err = cli.AppInit(&client.AppInitRequest{AppName: appName})
	Expect(err).NotTo(HaveOccurred())
})

var _ = AfterSuite(func() {
	_, err := cli.AppDelete()
	Expect(err).NotTo(HaveOccurred())
})
```

- [ ] **Step 2: Create the scenarios file**

Create `e2e/lb_ipv6/lb_ipv6_test.go`:

```go
// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package lb_ipv6_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Public ALB Dual-Stack Ingress", Ordered, func() {
	When("deployed into an IPv6-enabled env, a LoadBalancedWebService", func() {
		It("makes the public ALB dualstack", func() {
			_ = appName
			_ = cli
			_ = Expect
			// Setup:
			//   1. Write copilot/environments/test/manifest.yml with
			//      network.vpc.ipv6.enabled: true and http.public.certificates.
			//   2. copilot env init / env deploy.
			//   3. copilot svc init --name frontend --svc-type "Load Balanced Web Service".
			//   4. copilot svc deploy.
			//
			// Assertions via AWS SDK:
			//   - ELBv2 DescribeLoadBalancers: public ALB has IpAddressType == "dualstack".
			//   - EC2 DescribeSecurityGroups: the PublicHTTPLoadBalancerSecurityGroup
			//     has an IpPermissions entry with Ipv6Ranges[0].CidrIpv6 == "::/0"
			//     and FromPort/ToPort == 80.
			Skip("TODO: fill in once e2e/internal/client has ELBv2/EC2 describe helpers")
		})

		It("emits AAAA record for the service alias", func() {
			_ = appName
			_ = cli
			_ = Expect
			// Setup: same as above.
			//
			// Assertions:
			//   - Route 53 ListResourceRecordSets on the app hosted zone.
			//     Filter records by Name == "<frontend>.<test>.<app>.<domain>".
			//     Assert at least one record has Type == "AAAA" and its
			//     AliasTarget.HostedZoneId equals the public ALB's
			//     CanonicalHostedZoneId from DescribeLoadBalancers.
			Skip("TODO: fill in once e2e/internal/client has Route53 helpers")
		})

		It("serves HTTP over IPv6 end-to-end", func() {
			_ = appName
			_ = cli
			_ = Expect
			// Setup: same as above.
			//
			// Assertions via an IPv6-capable ECS task running curl:
			//   - RunTask with a probe container that runs
			//     `curl -6 --max-time 20 --fail http://<alias>/` and writes
			//     the HTTP status to CloudWatch Logs.
			//   - FilterLogEvents: look for HTTP 200.
			//   - Also run `curl -4` and assert it also succeeds (v4 still works).
			Skip("TODO: fill in once e2e/internal/client has RunTask/CloudWatch Logs helpers")
		})
	})
})

var _ = Describe("Internal ALB Dual-Stack Ingress", Ordered, func() {
	When("deployed with http.private.security_groups.ingress.from_vpc: true", func() {
		It("gives the internal ALB dualstack and a ::/0 SG rule", func() {
			_ = appName
			_ = cli
			_ = Expect
			// Setup:
			//   1. Env with network.vpc.ipv6.enabled: true AND
			//      http.private.security_groups.ingress.from_vpc: true.
			//   2. BackendService with http.path: "/".
			//
			// Assertions via AWS SDK:
			//   - ELBv2 DescribeLoadBalancers: internal ALB IpAddressType == "dualstack".
			//   - EC2 DescribeSecurityGroups: InternalLoadBalancerSecurityGroup
			//     contains IpPermissions entries with Ipv6Ranges[0].CidrIpv6 == "::/0"
			//     for both 80 and 443.
			Skip("TODO: fill in once e2e/internal/client has ELBv2/EC2 helpers")
		})
	})
})

var _ = Describe("IPv4-only env is unaffected", Ordered, func() {
	When("the env has network.vpc.ipv6.enabled: false (default)", func() {
		It("does not render IpAddressType: dualstack or AAAA records", func() {
			_ = appName
			_ = cli
			_ = Expect
			// Setup: env with default config (no ipv6 stanza), LBWS deployed.
			//
			// Assertions:
			//   - ELBv2 DescribeLoadBalancers: public ALB IpAddressType == "ipv4".
			//   - Route 53: no AAAA record for the service alias.
			Skip("TODO: fill in once e2e/internal/client has Route53/ELBv2 helpers")
		})
	})
})
```

- [ ] **Step 3: Verify the skeleton compiles**

```bash
go build ./e2e/lb_ipv6/...
```

Expected: no errors.

- [ ] **Step 4: Verify Ginkgo discovers but skips the specs (no AWS credentials required for Skip)**

```bash
go test ./e2e/lb_ipv6/ -v 2>&1 | tail -20
```

Expected: the suite runs, every spec is `SKIP`ped with the TODO message.

- [ ] **Step 5: Commit**

```bash
git add e2e/lb_ipv6/
git commit -m "$(cat <<'EOF'
E2E: LB IPv6 suite skeleton

Three Describe blocks: Public ALB dual-stack, Internal ALB dual-stack,
and v4-only regression. All specs are Skip()ed with documented setup
and AWS SDK assertions — matches the convention #2 established.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 17: Documentation and CHANGELOG

**Files:**
- Modify: `site/content/docs/manifest/environment.en.md` (existing `ipv6` section)
- Modify: `CHANGELOG.md`

- [ ] **Step 1: Find the existing ipv6 section**

```bash
grep -n "ipv6\|IPv6" site/content/docs/manifest/environment.en.md | head -20
```

- [ ] **Step 2: Extend the ipv6 section with LB-ingress notes**

After the existing description of `ipv6.enabled`, append:

```markdown
When enabled, copilot also configures the shared public and internal
Application Load Balancers to run in `dualstack` mode. Every
LoadBalancedWebService and BackendService alias gets a Route 53 AAAA
record alongside the existing A record, so clients can reach the
service over IPv6. Security group ingress on the shared ALBs is opened
for `::/0` (or, when `http.public.security_groups.ingress` supplies
explicit CIDRs, v6 CIDRs in that list render as `CidrIpv6`).

IPv6 ingress is not available for per-workload Network Load Balancers
yet — that sub-project is tracked separately.
```

- [ ] **Step 3: Add CHANGELOG entry**

```bash
grep -nE "^## |Unreleased" CHANGELOG.md | head -5
```

Add under the Unreleased section (or the top entry for the in-progress `feature/ipv6-support` umbrella):

```markdown
- Route internet IPv6 traffic to services in dual-stack environments.
  Shared Application Load Balancers run in `dualstack` mode, security
  groups accept IPv6 ingress, and Route 53 AAAA records are emitted
  alongside A records for every LoadBalancedWebService and
  BackendService alias.
```

- [ ] **Step 4: Commit**

```bash
git add site/content/docs/manifest/environment.en.md CHANGELOG.md
git commit -m "$(cat <<'EOF'
Docs: document dual-stack LB ingress and AAAA record emission

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 18: Final end-to-end verification

**Files:** none (verification only).

- [ ] **Step 1: Run all unit tests**

```bash
make run-unit-test 2>&1 | tail -30
```

Expected: 0 failures.

- [ ] **Step 2: Run local integration**

```bash
go test -tags localintegration ./internal/pkg/deploy/cloudformation/stack/ -v 2>&1 | tail -20
```

Expected: all tests PASS.

- [ ] **Step 3: Verify mocks are up-to-date**

```bash
make gen-mocks
git status
```

Expected: `git status` clean (no mock files touched; no interface changed).

- [ ] **Step 4: Lint**

```bash
make lint 2>&1 | tail -20 || golangci-lint run ./... 2>&1 | tail -20
```

Expected: no new errors.

- [ ] **Step 5: License headers**

```bash
./scripts/license.sh .
```

Expected: no violations. Any new file needs the Apache 2.0 header (copy from any existing file in the same package).

- [ ] **Step 6: Confirm byte-identity for v4-only envs — the most important regression gate**

```bash
go test -tags localintegration ./internal/pkg/deploy/cloudformation/stack/ -run 'TestEnvStack_Template$' -v 2>&1 | grep -E "PASS|FAIL"
```

Expected: every subtest PASSes, including all the v4-only fixtures and the IPv6 fixtures.

- [ ] **Step 7: Summary commit if any fix-up needed**

If earlier tasks missed anything (typically license headers on new E2E files or a mock regen), commit those fixes here:

```bash
git add -A
git status
git commit -m "$(cat <<'EOF'
Post-integration fix-ups for sub-project #3

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

- [ ] **Step 8: Push the implementation branch**

```bash
git push -u origin feature/ipv6-lb-ingress
```

Do NOT merge to `feature/ipv6-support` from within this plan — the human reviewer opens the merge PR.

---

## Rollout

After the implementation branch merges back into `feature/ipv6-support`:

1. Update `docs/superpowers/specs/2026-04-20-ipv6-support-roadmap.md` status table: sub-project #3 → `✅ DONE`.
2. Add the merge SHA to the roadmap.
3. Sub-project #4 (imported-VPC IPv6) becomes startable.

These are out of scope for this plan's branch — they land on `feature/ipv6-support` directly, after the merge.
