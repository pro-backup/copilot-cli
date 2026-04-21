// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package imported_vpc_ipv6_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/cloudformation"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/aws/aws-sdk-go/service/elbv2"
	"github.com/aws/copilot-cli/e2e/internal/client"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Imported VPC + IPv6", Ordered, func() {
	const (
		envName = "test"
		svcName = "frontend"
	)

	Context("when initializing the copilot app", func() {
		var appInitErr error

		BeforeAll(func() {
			_, appInitErr = copilotCLI.AppInit(&client.AppInitRequest{
				AppName: appName,
			})
		})

		It("app init succeeds", func() {
			Expect(appInitErr).NotTo(HaveOccurred())
		})

		It("app init creates a copilot directory", func() {
			Expect("./copilot").Should(BeADirectory())
		})
	})

	Context("when initializing an imported-VPC env", func() {
		var envInitErr error

		BeforeAll(func() {
			_, envInitErr = copilotCLI.EnvInit(&client.EnvInitRequest{
				AppName: appName,
				EnvName: envName,
				Profile: "test",
				VPCImport: client.EnvInitRequestVPCImport{
					ID:               dualStackVPCID,
					PublicSubnetIDs:  strings.Join(dualStackPublicSubnetIDs, ","),
					PrivateSubnetIDs: strings.Join(dualStackPrivateSubnetIDs, ","),
				},
				CustomizedEnv: true,
			})
		})

		It("env init should succeed", func() {
			Expect(envInitErr).NotTo(HaveOccurred())
		})

		It("should write a manifest under copilot/environments/test/", func() {
			Expect(filepath.Join("copilot", "environments", envName, "manifest.yml")).Should(BeAnExistingFile())
		})
	})

	Context("when patching the env manifest to enable IPv6 and deploying", func() {
		var envDeployErr error

		BeforeAll(func() {
			patchEnvManifestAddIPv6(envName)

			_, envDeployErr = copilotCLI.EnvDeploy(&client.EnvDeployRequest{
				AppName: appName,
				Name:    envName,
			})
		})

		It("env deploy should succeed", func() {
			Expect(envDeployErr).NotTo(HaveOccurred())
		})
	})

	Context("when initializing and deploying a Load Balanced Web Service", func() {
		var (
			svcInitErr   error
			svcDeployErr error
		)

		BeforeAll(func() {
			_, svcInitErr = copilotCLI.SvcInit(&client.SvcInitRequest{
				Name:       svcName,
				SvcType:    "Load Balanced Web Service",
				Dockerfile: "./frontend/Dockerfile",
				SvcPort:    "80",
			})
			if svcInitErr != nil {
				return
			}

			_, svcDeployErr = copilotCLI.SvcDeploy(&client.SvcDeployInput{
				Name:     svcName,
				EnvName:  envName,
				ImageTag: "latest",
			})
		})

		It("svc init should succeed", func() {
			Expect(svcInitErr).NotTo(HaveOccurred())
		})

		It("svc deploy should succeed", func() {
			Expect(svcDeployErr).NotTo(HaveOccurred())
		})
	})

	Context("when deploying into a v4-only imported VPC with IPv6 enabled", func() {
		const negEnvName = "v4only"
		var (
			envInitErr    error
			envDeployOut  string
			envDeployErr  error
		)

		BeforeAll(func() {
			_, envInitErr = copilotCLI.EnvInit(&client.EnvInitRequest{
				AppName: appName,
				EnvName: negEnvName,
				Profile: "test",
				VPCImport: client.EnvInitRequestVPCImport{
					ID:               v4OnlyVPCID,
					PublicSubnetIDs:  strings.Join(v4OnlyPublicSubnetIDs, ","),
					PrivateSubnetIDs: strings.Join(v4OnlyPrivateSubnetIDs, ","),
				},
				CustomizedEnv: true,
			})
			if envInitErr != nil {
				return
			}

			patchEnvManifestAddIPv6(negEnvName)

			envDeployOut, envDeployErr = copilotCLI.EnvDeploy(&client.EnvDeployRequest{
				AppName: appName,
				Name:    negEnvName,
			})
		})

		It("env init into v4-only VPC should succeed", func() {
			Expect(envInitErr).NotTo(HaveOccurred())
		})

		It("rejects an imported v4-only VPC with a pre-flight error", func() {
			By("env deploy must fail with the missing-IPv6 error before any CFN call")
			Expect(envDeployErr).To(HaveOccurred())
			Expect(envDeployOut).To(ContainSubstring(v4OnlyVPCID),
				"error message should name the offending VPC")
			Expect(strings.ToLower(envDeployOut)).To(ContainSubstring("ipv6"),
				"error message should mention IPv6")

			By("asserting no copilot env stack was created for the v4-only env")
			sess, err := session.NewSession(&aws.Config{Region: aws.String(region)})
			Expect(err).NotTo(HaveOccurred())
			cfn := cloudformation.New(sess)
			_, describeErr := cfn.DescribeStacks(&cloudformation.DescribeStacksInput{
				StackName: aws.String(fmt.Sprintf("%s-%s", appName, negEnvName)),
			})
			Expect(describeErr).To(HaveOccurred(),
				"env stack should not exist — deploy was aborted pre-flight")
			Expect(describeErr.Error()).To(ContainSubstring("does not exist"))
		})
	})

	Context("when asserting ALB and ENI IPv6 properties", func() {
		var (
			sess      *session.Session
			publicALB *elbv2.LoadBalancer
		)

		BeforeAll(func() {
			var err error
			sess, err = session.NewSession(&aws.Config{Region: aws.String(region)})
			Expect(err).NotTo(HaveOccurred())
		})

		It("the shared public ALB is dualstack", func() {
			elb := elbv2.New(sess)

			var lbs []*elbv2.LoadBalancer
			err := elb.DescribeLoadBalancersPages(&elbv2.DescribeLoadBalancersInput{},
				func(page *elbv2.DescribeLoadBalancersOutput, _ bool) bool {
					lbs = append(lbs, page.LoadBalancers...)
					return true
				})
			Expect(err).NotTo(HaveOccurred())

			for _, lb := range lbs {
				if aws.StringValue(lb.VpcId) == dualStackVPCID &&
					aws.StringValue(lb.Scheme) == "internet-facing" {
					publicALB = lb
					break
				}
			}

			Expect(publicALB).NotTo(BeNil(), "public internet-facing ALB not found in dual-stack VPC")
			Expect(aws.StringValue(publicALB.IpAddressType)).To(Equal("dualstack"),
				"public ALB should have IpAddressType=dualstack")

			albDNS := aws.StringValue(publicALB.DNSName)
			Expect(albDNS).NotTo(BeEmpty())
			fmt.Fprintf(GinkgoWriter, "public ALB DNSName=%s (dualstack)\n", albDNS)
		})

		It("the task ENI has a global IPv6 address", func() {
			ec2svc := ec2.New(sess)

			enis, err := ec2svc.DescribeNetworkInterfaces(&ec2.DescribeNetworkInterfacesInput{
				Filters: []*ec2.Filter{
					{
						Name:   aws.String("subnet-id"),
						Values: aws.StringSlice(dualStackPrivateSubnetIDs),
					},
				},
			})
			Expect(err).NotTo(HaveOccurred())

			var hasGlobalIPv6 bool
			for _, eni := range enis.NetworkInterfaces {
				for _, addr := range eni.Ipv6Addresses {
					if addr.Ipv6Address != nil &&
						!strings.HasPrefix(aws.StringValue(addr.Ipv6Address), "fe80") {
						hasGlobalIPv6 = true
						break
					}
				}
				if hasGlobalIPv6 {
					break
				}
			}
			Expect(hasGlobalIPv6).To(BeTrue(), "no task ENI in the private subnets has a global IPv6 address")
		})
	})
})

// patchEnvManifestAddIPv6 reads the env manifest written by env init and inserts
// "    ipv6:\n      enabled: true\n" immediately after the "  vpc:\n" header.
// env init has no --ipv6 flag, so this simulates the user hand-editing the
// manifest before env deploy.
func patchEnvManifestAddIPv6(env string) {
	path := filepath.Join("copilot", "environments", env, "manifest.yml")
	data, err := os.ReadFile(path)
	Expect(err).NotTo(HaveOccurred())

	const vpcHeader = "  vpc:\n"
	insert := "  vpc:\n    ipv6:\n      enabled: true\n"

	if !strings.Contains(string(data), vpcHeader) {
		Fail(fmt.Sprintf("patchEnvManifestAddIPv6: could not find %q in manifest at %s", vpcHeader, path))
	}
	patched := strings.Replace(string(data), vpcHeader, insert, 1)
	err = os.WriteFile(path, []byte(patched), 0o644)
	Expect(err).NotTo(HaveOccurred())
}
