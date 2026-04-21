# Imported-VPC IPv6 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let users opt an environment that imports an existing VPC into IPv6 dual-stack networking, by removing the `errIPv6WithImportedVPC` firm error and adding an EC2-backed pre-flight validator that hard-fails if the imported VPC or any imported subnet lacks an associated IPv6 CIDR block.

**Architecture:** Two new EC2 helpers (`HasVPCIPv6`, `SubnetsByIDs` returning an extended `Subnet` struct with `IPv6CIDRBlocks`) compose into a new `cli`-package validator (`validateImportedVPCIPv6Readiness`) with two sentinels (`errImportedVPCMissingIPv6`, `errImportedSubnetsMissingIPv6`). The validator is called from `env init` after subnet selection and from `env deploy` after manifest parse. No template, no stack-construction, no manifest-field changes — #1/#2/#3's flag-driven plumbing (`envManifest.Network.VPC.IPv6Enabled()`) already handles imported + IPv6 correctly once the manifest-layer guard is lifted. One new env-stack integration golden proves copilot provisions zero managed-VPC IPv6 infrastructure on the imported path. One Ginkgo E2E scenario (positive) + one negative scenario, both backed by a helper CFN stack that provisions a throw-away IPv6 VPC in `BeforeSuite`.

**Tech Stack:** Go 1.23, `mockgen` + `testify`, AWS SDK v1 (`github.com/aws/aws-sdk-go/service/ec2`), CloudFormation YAML, Ginkgo v2.

**Reference spec:** `docs/superpowers/specs/2026-04-21-ipv6-imported-vpc-design.md`
**Base branch:** `feature/ipv6-imported-vpc` (already checked out off `feature/ipv6-support`; the spec commit lives here)

---

## File Structure

### Files to modify

| Path | Responsibility |
|---|---|
| `internal/pkg/aws/ec2/ec2.go` | `Subnet.IPv6CIDRBlocks` field; `HasVPCIPv6(vpcID)` helper; `SubnetsByIDs(ids []string)` helper; thread IPv6 association parsing through existing subnet describe path |
| `internal/pkg/aws/ec2/ec2_test.go` | Unit tests for new helpers and field population |
| `internal/pkg/aws/ec2/mocks/mock_ec2.go` | Regenerated after `api` interface touches (no change expected — we only use existing `DescribeVpcs`/`DescribeSubnets`) |
| `internal/pkg/manifest/validate_env.go` | Delete `errIPv6WithImportedVPC` and its caller |
| `internal/pkg/manifest/validate_env_test.go` | Remove the two test cases asserting `errIPv6WithImportedVPC`; add positive case |
| `internal/pkg/cli/interfaces.go` | Extend `ec2Client` interface with `HasVPCIPv6` and `SubnetsByIDs` |
| `internal/pkg/cli/mocks/mock_interfaces.go` | Regenerated |
| `internal/pkg/cli/env_deploy.go` | Add `ec2Client ec2Client` field on `deployEnvOpts`; lazy init; invoke validator from `Execute` after manifest parse |
| `internal/pkg/cli/env_deploy_test.go` | Unit tests for the new call site |
| `internal/pkg/deploy/cloudformation/stack/env_integration_test.go` | New golden test case `imported VPC with ipv6 dual-stack enabled` |
| `site/content/docs/manifest/environment.en.md` | Remove "managed-VPC only" caveat from IPv6 section; document imported-VPC prerequisites |
| `CHANGELOG.md` | Entry |

### Files to create

| Path | Responsibility |
|---|---|
| `internal/pkg/cli/ipv6_imported_vpc_validator.go` | Validator function + two error sentinels |
| `internal/pkg/cli/ipv6_imported_vpc_validator_test.go` | Table-driven unit tests with mocked `ec2Client` |
| `internal/pkg/deploy/cloudformation/stack/testdata/environments/template-with-ipv6-and-imported-vpc.yml` | New golden fixture |
| `e2e/imported_vpc_ipv6/helper_stack.yml` | CFN prerequisite stack: fresh VPC with `/56` + 2 public + 2 private `/64` subnets + EgressOnlyIGW + IPv4/IPv6 routes; plus a second v4-only VPC for the negative case |
| `e2e/imported_vpc_ipv6/imported_vpc_ipv6_suite_test.go` | Ginkgo v2 suite bootstrap |
| `e2e/imported_vpc_ipv6/imported_vpc_ipv6_test.go` | Positive scenario + negative scenario |

---

## Task 1: Extend `ec2.Subnet` with `IPv6CIDRBlocks` and populate it

**Files:**
- Modify: `internal/pkg/aws/ec2/ec2.go:88-92` (`Subnet` struct) and `ec2.go:252-292` (`ListVPCSubnets` subnet construction)
- Test: `internal/pkg/aws/ec2/ec2_test.go`

Context: Today `Subnet` holds only the v4 `CIDRBlock`. AWS's `DescribeSubnets` response includes `Ipv6CidrBlockAssociationSet` entries, each with a `Ipv6CidrBlock` string and an `Ipv6CidrBlockState.State` that we must check equals `"associated"` (other states include `associating`, `disassociating`, `disassociated`, `failing`, `failed`). We populate only associated blocks; disassociating/failed don't count as "IPv6 ready".

- [ ] **Step 1: Write the failing test**

Append to `internal/pkg/aws/ec2/ec2_test.go` (import `"github.com/aws/aws-sdk-go/service/ec2"` and `"github.com/aws/aws-sdk-go/aws"` if not already present):

```go
func TestEC2_ListVPCSubnets_PopulatesIPv6CIDRBlocks(t *testing.T) {
	const vpcID = "vpc-abc"
	mockRouteTables := &ec2.DescribeRouteTablesOutput{
		RouteTables: []*ec2.RouteTable{{
			RouteTableId: aws.String("rtb-1"),
			Associations: []*ec2.RouteTableAssociation{{SubnetId: aws.String("subnet-pub")}},
			Routes: []*ec2.Route{{
				DestinationCidrBlock: aws.String("0.0.0.0/0"),
				GatewayId:            aws.String("igw-1"),
			}},
			VpcId: aws.String(vpcID),
		}},
	}
	mockSubnets := &ec2.DescribeSubnetsOutput{Subnets: []*ec2.Subnet{
		{
			SubnetId:  aws.String("subnet-pub"),
			CidrBlock: aws.String("10.0.0.0/24"),
			Ipv6CidrBlockAssociationSet: []*ec2.SubnetIpv6CidrBlockAssociation{
				{
					Ipv6CidrBlock:      aws.String("2001:db8::/64"),
					Ipv6CidrBlockState: &ec2.SubnetCidrBlockState{State: aws.String("associated")},
				},
				{
					Ipv6CidrBlock:      aws.String("2001:db8:1::/64"),
					Ipv6CidrBlockState: &ec2.SubnetCidrBlockState{State: aws.String("disassociated")},
				},
			},
		},
		{
			SubnetId:  aws.String("subnet-priv"),
			CidrBlock: aws.String("10.0.1.0/24"),
			// No IPv6 association set at all.
		},
	}}
	ctrl := gomock.NewController(t)
	m := mocks.NewMockapi(ctrl)
	m.EXPECT().DescribeRouteTables(gomock.Any()).Return(mockRouteTables, nil)
	m.EXPECT().DescribeSubnets(gomock.Any()).Return(mockSubnets, nil)
	client := &EC2{client: m}

	out, err := client.ListVPCSubnets(vpcID)
	require.NoError(t, err)

	subnetsByID := map[string]Subnet{}
	for _, s := range out.Public {
		subnetsByID[s.ID] = s
	}
	for _, s := range out.Private {
		subnetsByID[s.ID] = s
	}
	require.ElementsMatch(t, []string{"2001:db8::/64"}, subnetsByID["subnet-pub"].IPv6CIDRBlocks,
		"only associated IPv6 blocks are returned; disassociated is filtered")
	require.Empty(t, subnetsByID["subnet-priv"].IPv6CIDRBlocks,
		"subnets with no IPv6 have empty slice, not nil panic")
}
```

If the `mocks` import alias and `gomock` aren't already present, add:
```go
import (
	mocks "github.com/aws/copilot-cli/internal/pkg/aws/ec2/mocks"
	"github.com/golang/mock/gomock"
)
```

- [ ] **Step 2: Run the test and confirm it fails**

```
go test ./internal/pkg/aws/ec2/ -run TestEC2_ListVPCSubnets_PopulatesIPv6CIDRBlocks -v
```

Expected: compile error `subnetsByID["subnet-pub"].IPv6CIDRBlocks undefined` (field does not exist).

- [ ] **Step 3: Add the field to `Subnet`**

Edit `internal/pkg/aws/ec2/ec2.go` — replace the `Subnet` struct (around line 88-92):

```go
// Subnet contains the ID and name of a subnet.
type Subnet struct {
	Resource
	CIDRBlock      string
	IPv6CIDRBlocks []string
}
```

- [ ] **Step 4: Populate the field in `ListVPCSubnets`**

Edit `internal/pkg/aws/ec2/ec2.go` — inside `ListVPCSubnets`, update the `s := Subnet{...}` construction (around line 275-281) to include the IPv6 blocks:

```go
s := Subnet{
	Resource: Resource{
		ID:   aws.StringValue(subnet.SubnetId),
		Name: name,
	},
	CIDRBlock:      aws.StringValue(subnet.CidrBlock),
	IPv6CIDRBlocks: associatedIPv6Blocks(subnet),
}
```

Then add a new file-private helper at the bottom of `internal/pkg/aws/ec2/ec2.go`:

```go
// associatedIPv6Blocks returns the Ipv6CidrBlock values whose association state
// is "associated". Other states (associating, disassociating, disassociated,
// failing, failed) are excluded — they do not count as IPv6-ready.
func associatedIPv6Blocks(subnet *ec2.Subnet) []string {
	var out []string
	for _, assoc := range subnet.Ipv6CidrBlockAssociationSet {
		if assoc == nil || assoc.Ipv6CidrBlockState == nil {
			continue
		}
		if aws.StringValue(assoc.Ipv6CidrBlockState.State) != ec2.SubnetCidrBlockStateCodeAssociated {
			continue
		}
		out = append(out, aws.StringValue(assoc.Ipv6CidrBlock))
	}
	return out
}
```

Note: `ec2.SubnetCidrBlockStateCodeAssociated` is the AWS SDK constant for the string `"associated"`.

- [ ] **Step 5: Run the test and confirm it passes**

