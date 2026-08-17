#!/usr/bin/env bash
# Podmin <https://podmin.dev>
# Copyright The Podmin Authors
# SPDX-License-Identifier: Apache-2.0
# AWS user-data template rendered by the Podmin CLI.

set -euo pipefail

# Values below are rendered by the Podmin CLI.
bucket='PODMIN_BUCKET'
region='PODMIN_REGION'
cluster='PODMIN_CLUSTER'
nodegroup='PODMIN_NODEGROUP'
architecture='PODMIN_ARCH'
pause_image='PODMIN_PAUSE_IMAGE'
downloads=/opt/podmin/downloads
destination=/opt/podmin/dependencies
export AWS_USE_DUALSTACK_ENDPOINT=true

# Each row contains the local name, object key, and expected digest.
dependencies=(
  # PODMIN_DEPENDENCIES
)

# The official Debian EC2 image includes AWS CLI; fail clearly if that changes.
command -v aws >/dev/null 2>&1 || {
  printf '%s\n' 'aws CLI is required but is not installed' >&2
  exit 1
}

# fatal prints an error and terminates bootstrap.
fatal() {
  printf '%s\n' "$*" >&2
  exit 1
}

# require_tcx_kernel verifies the host kernel has the TCX baseline used by the dataplane.
require_tcx_kernel() {
  local release major minor
  release=$(uname -r)
  IFS=. read -r major minor _ <<<"$release"
  if ((major < 6 || (major == 6 && minor < 6))); then
    fatal "kernel ${release} does not meet the TCX baseline (6.6 or newer)"
  fi
}

# install_dependency extracts one executable from a release archive.
install_dependency() {
  local archive="$1" binary="$2" target="$3" unpacked source_file
  unpacked=$(mktemp -d)
  tar -xzf "$archive" -C "$unpacked"
  source_file=$(find "$unpacked" -type f -name "$binary" -print -quit)
  if [ -z "$source_file" ] && [ "$binary" = zot ]; then
    source_file=$(find "$unpacked" -type f -name 'zot-*' -print -quit)
  fi
  [ -n "$source_file" ] || fatal "$binary is missing from $archive"
  install -m 0755 "$source_file" "$target"
  rm -rf "$unpacked"
}

# install_service writes a service using Podmin's common systemd policy.
install_service() {
  local name="$1" description="$2" user="$3" type="$4" command="$5" after="$6" wants="$7" requires="$8"
  shift 8
  {
    printf '[Unit]\nDescription=%s\nAfter=%s\nWants=%s\n' "$description" "$after" "$wants"
    [ -z "$requires" ] || printf 'Requires=%s\n' "$requires"
    printf '[Service]\nType=%s\nUser=%s\nGroup=%s\nExecStart=%s\nRestart=always\nRestartSec=5\n' "$type" "$user" "$user" "$command"
    printf '%s\n' "$@"
    printf '[Install]\nWantedBy=multi-user.target\n'
  } > "/etc/systemd/system/${name}.service"
}

# Refuse artifacts built for a different machine architecture.
case "$(uname -m):${architecture}" in
  x86_64:amd64|aarch64:arm64) ;;
  *) printf 'unexpected machine architecture: %s (wanted %s)\n' "$(uname -m)" "$architecture" >&2; exit 1 ;;
esac
require_tcx_kernel

# Enable routed Pod IPv6 and keep the dataplane's SNAT ports out of host ephemeral allocation.
cat > /etc/sysctl.d/99-podmin.conf <<'EOF'
net.ipv6.conf.all.forwarding = 1
net.ipv4.ip_local_reserved_ports = 30000-32767
EOF
sysctl --system >/dev/null

mkdir -p "$downloads" "$destination"

# Download only this machine's pinned dependency objects.
includes=(--exclude '*')
for dependency in "${dependencies[@]}"; do
  IFS='|' read -r _ object _ <<<"$dependency"
  includes+=(--include "${object#dependencies/}")
done

aws s3 sync "s3://${bucket}/dependencies/" "$downloads/" \
  --region "$region" \
  --only-show-errors \
  "${includes[@]}"

