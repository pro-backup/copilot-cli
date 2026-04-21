// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package imported_vpc_ipv6_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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
})

var _ = AfterSuite(func() {
	_, _ = copilotCLI.AppDelete()

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
