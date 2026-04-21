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
			wantErrSubstr: "subnet-priv1, subnet-pub1",
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