# Verify every download before installing anything.
for dependency in "${dependencies[@]}"; do
  IFS='|' read -r name object digest <<<"$dependency"
  source_file="${downloads}/${object#dependencies/}"
  case "$digest" in
    sha512:*) printf '%s  %s\n' "${digest#sha512:}" "$source_file" | sha512sum --check --status ;;
    sha256:*) printf '%s  %s\n' "${digest#sha256:}" "$source_file" | sha256sum --check --status ;;
    *) printf 'unsupported digest for %s: %s\n' "$name" "$digest" >&2; exit 1 ;;
  esac
done

# Preserve verified artifacts at stable paths for installation and diagnostics.
for dependency in "${dependencies[@]}"; do
  IFS='|' read -r name object _ <<<"$dependency"
  source_file="${downloads}/${object#dependencies/}"
  install -m 0644 "$source_file" "${destination}/${name}"
done

rm -rf "$downloads"

# Install containerd without its runc shim.
install -d -m 0755 /usr/local/bin /opt/cni/bin

unpacked=$(mktemp -d)
tar -xzf "${destination}/containerd.tar.gz" -C "$unpacked"
containerd_binary=$(find "$unpacked" -type f -name containerd -print -quit)
[ -n "$containerd_binary" ] || fatal 'containerd is missing from containerd.tar.gz'
install -m 0755 "$containerd_binary" /usr/local/bin/containerd
rm -rf "$unpacked"

# Install the complete gVisor payload, including its required sidecar files.
unpacked=$(mktemp -d)
tar -xjf "${destination}/gvisor.tar.bz2" -C "$unpacked"
runsc=$(find "$unpacked" -type f -name runsc -print -quit)
[ -n "$runsc" ] || fatal 'runsc is missing from gvisor.tar.bz2'
gvisor_root=$(dirname "$runsc")
install -m 0755 "$runsc" /usr/local/bin/runsc
install -m 0755 "${gvisor_root}/containerd-shim-runsc-v1" /usr/local/bin/containerd-shim-runsc-v1
cp -a "${gvisor_root}/gvisor-bin" /usr/local/bin/gvisor-bin
chown -R root:root /usr/local/bin/gvisor-bin
rm -rf "$unpacked"

# Install CNI plugins and standalone service binaries.
tar -xzf "${destination}/cni-plugins.tar.gz" -C /opt/cni/bin
chown -R root:root /opt/cni/bin
find /opt/cni/bin -type f -exec chmod 0755 {} +
install -m 0755 "${destination}/kubelet" /usr/local/bin/kubelet
install_dependency "${destination}/coredns.tar.gz" coredns /usr/local/bin/coredns
install -m 0755 "${destination}/zot" /usr/local/bin/zot
install_dependency "${destination}/podmin-agent.tar.gz" podmin-agent /usr/local/bin/podmin-agent

# Use one unambiguous node address for kubelet and Pod DNS.
mapfile -t node_addresses < <(ip -6 -o address show scope global up | awk '{split($4, a, "/"); print a[1]}' | sort -u)
[ "${#node_addresses[@]}" -eq 1 ] || fatal "expected one global IPv6 address, found ${#node_addresses[@]}"
node_ipv6=${node_addresses[0]}

