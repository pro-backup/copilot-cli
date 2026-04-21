# Env-Managed Dual-Stack VPC (IPv6 Foundation) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add opt-in dual-stack (IPv4 + IPv6) support to copilot-managed environment VPCs, so downstream sub-projects can enable IPv6 task egress, dual-stack load balancers, and eventual NAT-Gateway removal.

**Architecture:** A new optional `network.vpc.ipv6.enabled` field on the environment manifest flips a render-time flag (`VPCConfig.IPv6Enabled`) threaded into the env CloudFormation template. When on, the template additionally provisions an Amazon-provided `/56` IPv6 CIDR, `/64` blocks on every subnet, an `EgressOnlyInternetGateway`, IPv6 default routes (public → IGW, private → EgressOnlyIGW), a widened `CreatePrivateRouteTables` gate so IPv6-with-no-NAT still has a route table, and new stack outputs (`IPv6Enabled`, `VpcIpv6CidrBlock`, `PublicSubnetsIpv6CidrBlocks`, `PrivateSubnetsIpv6CidrBlocks`). Imported VPCs and toggling IPv6 on existing environments are firm-errored in this sub-project and deferred to #4/#5.

**Tech Stack:** Go 1.23, Cobra CLI, AWS CloudFormation, Go `text/template`, `golang/mock`, `testify`, Ginkgo v2 (E2E), `afero` for template FS mocking.

**Spec:** `docs/superpowers/specs/2026-04-20-env-managed-dualstack-vpc-design.md`

---

## File Structure

### Files to modify

| Path | Responsibility |
|---|---|
| `internal/pkg/manifest/env.go` | New `ipv6VPCConfig` struct, field on `environmentVPCConfig`, `IPv6Enabled()` accessor |
| `internal/pkg/manifest/validate_env.go` | Firm error when `ipv6.enabled: true` combined with imported VPC |
| `internal/pkg/manifest/env_test.go` | YAML round-trip + accessor unit tests |
| `internal/pkg/manifest/validate_env_test.go` | Validation unit tests |
| `internal/pkg/template/env.go` | `IPv6Enabled` field on `template.VPCConfig`; `add` template helper in env FuncMap |
| `internal/pkg/template/env_test.go` | `add` helper unit test |
| `internal/pkg/template/templates/environment/cf.yml` | `CreatePrivateRouteTables` condition (IPv6-only emission), new stack outputs |
| `internal/pkg/template/templates/environment/partials/vpc-resources.yml` | IPv6 VPC CIDR, EgressOnlyIGW, subnet IPv6 blocks, public IPv6 default route |
| `internal/pkg/template/templates/environment/partials/nat-gateways.yml` | Private IPv6 routes, conditional rewrite of private route-table gate |
| `internal/pkg/deploy/cloudformation/stack/env.go` | Thread `IPv6Enabled` into `template.VPCConfig` |
| `internal/pkg/deploy/cloudformation/stack/env_integration_test.go` | New golden test case for IPv6-enabled env |
| `internal/pkg/deploy/cloudformation/stack/testdata/environments/template-with-ipv6-enabled.yml` | New golden fixture |
| `internal/pkg/cli/env_deploy.go` | Wire stack-state validation (IPv6 toggle → firm error) |
| `site/content/docs/manifest/environment.en.md` | Documentation for `ipv6.enabled` manifest field |
| `CHANGELOG.md` | Entry for the new opt-in feature |

### Files to create

| Path | Responsibility |
|---|---|
| `e2e/env_ipv6/env_ipv6_suite_test.go` | Ginkgo v2 suite bootstrap for the new E2E |
| `e2e/env_ipv6/env_ipv6_test.go` | E2E scenarios: IPv6 + NAT, and IPv6 + no-NAT |

---

## Task 1: Manifest struct and accessor

**Files:**
- Modify: `internal/pkg/manifest/env.go:117-127` (`environmentVPCConfig` struct)
- Modify: `internal/pkg/manifest/env.go:340-350` (add accessor near `imported()`/`managedVPCCustomized()`)
- Test: `internal/pkg/manifest/env_test.go`

- [ ] **Step 1: Write the failing unit tests**

Append to `internal/pkg/manifest/env_test.go`:

```go
func TestEnvironmentVPCConfig_IPv6Enabled(t *testing.T) {
	testCases := map[string]struct {
		yaml   string
		wantOn bool
	}{
		"field absent": {
			yaml:   "cidr: 10.0.0.0/16\n",
			wantOn: false,
		},
		"ipv6 empty map": {
			yaml:   "cidr: 10.0.0.0/16\nipv6: {}\n",
			wantOn: false,
		},
		"ipv6.enabled false": {
			yaml:   "cidr: 10.0.0.0/16\nipv6:\n  enabled: false\n",
			wantOn: false,
		},
		"ipv6.enabled true": {
			yaml:   "cidr: 10.0.0.0/16\nipv6:\n  enabled: true\n",
			wantOn: true,
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			var cfg environmentVPCConfig
			require.NoError(t, yaml.Unmarshal([]byte(tc.yaml), &cfg))
			require.Equal(t, tc.wantOn, cfg.IPv6Enabled())
		})
	}
}

func TestEnvironmentVPCConfig_IPv6Enabled_RoundTrip(t *testing.T) {
	in := "cidr: 10.0.0.0/16\nipv6:\n  enabled: true\n"
	var cfg environmentVPCConfig
	require.NoError(t, yaml.Unmarshal([]byte(in), &cfg))

	out, err := yaml.Marshal(cfg)
	require.NoError(t, err)
	var cfg2 environmentVPCConfig
	require.NoError(t, yaml.Unmarshal(out, &cfg2))
	require.True(t, cfg2.IPv6Enabled())
}
```

Confirm `"gopkg.in/yaml.v3"` is in the import block (the package uses it heavily; it should already be present).

- [ ] **Step 2: Run the tests and confirm they fail**

```
go test ./internal/pkg/manifest/ -run TestEnvironmentVPCConfig_IPv6Enabled -v
```

Expected: compile error `cfg.IPv6Enabled undefined` and/or `ipv6 field not found`.

- [ ] **Step 3: Implement `ipv6VPCConfig` struct and `IPv6Enabled()` accessor**

Edit `internal/pkg/manifest/env.go` — modify the `environmentVPCConfig` struct (lines 121-127):

