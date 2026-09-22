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
		"${asg}", "cluster-nat64-shared-us-west-2a",
		"${bucket}", "cluster-bucket",
		"${data_eni}", "eni-123",
		"${data_ipv4}", "10.0.0.10",
		"${data_ipv4_cidr}", "10.0.0.0/28",
		"${data_ipv4_gateway}", "10.0.0.1",
		"${data_ipv6}", "2001:db8::10",
		"${data_mac}", "02:00:00:00:00:01",
		"${eip}", "198.51.100.1",
		"${generation}", "2026-09-16T00:00:00Z",
		"${hook}", "cluster-nat64-launch",
		"${jool_modules}", "  6.12.107+deb13-cloud-arm64) jool_object='dependencies/jool/jool-v4.1.15-kernel-6.12.107+deb13-linux-arm64.tar.gz'; jool_digest='sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa' ;;",
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

// TestResourceNames verifies that AWS-generated names retain their Podmin context.
func TestResourceNames(t *testing.T) {
	compute, err := Module.ReadFile("compute.tf")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(compute), `name_prefix               = "${var.cluster_id}-${each.key}-"`) {
		t.Error("nodegroup Auto Scaling groups do not use the cluster and nodegroup name prefix")
	}

	nat64, err := Module.ReadFile("nat64.tf")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`name_prefix   = "${var.cluster_id}-nat64-${each.key}-"`,
		`name                      = "${var.cluster_id}-nat64-${each.key}"`,
		`name                 = "${var.cluster_id}-nat64-${each.key}-launch"`,
	} {
		if !strings.Contains(string(nat64), want) {
			t.Errorf("nat64.tf does not contain %q", want)
		}
	}
}

// TestRootVolumesAreEncrypted verifies every EC2 launch template enforces encryption with the account's default EBS key.
func TestRootVolumesAreEncrypted(t *testing.T) {
	for _, name := range []string{"compute.tf", "nat64.tf"} {
		body, err := Module.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{
			"device_name = var.images[each.value.architecture].root_device_name",
			"delete_on_termination = true",
			"encrypted             = true",
			`volume_type           = "gp3"`,
		} {
			if !strings.Contains(string(body), want) {
				t.Errorf("%s does not contain %q", name, want)
			}
		}
		if strings.Contains(string(body), "kms_key_id") {
			t.Errorf("%s overrides the account's default EBS key", name)
		}
	}
}

// TestPodENIAddressPrecedesPrefix verifies bootstrap follows AWS's proven IPv6 allocation order.
func TestPodENIAddressPrecedesPrefix(t *testing.T) {
	compute, err := Module.ReadFile("compute.tf")
	if err != nil {
		t.Fatal(err)
	}
	body := string(compute)
	if strings.Count(body, "ipv6_address_count") != 2 {
		t.Error("compute launch template does not assign one ordinary IPv6 address to each ENI")
	}
	for _, want := range []string{`"ec2:AssignIpv6Addresses"`, `"ec2:ResourceTag/podmin:cluster"`, `${data.aws_caller_identity.current.account_id}:network-interface/*`, `depends_on    = [aws_iam_role_policy.instance]`} {
		if !strings.Contains(body, want) {
			t.Errorf("compute.tf does not contain %q", want)
		}
	}
	if strings.Contains(body, "ipv6_prefix_count") {
		t.Error("compute launch template assigns the Pod prefix before bootstrap")
	}
}

// TestWorkloadCAPublicationPermissions verifies external trust writes are object-scoped.
func TestWorkloadCAPublicationPermissions(t *testing.T) {
	for _, name := range []string{"compute.tf", "network.tf"} {
		body, err := Module.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{
			`var.workload_ca_publication == null ? []`,
			`Action = "s3:ListBucket"`,
			`Resource = "arn:aws:s3:::${var.workload_ca_publication.bucket}"`,
			`StringEquals = { "s3:prefix" = var.workload_ca_publication.key }`,
			`"s3:GetObject", "s3:PutObject"`,
			`arn:aws:s3:::${var.workload_ca_publication.bucket}/${var.workload_ca_publication.key}`,
		} {
			if !strings.Contains(string(body), want) {
				t.Errorf("%s does not contain %q", name, want)
			}
		}
	}
}

// TestOTelLogsCAPermissions verifies external server trust reads are object-scoped.
func TestOTelLogsCAPermissions(t *testing.T) {
	for _, name := range []string{"compute.tf", "network.tf"} {
		body, err := Module.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{`var.otel_logs_ca == null ? []`, `Action = "s3:GetObject"`, `arn:aws:s3:::${var.otel_logs_ca.bucket}/${var.otel_logs_ca.key}`} {
			if !strings.Contains(string(body), want) {
				t.Errorf("%s does not contain %q", name, want)
			}
		}
	}
}

// TestNodeGroupSubnetReplacementOrder verifies zone changes release the old IPv6 CIDR first.
func TestNodeGroupSubnetReplacementOrder(t *testing.T) {
	compute, err := Module.ReadFile("compute.tf")
	if err != nil {
		t.Fatal(err)
	}
	body := string(compute)
	if !strings.Contains(body, "replace_triggered_by = [aws_subnet.nodegroup[each.key].id]") {
		t.Error("nodegroup Auto Scaling group is not replaced with its subnet")
	}
	if strings.Contains(body, "create_before_destroy") {
		t.Error("nodegroup Auto Scaling group propagates create-before-destroy to its subnet")
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
		"encrypted             = true",
		"associate_public_ip_address = false",
		"instance_warmup        = 0",
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
	if strings.Count(string(infrastructure), `"ec2:DetachNetworkInterface"`) != 2 {
		t.Error("nat64 handoff does not authorize attach and detach on both the stable ENI and tagged ASG instances")
	}
	if !strings.Contains(string(infrastructure), "depends_on = [aws_eip.nat64, aws_iam_role_policy.nat64]") {
		t.Error("nat64 refresh can start before its handoff policy is applied")
	}

	userData, err := Module.ReadFile("nat64.sh.tftpl")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"export AWS_USE_DUALSTACK_ENDPOINT=true",
		"aws s3 cp \\",
		`$${jool_digest#sha256:}`,
		`modinfo -F vermagic`,
		`depmod -a`,
		`iifname "n64-host" ip6 daddr 64:ff9b::/96 ip6 saddr != fd64:706f:646d:696e::2/128 drop`,
		`ip6 saddr { ${workload_sources} } drop`,
		"64:ff9b::c058:6300/120",
		"in_asg \"$previous_owner\"",
	} {
		if !strings.Contains(string(userData), want) {
			t.Errorf("nat64.sh.tftpl does not contain %q", want)
		}
	}
	for _, forbidden := range []string{"jool-dkms", "linux-headers", "swap_file", "fallocate", "mkswap", "swapon"} {
		if strings.Contains(string(userData), forbidden) {
			t.Errorf("nat64.sh.tftpl still contains build dependency %q", forbidden)
		}
	}
}