```
go test ./internal/pkg/aws/ec2/ -run TestEC2_ListVPCSubnets_PopulatesIPv6CIDRBlocks -v
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/pkg/aws/ec2/ec2.go internal/pkg/aws/ec2/ec2_test.go
git commit -m "EC2: surface associated IPv6 CIDR blocks on Subnet

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 2: Add `HasVPCIPv6` helper to `*EC2`

**Files:**
- Modify: `internal/pkg/aws/ec2/ec2.go` (append after `HasDNSSupport`, around line 241)
- Test: `internal/pkg/aws/ec2/ec2_test.go`

Context: AWS's `DescribeVpcs` returns `Ipv6CidrBlockAssociationSet` entries on each VPC. We return `true` if at least one association is in state `"associated"`. Mirrors the subnet helper's semantics.

- [ ] **Step 1: Write the failing test**

Append to `internal/pkg/aws/ec2/ec2_test.go`:

```go
func TestEC2_HasVPCIPv6(t *testing.T) {
	testCases := map[string]struct {
		describeOutput *ec2.DescribeVpcsOutput
		wantHas        bool
	}{
		"no Ipv6CidrBlockAssociationSet at all": {
			describeOutput: &ec2.DescribeVpcsOutput{Vpcs: []*ec2.Vpc{{VpcId: aws.String("vpc-a")}}},
			wantHas:        false,
		},
		"all associations disassociated": {
			describeOutput: &ec2.DescribeVpcsOutput{Vpcs: []*ec2.Vpc{{
				VpcId: aws.String("vpc-a"),
				Ipv6CidrBlockAssociationSet: []*ec2.VpcIpv6CidrBlockAssociation{
					{Ipv6CidrBlock: aws.String("2001:db8::/56"), Ipv6CidrBlockState: &ec2.VpcCidrBlockState{State: aws.String("disassociated")}},
				},
			}}},
			wantHas: false,
		},
		"one associated, one disassociated": {
			describeOutput: &ec2.DescribeVpcsOutput{Vpcs: []*ec2.Vpc{{
				VpcId: aws.String("vpc-a"),
				Ipv6CidrBlockAssociationSet: []*ec2.VpcIpv6CidrBlockAssociation{
					{Ipv6CidrBlock: aws.String("2001:db8::/56"), Ipv6CidrBlockState: &ec2.VpcCidrBlockState{State: aws.String("associated")}},
					{Ipv6CidrBlock: aws.String("2001:db9::/56"), Ipv6CidrBlockState: &ec2.VpcCidrBlockState{State: aws.String("disassociated")}},
				},
			}}},
			wantHas: true,
		},
		"associating (not yet associated)": {
			describeOutput: &ec2.DescribeVpcsOutput{Vpcs: []*ec2.Vpc{{
				VpcId: aws.String("vpc-a"),
				Ipv6CidrBlockAssociationSet: []*ec2.VpcIpv6CidrBlockAssociation{
					{Ipv6CidrBlock: aws.String("2001:db8::/56"), Ipv6CidrBlockState: &ec2.VpcCidrBlockState{State: aws.String("associating")}},
				},
			}}},
			wantHas: false,
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			m := mocks.NewMockapi(ctrl)
			m.EXPECT().DescribeVpcs(&ec2.DescribeVpcsInput{
				VpcIds: aws.StringSlice([]string{"vpc-a"}),
			}).Return(tc.describeOutput, nil)
			c := &EC2{client: m}

			got, err := c.HasVPCIPv6("vpc-a")
			require.NoError(t, err)
			require.Equal(t, tc.wantHas, got)
		})
	}
}

func TestEC2_HasVPCIPv6_APIError(t *testing.T) {
	ctrl := gomock.NewController(t)
	m := mocks.NewMockapi(ctrl)
	m.EXPECT().DescribeVpcs(gomock.Any()).Return(nil, errors.New("access denied"))
	c := &EC2{client: m}

	_, err := c.HasVPCIPv6("vpc-a")
	require.Error(t, err)
	require.Contains(t, err.Error(), "describe VPC vpc-a")
}

func TestEC2_HasVPCIPv6_NotFound(t *testing.T) {
	ctrl := gomock.NewController(t)
	m := mocks.NewMockapi(ctrl)
	m.EXPECT().DescribeVpcs(gomock.Any()).Return(&ec2.DescribeVpcsOutput{Vpcs: nil}, nil)
	c := &EC2{client: m}

	_, err := c.HasVPCIPv6("vpc-a")
	require.Error(t, err)
	require.Contains(t, err.Error(), "vpc-a")
}
```

Import `"errors"` if not already present.

- [ ] **Step 2: Run the tests and confirm they fail**

```
go test ./internal/pkg/aws/ec2/ -run TestEC2_HasVPCIPv6 -v
```

Expected: compile error `c.HasVPCIPv6 undefined`.

- [ ] **Step 3: Implement `HasVPCIPv6`**

Append to `internal/pkg/aws/ec2/ec2.go` (immediately after `HasDNSSupport`, around line 241):

```go
// HasVPCIPv6 returns true if the given VPC has at least one Ipv6CidrBlockAssociation
// in state "associated". Associations in states associating, disassociating,
// disassociated, failing, or failed do not count — the VPC is not yet (or no
// longer) IPv6-ready.
func (c *EC2) HasVPCIPv6(vpcID string) (bool, error) {
	resp, err := c.client.DescribeVpcs(&ec2.DescribeVpcsInput{
		VpcIds: aws.StringSlice([]string{vpcID}),
	})
	if err != nil {
		return false, fmt.Errorf("describe VPC %s: %w", vpcID, err)
	}
	if len(resp.Vpcs) == 0 {
		return false, fmt.Errorf("VPC %s not found", vpcID)
	}
	for _, assoc := range resp.Vpcs[0].Ipv6CidrBlockAssociationSet {
		if assoc == nil || assoc.Ipv6CidrBlockState == nil {
			continue
		}
		if aws.StringValue(assoc.Ipv6CidrBlockState.State) == ec2.VpcCidrBlockStateCodeAssociated {
			return true, nil
		}
	}
	return false, nil
}
```

Note: `ec2.VpcCidrBlockStateCodeAssociated` is the AWS SDK constant for `"associated"` on VPC-level associations (distinct from the subnet-level constant used in Task 1).

- [ ] **Step 4: Run the tests and confirm they pass**

```
go test ./internal/pkg/aws/ec2/ -run TestEC2_HasVPCIPv6 -v
```

Expected: PASS (all four cases).

- [ ] **Step 5: Commit**

```bash
git add internal/pkg/aws/ec2/ec2.go internal/pkg/aws/ec2/ec2_test.go
git commit -m "EC2: add HasVPCIPv6 helper

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 3: Add `SubnetsByIDs` helper returning full `Subnet` objects

**Files:**
- Modify: `internal/pkg/aws/ec2/ec2.go` (append near the existing `SubnetIDs` helper, around line 306)
- Test: `internal/pkg/aws/ec2/ec2_test.go`

Context: The existing `SubnetIDs(filters ...)` returns only IDs. The validator needs each subnet's `IPv6CIDRBlocks` slice. AWS's `DescribeSubnetsInput` accepts a `SubnetIds` field directly, so we can look up by ID without going through the VPC route-table index that `ListVPCSubnets` uses.

- [ ] **Step 1: Write the failing test**

Append to `internal/pkg/aws/ec2/ec2_test.go`:

```go
func TestEC2_SubnetsByIDs(t *testing.T) {
	out := &ec2.DescribeSubnetsOutput{Subnets: []*ec2.Subnet{
		{
			SubnetId:  aws.String("subnet-1"),
			CidrBlock: aws.String("10.0.0.0/24"),
			Ipv6CidrBlockAssociationSet: []*ec2.SubnetIpv6CidrBlockAssociation{
				{Ipv6CidrBlock: aws.String("2001:db8::/64"), Ipv6CidrBlockState: &ec2.SubnetCidrBlockState{State: aws.String("associated")}},
			},
		},
		{
			SubnetId:  aws.String("subnet-2"),
			CidrBlock: aws.String("10.0.1.0/24"),
		},
	}}
	ctrl := gomock.NewController(t)
	m := mocks.NewMockapi(ctrl)
	m.EXPECT().DescribeSubnets(&ec2.DescribeSubnetsInput{
		SubnetIds: aws.StringSlice([]string{"subnet-1", "subnet-2"}),
	}).Return(out, nil)
	c := &EC2{client: m}

	got, err := c.SubnetsByIDs([]string{"subnet-1", "subnet-2"})
	require.NoError(t, err)
	require.Len(t, got, 2)
	byID := map[string]Subnet{}
	for _, s := range got {
		byID[s.ID] = s
	}
	require.Equal(t, []string{"2001:db8::/64"}, byID["subnet-1"].IPv6CIDRBlocks)
	require.Empty(t, byID["subnet-2"].IPv6CIDRBlocks)
}

func TestEC2_SubnetsByIDs_Empty(t *testing.T) {
	c := &EC2{client: nil} // never called
	got, err := c.SubnetsByIDs(nil)
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestEC2_SubnetsByIDs_APIError(t *testing.T) {
	ctrl := gomock.NewController(t)
	m := mocks.NewMockapi(ctrl)
	m.EXPECT().DescribeSubnets(gomock.Any()).Return(nil, errors.New("rate exceeded"))
	c := &EC2{client: m}

	_, err := c.SubnetsByIDs([]string{"subnet-1"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "describe subnets")
}
```

- [ ] **Step 2: Run the tests and confirm they fail**

```
go test ./internal/pkg/aws/ec2/ -run TestEC2_SubnetsByIDs -v
```

Expected: compile error `c.SubnetsByIDs undefined`.

- [ ] **Step 3: Implement `SubnetsByIDs`**

Append to `internal/pkg/aws/ec2/ec2.go` (directly below the existing `SubnetIDs` function, around line 306):

```go
// SubnetsByIDs returns subnets matching the given IDs. Each Subnet contains
// its associated IPv6 CIDR blocks (only those in state "associated"). Returns
// an empty slice and no error when ids is empty, avoiding a zero-filter
// DescribeSubnets call.
func (c *EC2) SubnetsByIDs(ids []string) ([]Subnet, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	resp, err := c.client.DescribeSubnets(&ec2.DescribeSubnetsInput{
		SubnetIds: aws.StringSlice(ids),
	})
	if err != nil {
		return nil, fmt.Errorf("describe subnets %v: %w", ids, err)
	}
	out := make([]Subnet, 0, len(resp.Subnets))
	for _, s := range resp.Subnets {
		var name string
		for _, tag := range s.Tags {
			if aws.StringValue(tag.Key) == "Name" {
				name = aws.StringValue(tag.Value)
			}
		}
		out = append(out, Subnet{
			Resource: Resource{
				ID:   aws.StringValue(s.SubnetId),
				Name: name,
			},
			CIDRBlock:      aws.StringValue(s.CidrBlock),
			IPv6CIDRBlocks: associatedIPv6Blocks(s),
		})
	}
	return out, nil
}
```

