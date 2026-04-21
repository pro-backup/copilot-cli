// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package template

import (
	"bytes"
	"fmt"
	"testing"
	"text/template"

	"github.com/spf13/afero"

	"github.com/stretchr/testify/require"
)

func TestTemplate_ParseEnv(t *testing.T) {
	// GIVEN
	fs := afero.NewMemMapFs()
	_ = fs.MkdirAll("templates/environment", 0755)
	_ = afero.WriteFile(fs, "templates/environment/cf.yml", []byte("test"), 0644)
	_ = afero.WriteFile(fs, "templates/environment/partials/cdn-resources.yml", []byte("cdn-resources"), 0644)
	_ = afero.WriteFile(fs, "templates/environment/partials/cfn-execution-role.yml", []byte("cfn-execution-role"), 0644)
	_ = afero.WriteFile(fs, "templates/environment/partials/custom-resources.yml", []byte("custom-resources"), 0644)
	_ = afero.WriteFile(fs, "templates/environment/partials/custom-resources-role.yml", []byte("custom-resources-role"), 0644)
	_ = afero.WriteFile(fs, "templates/environment/partials/environment-manager-role.yml", []byte("environment-manager-role"), 0644)
	_ = afero.WriteFile(fs, "templates/environment/partials/lambdas.yml", []byte("lambdas"), 0644)
	_ = afero.WriteFile(fs, "templates/environment/partials/vpc-resources.yml", []byte("vpc-resources"), 0644)
	_ = afero.WriteFile(fs, "templates/environment/partials/nat-gateways.yml", []byte("nat-gateways"), 0644)
	_ = afero.WriteFile(fs, "templates/environment/partials/bootstrap-resources.yml", []byte("bootstrap"), 0644)
	_ = afero.WriteFile(fs, "templates/environment/partials/elb-access-logs.yml", []byte("elb-access-logs"), 0644)
	_ = afero.WriteFile(fs, "templates/environment/partials/mappings-regional-configs.yml", []byte("mappings-regional-configs"), 0644)
	_ = afero.WriteFile(fs, "templates/environment/partials/ar-vpc-connector.yml", []byte("ar-vpc-connector"), 0644)
	tpl := &Template{
		fs: &mockFS{
			Fs: fs,
		},
	}

	// WHEN
	c, err := tpl.ParseEnv(&EnvOpts{})

	// THEN
	require.NoError(t, err)
	require.Equal(t, "test", c.String())
}

func TestTemplate_ParseEnvBootstrap(t *testing.T) {
	// GIVEN
	fs := afero.NewMemMapFs()
	_ = fs.MkdirAll("templates/environment/partials", 0755)
	_ = afero.WriteFile(fs, "templates/environment/bootstrap-cf.yml", []byte("test"), 0644)
	_ = afero.WriteFile(fs, "templates/environment/partials/cfn-execution-role.yml", []byte("cfn-execution-role"), 0644)
	_ = afero.WriteFile(fs, "templates/environment/partials/environment-manager-role.yml", []byte("environment-manager-role"), 0644)
	_ = afero.WriteFile(fs, "templates/environment/partials/bootstrap-resources.yml", []byte("bootstrap"), 0644)
	tpl := &Template{
		fs: &mockFS{
			Fs: fs,
		},
	}

	// WHEN
	c, err := tpl.ParseEnvBootstrap(&EnvOpts{})

	// THEN
	require.NoError(t, err)
	require.Equal(t, "test", c.String())
}

func TestTruncate(t *testing.T) {
	tests := map[string]struct {
		s      string
		maxLen int

		expected string
	}{
		"empty string": {
			s:        "",
			maxLen:   10,
			expected: "",
		},
		"maxLen < len(string)": {
			s:        "qwerty",
			maxLen:   4,
			expected: "qwer",
		},
		"maxLen > len(string)": {
			s:        "qwerty",
			maxLen:   7,
			expected: "qwerty",
		},
		"maxLen == len(string)": {
			s:        "qwerty",
			maxLen:   6,
			expected: "qwerty",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.expected, truncate(tc.s, tc.maxLen))
		})
	}
}

func TestWithEnvParsingFuncs_AddHelper(t *testing.T) {
	testCases := map[string]struct {
		a, b int
		want string
	}{
		"basic positive": {3, 4, "7"},
		"zero operand":   {0, 5, "5"},
		"negative":       {3, -2, "1"},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			tpl := template.New("t")
			tpl = withEnvParsingFuncs()(tpl)
			parsed, err := tpl.Parse(fmt.Sprintf(`{{add %d %d}}`, tc.a, tc.b))
			require.NoError(t, err)
			var buf bytes.Buffer
			require.NoError(t, parsed.Execute(&buf, nil))
			require.Equal(t, tc.want, buf.String())
		})
	}
}

func TestIsIPv6CIDR(t *testing.T) {
	testCases := map[string]struct {
		in     string
		wanted bool
	}{
		"IPv4 CIDR /32":      {in: "10.0.0.0/32", wanted: false},
		"IPv4 CIDR /8":       {in: "10.0.0.0/8", wanted: false},
		"IPv4 default route": {in: "0.0.0.0/0", wanted: false},
		"IPv6 CIDR /128":     {in: "2001:db8::1/128", wanted: true},
		"IPv6 CIDR /32":      {in: "2001:db8::/32", wanted: true},
		"IPv6 default route": {in: "::/0", wanted: true},
		"IPv4-mapped IPv6":   {in: "::ffff:10.0.0.0/104", wanted: false},
		"bare IPv4 no mask":  {in: "10.0.0.0", wanted: false},
		"bare IPv6 no mask":  {in: "2001:db8::", wanted: false},
		"empty string":       {in: "", wanted: false},
		"garbage":            {in: "not-a-cidr", wanted: false},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.wanted, IsIPv6CIDR(tc.in))
		})
	}
}
