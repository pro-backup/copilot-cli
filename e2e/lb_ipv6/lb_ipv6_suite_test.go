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