# Find the secondary ENI carrying the directly routable Pod prefix.
curl_options=(--fail --silent --show-error --connect-timeout 2 --max-time 10 --retry 3 --retry-all-errors)
imds_token=$(curl "${curl_options[@]}" --request PUT \
  --header 'X-aws-ec2-metadata-token-ttl-seconds: 300' \
  http://169.254.169.254/latest/api/token)
pod_mac=
pod_prefix=
while read -r mac; do
  mac=${mac%/}
  eni=$(curl "${curl_options[@]}" \
    --header "X-aws-ec2-metadata-token: ${imds_token}" \
    "http://169.254.169.254/latest/meta-data/network/interfaces/macs/${mac}/interface-id")
  aws ec2 modify-network-interface-attribute \
    --region "$region" \
    --network-interface-id "$eni" \
    --no-source-dest-check
  prefix=$(curl "${curl_options[@]}" \
    --header "X-aws-ec2-metadata-token: ${imds_token}" \
    "http://169.254.169.254/latest/meta-data/network/interfaces/macs/${mac}/ipv6-prefix" 2>/dev/null || true)
  [ -n "$prefix" ] || continue
  [ -z "$pod_prefix" ] || fatal 'multiple ENIs have delegated IPv6 prefixes'
  pod_mac=$mac
  pod_prefix=$(head -n 1 <<<"$prefix")
done < <(curl "${curl_options[@]}" \
  --header "X-aws-ec2-metadata-token: ${imds_token}" \
  http://169.254.169.254/latest/meta-data/network/interfaces/macs/)
[ -n "$pod_prefix" ] || fatal 'no ENI has a delegated IPv6 prefix'
pod_interface=
for interface_path in /sys/class/net/*; do
  [ "$(cat "${interface_path}/address")" = "$pod_mac" ] || continue
  pod_interface=${interface_path##*/}
  break
done
[ -n "$pod_interface" ] || fatal "no local interface has Pod ENI MAC ${pod_mac}"

# Route Pod sources through their ENI while retaining specific local Pod routes.
{
  printf '#!/bin/sh\nset -eu\n'
  printf "pod_interface='%s'\npod_prefix='%s'\n" "$pod_interface" "$pod_prefix"
  cat <<'EOF'
ip link set "$pod_interface" up
ip -6 route replace "$pod_prefix" dev "$pod_interface" metric 50
ip -6 route replace table 80 default via fe80:ec2::1 dev "$pod_interface"
ip -6 rule del priority 80 2>/dev/null || true
ip -6 rule del priority 81 2>/dev/null || true
ip -6 rule add priority 80 from "$pod_prefix" lookup main suppress_prefixlength 0
ip -6 rule add priority 81 from "$pod_prefix" lookup 80
EOF
} > /usr/local/sbin/podmin-network
chmod 0755 /usr/local/sbin/podmin-network
cat > /etc/systemd/system/podmin-network.service <<EOF
[Unit]
Description=Podmin Pod network
After=network-online.target
Wants=network-online.target
Before=kubelet.service podmin-agent.service

[Service]
Type=oneshot
ExecStart=/usr/local/sbin/podmin-network
RemainAfterExit=yes

[Install]
WantedBy=multi-user.target
EOF

# Create service identities and runtime directories idempotently.
id -u zot >/dev/null 2>&1 || useradd --system --home-dir /var/lib/zot --shell /usr/sbin/nologin zot
id -u coredns >/dev/null 2>&1 || useradd --system --home-dir /var/lib/coredns --shell /usr/sbin/nologin coredns
install -d -m 0755 /etc/cni/net.d /etc/containerd/certs.d/registry.podmin.internal /etc/coredns /etc/kubernetes /etc/podmin/manifests /opt/cni/bin /var/lib/coredns
install -d -m 0700 /run/podmin
install -d -o zot -g zot -m 0700 /var/lib/zot

# Configure containerd to use only runsc and the local read-only registry.
cat > /etc/containerd/config.toml <<EOF
version = 3
oom_score = -999

[plugins."io.containerd.cri.v1.images".registry]
  config_path = "/etc/containerd/certs.d"
[plugins."io.containerd.cri.v1.images".pinned_images]
  sandbox = "${pause_image}"
[plugins."io.containerd.cri.v1.runtime".containerd]
  default_runtime_name = "runsc"
[plugins."io.containerd.cri.v1.runtime".containerd.runtimes.runsc]
  runtime_type = "io.containerd.runsc.v1"
  runtime_path = "/usr/local/bin/containerd-shim-runsc-v1"
EOF

cat > /etc/containerd/certs.d/registry.podmin.internal/hosts.toml <<'EOF'
server = "http://127.0.0.1:5000"
[host."http://127.0.0.1:5000"]
  capabilities = ["pull", "resolve"]
EOF

# Configure Zot's read-only S3-backed registry.
cat > /etc/zot.json <<EOF
{
  "distSpecVersion": "1.1.1",
  "storage": {
    "rootDirectory": "/var/lib/zot",
    "dedupe": false,
    "gc": false,
    "storageDriver": {
      "name": "s3",
      "bucket": "${bucket}",
      "region": "${region}"
    }
  },
  "http": {
    "address": "127.0.0.1",
    "port": "5000",
    "accessControl": {
      "repositories": {
        "**": {"anonymousPolicy": ["read"]}
      }
    }
  }
}
EOF
chown root:zot /etc/zot.json
chmod 0640 /etc/zot.json

# The VPC routes the delegated prefix to this ENI; ptp and host-local divide it among Pods.
cat > /etc/cni/net.d/10-podmin.conflist <<EOF
{
  "cniVersion": "1.0.0",
  "name": "podmin",
  "plugins": [
    {
      "type": "ptp",
      "ipMasq": false,
      "ipam": {
        "type": "host-local",
        "ranges": [[{"subnet": "${pod_prefix}"}]],
        "routes": [{"dst": "::/0"}]
      }
    }
  ]
}
EOF

# Resolve Podmin names through the agent and everything else through Debian.
cat > /etc/coredns/Corefile <<EOF
svc.cluster.local:53 {
  bind ${node_ipv6}
  forward . 127.0.0.1:1053
}
.:53 {
  bind ${node_ipv6}
  forward . /run/systemd/resolve/resolv.conf
}
EOF

# Run upstream kubelet in standalone static-Pod mode.
cat > /etc/kubernetes/kubelet.yaml <<EOF
apiVersion: kubelet.config.k8s.io/v1beta1
kind: KubeletConfiguration
featureGates:
  PodsAPI: true
authentication:
  webhook:
    enabled: false
authorization:
  mode: AlwaysAllow
enableServer: false
readOnlyPort: 0
staticPodPath: /etc/podmin/manifests
containerRuntimeEndpoint: unix:///run/containerd/containerd.sock
cgroupDriver: systemd
failCgroupV1: true
clusterDNS:
  - "${node_ipv6}"
clusterDomain: cluster.local
resolvConf: /run/systemd/resolve/resolv.conf
EOF

# Declare startup dependencies in systemd rather than relying on command order.
install_service containerd 'containerd container runtime' root notify \
  '/usr/local/bin/containerd --config /etc/containerd/config.toml' \
  'network-online.target' 'network-online.target' '' \
  'Delegate=yes' 'KillMode=process' 'TasksMax=infinity' 'LimitNPROC=infinity' 'LimitCORE=infinity' 'OOMScoreAdjust=-999'
install_service zot 'Podmin local registry' zot exec \
  '/usr/local/bin/zot serve /etc/zot.json' \
  'network-online.target' 'network-online.target' ''
install_service podmin-agent 'Podmin agent' root exec \
  "/usr/local/bin/podmin-agent --provider=aws --bucket=${bucket} --region=${region} --cluster=${cluster} --nodegroup=${nodegroup} --ipv6-prefix=${pod_prefix}" \
  'network-online.target podmin-network.service kubelet.service' 'network-online.target' 'podmin-network.service'
install_service coredns 'Podmin DNS' coredns exec \
  '/usr/local/bin/coredns -conf /etc/coredns/Corefile' \
  'network-online.target podmin-agent.service' 'network-online.target podmin-agent.service' \
  '' \
  'CapabilityBoundingSet=CAP_NET_BIND_SERVICE' 'AmbientCapabilities=CAP_NET_BIND_SERVICE' 'NoNewPrivileges=true'
install_service kubelet 'Kubernetes node agent' root exec \
  "/usr/local/bin/kubelet --config=/etc/kubernetes/kubelet.yaml --node-ip=${node_ipv6}" \
  'containerd.service zot.service podmin-network.service' \
  'zot.service' \
  'containerd.service podmin-network.service' \
  "ExecStartPost=/bin/sh -c 'for attempt in \$(seq 1 60); do test -S /var/lib/kubelet/pods-api/pods-api.sock && exit 0; sleep 1; done; exit 1'"

# Enabling persists services across reboot; ordered starts avoid a dependency cycle.
systemctl daemon-reload
systemctl enable containerd zot podmin-network podmin-agent coredns kubelet
systemctl start kubelet
systemctl start podmin-agent coredns
