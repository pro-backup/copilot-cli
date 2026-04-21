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