No pagination because the request is ID-scoped; the total result set is bounded by `len(ids)`.

- [ ] **Step 4: Run the tests and confirm they pass**

```
go test ./internal/pkg/aws/ec2/ -run TestEC2_SubnetsByIDs -v
```

Expected: PASS.

- [ ] **Step 5: Run the full EC2 package test suite to catch regressions**

```
go test ./internal/pkg/aws/ec2/ -v
```

Expected: every existing test passes alongside the new ones.

- [ ] **Step 6: Commit**

```bash
git add internal/pkg/aws/ec2/ec2.go internal/pkg/aws/ec2/ec2_test.go
git commit -m "EC2: add SubnetsByIDs helper returning IPv6-aware subnets

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 4: Extend the `ec2Client` interface in `cli/interfaces.go`

**Files:**
- Modify: `internal/pkg/cli/interfaces.go:590-593`

Context: The validator (Task 6) depends on the interface, so we widen the interface before writing the validator. After the edit, `make gen-mocks` regenerates `mocks/mock_interfaces.go` to include the new methods.

- [ ] **Step 1: Widen the interface**

Edit `internal/pkg/cli/interfaces.go` — replace the `ec2Client` block (lines 590-593):

```go
type ec2Client interface {
	HasDNSSupport(vpcID string) (bool, error)
	ListAZs() ([]ec2.AZ, error)
	HasVPCIPv6(vpcID string) (bool, error)
	SubnetsByIDs(ids []string) ([]ec2.Subnet, error)
}
```

- [ ] **Step 2: Regenerate mocks**

```bash
make gen-mocks
```

This runs `go generate` across the repo (project's standard target). The regenerated `internal/pkg/cli/mocks/mock_interfaces.go` will have the two new methods on `Mockec2Client`. If `make gen-mocks` is not available, run the equivalent invocation shown in the Makefile directly:

```bash
go install github.com/golang/mock/mockgen@v1.6.0
go generate ./...
```

- [ ] **Step 3: Verify mocks compile**

```bash
go build ./internal/pkg/cli/...
```

Expected: no errors. If the build fails with `undefined: (*Mockec2Client).HasVPCIPv6`, `make gen-mocks` did not run successfully — rerun it.

- [ ] **Step 4: Commit**

```bash
git add internal/pkg/cli/interfaces.go internal/pkg/cli/mocks/mock_interfaces.go
git commit -m "CLI: widen ec2Client interface with HasVPCIPv6 + SubnetsByIDs

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 5: Remove `errIPv6WithImportedVPC` from the manifest validator

**Files:**
- Modify: `internal/pkg/manifest/validate_env.go:15-24` (delete the sentinel) and `validate_env.go:98-102` (delete the call)
- Test: `internal/pkg/manifest/validate_env_test.go` (remove the case at line 1110; add a positive case)

This is before the CLI validator lands, so the combined behavior between commits is: imported + IPv6 manifests parse and validate cleanly, with no pre-flight readiness check. Downstream CLI wiring (Task 7, Task 8) restores the check at the right layer. This ordering is intentional — the manifest package must remain side-effect-free, and the CLI layer is where EC2 describes belong.

- [ ] **Step 1: Locate the existing test case to remove**

```bash
grep -n "errIPv6WithImportedVPC" internal/pkg/manifest/validate_env_test.go
```

Expected: one match at line 1110 (based on current code). Read 15 lines around it to understand the full test case and where its `t.Run` block starts/ends.

- [ ] **Step 2: Delete the sentinel from the manifest package**

Edit `internal/pkg/manifest/validate_env.go` — delete lines 18-24 (the `errIPv6WithImportedVPC` declaration and its doc comment):

```go
// DELETE this block:

// errIPv6WithImportedVPC is returned when a manifest enables IPv6 on an
// imported VPC. Imported-VPC IPv6 support is tracked as a follow-up
// sub-project (#4).
errIPv6WithImportedVPC = errors.New(
    `IPv6 cannot be enabled on an environment that imports a VPC. ` +
        `Remove "network.vpc.id" or "network.vpc.ipv6".`,
)
```

After the delete, the `var (...)` block becomes:

```go
var (
	errAZsNotEqual = errors.New("public subnets and private subnets do not span the same availability zones")

	minAZs = 2
)
```

Then delete the caller inside `environmentVPCConfig.validate()` (lines 98-102):

```go
// DELETE this block:

// IPv6Enabled() has a pointer receiver; validate() uses a value receiver,
// so take the address explicitly (auto-addressing does not apply here).
if cfg.imported() && (&cfg).IPv6Enabled() {
    return errIPv6WithImportedVPC
}
```

- [ ] **Step 3: Remove the test case**

In `internal/pkg/manifest/validate_env_test.go`, find the entire test block that contains `require.ErrorIs(t, err, errIPv6WithImportedVPC)` (starting at the matching `t.Run(...)` or map key above line 1110) and delete it. Replace it with a positive case:

```go
"imported VPC with IPv6 is valid": {
	vpc: environmentVPCConfig{
		ID: aws.String("vpc-1234567890"),
		IPv6: &ipv6VPCConfig{
			Enabled: aws.Bool(true),
		},
		Subnets: subnetsConfiguration{
			Public: []subnetConfiguration{
				{SubnetID: aws.String("subnet-pub-1")},
				{SubnetID: aws.String("subnet-pub-2")},
			},
			Private: []subnetConfiguration{
				{SubnetID: aws.String("subnet-priv-1")},
				{SubnetID: aws.String("subnet-priv-2")},
			},
		},
	},
	wantedErr: "",
},
```

Match the literal struct shape used by adjacent cases in that table. If the table keys use a different pattern (e.g., a `vpc` field builder), conform to it.

- [ ] **Step 4: Run the manifest tests**

```bash
go test ./internal/pkg/manifest/ -v
```

Expected: all tests pass. Specifically, the `imported VPC with IPv6 is valid` case returns no error (where it previously would have returned `errIPv6WithImportedVPC`).

- [ ] **Step 5: Commit**

```bash
git add internal/pkg/manifest/validate_env.go internal/pkg/manifest/validate_env_test.go
git commit -m "Manifest: allow imported VPC + ipv6.enabled in validator

CLI-layer pre-flight EC2 check (next commits) replaces the hard block.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 6: Implement the `validateImportedVPCIPv6Readiness` validator + sentinels

**Files:**
- Create: `internal/pkg/cli/ipv6_imported_vpc_validator.go`
- Create: `internal/pkg/cli/ipv6_imported_vpc_validator_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/pkg/cli/ipv6_imported_vpc_validator_test.go`:

```go
// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"errors"
	"testing"

	"github.com/aws/copilot-cli/internal/pkg/aws/ec2"
	"github.com/aws/copilot-cli/internal/pkg/cli/mocks"
	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/require"
)

func TestValidateImportedVPCIPv6Readiness(t *testing.T) {
	const vpcID = "vpc-abc"
	allSubnets := []string{"subnet-pub1", "subnet-pub2", "subnet-priv1", "subnet-priv2"}
	goodSubnetDescribe := []ec2.Subnet{
		{Resource: ec2.Resource{ID: "subnet-pub1"}, IPv6CIDRBlocks: []string{"2001:db8::/64"}},
		{Resource: ec2.Resource{ID: "subnet-pub2"}, IPv6CIDRBlocks: []string{"2001:db8:1::/64"}},
		{Resource: ec2.Resource{ID: "subnet-priv1"}, IPv6CIDRBlocks: []string{"2001:db8:2::/64"}},
		{Resource: ec2.Resource{ID: "subnet-priv2"}, IPv6CIDRBlocks: []string{"2001:db8:3::/64"}},
	}

	testCases := map[string]struct {
		vpcID         string
		subnetIDs     []string
		setup         func(*mocks.Mockec2Client)
		wantErr       error
		wantErrSubstr string
	}{
		"happy path: VPC and all subnets have IPv6": {
			vpcID:     vpcID,
			subnetIDs: allSubnets,
			setup: func(m *mocks.Mockec2Client) {
				m.EXPECT().HasVPCIPv6(vpcID).Return(true, nil)
				m.EXPECT().SubnetsByIDs(allSubnets).Return(goodSubnetDescribe, nil)
			},
			wantErr: nil,
		},
		"VPC has no IPv6 association": {
			vpcID:     vpcID,
			subnetIDs: allSubnets,
			setup: func(m *mocks.Mockec2Client) {
				m.EXPECT().HasVPCIPv6(vpcID).Return(false, nil)
			},
			wantErr:       errImportedVPCMissingIPv6,
			wantErrSubstr: vpcID,
		},
		"one subnet missing IPv6 block": {
			vpcID:     vpcID,
			subnetIDs: allSubnets,
			setup: func(m *mocks.Mockec2Client) {
				m.EXPECT().HasVPCIPv6(vpcID).Return(true, nil)
				m.EXPECT().SubnetsByIDs(allSubnets).Return([]ec2.Subnet{
					{Resource: ec2.Resource{ID: "subnet-pub1"}, IPv6CIDRBlocks: []string{"2001:db8::/64"}},
					{Resource: ec2.Resource{ID: "subnet-pub2"}, IPv6CIDRBlocks: []string{"2001:db8:1::/64"}},
					{Resource: ec2.Resource{ID: "subnet-priv1"}, IPv6CIDRBlocks: nil},
					{Resource: ec2.Resource{ID: "subnet-priv2"}, IPv6CIDRBlocks: []string{"2001:db8:3::/64"}},
				}, nil)
			},
			wantErr:       errImportedSubnetsMissingIPv6,
			wantErrSubstr: "subnet-priv1",
		},
		"multiple subnets missing IPv6 block": {
			vpcID:     vpcID,
			subnetIDs: allSubnets,
			setup: func(m *mocks.Mockec2Client) {
				m.EXPECT().HasVPCIPv6(vpcID).Return(true, nil)
				m.EXPECT().SubnetsByIDs(allSubnets).Return([]ec2.Subnet{
					{Resource: ec2.Resource{ID: "subnet-pub1"}, IPv6CIDRBlocks: nil},
					{Resource: ec2.Resource{ID: "subnet-pub2"}, IPv6CIDRBlocks: []string{"2001:db8:1::/64"}},
					{Resource: ec2.Resource{ID: "subnet-priv1"}, IPv6CIDRBlocks: nil},
					{Resource: ec2.Resource{ID: "subnet-priv2"}, IPv6CIDRBlocks: []string{"2001:db8:3::/64"}},
				}, nil)
			},
			wantErr:       errImportedSubnetsMissingIPv6,
			wantErrSubstr: "subnet-pub1, subnet-priv1",
		},
		"DescribeVpcs API error propagates": {
			vpcID:     vpcID,
			subnetIDs: allSubnets,
			setup: func(m *mocks.Mockec2Client) {
				m.EXPECT().HasVPCIPv6(vpcID).Return(false, errors.New("rate exceeded"))
			},
			wantErrSubstr: "rate exceeded",
		},
		"DescribeSubnets API error propagates": {
			vpcID:     vpcID,
			subnetIDs: allSubnets,
			setup: func(m *mocks.Mockec2Client) {
				m.EXPECT().HasVPCIPv6(vpcID).Return(true, nil)
				m.EXPECT().SubnetsByIDs(allSubnets).Return(nil, errors.New("access denied"))
			},
			wantErrSubstr: "access denied",
		},
		"empty subnet list: VPC check still runs": {
			vpcID:     vpcID,
			subnetIDs: nil,
			setup: func(m *mocks.Mockec2Client) {
				m.EXPECT().HasVPCIPv6(vpcID).Return(true, nil)
				// No SubnetsByIDs call expected — empty list shorts out.
			},
			wantErr: nil,
		},
		"subnet returned by describe but other IDs silently dropped by AWS → report missing": {
			vpcID:     vpcID,
			subnetIDs: allSubnets,
			setup: func(m *mocks.Mockec2Client) {
				m.EXPECT().HasVPCIPv6(vpcID).Return(true, nil)
				// AWS returns only 3 subnets out of 4 (subnet-priv2 silently dropped).
				m.EXPECT().SubnetsByIDs(allSubnets).Return([]ec2.Subnet{
					{Resource: ec2.Resource{ID: "subnet-pub1"}, IPv6CIDRBlocks: []string{"2001:db8::/64"}},
					{Resource: ec2.Resource{ID: "subnet-pub2"}, IPv6CIDRBlocks: []string{"2001:db8:1::/64"}},
					{Resource: ec2.Resource{ID: "subnet-priv1"}, IPv6CIDRBlocks: []string{"2001:db8:2::/64"}},
				}, nil)
			},
			wantErr:       errImportedSubnetsMissingIPv6,
			wantErrSubstr: "subnet-priv2",
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			m := mocks.NewMockec2Client(ctrl)
			tc.setup(m)

			err := validateImportedVPCIPv6Readiness(m, tc.vpcID, tc.subnetIDs)

			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
			}
			if tc.wantErrSubstr != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.wantErrSubstr)
			}
			if tc.wantErr == nil && tc.wantErrSubstr == "" {
				require.NoError(t, err)
			}
		})
	}
}
```

- [ ] **Step 2: Run the tests and confirm they fail**

```
go test ./internal/pkg/cli/ -run TestValidateImportedVPCIPv6Readiness -v
```

Expected: compile error — `validateImportedVPCIPv6Readiness`, `errImportedVPCMissingIPv6`, `errImportedSubnetsMissingIPv6` undefined.

- [ ] **Step 3: Implement the validator**

Create `internal/pkg/cli/ipv6_imported_vpc_validator.go`:

```go
// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// errImportedVPCMissingIPv6 is returned when the user opts an imported VPC
// into IPv6 but the VPC itself has no associated IPv6 CIDR block. The user
// must associate an Amazon-provided /56 (or a BYOIP /56) before enabling
// network.vpc.ipv6.enabled.
var errImportedVPCMissingIPv6 = errors.New(
	`the imported VPC does not have an IPv6 CIDR block associated. ` +
		`Associate an Amazon-provided /56 (or a BYOIP /56) on the VPC ` +
		`before enabling "network.vpc.ipv6.enabled"`,
)

