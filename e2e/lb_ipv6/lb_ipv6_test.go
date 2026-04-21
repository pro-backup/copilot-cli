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
