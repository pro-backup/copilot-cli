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
			_ = Expect
			// Setup: write copilot/environments/test/manifest.yml with
			//   network.vpc.ipv6.enabled: true
			// then copilot env init --name test + copilot env deploy --name test.
			// Assertions via AWS SDK:
			//   - DescribeVpcs: Ipv6CidrBlockAssociationSet non-empty, prefix length == 56.
			//   - DescribeSubnets: every subnet has Ipv6CidrBlock with /64 prefix.
			//   - AssignIpv6AddressOnCreation == true on every subnet.
			//   - DescribeEgressOnlyInternetGateways: one EOIGW attached to VPC.
			//   - DescribeRouteTables: each private RT has a route with DestinationIpv6CidrBlock="::/0"
			//     and EgressOnlyInternetGatewayId matching the EOIGW id.
			//   - Public RT has a route with DestinationIpv6CidrBlock="::/0" and GatewayId matching IGW id.
			//   - Subnet IPv6 blocks are pairwise disjoint.
			Skip("TODO: fill in once e2e/internal/client has EC2 describe helpers")
		})
	})
})