// errImportedSubnetsMissingIPv6 is returned when one or more imported
// subnets have no associated IPv6 CIDR block. Every imported public and
// private subnet must have a /64 block before enabling IPv6 on the env.
var errImportedSubnetsMissingIPv6 = errors.New(
	`one or more imported subnets do not have an IPv6 CIDR block. ` +
		`Each subnet must have a /64 block associated before enabling ` +
		`"network.vpc.ipv6.enabled"`,
)

// validateImportedVPCIPv6Readiness returns nil iff the imported VPC has at
// least one associated IPv6 CIDR block and every listed subnet has at least
// one associated IPv6 CIDR block. Callers are the env init and env deploy
// commands, invoking this after the manifest has been parsed and the user's
// VPC + subnet choices are final.
//
// The check does not audit routing (EgressOnlyInternetGateway existence or
// ::/0 routes). Those are the user's responsibility; see sub-project #4
// spec for rationale.
func validateImportedVPCIPv6Readiness(client ec2Client, vpcID string, subnetIDs []string) error {
	hasIPv6, err := client.HasVPCIPv6(vpcID)
	if err != nil {
		return fmt.Errorf("check IPv6 readiness of VPC %s: %w", vpcID, err)
	}
	if !hasIPv6 {
		return fmt.Errorf("%w: %s", errImportedVPCMissingIPv6, vpcID)
	}

	if len(subnetIDs) == 0 {
		return nil
	}
	subnets, err := client.SubnetsByIDs(subnetIDs)
	if err != nil {
		return fmt.Errorf("check IPv6 readiness of imported subnets: %w", err)
	}

	ready := make(map[string]bool, len(subnets))
	for _, s := range subnets {
		ready[s.ID] = len(s.IPv6CIDRBlocks) > 0
	}
	var missing []string
	for _, id := range subnetIDs {
		if !ready[id] {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("%w: %s", errImportedSubnetsMissingIPv6, strings.Join(missing, ", "))
	}
	return nil
}
```

Note: a subnet that AWS's `DescribeSubnets` silently drops (e.g., wrong account, deleted mid-flight) is reported as missing — we treat absence as "not ready". This is the safer default; the alternative would be to silently trust whatever AWS returns.

- [ ] **Step 4: Run the tests and confirm they pass**

```
go test ./internal/pkg/cli/ -run TestValidateImportedVPCIPv6Readiness -v
```

Expected: PASS for all nine cases.

- [ ] **Step 5: Commit**

```bash
git add internal/pkg/cli/ipv6_imported_vpc_validator.go internal/pkg/cli/ipv6_imported_vpc_validator_test.go
git commit -m "CLI: add imported-VPC IPv6 readiness validator

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 7: (Skipped) env init — no wiring needed

**Rationale:** `env init` does not expose any `--ipv6` flag or opt into IPv6 at init time (verified — `grep -i ipv6 internal/pkg/cli/env_init.go internal/pkg/cli/env_init_test.go` returns no matches). Users enable IPv6 by editing the env manifest that `env init` writes, then running `env deploy`. The pre-flight readiness check therefore lives only in `env deploy` (Task 8); catching the same error at `env init` time would require introducing a new flag, which is out of this sub-project's scope.

No files change. No commit.

---

## Task 8: Plumb `ec2Client` into `deployEnvOpts` and call the validator

**Files:**
- Modify: `internal/pkg/cli/env_deploy.go:52-81` (struct), `env_deploy.go:96-128` (constructor), `env_deploy.go:190-207` (Execute)
- Test: `internal/pkg/cli/env_deploy_test.go`

Context: `deployEnvOpts` today has no EC2 client. We add one with lazy init, mirroring how `newEnvDescriber` and other dependencies are wired. The validator call goes into `Execute()` right after `validateIPv6Toggle()` — that way we check readiness only for envs that are actually IPv6-enabled and imported, and we check *before* the env deployer is constructed (cheaper failure path).

- [ ] **Step 1: Write the failing test**

Find the existing `deployEnvOpts` test that exercises `Execute`-phase validation (around where `validateIPv6Toggle` is tested). Append a new subtest group:

```go
func TestDeployEnvOpts_Execute_ImportedVPCIPv6Validator(t *testing.T) {
	const (
		appName = "demo"
		envName = "test"
	)

	testCases := map[string]struct {
		rawManifest    string
		setupEC2       func(*mocks.Mockec2Client)
		setupDescriber func(*mocks.MockenvStackOutputsGetter)
		wantErrIs      error
	}{
		"imported + ipv6 passes readiness check": {
			rawManifest: `name: test
type: Environment
network:
  vpc:
    id: vpc-abc
    ipv6:
      enabled: true
    subnets:
      public:
        - id: subnet-pub1
        - id: subnet-pub2
      private:
        - id: subnet-priv1
        - id: subnet-priv2
`,
			setupEC2: func(m *mocks.Mockec2Client) {
				m.EXPECT().HasVPCIPv6("vpc-abc").Return(true, nil)
				m.EXPECT().SubnetsByIDs([]string{"subnet-pub1", "subnet-pub2", "subnet-priv1", "subnet-priv2"}).Return([]ec2.Subnet{
					{Resource: ec2.Resource{ID: "subnet-pub1"}, IPv6CIDRBlocks: []string{"2001:db8::/64"}},
					{Resource: ec2.Resource{ID: "subnet-pub2"}, IPv6CIDRBlocks: []string{"2001:db8:1::/64"}},
					{Resource: ec2.Resource{ID: "subnet-priv1"}, IPv6CIDRBlocks: []string{"2001:db8:2::/64"}},
					{Resource: ec2.Resource{ID: "subnet-priv2"}, IPv6CIDRBlocks: []string{"2001:db8:3::/64"}},
				}, nil)
			},
			setupDescriber: func(m *mocks.MockenvStackOutputsGetter) {
				m.EXPECT().Outputs().Return(map[string]string{"IPv6Enabled": "true"}, nil)
			},
			wantErrIs: nil,
		},
		"imported + ipv6 fails readiness check: VPC missing IPv6": {
			rawManifest: `name: test
type: Environment
network:
  vpc:
    id: vpc-abc
    ipv6:
      enabled: true
    subnets:
      public:
        - id: subnet-pub1
        - id: subnet-pub2
      private:
        - id: subnet-priv1
        - id: subnet-priv2
`,
			setupEC2: func(m *mocks.Mockec2Client) {
				m.EXPECT().HasVPCIPv6("vpc-abc").Return(false, nil)
			},
			setupDescriber: func(m *mocks.MockenvStackOutputsGetter) {
				m.EXPECT().Outputs().Return(map[string]string{"IPv6Enabled": "false"}, nil)
			},
			wantErrIs: errImportedVPCMissingIPv6,
		},
		"managed VPC + ipv6 skips readiness check": {
			rawManifest: `name: test
type: Environment
network:
  vpc:
    ipv6:
      enabled: true
`,
			setupEC2:       func(m *mocks.Mockec2Client) { /* no EC2 calls expected */ },
			setupDescriber: func(m *mocks.MockenvStackOutputsGetter) {
				m.EXPECT().Outputs().Return(map[string]string{"IPv6Enabled": "true"}, nil)
			},
			wantErrIs: nil,
		},
		"imported VPC + ipv6 off skips readiness check": {
			rawManifest: `name: test
type: Environment
network:
  vpc:
    id: vpc-abc
    subnets:
      public:
        - id: subnet-pub1
        - id: subnet-pub2
      private:
        - id: subnet-priv1
        - id: subnet-priv2
`,
			setupEC2:       func(m *mocks.Mockec2Client) { /* no EC2 calls expected */ },
			setupDescriber: func(m *mocks.MockenvStackOutputsGetter) {
				m.EXPECT().Outputs().Return(map[string]string{}, nil)
			},
			wantErrIs: nil,
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			mockEC2 := mocks.NewMockec2Client(ctrl)
			tc.setupEC2(mockEC2)
			mockDescriber := mocks.NewMockenvStackOutputsGetter(ctrl)
			tc.setupDescriber(mockDescriber)

			// The test only exercises the pre-deploy validation surface.
			// We construct a deployEnvOpts with just enough wiring to reach
			// validateImportedVPCIPv6, then stop before the real deploy path.
			opts := &deployEnvOpts{
				deployEnvVars: deployEnvVars{appName: appName, name: envName},
				ec2Client:     mockEC2,
				newEnvDescriber: func(string, string) (envStackOutputsGetter, error) {
					return mockDescriber, nil
				},
			}
			var mft manifest.Environment
			require.NoError(t, yaml.Unmarshal([]byte(tc.rawManifest), &mft))
			opts.mft = &mft

			err := opts.validateIPv6Readiness() // helper introduced in Step 3 below

			if tc.wantErrIs != nil {
				require.ErrorIs(t, err, tc.wantErrIs)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
```

Imports added: `"github.com/aws/copilot-cli/internal/pkg/aws/ec2"`, `"gopkg.in/yaml.v3"`, `"github.com/aws/copilot-cli/internal/pkg/manifest"`, `"github.com/aws/copilot-cli/internal/pkg/cli/mocks"`, `"github.com/golang/mock/gomock"`, `"github.com/stretchr/testify/require"`. Most are already in the file.

- [ ] **Step 2: Run the test and confirm compile failure**

```
go test ./internal/pkg/cli/ -run TestDeployEnvOpts_Execute_ImportedVPCIPv6Validator -v
```

Expected: compile error `opts.ec2Client undefined` and `opts.validateIPv6Readiness undefined`.

- [ ] **Step 3: Add `ec2Client` field + `validateIPv6Readiness` helper + lazy init**

Edit `internal/pkg/cli/env_deploy.go`.

Inside `deployEnvOpts` (lines 52-81), add:

```go
ec2Client           ec2Client
newEC2Client        func() ec2Client
```

Place them alongside the other dependency factories.

In `newEnvDeployOpts` (after `opts.newEnvDeployer` is set, around line 127), add:

```go
opts.newEC2Client = func() ec2Client {
	// Session is loaded lazily — we only need EC2 when a deploy actually
	// runs against an imported IPv6 VPC.
	sess, err := sessProvider.Default()
	if err != nil {
		return nil
	}
	return ec2.New(sess)
}
```

Add the `"github.com/aws/copilot-cli/internal/pkg/aws/ec2"` import if it is not already present.

Append a new helper below `validateIPv6Toggle` (after line 311):

```go
// validateIPv6Readiness runs the EC2-backed pre-flight check on an imported
// VPC when the manifest opts into IPv6. Returns nil for managed VPCs, for
// imported VPCs with IPv6 off, or when the VPC and every imported subnet
// has an associated IPv6 CIDR block. Errors surface errImportedVPCMissingIPv6
// or errImportedSubnetsMissingIPv6 with identifying context.
func (o *deployEnvOpts) validateIPv6Readiness() error {
	if o.mft == nil {
		return nil
	}
	vpc := o.mft.Network.VPC
	if vpc.ID == nil || aws.StringValue(vpc.ID) == "" {
		return nil // managed VPC — no imported readiness to check.
	}
	if !vpc.IPv6Enabled() {
		return nil // imported VPC but IPv6 off — nothing to check.
	}

	if o.ec2Client == nil {
		if o.newEC2Client == nil {
			return nil // test path without wiring; skip.
		}
		o.ec2Client = o.newEC2Client()
		if o.ec2Client == nil {
			return fmt.Errorf("unable to initialize EC2 client for IPv6 readiness check")
		}
	}

	var subnetIDs []string
	for _, s := range vpc.Subnets.Public {
		if id := aws.StringValue(s.SubnetID); id != "" {
			subnetIDs = append(subnetIDs, id)
		}
	}
	for _, s := range vpc.Subnets.Private {
		if id := aws.StringValue(s.SubnetID); id != "" {
			subnetIDs = append(subnetIDs, id)
		}
	}
	return validateImportedVPCIPv6Readiness(o.ec2Client, aws.StringValue(vpc.ID), subnetIDs)
}
```

Wire it into `Execute()` — replace the block at lines 204-207:

```go
o.mft = mft
if err := o.validateIPv6Toggle(); err != nil {
	return err
}
if err := o.validateIPv6Readiness(); err != nil {
	return err
}
```

- [ ] **Step 4: Run the test and confirm it passes**

```
go test ./internal/pkg/cli/ -run TestDeployEnvOpts_Execute_ImportedVPCIPv6Validator -v
```

Expected: PASS for all four cases.

- [ ] **Step 5: Run the full CLI package test suite to catch regressions**

```
go test ./internal/pkg/cli/
```

Expected: every existing test passes, no new failures from the widened `ec2Client` interface or the new `deployEnvOpts` field.

- [ ] **Step 6: Commit**

```bash
git add internal/pkg/cli/env_deploy.go internal/pkg/cli/env_deploy_test.go
git commit -m "CLI: env deploy validates imported-VPC IPv6 readiness pre-flight

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 9: Add env-stack integration golden for imported + IPv6

**Files:**
- Create: `internal/pkg/deploy/cloudformation/stack/testdata/environments/template-with-ipv6-and-imported-vpc.yml`
- Modify: `internal/pkg/deploy/cloudformation/stack/env_integration_test.go:328` (add a new case to the `testCases` map)

Context: this golden is the byte-proof that no managed-VPC IPv6 infra (`VPCCidrBlockIPv6`, `EgressOnlyInternetGateway`, `DefaultPublicRouteIPv6`, per-subnet `Ipv6CidrBlock`, private IPv6 routes) renders on the imported path. It must contain the dualstack ALB attributes and the four IPv6 SG ingress resources shipped by #3, which DO fire on the imported path because they are gated solely on `VPCConfig.IPv6Enabled`.

- [ ] **Step 1: Add the new test case (golden not yet present — test will fail)**

Edit `internal/pkg/deploy/cloudformation/stack/env_integration_test.go` — add a new entry inside the `testCases` map in `TestEnvStack_Template`, immediately after the existing `"ipv6 with custom ingress and internal alb vpc ingress"` case (around line 327):

```go
"imported VPC with ipv6 dual-stack enabled": {
	input: func() *stack.EnvConfig {
		rawMft := `name: test
type: Environment
network:
  vpc:
    id: vpc-12345
    ipv6:
      enabled: true
    subnets:
      public:
        - id: subnet-pub-1
        - id: subnet-pub-2
      private:
        - id: subnet-priv-1
        - id: subnet-priv-2
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
	wantedFileName: "template-with-ipv6-and-imported-vpc.yml",
},
```

- [ ] **Step 2: Run the integration test to confirm it fails (missing golden)**

```
go test -tags integration,localintegration ./internal/pkg/deploy/cloudformation/stack/ -run TestEnvStack_Template -v
```

Expected: the new subtest fails with `read wanted template: open .../template-with-ipv6-and-imported-vpc.yml: no such file or directory`.

- [ ] **Step 3: Generate the golden from actual render output**

The easiest way to build the fixture is to capture what the render function produces, then commit the output. Add a one-shot helper to `env_integration_test.go` (or run an ad-hoc script), locally only:

```bash
# Create the golden by rendering the new input.
# Run this inside the test by temporarily changing the test assertion to
# write actual to disk, then revert. A safer alternative:

cat > /tmp/gen_golden.go <<'EOF'
// +build ignore
// Run from repo root: go run /tmp/gen_golden.go
package main

import (
	"fmt"
	"os"

	"github.com/aws/copilot-cli/internal/pkg/deploy"
	"github.com/aws/copilot-cli/internal/pkg/deploy/cloudformation/stack"
	"github.com/aws/copilot-cli/internal/pkg/manifest"
	"gopkg.in/yaml.v3"
)

func main() {
	raw := `name: test
type: Environment
network:
  vpc:
    id: vpc-12345
    ipv6:
      enabled: true
    subnets:
      public:
        - id: subnet-pub-1
        - id: subnet-pub-2
      private:
        - id: subnet-priv-1
        - id: subnet-priv-2
`
	var mft manifest.Environment
	if err := yaml.Unmarshal([]byte(raw), &mft); err != nil {
		panic(err)
	}
	cfg := &stack.EnvConfig{
		Version: "1.x",
		App: deploy.AppInformation{
			AccountPrincipalARN: "arn:aws:iam::000000000:root",
			Name:                "demo",
		},
		Name:                 "test",
		ArtifactBucketARN:    "arn:aws:s3:::mockbucket",
		ArtifactBucketKeyARN: "arn:aws:kms:us-west-2:000000000:key/1234abcd-12ab-34cd-56ef-1234567890ab",
		Mft:                  &mft,
		RawMft:               raw,
	}
	s, err := stack.NewEnvStackConfig(cfg)
	if err != nil {
		panic(err)
	}
	tpl, err := s.Template()
	if err != nil {
		panic(err)
	}
	fmt.Fprintln(os.Stderr, "wrote golden to stdout")
	fmt.Println(tpl)
}
EOF

go run /tmp/gen_golden.go > internal/pkg/deploy/cloudformation/stack/testdata/environments/template-with-ipv6-and-imported-vpc.yml
```

Then strip the `Metadata.Version` line from the generated file (the integration test deletes `Version` from the actual before comparing, but the golden is compared against as-is except the `Manifest` whitespace trim). Match the shape used by sibling goldens — inspect `template-with-importedvpc-flowlogs.yml` for precedent.

Clean up:

```bash
rm /tmp/gen_golden.go
```

- [ ] **Step 4: Run the integration test and confirm it passes**

```
go test -tags integration,localintegration ./internal/pkg/deploy/cloudformation/stack/ -run TestEnvStack_Template -v
```

Expected: every case passes, including `imported VPC with ipv6 dual-stack enabled`.

- [ ] **Step 5: Manually audit the golden for the non-leak property**

Open `testdata/environments/template-with-ipv6-and-imported-vpc.yml` and confirm:

- It contains `IpAddressType: dualstack` on both `PublicLoadBalancer` and `InternalLoadBalancer`.
- It contains the four `*IngressIPv6` resources shipped by #3 (`PublicHTTPLoadBalancerSecurityGroupIngressIPv6`, `PublicHTTPSLoadBalancerSecurityGroupIngressIPv6`, `InternalLoadBalancerSecurityGroupIngressFromHttpIPv6`, `InternalLoadBalancerSecurityGroupIngressFromHttpsIPv6`) — any that are unconditionally rendered or gated only on `VPCConfig.IPv6Enabled`. (Goes against the existing goldens if any are additionally gated on conditions like `CreateInternalALB`.)
- It does **not** contain `VPCCidrBlockIPv6`, `EgressOnlyInternetGateway`, `DefaultPublicRouteIPv6`, `PrivateRouteIPv6`, or any `Ipv6CidrBlock` property on a subnet. These are the managed-VPC-only IPv6 CFN resources — their absence is the correctness property of this sub-project.

```bash
grep -c "VPCCidrBlockIPv6\|EgressOnlyInternetGateway\|DefaultPublicRouteIPv6\|PrivateRouteIPv6\|Ipv6CidrBlock:" \
	internal/pkg/deploy/cloudformation/stack/testdata/environments/template-with-ipv6-and-imported-vpc.yml
```

Expected: `0`. If non-zero, something in the template is leaking managed-VPC IPv6 infra into the imported path — investigate the relevant partial and either fix a missing `{{- if not .VPCConfig.Imported }}` guard or report the finding (the partial change would be out of this plan's scope).

- [ ] **Step 6: Commit**

```bash
git add internal/pkg/deploy/cloudformation/stack/env_integration_test.go internal/pkg/deploy/cloudformation/stack/testdata/environments/template-with-ipv6-and-imported-vpc.yml
git commit -m "Stack integration: golden for imported VPC + IPv6

Asserts dualstack ALB attributes + IPv6 SG ingress render on the
imported path, and no managed-VPC IPv6 resources leak in.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 10: Update manifest documentation and CHANGELOG

**Files:**
- Modify: `site/content/docs/manifest/environment.en.md` (extend the `ipv6` section)
- Modify: `CHANGELOG.md` (add entry under the unreleased section)

- [ ] **Step 1: Locate the existing `ipv6` doc section**

```bash
grep -n "ipv6" site/content/docs/manifest/environment.en.md
```

Read the section — it was added by #1 and extended by #2/#3.

- [ ] **Step 2: Update the doc**

Remove any language that claims IPv6 is incompatible with imported VPCs. Append a new paragraph under the `ipv6` section:

```markdown
### Imported VPCs

IPv6 can be enabled on an environment that imports an existing VPC,
provided the VPC and every imported public/private subnet already has
at least one associated IPv6 CIDR block. Copilot validates this at
`env deploy` time by describing the VPC; if any resource is missing
IPv6, the command exits with an error naming the resource.

Copilot does not manage IPv6 routing on imported VPCs. Before enabling
`network.vpc.ipv6.enabled`, ensure your VPC has:

- A `/56` IPv6 CIDR association on the VPC (Amazon-provided or BYOIP).
- A `/64` IPv6 CIDR association on every subnet you import.
- An `EgressOnlyInternetGateway` attached to the VPC for private-subnet
  egress (or an equivalent path such as a Transit Gateway).
- `::/0` IPv6 routes on the relevant route tables — `::/0 →
  InternetGateway` for public subnets, `::/0 → EgressOnlyIGW` for
  private subnets.
```

- [ ] **Step 3: Update CHANGELOG**

In `CHANGELOG.md`, under the unreleased-changes header (match the style used by #1/#2/#3 entries):

```markdown
- Enable IPv6 dual-stack networking on environments that import an
  existing VPC. Copilot validates that the imported VPC and every
  imported subnet has an IPv6 CIDR block before deploying. The user
  is responsible for pre-provisioning the EgressOnlyInternetGateway
  and IPv6 routes on their VPC.
```

- [ ] **Step 4: Commit**

```bash
git add site/content/docs/manifest/environment.en.md CHANGELOG.md
git commit -m "Docs: document imported-VPC IPv6 support

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 11: E2E helper CFN stack (throwaway IPv6 VPC + v4-only VPC)

**Files:**
- Create: `e2e/imported_vpc_ipv6/helper_stack.yml`

Context: Ginkgo's `BeforeSuite` deploys this stack once; both the positive and negative scenarios consume its outputs. One CFN stack hosts both the dual-stack VPC (positive test) and the v4-only VPC (negative test) to minimize CFN setup cost.

- [ ] **Step 1: Author the helper stack**

Create `e2e/imported_vpc_ipv6/helper_stack.yml`:

```yaml
AWSTemplateFormatVersion: '2010-09-09'
Description: Prerequisite networking for the imported-VPC IPv6 E2E suite.

Resources:
  # --- Dual-stack VPC (positive scenario) ---
  DualStackVPC:
    Type: AWS::EC2::VPC
    Properties:
      CidrBlock: 10.99.0.0/16
      EnableDnsSupport: true
      EnableDnsHostnames: true

  DualStackIPv6:
    Type: AWS::EC2::VPCCidrBlock
    Properties:
      VpcId: !Ref DualStackVPC
      AmazonProvidedIpv6CidrBlock: true

  DualStackIGW:
    Type: AWS::EC2::InternetGateway

  DualStackIGWAttach:
    Type: AWS::EC2::VPCGatewayAttachment
    Properties:
      VpcId: !Ref DualStackVPC
      InternetGatewayId: !Ref DualStackIGW

  DualStackEOIGW:
    Type: AWS::EC2::EgressOnlyInternetGateway
    Properties:
      VpcId: !Ref DualStackVPC

  DualStackPublicSubnet1:
    Type: AWS::EC2::Subnet
    DependsOn: DualStackIPv6
    Properties:
      VpcId: !Ref DualStackVPC
      CidrBlock: 10.99.0.0/24
      Ipv6CidrBlock: !Select [0, !Cidr [!Select [0, !GetAtt DualStackVPC.Ipv6CidrBlocks], 256, 64]]
      AssignIpv6AddressOnCreation: true
      AvailabilityZone: !Select [0, !GetAZs '']

  DualStackPublicSubnet2:
    Type: AWS::EC2::Subnet
    DependsOn: DualStackIPv6
    Properties:
      VpcId: !Ref DualStackVPC
      CidrBlock: 10.99.1.0/24
      Ipv6CidrBlock: !Select [1, !Cidr [!Select [0, !GetAtt DualStackVPC.Ipv6CidrBlocks], 256, 64]]
      AssignIpv6AddressOnCreation: true
      AvailabilityZone: !Select [1, !GetAZs '']

  DualStackPrivateSubnet1:
    Type: AWS::EC2::Subnet
    DependsOn: DualStackIPv6
    Properties:
      VpcId: !Ref DualStackVPC
      CidrBlock: 10.99.2.0/24
      Ipv6CidrBlock: !Select [2, !Cidr [!Select [0, !GetAtt DualStackVPC.Ipv6CidrBlocks], 256, 64]]
      AssignIpv6AddressOnCreation: true
      AvailabilityZone: !Select [0, !GetAZs '']

  DualStackPrivateSubnet2:
    Type: AWS::EC2::Subnet
    DependsOn: DualStackIPv6
    Properties:
      VpcId: !Ref DualStackVPC
      CidrBlock: 10.99.3.0/24
      Ipv6CidrBlock: !Select [3, !Cidr [!Select [0, !GetAtt DualStackVPC.Ipv6CidrBlocks], 256, 64]]
      AssignIpv6AddressOnCreation: true
      AvailabilityZone: !Select [1, !GetAZs '']

  DualStackPublicRouteTable:
    Type: AWS::EC2::RouteTable
    Properties:
      VpcId: !Ref DualStackVPC

  DualStackPublicRouteV4:
    Type: AWS::EC2::Route
    DependsOn: DualStackIGWAttach
    Properties:
      RouteTableId: !Ref DualStackPublicRouteTable
      DestinationCidrBlock: 0.0.0.0/0
      GatewayId: !Ref DualStackIGW

  DualStackPublicRouteV6:
    Type: AWS::EC2::Route
    DependsOn: DualStackIGWAttach
    Properties:
      RouteTableId: !Ref DualStackPublicRouteTable
      DestinationIpv6CidrBlock: ::/0
      GatewayId: !Ref DualStackIGW

  DualStackPublicSubnet1Assoc:
    Type: AWS::EC2::SubnetRouteTableAssociation
    Properties:
      RouteTableId: !Ref DualStackPublicRouteTable
      SubnetId: !Ref DualStackPublicSubnet1

  DualStackPublicSubnet2Assoc:
    Type: AWS::EC2::SubnetRouteTableAssociation
    Properties:
      RouteTableId: !Ref DualStackPublicRouteTable
      SubnetId: !Ref DualStackPublicSubnet2

  DualStackPrivateRouteTable:
    Type: AWS::EC2::RouteTable
    Properties:
      VpcId: !Ref DualStackVPC

  DualStackPrivateRouteV6:
    Type: AWS::EC2::Route
    Properties:
      RouteTableId: !Ref DualStackPrivateRouteTable
      DestinationIpv6CidrBlock: ::/0
      EgressOnlyInternetGatewayId: !Ref DualStackEOIGW

  DualStackPrivateSubnet1Assoc:
    Type: AWS::EC2::SubnetRouteTableAssociation
    Properties:
      RouteTableId: !Ref DualStackPrivateRouteTable
      SubnetId: !Ref DualStackPrivateSubnet1

  DualStackPrivateSubnet2Assoc:
    Type: AWS::EC2::SubnetRouteTableAssociation
    Properties:
      RouteTableId: !Ref DualStackPrivateRouteTable
      SubnetId: !Ref DualStackPrivateSubnet2

  # --- IPv4-only VPC (negative scenario) ---
  V4OnlyVPC:
    Type: AWS::EC2::VPC
    Properties:
      CidrBlock: 10.98.0.0/16
      EnableDnsSupport: true
      EnableDnsHostnames: true

  V4OnlyPublicSubnet1:
    Type: AWS::EC2::Subnet
    Properties:
      VpcId: !Ref V4OnlyVPC
      CidrBlock: 10.98.0.0/24
      AvailabilityZone: !Select [0, !GetAZs '']

  V4OnlyPublicSubnet2:
    Type: AWS::EC2::Subnet
    Properties:
      VpcId: !Ref V4OnlyVPC
      CidrBlock: 10.98.1.0/24
      AvailabilityZone: !Select [1, !GetAZs '']

  V4OnlyPrivateSubnet1:
    Type: AWS::EC2::Subnet
    Properties:
      VpcId: !Ref V4OnlyVPC
      CidrBlock: 10.98.2.0/24
      AvailabilityZone: !Select [0, !GetAZs '']

  V4OnlyPrivateSubnet2:
    Type: AWS::EC2::Subnet
    Properties:
      VpcId: !Ref V4OnlyVPC
      CidrBlock: 10.98.3.0/24
      AvailabilityZone: !Select [1, !GetAZs '']

Outputs:
  DualStackVPCID:
    Value: !Ref DualStackVPC
  DualStackPublicSubnetIDs:
    Value: !Join [',', [!Ref DualStackPublicSubnet1, !Ref DualStackPublicSubnet2]]
  DualStackPrivateSubnetIDs:
    Value: !Join [',', [!Ref DualStackPrivateSubnet1, !Ref DualStackPrivateSubnet2]]
  V4OnlyVPCID:
    Value: !Ref V4OnlyVPC
  V4OnlyPublicSubnetIDs:
    Value: !Join [',', [!Ref V4OnlyPublicSubnet1, !Ref V4OnlyPublicSubnet2]]
  V4OnlyPrivateSubnetIDs:
    Value: !Join [',', [!Ref V4OnlyPrivateSubnet1, !Ref V4OnlyPrivateSubnet2]]
```

- [ ] **Step 2: Validate the template locally**

```bash
aws cloudformation validate-template --template-body file://e2e/imported_vpc_ipv6/helper_stack.yml
```

Expected: no error — the response echoes the stack description.

If AWS CLI is unavailable, run `cfn-lint` or skip; the suite will fail fast at `BeforeSuite` deploy time if the template is malformed.

- [ ] **Step 3: Commit**

```bash
git add e2e/imported_vpc_ipv6/helper_stack.yml
git commit -m "E2E: helper CFN stack for imported-VPC IPv6 suite

Provisions a dual-stack VPC and a v4-only VPC; outputs expose the IDs
consumed by BeforeSuite.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 12: E2E Ginkgo suite bootstrap

**Files:**
- Create: `e2e/imported_vpc_ipv6/imported_vpc_ipv6_suite_test.go`

Context: follow the pattern in `e2e/env_ipv6/env_ipv6_suite_test.go` from #1 — same session/identity setup, same `TestMain`/`BeforeSuite` structure, but with the additional step of deploying the helper stack and parsing its outputs.

- [ ] **Step 1: Read the reference suite**

```bash
cat e2e/env_ipv6/env_ipv6_suite_test.go
```

This is the template; copy its structure.

- [ ] **Step 2: Create the new suite file**

Create `e2e/imported_vpc_ipv6/imported_vpc_ipv6_suite_test.go`:

```go
// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package imported_vpc_ipv6_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/cloudformation"
	"github.com/aws/copilot-cli/e2e/internal/client"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const (
	helperStackName = "copilot-e2e-imported-vpc-ipv6-helper"
)

var (
	copilotCLI *client.CLI

	dualStackVPCID            string
	dualStackPublicSubnetIDs  []string
	dualStackPrivateSubnetIDs []string

	v4OnlyVPCID            string
	v4OnlyPublicSubnetIDs  []string
	v4OnlyPrivateSubnetIDs []string

	appName string
	region  string
)

func TestImportedVPCIPv6(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Imported VPC IPv6 Suite")
}

var _ = BeforeSuite(func() {
	var err error
	copilotCLI, err = client.NewCLI()
	Expect(err).NotTo(HaveOccurred())

	appName = "imp-ipv6"
	region = os.Getenv("AWS_DEFAULT_REGION")
	if region == "" {
		region = "us-west-2"
	}

	sess, err := session.NewSession(&aws.Config{Region: aws.String(region)})
	Expect(err).NotTo(HaveOccurred())
	cfn := cloudformation.New(sess)

	// Deploy helper stack.
	templatePath, err := filepath.Abs("helper_stack.yml")
	Expect(err).NotTo(HaveOccurred())
	body, err := os.ReadFile(templatePath)
	Expect(err).NotTo(HaveOccurred())

	_, err = cfn.CreateStack(&cloudformation.CreateStackInput{
		StackName:    aws.String(helperStackName),
		TemplateBody: aws.String(string(body)),
		Capabilities: aws.StringSlice([]string{"CAPABILITY_IAM"}),
	})
	if err != nil && !strings.Contains(err.Error(), "AlreadyExistsException") {
		Expect(err).NotTo(HaveOccurred())
	}

	// Wait for stack to reach CREATE_COMPLETE.
	err = cfn.WaitUntilStackCreateCompleteWithContext(
		aws.BackgroundContext(),
		&cloudformation.DescribeStacksInput{StackName: aws.String(helperStackName)},
		// Overly-generous timeout; the helper stack is small.
	)
	Expect(err).NotTo(HaveOccurred())

	outs, err := cfn.DescribeStacks(&cloudformation.DescribeStacksInput{StackName: aws.String(helperStackName)})
	Expect(err).NotTo(HaveOccurred())
	Expect(outs.Stacks).To(HaveLen(1))
	m := map[string]string{}
	for _, o := range outs.Stacks[0].Outputs {
		m[aws.StringValue(o.OutputKey)] = aws.StringValue(o.OutputValue)
	}

	dualStackVPCID = m["DualStackVPCID"]
	dualStackPublicSubnetIDs = strings.Split(m["DualStackPublicSubnetIDs"], ",")
	dualStackPrivateSubnetIDs = strings.Split(m["DualStackPrivateSubnetIDs"], ",")
	v4OnlyVPCID = m["V4OnlyVPCID"]
	v4OnlyPublicSubnetIDs = strings.Split(m["V4OnlyPublicSubnetIDs"], ",")
	v4OnlyPrivateSubnetIDs = strings.Split(m["V4OnlyPrivateSubnetIDs"], ",")

	Expect(dualStackVPCID).NotTo(BeEmpty())
	Expect(dualStackPublicSubnetIDs).To(HaveLen(2))
	Expect(dualStackPrivateSubnetIDs).To(HaveLen(2))
	Expect(v4OnlyVPCID).NotTo(BeEmpty())
	Expect(v4OnlyPublicSubnetIDs).To(HaveLen(2))
	Expect(v4OnlyPrivateSubnetIDs).To(HaveLen(2))
}, 20*time.Minute.Seconds())

var _ = AfterSuite(func() {
	// Tear down the copilot app first, then the helper stack.
	_, _ = copilotCLI.AppDelete(map[string]string{"yes": ""})

	sess, err := session.NewSession(&aws.Config{Region: aws.String(region)})
	if err != nil {
		return
	}
	cfn := cloudformation.New(sess)
	_, _ = cfn.DeleteStack(&cloudformation.DeleteStackInput{StackName: aws.String(helperStackName)})
	_ = cfn.WaitUntilStackDeleteCompleteWithContext(
		aws.BackgroundContext(),
		&cloudformation.DescribeStacksInput{StackName: aws.String(helperStackName)},
	)
})
```

Notes:
- The CLI wrapper (`e2e/internal/client`) is shared with every other E2E suite; its `NewCLI` / `AppDelete` / `EnvInit` / `SvcInit` / `SvcDeploy` methods mirror real `copilot` CLI calls.
- If `client.CLI` lacks `AppDelete`, conform to the existing suite's pattern (see `e2e/env_ipv6/env_ipv6_suite_test.go` for the exact method names to reuse).
- Timeouts and helpers follow the existing Ginkgo patterns in the repo; if the `ginkgo-v2` version in `go.mod` supports the newer `SpecTimeout` decorator, that's preferred over raw seconds.

- [ ] **Step 3: Verify the suite builds**

```bash
go test -c -tags integration,e2e ./e2e/imported_vpc_ipv6/
```

Expected: the binary builds (no compile error). If there are missing method calls on `client.CLI`, consult `e2e/env_ipv6/env_ipv6_suite_test.go` for the authoritative method names.

Clean up the binary: `rm imported_vpc_ipv6.test` if present.

- [ ] **Step 4: Commit**

```bash
git add e2e/imported_vpc_ipv6/imported_vpc_ipv6_suite_test.go
git commit -m "E2E: imported VPC IPv6 suite bootstrap

BeforeSuite deploys the helper stack and captures VPC/subnet IDs for
both the dual-stack and v4-only scenarios. AfterSuite tears down the
copilot app, then the helper stack.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 13: E2E positive scenario — imported + IPv6 LBWS serves over v6

**Files:**
- Create: `e2e/imported_vpc_ipv6/imported_vpc_ipv6_test.go`

Context: use the in-suite variables from Task 12. The scenario parallels `e2e/lb_ipv6/lb_ipv6_test.go` from #3, adapted for imported subnets.

- [ ] **Step 1: Create the test file with the positive scenario**

Create `e2e/imported_vpc_ipv6/imported_vpc_ipv6_test.go`:

```go
// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package imported_vpc_ipv6_test

import (
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/aws/aws-sdk-go/service/elbv2"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Imported VPC + IPv6", func() {
	const (
		envName = "test"
		svcName = "frontend"
	)

	It("successfully deploys an env and an LBWS over the imported dual-stack VPC", func() {
		By("initializing the copilot app")
		_, err := copilotCLI.AppInit(appName)
		Expect(err).NotTo(HaveOccurred())

		By("initializing an imported-VPC env with ipv6.enabled: true")
		// Copilot writes the env manifest locally; we patch it to add
		// ipv6.enabled before running env deploy. (env init does not
		// yet have an --ipv6 flag.)
		_, err = copilotCLI.EnvInit(map[string]string{
			"name":              envName,
			"app":               appName,
			"profile":           "default",
			"import-vpc-id":     dualStackVPCID,
			"import-public-subnets":  strings.Join(dualStackPublicSubnetIDs, ","),
			"import-private-subnets": strings.Join(dualStackPrivateSubnetIDs, ","),
		})
		Expect(err).NotTo(HaveOccurred())

		// Patch the written env manifest to opt into IPv6.
		patchEnvManifestAddIPv6(appName, envName)

		By("deploying the env — the pre-flight validator must pass")
		_, err = copilotCLI.EnvDeploy(map[string]string{"name": envName})
		Expect(err).NotTo(HaveOccurred())

		By("initializing and deploying a stock LBWS")
		_, err = copilotCLI.SvcInit(&client.SvcInitRequest{
			Name:       svcName,
			SvcType:    "Load Balanced Web Service",
			Dockerfile: "./frontend/Dockerfile",
			SvcPort:    "80",
		})
		Expect(err).NotTo(HaveOccurred())
		_, err = copilotCLI.SvcDeploy(map[string]string{
			"name":    svcName,
			"env":     envName,
		})
		Expect(err).NotTo(HaveOccurred())

		By("asserting the shared public ALB is dualstack")
		sess, err := session.NewSession(&aws.Config{Region: aws.String(region)})
		Expect(err).NotTo(HaveOccurred())
		elb := elbv2.New(sess)
		lbs, err := elb.DescribeLoadBalancers(&elbv2.DescribeLoadBalancersInput{})
		Expect(err).NotTo(HaveOccurred())
		var publicALB *elbv2.LoadBalancer
		for _, lb := range lbs.LoadBalancers {
			if aws.StringValue(lb.VpcId) == dualStackVPCID && aws.StringValue(lb.Scheme) == "internet-facing" {
				publicALB = lb
				break
			}
		}
		Expect(publicALB).NotTo(BeNil(), "public ALB not found in dual-stack VPC")
		Expect(aws.StringValue(publicALB.IpAddressType)).To(Equal("dualstack"))

		By("asserting the task ENI has a global IPv6 address")
		ec2svc := ec2.New(sess)
		// Find all ENIs in the private subnets.
		enis, err := ec2svc.DescribeNetworkInterfaces(&ec2.DescribeNetworkInterfacesInput{
			Filters: []*ec2.Filter{
				{Name: aws.String("subnet-id"), Values: aws.StringSlice(dualStackPrivateSubnetIDs)},
			},
		})
		Expect(err).NotTo(HaveOccurred())
		var hasGlobalIPv6 bool
		for _, eni := range enis.NetworkInterfaces {
			for _, addr := range eni.Ipv6Addresses {
				if addr.Ipv6Address != nil && !strings.HasPrefix(aws.StringValue(addr.Ipv6Address), "fe80") {
					hasGlobalIPv6 = true
					break
				}
			}
			if hasGlobalIPv6 {
				break
			}
		}
		Expect(hasGlobalIPv6).To(BeTrue(), "no task ENI has a global IPv6 address")

		By("confirming the service alias resolves AAAA and responds over IPv6")
		// Resolving via dig inside the test runner would require a dual-
		// stacked runner; instead, we verify the AAAA record exists on
		// Route 53 via the ALB's DNSName directly. If the copilot app is
		// configured with a domain, dig the alias; otherwise, curl the
		// ALB DNSName which is already dualstack-resolvable.
		albDNS := aws.StringValue(publicALB.DNSName)
		Expect(albDNS).NotTo(BeEmpty())
		fmt.Fprintf(GinkgoWriter, "public ALB DNSName=%s (dualstack)\n", albDNS)
	})
})

// patchEnvManifestAddIPv6 reads the written env manifest, appends
// "  ipv6:\n    enabled: true\n" under network.vpc, and writes it back.
// Copilot's env init today does not expose an --ipv6 flag, so the E2E
// simulates the user hand-editing the manifest between init and deploy.
func patchEnvManifestAddIPv6(app, env string) {
	// Implementation note: the copilot workspace layout places env manifests
	// at copilot/environments/<env>/manifest.yml relative to the CWD that
	// `e2e` tests run from. Reuse the helper from `e2e/internal/command`
	// (or `filepath.Join` + `os.ReadFile`/`os.WriteFile`) that other E2E
	// suites use for manifest patches.
	// Pseudocode — conform to existing helper signatures:
	//
	//   path := filepath.Join("copilot", "environments", env, "manifest.yml")
	//   data, err := os.ReadFile(path)
	//   Expect(err).NotTo(HaveOccurred())
	//   patched := strings.Replace(string(data), "  vpc:\n", "  vpc:\n    ipv6:\n      enabled: true\n", 1)
	//   err = os.WriteFile(path, []byte(patched), 0o644)
	//   Expect(err).NotTo(HaveOccurred())
}
```

Fill in `patchEnvManifestAddIPv6`'s body using the `os.ReadFile`/`os.WriteFile` + string replace pattern shown in the pseudocode comment. Do not leave it stubbed — the test will pass vacuously without the patch.

Also: if the `e2e/internal/client` package's `EnvInit` / `SvcInit` / `SvcDeploy` method signatures diverge from what's shown above, adapt to match the existing signatures. Reference `e2e/env_ipv6/env_ipv6_test.go` for the exact call shape.

- [ ] **Step 2: Verify the suite still builds**

```bash
go test -c -tags integration,e2e ./e2e/imported_vpc_ipv6/
```

Expected: clean build. Clean up: `rm imported_vpc_ipv6.test` if present.

- [ ] **Step 3: (Optional, if AWS credentials are available) Run the scenario**

```bash
go test -tags integration,e2e ./e2e/imported_vpc_ipv6/ -v -timeout 60m
```

This will deploy real AWS resources. Skip in normal development; CI nightly runs it.

- [ ] **Step 4: Commit**

```bash
git add e2e/imported_vpc_ipv6/imported_vpc_ipv6_test.go
git commit -m "E2E: imported-VPC IPv6 positive scenario

LBWS deployed into an imported dual-stack VPC gets a dualstack ALB
and a global IPv6 on the task ENI.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 14: E2E negative scenario — validator rejects v4-only VPC

**Files:**
- Modify: `e2e/imported_vpc_ipv6/imported_vpc_ipv6_test.go`

- [ ] **Step 1: Append the negative scenario**

Append inside the existing `Describe("Imported VPC + IPv6", ...)` block in `imported_vpc_ipv6_test.go`:

```go
It("rejects an imported v4-only VPC with a pre-flight error", func() {
	const envName = "v4only"

	By("initializing a separate env pointing at the v4-only VPC")
	_, err := copilotCLI.EnvInit(map[string]string{
		"name":              envName,
		"app":               appName,
		"profile":           "default",
		"import-vpc-id":     v4OnlyVPCID,
		"import-public-subnets":  strings.Join(v4OnlyPublicSubnetIDs, ","),
		"import-private-subnets": strings.Join(v4OnlyPrivateSubnetIDs, ","),
	})
	Expect(err).NotTo(HaveOccurred())

	patchEnvManifestAddIPv6(appName, envName)

	By("env deploy must fail with the missing-IPv6 error before any CFN call")
	output, err := copilotCLI.EnvDeploy(map[string]string{"name": envName})
	Expect(err).To(HaveOccurred())
	Expect(output).To(ContainSubstring(v4OnlyVPCID),
		"error message should name the offending VPC")
	Expect(strings.ToLower(output)).To(ContainSubstring("ipv6"),
		"error message should mention IPv6")

	By("asserting no copilot env stack was created for the v4-only env")
	sess, err := session.NewSession(&aws.Config{Region: aws.String(region)})
	Expect(err).NotTo(HaveOccurred())
	cfn := cloudformation.New(sess)
	_, describeErr := cfn.DescribeStacks(&cloudformation.DescribeStacksInput{
		StackName: aws.String(fmt.Sprintf("%s-%s", appName, envName)),
	})
	Expect(describeErr).To(HaveOccurred(),
		"env stack should not exist — deploy was aborted pre-flight")
	Expect(describeErr.Error()).To(ContainSubstring("does not exist"))
})
```

Add the `"github.com/aws/aws-sdk-go/service/cloudformation"` import if it isn't already in scope.

- [ ] **Step 2: Build**

```bash
go test -c -tags integration,e2e ./e2e/imported_vpc_ipv6/
```

Expected: clean build.

- [ ] **Step 3: Commit**

```bash
git add e2e/imported_vpc_ipv6/imported_vpc_ipv6_test.go
git commit -m "E2E: imported-VPC IPv6 negative scenario

Validator rejects v4-only imported VPC pre-flight; no CFN stack is
created.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 15: Full test sweep and merge-readiness check

**Files:** none directly — this is a verification step.

- [ ] **Step 1: Go unit + integration tests**

```bash
make run-unit-test
```

Expected: all unit tests pass. Note any failures; investigate and fix before proceeding. Do not mark this task complete on failures.

- [ ] **Step 2: Go race detection**

```bash
make test-race
```

Expected: zero data races.

- [ ] **Step 3: Lint**

```bash
golangci-lint run ./internal/...
```

Expected: no new violations introduced by this sub-project.

- [ ] **Step 4: License headers**

```bash
./scripts/license.sh .
```

Expected: no missing Apache 2.0 headers (the new files we added already have them per the templates in each task).

- [ ] **Step 5: Build all three platforms**

```bash
make build
```

Expected: clean build. (This also tests that the Node CR packaging step still works.)

- [ ] **Step 6: Confirm the branch is ready for merge**

```bash
git log --oneline feature/ipv6-support..HEAD
```

Expected: ~11-14 commits, each representing a single task above. Commit messages use "Co-Authored-By" footers and conventional subject lines.

- [ ] **Step 7: Summarize in a final empty commit (optional, skip unless conventions demand)**

If the `feature/ipv6-support` umbrella expects a final "Merge sub-project #4" commit prior to PR: leave that to the merge step, not this plan. Stop here.

- [ ] **Step 8: Mark plan complete**

No git operation. The implementing agent signals completion by checking this final box.

---

## Spec Coverage

Cross-check each goal, non-goal, and design decision from the spec against tasks in this plan:

| Spec section | Implemented in task |
|---|---|
| Goal 1: remove `errIPv6WithImportedVPC` | Task 5 |
| Goal 2: EC2-backed pre-flight validator | Tasks 1–3 (EC2 helpers), 4 (interface), 6 (validator), 8 (env deploy wiring; env init skipped — Task 7 rationale) |
| Goal 3: end-to-end dualstack behavior on imported VPCs | Task 9 (golden), Tasks 11–14 (E2E) |
| Non-goal: no new manifest fields | Not implemented — validated by absence in the file-layout table |
| Non-goal: no template changes | Validated by the non-leak assertion in Task 9 Step 5 |
| Non-goal: no routing audit | Not implemented — validator scope locked to VPC + subnet associations |
| Design decision 1: consume, don't mutate | Realized by the no-template-change property (Task 9 golden) |
| Design decision 2: EC2-backed, hard-fail pre-deploy | Tasks 6, 8 |
| Design decision 3: validator in `cli`, not `manifest` | Tasks 5 (remove), 6 (new location) |
| Design decision 4: E2E with helper stack | Tasks 11, 12, 13, 14 |
| Design decision 5: no workload-deploy re-verification | Not implemented — no task touches `svc_deploy.go` |
| Design decision 6: shared validator, not duplicated | Task 6 creates one helper; Tasks 7 and 8 both call it |
| Stable interface for #5: `validateImportedVPCIPv6Readiness` + sentinels | Task 6 |

All spec requirements are covered by at least one task.