```go
type environmentVPCConfig struct {
	ID                  *string                       `yaml:"id,omitempty"`
	CIDR                *IPNet                        `yaml:"cidr,omitempty"`
	IPv6                *ipv6VPCConfig                `yaml:"ipv6,omitempty"`
	Subnets             subnetsConfiguration          `yaml:"subnets,omitempty"`
	SecurityGroupConfig securityGroupConfig           `yaml:"security_group,omitempty"`
	FlowLogs            Union[*bool, VPCFlowLogsArgs] `yaml:"flow_logs,omitempty"`
}

// ipv6VPCConfig configures IPv6 dual-stack networking on the managed VPC.
// Only `Enabled` is honored in the foundation sub-project; reserved fields
// (BYOIP CIDR, IPAM pool) will land in a follow-up.
type ipv6VPCConfig struct {
	Enabled *bool `yaml:"enabled,omitempty"`
}
```

Add the accessor near the existing `imported()` / `managedVPCCustomized()` helpers (around line 349):

```go
// IPv6Enabled reports whether the environment manifest opts into
// IPv6 dual-stack networking. Returns false when the field is absent,
// the struct is empty, or `enabled` is explicitly false.
func (cfg *environmentVPCConfig) IPv6Enabled() bool {
	if cfg == nil || cfg.IPv6 == nil {
		return false
	}
	return aws.BoolValue(cfg.IPv6.Enabled)
}
```

- [ ] **Step 4: Run tests and confirm they pass**

```
go test ./internal/pkg/manifest/ -run TestEnvironmentVPCConfig_IPv6Enabled -v
```

Expected: 5 subtests pass (4 table cases + round-trip).

- [ ] **Step 5: Run the broader manifest test package to ensure no regressions**

```
go test ./internal/pkg/manifest/ -count=1
```

Expected: all tests pass.

- [ ] **Step 6: Commit**

```bash
git add internal/pkg/manifest/env.go internal/pkg/manifest/env_test.go
git commit -m "$(cat <<'EOF'
Add ipv6VPCConfig manifest struct for dual-stack VPC opt-in

First slice of IPv6 support (issue aws/copilot-cli#5339, sub-project #1).
Adds an optional network.vpc.ipv6.enabled field on the environment
manifest and an IPv6Enabled() accessor. No behavior change yet — the
flag is read by downstream template plumbing in later commits.
EOF
)"
```

---

## Task 2: Manifest validation — reject IPv6 on imported VPCs

**Files:**
- Modify: `internal/pkg/manifest/validate_env.go:86-107` (`environmentVPCConfig.validate()`)
- Test: `internal/pkg/manifest/validate_env_test.go`

- [ ] **Step 1: Write the failing unit tests**

Append to `internal/pkg/manifest/validate_env_test.go`:

```go
func TestEnvironmentVPCConfig_validate_IPv6Imported(t *testing.T) {
	trueVal := true
	cfg := environmentVPCConfig{
		ID:   aws.String("vpc-12345"),
		IPv6: &ipv6VPCConfig{Enabled: &trueVal},
	}
	err := cfg.validate()
	require.Error(t, err)
	require.ErrorIs(t, err, errIPv6WithImportedVPC)
}

func TestEnvironmentVPCConfig_validate_IPv6ManagedOK(t *testing.T) {
	trueVal := true
	cfg := environmentVPCConfig{
		IPv6: &ipv6VPCConfig{Enabled: &trueVal},
	}
	require.NoError(t, cfg.validate())
}

func TestEnvironmentVPCConfig_validate_IPv6DisabledWithImport(t *testing.T) {
	falseVal := false
	cfg := environmentVPCConfig{
		ID:   aws.String("vpc-12345"),
		IPv6: &ipv6VPCConfig{Enabled: &falseVal},
	}
	// imported VPC with ipv6 disabled is fine
	require.NoError(t, cfg.validate())
}
```

Confirm the file imports `github.com/aws/aws-sdk-go/aws` and `github.com/stretchr/testify/require`.

- [ ] **Step 2: Run tests and confirm they fail**

```
go test ./internal/pkg/manifest/ -run TestEnvironmentVPCConfig_validate_IPv6 -v
```

Expected: compile error `undefined: errIPv6WithImportedVPC`.

- [ ] **Step 3: Implement the sentinel error and validation branch**

Edit `internal/pkg/manifest/validate_env.go`. Near the top of the file (alongside other error declarations) add:

```go
// errIPv6WithImportedVPC is returned when a manifest enables IPv6 on an
// imported VPC. Imported-VPC IPv6 support is tracked as a follow-up
// sub-project (#4).
var errIPv6WithImportedVPC = errors.New(
	`IPv6 cannot be enabled on an environment that imports a VPC. ` +
		`Remove "network.vpc.id" or "network.vpc.ipv6".`,
)
```

Modify `environmentVPCConfig.validate()` (lines 86-107) — insert the new check right after the existing `imported() && managedVPCCustomized()` guard:

```go
func (cfg environmentVPCConfig) validate() error {
	if cfg.imported() && cfg.managedVPCCustomized() {
		return errors.New(`cannot import VPC resources (with "id" fields) and customize VPC resources (with "cidr" and "az" fields) at the same time`)
	}
	if cfg.imported() && (&cfg).IPv6Enabled() {
		return errIPv6WithImportedVPC
	}
	if err := cfg.Subnets.validate(); err != nil {
		return fmt.Errorf(`validate "subnets": %w`, err)
	}
	// ...remainder unchanged
```

Note: `validate()` is a value receiver; `IPv6Enabled()` is a pointer receiver. Use `(&cfg).IPv6Enabled()` to keep the signature change local.

- [ ] **Step 4: Run tests and confirm they pass**

```
go test ./internal/pkg/manifest/ -run TestEnvironmentVPCConfig_validate_IPv6 -v
```

Expected: 3 subtests pass.

- [ ] **Step 5: Run full package tests to catch regressions**

```
go test ./internal/pkg/manifest/ -count=1
```

Expected: all tests pass.

- [ ] **Step 6: Commit**

```bash
git add internal/pkg/manifest/validate_env.go internal/pkg/manifest/validate_env_test.go
git commit -m "$(cat <<'EOF'
Validate: reject IPv6 with imported VPC in env manifest

Imported-VPC IPv6 is tracked as sub-project #4. Surfacing it as a firm
validation error today prevents silent partial-deploy of a combination
we don't support yet.
EOF
)"
```

---

## Task 3: Add `IPv6Enabled` to template.VPCConfig and `add` template helper

**Files:**
- Modify: `internal/pkg/template/env.go:199-206` (`VPCConfig` struct)
- Modify: `internal/pkg/template/env.go:293-304` (`withEnvParsingFuncs`)
- Test: `internal/pkg/template/env_test.go`

