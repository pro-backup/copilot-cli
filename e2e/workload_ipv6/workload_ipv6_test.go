// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package workload_ipv6_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Workload IPv6 Egress", Ordered, func() {
	When("deployed into an IPv6-enabled env, a Linux BackendService", func() {
		It("receives a global IPv6 address on its task ENI", func() {
			_ = appName
			_ = cli
			_ = Expect
			// Setup:
			//   1. Write copilot/environments/test/manifest.yml with
			//      network.vpc.ipv6.enabled: true
			//   2. copilot env init --name test --default-config
			//   3. copilot env deploy --name test
			//   4. copilot svc init --name backend --svc-type "Backend Service" --dockerfile ./Dockerfile
			//   5. copilot svc deploy --name backend --env test
			//
			// Assertions via AWS SDK:
			//   - ECS DescribeTasks: retrieve the running task ARN for the backend service.
			//   - EC2 DescribeNetworkInterfaces filtered by the task's ENI attachment ID.
			//   - Ipv6Addresses on the ENI contains exactly one address.
			//   - The address falls within the VPC's /56 IPv6 CIDR block (from DescribeVpcs).
			//   - The address is a global unicast address (2000::/3 prefix range).
			Skip("TODO: fill in once e2e/internal/client has ECS/EC2/CloudWatch Logs describe helpers")
		})
		It("can reach an AWS dualstack endpoint over IPv6", func() {
			_ = appName
			_ = cli
			_ = Expect
			// Setup:
			//   1-5. Same as above — backend service deployed into IPv6-enabled env.
			//   6. The probe container (to be added later) runs:
			//      curl -6 -s https://s3.dualstack.<region>.amazonaws.com/ inside the task.
			//
			// Assertions via AWS SDK:
			//   - ECS RunTask (or exec) to execute the curl probe inside the backend container.
			//   - CloudWatch Logs FilterLogEvents on the service's log group:
			//     look for a log line containing HTTP 200 or the S3 XML response body.
			//   - Alternatively, capture the exit code of the curl command via ECS Exec
			//     and assert it is 0.
			Skip("TODO: fill in once e2e/internal/client has ECS/EC2/CloudWatch Logs describe helpers")
		})
		It("attaches to an env SG with a ::/0 IPv6 egress rule", func() {
			_ = appName
			_ = cli
			_ = Expect
			// Setup:
			//   1-5. Same as above — backend service deployed into IPv6-enabled env.
			//
			// Assertions via AWS SDK:
			//   - ECS DescribeTasks: collect the security group IDs attached to the task.
			//   - EC2 DescribeSecurityGroups filtered by those IDs.
			//   - At least one SG has an IpPermissionsEgress entry where:
			//       Ipv6Ranges[*].CidrIpv6 == "::/0"
			//       IpProtocol == "-1" (all traffic) or a relevant protocol.
			//   - The matching SG is the environment-level SG (tagged with
			//     copilot-environment == "test").
			Skip("TODO: fill in once e2e/internal/client has ECS/EC2/CloudWatch Logs describe helpers")
		})
	})
})

var _ = Describe("Workload IPv6 Disabled", Ordered, func() {
	When("deployed into a non-IPv6 env, a Linux BackendService", func() {
		It("renders no AssignIpv6Address on its CFN template", func() {
			_ = appName
			_ = cli
			_ = Expect
			// Setup:
			//   1. Write copilot/environments/test-v4/manifest.yml with no ipv6 stanza
			//      (or explicitly network.vpc.ipv6.enabled: false).
			//   2. copilot env init --name test-v4 --default-config
			//   3. copilot env deploy --name test-v4
			//   4. copilot svc init --name backend --svc-type "Backend Service" --dockerfile ./Dockerfile
			//   5. copilot svc deploy --name backend --env test-v4
			//
			// Assertions via AWS SDK:
			//   - CloudFormation GetTemplate for the backend service stack
			//     (stack name: copilot-<appName>-test-v4-backend).
			//   - Parse the template body and assert that no resource of type
			//     AWS::ECS::TaskDefinition has a property AssignPublicIp set to "ENABLED"
			//     with an Ipv6Address configuration.
			//   - More specifically, verify that the TaskDefinition's
			//     NetworkConfiguration.AwsvpcConfiguration does NOT contain
			//     AssignIpv6Address or that the rendered template contains no
			//     "AssignIpv6Addresses" key anywhere in the JSON/YAML body.
			Skip("TODO: fill in once e2e/internal/client has ECS/EC2/CloudWatch Logs describe helpers")
		})
		It("has no global IPv6 address on its task ENI", func() {
			_ = appName
			_ = cli
			_ = Expect
			// Setup:
			//   1-5. Same as above — backend service deployed into v4-only env.
			//
			// Assertions via AWS SDK:
			//   - ECS DescribeTasks: retrieve the running task ARN for the backend service.
			//   - EC2 DescribeNetworkInterfaces filtered by the task's ENI attachment ID.
			//   - Assert that Ipv6Addresses on the ENI is empty (len == 0).
			//   - DescribeVpcs for the env's VPC: confirm Ipv6CidrBlockAssociationSet is
			//     empty, meaning no /56 was assigned to the VPC at all.
			Skip("TODO: fill in once e2e/internal/client has ECS/EC2/CloudWatch Logs describe helpers")
		})
	})
})
