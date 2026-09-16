// Podmin <https://podmin.dev>
// Copyright The Podmin Authors
// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestNAT64UserData verifies the rendered launch and local scripts with Bash and ShellCheck.
func TestNAT64UserData(t *testing.T) {
	template, err := Module.ReadFile("nat64.sh.tftpl")
	if err != nil {
		t.Fatal(err)
	}
	replacer := strings.NewReplacer(
		"${architecture}", "arm64",
		"${asg}", "cluster-shared-us-west-2a-nat64",
		"${data_eni}", "eni-123",
		"${data_ipv4}", "10.0.0.10",
		"${data_ipv4_cidr}", "10.0.0.0/28",
		"${data_ipv4_gateway}", "10.0.0.1",
		"${data_ipv6}", "2001:db8::10",
		"${data_mac}", "02:00:00:00:00:01",
		"${eip}", "198.51.100.1",
		"${generation}", "2026-09-16T00:00:00Z",
		"${hook}", "cluster-nat64-launch",
		"${region}", "us-west-2",
		"${workload_cidrs}", "2001:db8:1::/64 2001:db8:2::/64",
		"${workload_sources}", "2001:db8:1::/64, 2001:db8:2::/64",
		"$${", "${",
	)
	rendered := replacer.Replace(string(template))
	if strings.Contains(rendered, "$${") {
		t.Fatal("rendered user-data retains escaped Terraform interpolation")
	}
	const localStart = "cat >/usr/local/libexec/podmin-nat64-local <<'SCRIPT'\n"
	start := strings.Index(rendered, localStart)
	if start == -1 {
		t.Fatal("rendered user-data does not contain the local script")
	}
	start += len(localStart)
	end := strings.Index(rendered[start:], "\nSCRIPT\n")
	if end == -1 {
		t.Fatal("rendered local script has no heredoc terminator")
	}
	scripts := map[string]string{"nat64.sh": rendered, "podmin-nat64-local": rendered[start : start+end]}
	for name, script := range scripts {
		path := filepath.Join(t.TempDir(), name)
		if err = os.WriteFile(path, []byte(script), 0600); err != nil {
			t.Fatal(err)
		}
		for _, check := range [][]string{{"bash", "-n", path}, {"shellcheck", "--shell=bash", path}} {
			if output, commandErr := exec.Command(check[0], check[1:]...).CombinedOutput(); commandErr != nil {
				t.Fatalf("%s %s: %v\n%s", check[0], name, commandErr, output)
			}
		}
	}
}

// TestNAT64SecurityControls guards the handoff and packet-filter boundaries.
func TestNAT64SecurityControls(t *testing.T) {
	infrastructure, err := Module.ReadFile("nat64.tf")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"ec2:ResourceTag/podmin:cluster"`,
		`"ec2:ResourceTag/podmin:nat64"`,
		`"ec2:InstanceProfile"`,
		`${data.aws_caller_identity.current.account_id}:instance/*`,
	} {
		if !strings.Contains(string(infrastructure), want) {
			t.Errorf("nat64.tf does not contain %q", want)
		}
	}
	for _, forbidden := range []string{`ec2:${var.region}:*:instance/*`, "ModifyNetworkInterfaceAttribute"} {
		if strings.Contains(string(infrastructure), forbidden) {
			t.Errorf("nat64.tf contains overbroad permission %q", forbidden)
		}
	}

	userData, err := Module.ReadFile("nat64.sh.tftpl")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`iifname "n64-host" ip6 daddr 64:ff9b::/96 ip6 saddr != fd64:706f:646d:696e::2/128 drop`,
		`ip6 saddr { ${workload_sources} } drop`,
		"64:ff9b::c058:6300/120",
		"in_asg \"$previous_owner\"",
	} {
		if !strings.Contains(string(userData), want) {
			t.Errorf("nat64.sh.tftpl does not contain %q", want)
		}
	}
}