- [ ] **Step 1: Write the failing unit test for the `add` helper**

Append to `internal/pkg/template/env_test.go`:

```go
func TestWithEnvParsingFuncs_AddHelper(t *testing.T) {
	tpl := template.New("t")
	tpl = withEnvParsingFuncs()(tpl)
	parsed, err := tpl.Parse(`{{add 3 4}}`)
	require.NoError(t, err)
	var buf bytes.Buffer
	require.NoError(t, parsed.Execute(&buf, nil))
	require.Equal(t, "7", buf.String())
}
```

Add imports `"bytes"` and `"text/template"` if not present.

- [ ] **Step 2: Run test and confirm it fails**

```
go test ./internal/pkg/template/ -run TestWithEnvParsingFuncs_AddHelper -v
```

Expected: `function "add" not defined`.

- [ ] **Step 3: Add the `add` helper**

Edit `internal/pkg/template/env.go:293-304`:

```go
func withEnvParsingFuncs() ParseOption {
	return func(t *template.Template) *template.Template {
		return t.Funcs(map[string]interface{}{
			"inc":               IncFunc,
			"add":               func(a, b int) int { return a + b },
			"fmtSlice":          FmtSliceFunc,
			"quote":             strconv.Quote,
			"truncate":          truncate,
			"bucketNameFromURL": bucketNameFromURL,
			"logicalIDSafe":     StripNonAlphaNumFunc,
		})
	}
}
```

- [ ] **Step 4: Add `IPv6Enabled` field to `template.VPCConfig`**

Edit `internal/pkg/template/env.go:199-206`:

```go
// VPCConfig represents the VPC configuration.
type VPCConfig struct {
	Imported            *ImportVPC
	Managed             ManagedVPC
	AllowVPCIngress     bool
	SecurityGroupConfig *SecurityGroupConfig
	FlowLogs            *VPCFlowLogs
	IPv6Enabled         bool // Opt-in dual-stack networking on the managed VPC.
}
```

- [ ] **Step 5: Run tests and confirm pass**

```
go test ./internal/pkg/template/ -count=1
```

Expected: all tests pass, including the new `TestWithEnvParsingFuncs_AddHelper`.

- [ ] **Step 6: Commit**

```bash
git add internal/pkg/template/env.go internal/pkg/template/env_test.go
git commit -m "$(cat <<'EOF'
Add IPv6Enabled to template.VPCConfig and 'add' template helper

Pure plumbing: downstream env CFN template needs both an IPv6Enabled
guard and index arithmetic (public_count + private_index) for IPv6
/64 subnet assignment.
EOF
)"
```

---

## Task 4: Thread IPv6Enabled from manifest into template.VPCConfig

