// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package stack

import (
	"testing"

	"github.com/aws/copilot-cli/internal/pkg/manifest"
	"github.com/stretchr/testify/require"
)

// mustEnvManifestWithIPv6 returns a *manifest.Environment whose
// network.vpc.ipv6.enabled field is set to the given value via a
// YAML round-trip (the IPv6 config struct is unexported).
func mustEnvManifestWithIPv6(t *testing.T, enabled bool) *manifest.Environment {
	t.Helper()
	var yml string
	if enabled {
		yml = `name: test
type: Environment
network:
  vpc:
    ipv6:
      enabled: true
`
	} else {
		yml = `name: test
type: Environment
`
	}
	env, err := manifest.UnmarshalEnvironment([]byte(yml))
	require.NoError(t, err)
	return env
}