**Files:**
- Modify: `internal/pkg/deploy/cloudformation/stack/env.go:525-541` (`vpcConfig()`)
- Test: `internal/pkg/deploy/cloudformation/stack/env_integration_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/pkg/deploy/cloudformation/stack/env_integration_test.go` (next to `TestEnvStack_Template`, use the file's existing imports):

```go
func TestEnvStack_IPv6ThreadedFromManifest(t *testing.T) {
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
	// Proof the flag reached the template: the EgressOnlyInternetGateway
	// resource only appears when IPv6Enabled is true.
	require.Contains(t, body, "EgressOnlyInternetGateway:")
}
```

- [ ] **Step 2: Run and confirm failure**

```
go test -tags 'integration localintegration' ./internal/pkg/deploy/cloudformation/stack/ -run TestEnvStack_IPv6ThreadedFromManifest -v
```

Expected: fails because the template body does not yet contain `EgressOnlyInternetGateway:`. This test stays red until Task 5.

- [ ] **Step 3: Wire `IPv6Enabled` through `vpcConfig()`**

Edit `internal/pkg/deploy/cloudformation/stack/env.go:525-541`:

```go
func (e *Env) vpcConfig() (template.VPCConfig, error) {
	securityGroupConfig, err := convertEnvSecurityGroupCfg(e.in.Mft)
	if err != nil {
		return template.VPCConfig{}, err
	}
	flowLogs, err := convertFlowLogsConfig(e.in.Mft)
	if err != nil {
		return template.VPCConfig{}, err
	}
	var ipv6Enabled bool
	if e.in.Mft != nil {
		ipv6Enabled = e.in.Mft.Network.VPC.IPv6Enabled()
	}
	return template.VPCConfig{
		Imported:            e.importVPC(),
		Managed:             e.managedVPC(),
		AllowVPCIngress:     e.in.Mft.HTTPConfig.Private.HasVPCIngress(),
		SecurityGroupConfig: securityGroupConfig,
		FlowLogs:            flowLogs,
		IPv6Enabled:         ipv6Enabled,
	}, nil
}
```

- [ ] **Step 4: Verify the test still fails (template doesn't yet emit the resource)**

```
go test -tags 'integration localintegration' ./internal/pkg/deploy/cloudformation/stack/ -run TestEnvStack_IPv6ThreadedFromManifest -v
```

Expected: still fails with "does not contain EgressOnlyInternetGateway:". The flag is now threaded but the template doesn't use it — Task 5-9 add the template work.

- [ ] **Step 5: Commit**

```bash
git add internal/pkg/deploy/cloudformation/stack/env.go internal/pkg/deploy/cloudformation/stack/env_integration_test.go
git commit -m "$(cat <<'EOF'
Thread IPv6Enabled from env manifest into template.VPCConfig

Wiring commit. The template-side guards still need to land; the new
TestEnvStack_IPv6ThreadedFromManifest will stay red until Task 5.
EOF
)"
```

---

## Task 5: CFN — add IPv6 base resources (VPCCidrBlock + EgressOnlyInternetGateway)

**Files:**
- Modify: `internal/pkg/template/templates/environment/partials/vpc-resources.yml`

- [ ] **Step 1: Edit the template**

Open `internal/pkg/template/templates/environment/partials/vpc-resources.yml`. Directly after `InternetGatewayAttachment` (ends at line 45), before the `{{- range ... PublicSubnetCIDRs }}` block at line 47, insert:

```yaml

{{- if .VPCConfig.IPv6Enabled }}
VPCCidrBlockIPv6:
  Metadata:
    'aws:copilot:description': 'An Amazon-provided IPv6 /56 CIDR block associated with the VPC'
  Type: AWS::EC2::VPCCidrBlock
  Properties:
    VpcId: !Ref VPC
    AmazonProvidedIpv6CidrBlock: true

EgressOnlyInternetGateway:
  Metadata:
    'aws:copilot:description': 'An egress-only internet gateway for private subnets to reach the IPv6 internet'
  Type: AWS::EC2::EgressOnlyInternetGateway
  Properties:
    VpcId: !Ref VPC
{{- end }}
```

- [ ] **Step 2: Run the threaded-flag test and confirm it passes**

```
go test -tags 'integration localintegration' ./internal/pkg/deploy/cloudformation/stack/ -run TestEnvStack_IPv6ThreadedFromManifest -v
```

Expected: PASS. `EgressOnlyInternetGateway:` is now in the rendered body.

- [ ] **Step 3: Run full env stack integration tests to confirm existing goldens are byte-identical**

```
go test -tags 'integration localintegration' ./internal/pkg/deploy/cloudformation/stack/ -run TestEnvStack_Template -v
```

Expected: all 7 existing subtests pass with no golden diffs. If a test fails with a YAML diff, the `{{- if }}` guard is leaking whitespace — tune the `{{- ... -}}` trimming.

- [ ] **Step 4: Commit**

```bash
git add internal/pkg/template/templates/environment/partials/vpc-resources.yml
git commit -m "$(cat <<'EOF'
Env CFN: add IPv6 VPCCidrBlock and EgressOnlyInternetGateway

Guarded by .VPCConfig.IPv6Enabled. Existing IPv4-only envs render
byte-identical output.
EOF
)"
```

---

## Task 6: CFN — IPv6 CIDR and auto-assignment on subnets

**Files:**
- Modify: `internal/pkg/template/templates/environment/partials/vpc-resources.yml:47-83` (both subnet `range` loops)

- [ ] **Step 1: Edit the public-subnet loop (lines 47-65)**

Rewrite to include the IPv6 `DependsOn` and IPv6 CIDR assignment. Preserve everything else exactly:

```yaml
{{- $azs := .AZs }}
{{- range $ind, $cidr := .PublicSubnetCIDRs}}
PublicSubnet{{inc $ind}}:
  Metadata:
    'aws:copilot:description': 'Public subnet {{inc $ind}} for resources that can access the internet'
  Type: AWS::EC2::Subnet
  {{- if $.VPCConfig.IPv6Enabled }}
  DependsOn: VPCCidrBlockIPv6
  {{- end }}
  Properties:
    CidrBlock: {{$cidr}}
    VpcId: !Ref VPC
    {{- if $azs }}
    AvailabilityZone: {{index $azs $ind}}
    {{- else }}
    AvailabilityZone: !Select [ {{$ind}}, !GetAZs '' ]
    {{- end }}
    MapPublicIpOnLaunch: true
    {{- if $.VPCConfig.IPv6Enabled }}
    Ipv6CidrBlock: !Select [{{$ind}}, !Cidr [!Select [0, !GetAtt VPC.Ipv6CidrBlocks], 256, 64]]
    AssignIpv6AddressOnCreation: true
    {{- end }}
    Tags:
      - Key: Name
        Value: !Sub 'copilot-${AppName}-${EnvironmentName}-pub{{$ind}}'
{{- end }}
```

- [ ] **Step 2: Edit the private-subnet loop (lines 66-83)**

Capture the public count outside the loop so IPv6 index arithmetic is correct regardless of configured subnet count:

```yaml
{{- $publicCount := len .PublicSubnetCIDRs }}
{{- range $ind, $cidr := .PrivateSubnetCIDRs }}
PrivateSubnet{{inc $ind}}:
  Metadata:
    'aws:copilot:description': 'Private subnet {{inc $ind}} for resources with no internet access'
  Type: AWS::EC2::Subnet
  {{- if $.VPCConfig.IPv6Enabled }}
  DependsOn: VPCCidrBlockIPv6
  {{- end }}
  Properties:
    CidrBlock: {{$cidr}}
    VpcId: !Ref VPC
    {{- if $azs }}
    AvailabilityZone: {{index $azs $ind}}
    {{- else }}
    AvailabilityZone: !Select [ {{$ind}}, !GetAZs '' ]
    {{- end }}
    MapPublicIpOnLaunch: false
    {{- if $.VPCConfig.IPv6Enabled }}
    Ipv6CidrBlock: !Select [{{add $publicCount $ind}}, !Cidr [!Select [0, !GetAtt VPC.Ipv6CidrBlocks], 256, 64]]
    AssignIpv6AddressOnCreation: true
    {{- end }}
    Tags:
      - Key: Name
        Value: !Sub 'copilot-${AppName}-${EnvironmentName}-priv{{$ind}}'
{{- end }}
```

- [ ] **Step 3: Run env stack integration tests — existing goldens must stay identical**

```
go test -tags 'integration localintegration' ./internal/pkg/deploy/cloudformation/stack/ -run TestEnvStack_Template -v
```

Expected: all 7 existing subtests pass with no diffs.

- [ ] **Step 4: Run the threaded-flag test — it should still pass**

```
go test -tags 'integration localintegration' ./internal/pkg/deploy/cloudformation/stack/ -run TestEnvStack_IPv6ThreadedFromManifest -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/pkg/template/templates/environment/partials/vpc-resources.yml
git commit -m "$(cat <<'EOF'
Env CFN: assign IPv6 /64 to every subnet when dual-stack is enabled

Public subnets carve from index 0..N-1, private from N..N+M-1 of the
VPC /56. Uses Fn::Cidr with a fixed count of 256 so any configured
subnet count is supported.
EOF
)"
```

---

## Task 7: CFN — add public IPv6 default route

**Files:**
- Modify: `internal/pkg/template/templates/environment/partials/vpc-resources.yml:24-30` (after existing `DefaultPublicRoute`)

- [ ] **Step 1: Edit the template**

Right after the existing `DefaultPublicRoute` resource (ends at line 30, before `InternetGateway` at line 32), insert:

```yaml

{{- if .VPCConfig.IPv6Enabled }}
DefaultPublicRouteIPv6:
  Type: AWS::EC2::Route
  DependsOn: InternetGatewayAttachment
  Properties:
    RouteTableId: !Ref PublicRouteTable
    DestinationIpv6CidrBlock: ::/0
    GatewayId: !Ref InternetGateway
{{- end }}
```

- [ ] **Step 2: Run tests to confirm existing goldens unchanged**

```
go test -tags 'integration localintegration' ./internal/pkg/deploy/cloudformation/stack/ -run TestEnvStack_Template -v
```

Expected: PASS, all 7 goldens byte-identical.

- [ ] **Step 3: Commit**

```bash
git add internal/pkg/template/templates/environment/partials/vpc-resources.yml
git commit -m "$(cat <<'EOF'
Env CFN: add IPv6 public default route (::/0 → IGW)

Single route on the shared PublicRouteTable, gated on IPv6Enabled.
EOF
)"
```

---

## Task 8: CFN — widen private-route-table gate to IPv6-enabled envs

**Files:**
- Modify: `internal/pkg/template/templates/environment/cf.yml:56-58` (add new condition, guarded)
- Modify: `internal/pkg/template/templates/environment/partials/nat-gateways.yml:21-38` (conditionally rewrite gates)

- [ ] **Step 1: Add the new CFN condition to cf.yml**

Edit `internal/pkg/template/templates/environment/cf.yml:56-58`. Immediately after the existing `CreateNATGateways:` block, insert:

```yaml
  CreateNATGateways:
    !Not [!Equals [ !Ref NATWorkloads, ""]]
{{- if .VPCConfig.IPv6Enabled }}
  CreatePrivateRouteTables: !Or
    - !Condition CreateNATGateways
    - !Equals [ "true", "true" ]
{{- end }}
  CreateAppRunnerVPCEndpoint:
```

(The `!Equals [ "true", "true" ]` clause ensures `CreatePrivateRouteTables` is always true when IPv6 is on. No new CFN parameter needed.)

- [ ] **Step 2: Edit `nat-gateways.yml` to swap the condition on private route-table resources**

Rewrite the range loop so `PrivateRouteTable{N}` and `PrivateRouteTable{N}Association` use the widened condition when IPv6 is on. Leave `NatGateway`, `EIP`, and `PrivateRoute` (v4 default) on `CreateNATGateways`:

```yaml
{{- range $ind, $cidr := .PrivateSubnetCIDRs}}
NatGateway{{inc $ind}}Attachment:
  Metadata:
    'aws:copilot:description': 'An Elastic IP for NAT Gateway {{inc $ind}}'
  Type: AWS::EC2::EIP
  Condition: CreateNATGateways
  DependsOn: InternetGatewayAttachment
  Properties:
    Domain: vpc
NatGateway{{inc $ind}}:
  Metadata:
    'aws:copilot:description': 'NAT Gateway {{inc $ind}} enabling workloads placed in private subnet {{inc $ind}} to reach the internet'
  Type: AWS::EC2::NatGateway
  Condition: CreateNATGateways
  Properties:
    AllocationId: !GetAtt NatGateway{{inc $ind}}Attachment.AllocationId
    SubnetId: !Ref PublicSubnet{{inc $ind}}
    Tags:
      - Key: Name
        Value: !Sub 'copilot-${AppName}-${EnvironmentName}-{{$ind}}'
PrivateRouteTable{{inc $ind}}:
  Type: AWS::EC2::RouteTable
  {{- if $.VPCConfig.IPv6Enabled }}
  Condition: CreatePrivateRouteTables
  {{- else }}
  Condition: CreateNATGateways
  {{- end }}
  Properties:
    VpcId: !Ref 'VPC'
PrivateRoute{{inc $ind}}:
  Type: AWS::EC2::Route
  Condition: CreateNATGateways
  Properties:
    RouteTableId: !Ref PrivateRouteTable{{inc $ind}}
    DestinationCidrBlock: 0.0.0.0/0
    NatGatewayId: !Ref NatGateway{{inc $ind}}
PrivateRouteTable{{inc $ind}}Association:
  Type: AWS::EC2::SubnetRouteTableAssociation
  {{- if $.VPCConfig.IPv6Enabled }}
  Condition: CreatePrivateRouteTables
  {{- else }}
  Condition: CreateNATGateways
  {{- end }}
  Properties:
    RouteTableId: !Ref PrivateRouteTable{{inc $ind}}
    SubnetId: !Ref PrivateSubnet{{inc $ind}}
  {{- end}}
```

- [ ] **Step 3: Run env stack integration tests — existing goldens must stay byte-identical**

```
go test -tags 'integration localintegration' ./internal/pkg/deploy/cloudformation/stack/ -run TestEnvStack_Template -v
```

Expected: all 7 existing subtests pass. When `IPv6Enabled` is false, both the `cf.yml` insert and the `nat-gateways.yml` if/else branches produce the original output.

- [ ] **Step 4: Commit**

```bash
git add internal/pkg/template/templates/environment/cf.yml internal/pkg/template/templates/environment/partials/nat-gateways.yml
git commit -m "$(cat <<'EOF'
Env CFN: widen private-route-table gate for IPv6-enabled envs

New CreatePrivateRouteTables condition (emitted only when IPv6 is on)
guarantees private subnets have a route table even when NAT is disabled.
IPv4-only envs still use CreateNATGateways verbatim.
EOF
)"
```

---

## Task 9: CFN — private IPv6 default route per private subnet

**Files:**
- Modify: `internal/pkg/template/templates/environment/partials/nat-gateways.yml`

- [ ] **Step 1: Edit the template**

Inside the `range $ind, $cidr := .PrivateSubnetCIDRs` loop (edited in Task 8), add a guarded IPv6 route resource right after `PrivateRouteTable{N}Association` and before the closing `{{- end}}` of the outer range:

```yaml
PrivateRouteTable{{inc $ind}}Association:
  Type: AWS::EC2::SubnetRouteTableAssociation
  {{- if $.VPCConfig.IPv6Enabled }}
  Condition: CreatePrivateRouteTables
  {{- else }}
  Condition: CreateNATGateways
  {{- end }}
  Properties:
    RouteTableId: !Ref PrivateRouteTable{{inc $ind}}
    SubnetId: !Ref PrivateSubnet{{inc $ind}}
  {{- if $.VPCConfig.IPv6Enabled }}
PrivateRouteIPv6{{inc $ind}}:
  Type: AWS::EC2::Route
  Properties:
    RouteTableId: !Ref PrivateRouteTable{{inc $ind}}
    DestinationIpv6CidrBlock: ::/0
    EgressOnlyInternetGatewayId: !Ref EgressOnlyInternetGateway
  {{- end }}
  {{- end}}
```

- [ ] **Step 2: Run tests — existing goldens unchanged**

```
go test -tags 'integration localintegration' ./internal/pkg/deploy/cloudformation/stack/ -run TestEnvStack_Template -v
```

Expected: PASS, 7 goldens byte-identical.

- [ ] **Step 3: Commit**

```bash
git add internal/pkg/template/templates/environment/partials/nat-gateways.yml
git commit -m "$(cat <<'EOF'
Env CFN: add IPv6 default route per private subnet

::/0 → EgressOnlyInternetGateway, gated on IPv6Enabled. Attached to
the same private route table as the v4 default route.
EOF
)"
```

---

## Task 10: CFN outputs + new golden file

**Files:**
- Modify: `internal/pkg/template/templates/environment/cf.yml` (Outputs section)
- Create: `internal/pkg/deploy/cloudformation/stack/testdata/environments/template-with-ipv6-enabled.yml`
- Modify: `internal/pkg/deploy/cloudformation/stack/env_integration_test.go` (add the test case)

- [ ] **Step 1: Add the new stack outputs**

Find the `Outputs:` section in `internal/pkg/template/templates/environment/cf.yml`. Append the four new outputs, all guarded so existing goldens stay byte-identical:

```yaml
{{- if .VPCConfig.IPv6Enabled }}
  IPv6Enabled:
    Description: "Whether the environment VPC has IPv6 dual-stack enabled."
    Value: "true"
    Export:
      Name: !Sub ${AppName}-${EnvironmentName}-IPv6Enabled
  VpcIpv6CidrBlock:
    Description: "The Amazon-provided IPv6 /56 CIDR associated with the VPC."
    Value: !Select [0, !GetAtt VPC.Ipv6CidrBlocks]
    Export:
      Name: !Sub ${AppName}-${EnvironmentName}-VpcIpv6CidrBlock
  PublicSubnetsIpv6CidrBlocks:
    Description: "Comma-delimited list of IPv6 CIDR blocks for the public subnets."
    Value: !Join [ ',', [ {{range $ind, $cidr := .VPCConfig.Managed.PublicSubnetCIDRs}}!Select [0, !GetAtt PublicSubnet{{inc $ind}}.Ipv6CidrBlocks], {{end}}] ]
    Export:
      Name: !Sub ${AppName}-${EnvironmentName}-PublicSubnetsIpv6CidrBlocks
  PrivateSubnetsIpv6CidrBlocks:
    Description: "Comma-delimited list of IPv6 CIDR blocks for the private subnets."
    Value: !Join [ ',', [ {{range $ind, $cidr := .VPCConfig.Managed.PrivateSubnetCIDRs}}!Select [0, !GetAtt PrivateSubnet{{inc $ind}}.Ipv6CidrBlocks], {{end}}] ]
    Export:
      Name: !Sub ${AppName}-${EnvironmentName}-PrivateSubnetsIpv6CidrBlocks
{{- end }}
```

Pick an insertion point inside `Outputs:` immediately after the last existing Output and before any trailing template directive closing the Outputs block.

- [ ] **Step 2: Add the new test case referencing the new golden**

In `internal/pkg/deploy/cloudformation/stack/env_integration_test.go`, add an entry to the `TestEnvStack_Template` testCases map:

```go
"ipv6 dual-stack enabled": {
	input: func() *stack.EnvConfig {
		rawMft := `name: test
type: Environment
network:
  vpc:
    ipv6:
      enabled: true
`
		var mft manifest.Environment
		err := yaml.Unmarshal([]byte(rawMft), &mft)
		require.NoError(t, err)
		return &stack.EnvConfig{
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
	}(),
	wantedFileName: "template-with-ipv6-enabled.yml",
},
```

- [ ] **Step 3: Generate the golden file**

First check whether the existing test supports an update flag:

```bash
grep -n "update" internal/pkg/deploy/cloudformation/stack/env_integration_test.go
```

If an update flag exists, use it:

```
go test -tags 'integration localintegration' ./internal/pkg/deploy/cloudformation/stack/ -run TestEnvStack_Template -update
```

If not, instrument the test by adding a one-line `os.WriteFile` call when the golden doesn't exist, run the test to produce the file, then remove the instrumentation:

```go
// TEMPORARY: regenerate golden on mismatch
if _, err := os.Stat(goldenPath); os.IsNotExist(err) {
	_ = os.WriteFile(goldenPath, []byte(got), 0644)
}
```

- [ ] **Step 4: Verify the golden captures expected IPv6 structure**

Manually spot-check the generated golden file contains:
- `VPCCidrBlockIPv6:` block with `AmazonProvidedIpv6CidrBlock: true`
- `EgressOnlyInternetGateway:` block
- Each subnet has `Ipv6CidrBlock: !Select [...]` and `AssignIpv6AddressOnCreation: true`
- `DefaultPublicRouteIPv6:` block
- Per-private-subnet `PrivateRouteIPv6{N}:` blocks
- `CreatePrivateRouteTables:` condition
- All four new `Outputs:` entries

- [ ] **Step 5: Run full tests — all goldens pass**

```
go test -tags 'integration localintegration' ./internal/pkg/deploy/cloudformation/stack/ -run TestEnvStack_Template -v
```

Expected: 8 subtests pass (7 existing + new IPv6 case).

- [ ] **Step 6: Commit**

```bash
git add internal/pkg/template/templates/environment/cf.yml \
        internal/pkg/deploy/cloudformation/stack/env_integration_test.go \
        internal/pkg/deploy/cloudformation/stack/testdata/environments/template-with-ipv6-enabled.yml
git commit -m "$(cat <<'EOF'
Env CFN: stack outputs for IPv6; add ipv6-enabled golden

Outputs IPv6Enabled, VpcIpv6CidrBlock, and per-subnet-tier IPv6 CIDR
lists so downstream sub-projects (#2, #3, #5) can consume them via
DescribeStacks. Adds template-with-ipv6-enabled.yml as the canonical
dual-stack env render.
EOF
)"
```

---

## Task 11: Stack-state validation for IPv6 toggle on existing envs

**Files:**
- Modify: `internal/pkg/cli/env_deploy.go:173-210` (`Execute()`)
- Test: `internal/pkg/cli/env_deploy_test.go`

- [ ] **Step 1: Inspect existing describer plumbing**

Open `internal/pkg/cli/env_deploy_test.go` and `internal/pkg/cli/env_deploy.go` to identify the factory pattern used for `newEnvVersionGetter`. The new `newEnvDescriber` factory should mirror it.

- [ ] **Step 2: Write the failing unit test**

Append to `internal/pkg/cli/env_deploy_test.go` (using gomock per the file's existing pattern):

```go
func TestDeployEnvOpts_validateIPv6Toggle(t *testing.T) {
	testCases := map[string]struct {
		manifestIPv6 bool
		stackOutputs map[string]string
		wantErrIs    error
	}{
		"both off — no-op": {
			manifestIPv6: false,
			stackOutputs: map[string]string{},
			wantErrIs:    nil,
		},
		"both on — no-op": {
			manifestIPv6: true,
			stackOutputs: map[string]string{"IPv6Enabled": "true"},
			wantErrIs:    nil,
		},
		"off → on toggle — rejected": {
			manifestIPv6: true,
			stackOutputs: map[string]string{},
			wantErrIs:    errIPv6ToggleOnExistingEnv,
		},
		"on → off toggle — rejected": {
			manifestIPv6: false,
			stackOutputs: map[string]string{"IPv6Enabled": "true"},
			wantErrIs:    errIPv6ToggleOnExistingEnv,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockDescriber := mocks.NewMockenvDescriber(ctrl)
			mockDescriber.EXPECT().Outputs().Return(tc.stackOutputs, nil)

			rawMft := "name: test\ntype: Environment\n"
			if tc.manifestIPv6 {
				rawMft += "network:\n  vpc:\n    ipv6:\n      enabled: true\n"
			}
			var mft manifest.Environment
			require.NoError(t, yaml.Unmarshal([]byte(rawMft), &mft))

			opts := &deployEnvOpts{
				deployEnvVars: deployEnvVars{appName: "demo", name: "test"},
				mft:           &mft,
				newEnvDescriber: func(_, _ string) (envDescriber, error) {
					return mockDescriber, nil
				},
			}

			err := opts.validateIPv6Toggle()
			if tc.wantErrIs == nil {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, tc.wantErrIs)
			}
		})
	}
}
```

(Adjust struct field names to match the real `deployEnvOpts`. The key point is four table cases covering both-off, both-on, off→on, on→off.)

- [ ] **Step 3: Run test and confirm failure**

```
go test ./internal/pkg/cli/ -run TestDeployEnvOpts_validateIPv6Toggle -v
```

Expected: compile error `undefined: errIPv6ToggleOnExistingEnv` / `undefined: envDescriber`.

- [ ] **Step 4: Implement the sentinel error, the `envDescriber` interface, and the validation helper**

Add to `internal/pkg/cli/env_deploy.go`:

```go
var errIPv6ToggleOnExistingEnv = errors.New(
	"IPv6 cannot be toggled on an existing environment. " +
		"Create a new environment with IPv6 enabled, or follow the upgrade path once it lands.",
)

type envDescriber interface {
	Outputs() (map[string]string, error)
}
```

Add a factory field to `deployEnvOpts` — follow the `newEnvVersionGetter` pattern:

```go
type deployEnvOpts struct {
	// ... existing fields ...
	newEnvDescriber func(appName, envName string) (envDescriber, error)
	mft             *manifest.Environment
}
```

Add the validator:

```go
func (o *deployEnvOpts) validateIPv6Toggle() error {
	manifestWantsIPv6 := o.mft != nil && o.mft.Network.VPC.IPv6Enabled()

	describer, err := o.newEnvDescriber(o.appName, o.name)
	if err != nil {
		// No existing env stack — nothing to validate against.
		return nil
	}
	outputs, err := describer.Outputs()
	if err != nil {
		return fmt.Errorf("describe existing env stack outputs: %w", err)
	}
	stackHasIPv6 := outputs["IPv6Enabled"] == "true"

	if manifestWantsIPv6 != stackHasIPv6 {
		return errIPv6ToggleOnExistingEnv
	}
	return nil
}
```

Call it from `Execute()` right after the manifest is parsed (around line 188):

```go
mft, interpolated, err := environmentManifest(o.name, o.ws, o.newInterpolator(o.appName, o.name))
if err != nil {
	return err
}
o.mft = mft
if err := o.validateIPv6Toggle(); err != nil {
	return err
}
```

Wire the `newEnvDescriber` factory in the constructor (follow the pattern used for `newEnvVersionGetter`). The factory should construct a real `describe.EnvDescriber` and return it wrapped.

- [ ] **Step 5: Regenerate mocks**

```
make gen-mocks
```

Expected: `internal/pkg/cli/mocks/mock_cli.go` (or similar) gains `MockenvDescriber`.

- [ ] **Step 6: Run tests and confirm pass**

```
go test ./internal/pkg/cli/ -run TestDeployEnvOpts_validateIPv6Toggle -v
go test ./internal/pkg/cli/ -count=1
```

Expected: new 4-case test passes, existing tests still pass.

- [ ] **Step 7: Commit**

```bash
git add internal/pkg/cli/env_deploy.go internal/pkg/cli/env_deploy_test.go internal/pkg/cli/mocks/
git commit -m "$(cat <<'EOF'
Env deploy: reject IPv6 toggle on existing environments

Compares manifest IPv6 flag against the deployed stack's IPv6Enabled
output. Mismatch returns errIPv6ToggleOnExistingEnv; upgrade/downgrade
support lands in sub-project #5.
EOF
)"
```

---

## Task 12: E2E — IPv6 dual-stack env with NAT enabled

**Files:**
- Create: `e2e/env_ipv6/env_ipv6_suite_test.go`
- Create: `e2e/env_ipv6/env_ipv6_test.go`

- [ ] **Step 1: Bootstrap the Ginkgo suite**

Mirror an existing suite file (e.g. `e2e/customized-env/customized_env_suite_test.go`). Create `e2e/env_ipv6/env_ipv6_suite_test.go`:

```go
// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package env_ipv6_test

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

func TestEnvIPv6(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Env IPv6 Suite")
}

var _ = BeforeSuite(func() {
	var err error
	cli, err = client.NewCLI()
	Expect(err).NotTo(HaveOccurred())
	appName = "e2e-env-ipv6"
	_, err = cli.AppInit(&client.AppInitRequest{AppName: appName})
	Expect(err).NotTo(HaveOccurred())
})

var _ = AfterSuite(func() {
	_, err := cli.AppDelete()
	Expect(err).NotTo(HaveOccurred())
})
```

- [ ] **Step 2: Write the IPv6-with-NAT scenario**

Create `e2e/env_ipv6/env_ipv6_test.go`:

```go
// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package env_ipv6_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Env IPv6 Dual-Stack", Ordered, func() {
	When("environment manifest opts into IPv6", func() {
		It("creates a VPC with a /56 IPv6 CIDR and dual-stack subnets", func() {
			_ = appName
			_ = cli
			// Setup: write copilot/environments/test/manifest.yml with
			//   network.vpc.ipv6.enabled: true
			// then copilot env init --name test + copilot env deploy --name test.
			// Assertions via AWS SDK:
			//   - DescribeVpcs: Ipv6CidrBlockAssociationSet non-empty,
			//                   prefix length == 56.
			//   - DescribeSubnets: every subnet has Ipv6CidrBlock with /64 prefix.
			//   - AssignIpv6AddressOnCreation == true on every subnet.
			//   - DescribeEgressOnlyInternetGateways: one EOIGW attached to VPC.
			//   - DescribeRouteTables: each private RT has a route with
			//     DestinationIpv6CidrBlock="::/0" and EgressOnlyInternetGatewayId
			//     matching the EOIGW id.
			//   - Public RT has a route with DestinationIpv6CidrBlock="::/0" and
			//     GatewayId matching the IGW id.
			//   - Subnet IPv6 blocks are pairwise disjoint.
			Skip("TODO: fill in once e2e/internal/client has EC2 describe helpers")
		})
	})
})
```

The `Skip()` stub is intentional until `e2e/internal/client` gains EC2 helpers. Documented so a follow-up PR can remove the skip.

- [ ] **Step 3: Verify the suite compiles**

```
go test -c ./e2e/env_ipv6/
```

Expected: a compiled binary is produced (or `-c` prints no errors).

- [ ] **Step 4: Commit**

```bash
git add e2e/env_ipv6/
git commit -m "$(cat <<'EOF'
Env IPv6 E2E skeleton

Adds the Ginkgo suite + scaffold for the IPv6 dual-stack E2E. Concrete
assertions are marked Skip() pending e2e/internal/client helpers for
EC2 DescribeVpcs/DescribeSubnets calls.
EOF
)"
```

---

## Task 13: E2E — IPv6 + no-NAT scenario

**Files:**
- Modify: `e2e/env_ipv6/env_ipv6_test.go`

- [ ] **Step 1: Append the second scenario**

Inside the existing `Describe("Env IPv6 Dual-Stack")` block:

```go
It("creates private route tables even with no NAT workloads", func() {
	// Setup: deploy an env with IPv6 on and no ALB/NAT workload.
	// Expected:
	//   - CreatePrivateRouteTables condition evaluates true.
	//   - Each private subnet is associated with a private route table.
	//   - Each private route table has route ::/0 → EgressOnlyIGW.
	//   - No NAT gateway exists.
	//   - No 0.0.0.0/0 default route on any private route table.
	Skip("TODO: fill in once e2e/internal/client has EC2 describe helpers")
})
```

- [ ] **Step 2: Verify compilation**

```
go test -c ./e2e/env_ipv6/
```

Expected: clean build.

- [ ] **Step 3: Commit**

```bash
git add e2e/env_ipv6/env_ipv6_test.go
git commit -m "$(cat <<'EOF'
Env IPv6 E2E: second scenario for no-NAT path

Exercises the CreatePrivateRouteTables widening — v6 egress must work
with no NAT gateway provisioned.
EOF
)"
```

---

## Task 14: Documentation

**Files:**
- Modify: `site/content/docs/manifest/environment.en.md`
- Modify: `CHANGELOG.md`

- [ ] **Step 1: Add manifest documentation**

Open `site/content/docs/manifest/environment.en.md`. Locate the section documenting `network.vpc` fields. Add a subsection near `network.vpc.cidr`:

````markdown
### `network.vpc.ipv6`

Configures IPv6 dual-stack networking on the managed VPC. Optional. When
absent or disabled, the environment is IPv4-only (current behavior).

Example:

```yaml
network:
  vpc:
    ipv6:
      enabled: true
```

When enabled:
- The VPC is associated with an Amazon-provided `/56` IPv6 CIDR block.
- Every managed subnet receives a disjoint `/64` block.
- Subnet-level `AssignIpv6AddressOnCreation` is set to `true`.
- Public subnets get a `::/0` default route to the Internet Gateway.
- Private subnets get a `::/0` default route to a new Egress-Only
  Internet Gateway (private subnets retain their existing IPv4 default
  route through the NAT Gateway, when present).

Restrictions (foundation release):

- Not supported with `network.vpc.id` (imported VPC).
- Not supported as an in-place toggle on an existing environment —
  create a new environment with IPv6 enabled.
````

- [ ] **Step 2: Add CHANGELOG entry**

Prepend a new entry to `CHANGELOG.md` under the unreleased/upcoming section:

```markdown
* Add opt-in dual-stack VPC support for environments via
  `network.vpc.ipv6.enabled` on the environment manifest (foundation;
  task/load-balancer support to follow).
```

- [ ] **Step 3: Commit**

```bash
git add site/content/docs/manifest/environment.en.md CHANGELOG.md
git commit -m "$(cat <<'EOF'
Docs: document env manifest ipv6.enabled field

Explains the new manifest field, what it does, and the two firm-error
restrictions (no imported VPCs, no in-place toggle).
EOF
)"
```

---

## Self-Review Summary

**Spec coverage:**
- Manifest schema → Task 1
- Validation (imported VPC) → Task 2
- Validation (existing-env toggle) → Task 11
- Template plumbing → Tasks 3, 4
- VPC + EgressOnlyIGW resources → Task 5
- Subnet IPv6 CIDR + auto-assign → Task 6
- Public IPv6 default route → Task 7
- Private route-table gate widening → Task 8
- Private IPv6 default routes → Task 9
- Stack outputs + new golden → Task 10
- E2E coverage (IPv6 + NAT, IPv6 + no-NAT) → Tasks 12, 13
- Documentation → Task 14

**Placeholder scan:** Tasks 12 and 13 use `Skip()` stubs pending `e2e/internal/client` EC2 helpers. This is documented in each task body so a follow-up can remove them. No other placeholders.

**Type consistency:** `IPv6Enabled()` is consistently the accessor name (manifest package), `IPv6Enabled` the field name (template package and stack output). `ipv6VPCConfig` is the struct. `errIPv6WithImportedVPC` and `errIPv6ToggleOnExistingEnv` are the error sentinels.

**Risks flagged:**
- Task 10's golden file generation assumes the existing test runner supports an `-update` flag; if not, the task provides an instrumentation fallback.
- Task 11 introduces a new `envDescriber` interface dependency on `deployEnvOpts`; mock-regeneration via `make gen-mocks` is explicit.
- Go-template whitespace: the `{{- ... -}}` guards must be tuned carefully to preserve byte-identity on existing goldens. Flagged in Task 5 step 3.
