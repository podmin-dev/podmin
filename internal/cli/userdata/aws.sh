#!/usr/bin/env bash
# Podmin <https://podmin.dev>
# Copyright The Podmin Authors
# SPDX-License-Identifier: Apache-2.0
# AWS user-data template rendered by the Podmin CLI.

set -Eeuo pipefail

# Values below are rendered by the Podmin CLI.
bucket='PODMIN_BUCKET'
region='PODMIN_REGION'
cluster='PODMIN_CLUSTER'
nodegroup='PODMIN_NODEGROUP'
architecture='PODMIN_ARCH'
pause_image='PODMIN_PAUSE_IMAGE'
otel_logs_enabled='PODMIN_OTEL_LOGS_ENABLED'
otel_logs_mtls='PODMIN_OTEL_LOGS_MTLS'
workload_ca_publish='PODMIN_WORKLOAD_CA_PUBLISH'
downloads=/opt/podmin/downloads
destination=/opt/podmin/dependencies
export AWS_USE_DUALSTACK_ENDPOINT=true

# log writes a timestamped bootstrap event to stdout
log() {
  printf '[userdata] %s\tts=%s\n' "$*" "$EPOCHREALTIME"
}

# fatal reports an expected bootstrap failure and terminates the script.
fatal() {
  log "Podmin AWS user-data failed: $*" >&2
  exit 1
}

# on_error reports an unexpected command failure before terminating bootstrap.
on_error() {
  local status=$? line=${BASH_LINENO[0]}
  trap - ERR
  log "Podmin AWS user-data failed at line ${line} (exit ${status})." >&2
  exit "$status"
}

# Report the point of failure before cloud-init records the script exit.
trap on_error ERR

log "Podmin AWS user-data has started for cluster ${cluster}, NodeGroup ${nodegroup}, architecture ${architecture}."

# Refuse packages built for a different machine architecture.
log 'Checking machine architecture...'
case "$(uname -m):${architecture}" in
  x86_64:amd64|aarch64:arm64) ;;
  *) fatal "unexpected machine architecture: $(uname -m) (wanted ${architecture})" ;;
esac
log 'Machine architecture check completed successfully.'

# Install SSM first so failed bootstrap remains remotely diagnosable.
log 'Ensuring AWS SSM Agent is installed and running...'
curl --fail --silent --show-error --location \
  --connect-timeout 5 --max-time 120 --retry 10 --retry-delay 3 --retry-all-errors \
  -o /tmp/amazon-ssm-agent.deb \
  "https://s3.dualstack.${region}.amazonaws.com/amazon-ssm-${region}/latest/debian_${architecture}/amazon-ssm-agent.deb"
dpkg -i /tmp/amazon-ssm-agent.deb
rm -f /tmp/amazon-ssm-agent.deb
ssm_config=/etc/amazon/ssm/amazon-ssm-agent.json
install -d -m 0755 /etc/amazon/ssm
cat > "$ssm_config" <<EOF
{
  "Agent": {
    "Region": "${region}",
    "UseDualStackEndpoint": true
  }
}
EOF
systemctl enable amazon-ssm-agent
systemctl restart amazon-ssm-agent
log 'AWS SSM Agent installation completed successfully.'

# Each row contains the local name, object key, and expected digest.
dependencies=(
  # PODMIN_DEPENDENCIES
)

# The official Debian EC2 image includes AWS CLI; fail clearly if that changes.
log 'Checking required host tools...'
command -v aws >/dev/null 2>&1 || {
  fatal 'aws CLI is required but is not installed'
}
command -v python3 >/dev/null 2>&1 || {
  fatal 'Python 3 is required (for bzip extraction) but is not installed'
}
log 'Required host tool checks completed successfully.'

# install_dependency extracts one executable from a release archive.
install_dependency() {
  local archive="$1" binary="$2" target="$3" unpacked source_file
  unpacked=$(mktemp -d)
  tar -xzf "$archive" -C "$unpacked"
  source_file=$(find "$unpacked" -type f -name "$binary" -print -quit)
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

log 'Checking host kernel requirements...'
release=$(uname -r)
IFS=. read -r major minor _ <<<"$release"
if ((major < 6 || (major == 6 && minor < 6))); then
  fatal "kernel ${release} does not meet the TCX baseline (6.6 or newer)"
fi
log 'Host kernel checks completed successfully.'

# Enable routed Pod IPv6 and keep the dataplane's SNAT ports out of host ephemeral allocation.
log 'Configuring kernel networking...'
cat > /etc/sysctl.d/99-podmin.conf <<'EOF'
net.ipv6.conf.all.forwarding = 1
net.ipv4.ip_local_reserved_ports = 30000-32767
EOF
sysctl --system >/dev/null
log 'Kernel networking configuration completed successfully.'

mkdir -p "$downloads" "$destination"

# Download only this machine's pinned dependency objects.
includes=(--exclude '*')
for dependency in "${dependencies[@]}"; do
  IFS='|' read -r _ object _ <<<"$dependency"
  includes+=(--include "${object#dependencies/}")
done

for ((attempt = 1; attempt <= 10; attempt++)); do
  log "Downloading ${#dependencies[@]} runtime dependencies (attempt ${attempt}/10)..."
  if aws s3 sync "s3://${bucket}/dependencies/" "$downloads/" \
    --region "$region" \
    --only-show-errors \
    "${includes[@]}"; then
    break
  fi
  if ((attempt == 10)); then
    fatal 'runtime dependency download failed after 10 attempts'
  fi
  log 'Runtime dependency download failed; retrying in 3 seconds...'
  sleep 3
done
log 'Runtime dependency download completed successfully.'

# Verify every download before installing anything.
log 'Verifying runtime dependency checksums...'
for dependency in "${dependencies[@]}"; do
  IFS='|' read -r name object digest <<<"$dependency"
  source_file="${downloads}/${object#dependencies/}"
  case "$digest" in
    sha512:*) printf '%s  %s\n' "${digest#sha512:}" "$source_file" | sha512sum --check --status ;;
    sha256:*) printf '%s  %s\n' "${digest#sha256:}" "$source_file" | sha256sum --check --status ;;
    *) fatal "unsupported digest for ${name}: ${digest}" ;;
  esac
done
log 'Runtime dependency verification completed successfully.'

# Preserve verified artifacts at stable paths for installation and diagnostics.
log 'Staging verified runtime dependencies...'
for dependency in "${dependencies[@]}"; do
  IFS='|' read -r name object _ <<<"$dependency"
  source_file="${downloads}/${object#dependencies/}"
  install -m 0644 "$source_file" "${destination}/${name}"
done
rm -rf "$downloads"
log 'Runtime dependency staging completed successfully.'

log 'Installing runtime dependencies...'

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
python3 -m tarfile -e "${destination}/gvisor.tar.bz2" "$unpacked"
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
install_dependency "${destination}/crictl.tar.gz" crictl /usr/local/bin/crictl
install_dependency "${destination}/coredns.tar.gz" coredns /usr/local/bin/coredns
install_dependency "${destination}/podmin-agent.tar.gz" podmin-agent /usr/local/bin/podmin-agent

# Point crictl directly at containerd CRI endpoint.
cat > /etc/crictl.yaml <<'EOF'
runtime-endpoint: unix:///run/containerd/containerd.sock
image-endpoint: unix:///run/containerd/containerd.sock
EOF

log 'Runtime dependency installation completed successfully.'

# Identify the primary and Pod ENIs from local instance metadata.
log 'Discovering node and Pod networking...'
curl_options=(--fail --silent --show-error --connect-timeout 2 --max-time 10 --retry 3 --retry-delay 1 --retry-all-errors)
imds_token=$(curl "${curl_options[@]}" --request PUT \
  --header 'X-aws-ec2-metadata-token-ttl-seconds: 300' \
  http://169.254.169.254/latest/api/token)
mac_list=$(curl "${curl_options[@]}" \
  --header "X-aws-ec2-metadata-token: ${imds_token}" \
  http://169.254.169.254/latest/meta-data/network/interfaces/macs/)
mapfile -t macs < <(printf '%s' "$mac_list")
node_mac=
pod_mac=
pod_eni=
for mac in "${macs[@]}"; do
  mac=${mac%/}
  device_number=$(curl "${curl_options[@]}" \
    --header "X-aws-ec2-metadata-token: ${imds_token}" \
    "http://169.254.169.254/latest/meta-data/network/interfaces/macs/${mac}/device-number")
  case "$device_number" in
    0) node_mac=$mac ;;
    1)
      pod_mac=$mac
      pod_eni=$(curl "${curl_options[@]}" \
        --header "X-aws-ec2-metadata-token: ${imds_token}" \
        "http://169.254.169.254/latest/meta-data/network/interfaces/macs/${mac}/interface-id")
      ;;
  esac
done
[ -n "$node_mac" ] || fatal 'primary ENI is missing from instance metadata'
[ -n "$pod_mac" ] || fatal 'Pod ENI is missing from instance metadata'

node_interface=
pod_interface=
for interface_path in /sys/class/net/*; do
  case "$(cat "${interface_path}/address")" in
    "$node_mac") node_interface=${interface_path##*/} ;;
    "$pod_mac") pod_interface=${interface_path##*/} ;;
  esac
done
[ -n "$node_interface" ] || fatal "no local interface has primary ENI MAC ${node_mac}"
[ -n "$pod_interface" ] || fatal "no local interface has Pod ENI MAC ${pod_mac}"

# Select the node address only from the primary ENI.
node_ipv6=
for ((attempt = 1; attempt <= 40; attempt++)); do
  mapfile -t node_addresses < <(ip -6 -o address show dev "$node_interface" scope global up | awk '{split($4, a, "/"); print a[1]}' | sort -u)
  if ((${#node_addresses[@]} == 1)); then
    node_ipv6=${node_addresses[0]}
    break
  fi
  ((${#node_addresses[@]} == 0)) || fatal "expected one global IPv6 address on ${node_interface}, found ${#node_addresses[@]}"
  if ((attempt == 40)); then
    fatal "no global IPv6 address became available on ${node_interface} after 40 attempts"
  fi
  log "Global node IPv6 address is not available (attempt ${attempt}/40); retrying in 3 seconds..."
  sleep 3
done
log 'Global node IPv6 address discovered successfully.'

# Configure the Pod ENI's ordinary address before assigning its delegated prefix.
mapfile -t pod_addresses < <(curl "${curl_options[@]}" \
  --header "X-aws-ec2-metadata-token: ${imds_token}" \
  "http://169.254.169.254/latest/meta-data/network/interfaces/macs/${pod_mac}/ipv6s")
((${#pod_addresses[@]} == 1)) || fatal "expected one ordinary IPv6 address on Pod ENI ${pod_eni}, found ${#pod_addresses[@]}"
ip link set "$pod_interface" up
ip -6 address replace "${pod_addresses[0]}/128" dev "$pod_interface" nodad

# Reuse or assign exactly one directly routable Pod prefix.
mapfile -t pod_prefixes < <(curl "${curl_options[@]}" \
  --header "X-aws-ec2-metadata-token: ${imds_token}" \
  "http://169.254.169.254/latest/meta-data/network/interfaces/macs/${pod_mac}/ipv6-prefix" 2>/dev/null || true)
if ((${#pod_prefixes[@]} == 0)); then
  log "Assigning delegated Pod IPv6 prefix to ${pod_eni}..."
  assignment_status=0
  aws ec2 assign-ipv6-addresses \
    --region "$region" \
    --network-interface-id "$pod_eni" \
    --ipv6-prefix-count 1 >/dev/null || assignment_status=$?
  for ((attempt = 1; attempt <= 40; attempt++)); do
    mapfile -t pod_prefixes < <(curl "${curl_options[@]}" \
      --header "X-aws-ec2-metadata-token: ${imds_token}" \
      "http://169.254.169.254/latest/meta-data/network/interfaces/macs/${pod_mac}/ipv6-prefix" 2>/dev/null || true)
    ((${#pod_prefixes[@]} == 0)) || break
    sleep 3
  done
  if ((${#pod_prefixes[@]} == 0)); then
    fatal "delegated Pod IPv6 prefix assignment failed with status ${assignment_status}"
  fi
fi
((${#pod_prefixes[@]} == 1)) || fatal "expected one delegated IPv6 prefix on Pod ENI ${pod_eni}, found ${#pod_prefixes[@]}"
pod_prefix=${pod_prefixes[0]}
log 'Delegated Pod IPv6 prefix discovered successfully.'

# Make networkd own Podmin's routes and policy rules so an interface
# reconfiguration restores them without competing imperative state.
network_dropin="/etc/systemd/network/10-netplan-${pod_interface}.network.d"
install -d -m 0755 "$network_dropin"
cat > "${network_dropin}/50-podmin.conf" <<EOF
[Route]
Destination=${pod_prefix}
Metric=50

[Route]
Destination=::/0
Gateway=fe80:ec2::1
GatewayOnLink=yes
Table=80

[RoutingPolicyRule]
From=${pod_prefix}
Table=main
Priority=80
SuppressPrefixLength=0

[RoutingPolicyRule]
From=${pod_prefix}
Table=80
Priority=81
EOF
networkctl reload

# Reconfigure the Pod ENI as an idempotent repair path for its networkd state.
{
  printf '#!/bin/sh\nset -eu\n'
  printf "pod_interface='%s'\n" "$pod_interface"
  cat <<'EOF'
ip link set "$pod_interface" up
networkctl reload
networkctl reconfigure "$pod_interface"
/usr/lib/systemd/systemd-networkd-wait-online --interface="$pod_interface" --timeout=60
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
log 'Node and Pod network discovery completed successfully.'

# Create service identities and runtime directories idempotently.
log 'Writing Podmin runtime configuration...'
id -u coredns >/dev/null 2>&1 || useradd --system --home-dir /var/lib/coredns --shell /usr/sbin/nologin coredns
install -d -m 0755 /etc/cni/net.d /etc/containerd/certs.d/registry.podmin.internal /etc/coredns /etc/kubernetes /etc/podmin/manifests /opt/cni/bin /var/lib/coredns
install -d -m 0700 /run/podmin /run/podmin/workloads

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
systemReserved:
  cpu: 100m
  memory: 96Mi
kubeReserved:
  cpu: 100m
  memory: 128Mi
enforceNodeAllocatable:
  - pods
clusterDNS:
  - "${node_ipv6}"
clusterDomain: cluster.local
resolvConf: /run/systemd/resolve/resolv.conf
EOF

if [ "$otel_logs_enabled" = true ]; then
  install -d -m 0700 /etc/fluent-bit /var/lib/fluent-bit/storage
  cat > /usr/local/sbin/podmin-fluent-bit-config <<'EOF'
#!/usr/bin/env bash
set -Eeuo pipefail
if [ 'PODMIN_OTEL_LOGS_MTLS' = true ]; then
  for attempt in $(seq 1 60); do
    identity=$(readlink /run/podmin/telemetry/identity 2>/dev/null || true)
    if [[ "$identity" =~ ^identity-generations/[0-9a-f]{128}$ ]]; then
      identity_dir="/run/podmin/telemetry/${identity}"
      if [ -s "${identity_dir}/tls.crt" ] && [ -s "${identity_dir}/tls.key" ]; then
        export PODMIN_FLUENT_BIT_IDENTITY_DIR="$identity_dir"
        break
      fi
    fi
    if [ "$attempt" -eq 60 ]; then
      echo 'Fluent Bit mTLS identity is unavailable' >&2
      exit 1
    fi
    sleep 1
  done
fi
headers_file=$(mktemp)
config_file=$(mktemp /etc/fluent-bit/fluent-bit.yaml.XXXXXX)
trap 'rm -f "$headers_file" "$config_file"' EXIT
if [ -n 'PODMIN_OTEL_LOGS_HEADERS_SECRET' ]; then
  case 'PODMIN_OTEL_LOGS_HEADERS_PROVIDER' in
    aws-parameter-store)
      aws ssm get-parameter --region 'PODMIN_REGION' --name 'PODMIN_OTEL_LOGS_HEADERS_SECRET' --with-decryption --query Parameter.Value --output text >"$headers_file"
      ;;
    aws-secrets-manager)
      aws secretsmanager get-secret-value --region 'PODMIN_REGION' --secret-id 'PODMIN_OTEL_LOGS_HEADERS_SECRET' --query SecretString --output text >"$headers_file"
      ;;
  esac
else
  printf '{}\n' >"$headers_file"
fi
python3 - "$headers_file" "$config_file" <<'PYTHON'
import json
import os
import re
import socket
import sys

with open(sys.argv[1], encoding="utf-8") as source:
    headers = json.load(source)
if not isinstance(headers, dict):
    raise SystemExit("OTLP headers secret must contain a JSON object")
for name, value in headers.items():
    if not isinstance(name, str) or not re.fullmatch(r"[!#$%&'*+.^_\x60|~0-9A-Za-z-]+", name):
        raise SystemExit("OTLP header names must be HTTP token strings")
    if not isinstance(value, str) or not value or any(ord(character) < 32 or ord(character) == 127 for character in value):
        raise SystemExit("OTLP header values must be non-empty strings without control characters")

output = {
    "name": "opentelemetry",
    "match": "pod.*",
    "host": "PODMIN_OTEL_LOGS_HOST",
    "port": PODMIN_OTEL_LOGS_PORT,
    "tls": "on",
    "tls.verify": "on",
    "tls.verify_hostname": "on",
    "grpc": "on" if "PODMIN_OTEL_LOGS_PROTOCOL" == "grpc" else "off",
    "logs_uri": "PODMIN_OTEL_LOGS_URI",
    "logs_body_key": "$log",
    "logs_body_key_attributes": True,
    "log_response_payload": False,
    "retry_limit": "no_limits",
    "storage.total_limit_size": "1G",
}
if headers:
    output["header"] = [f"{name} {value}" for name, value in sorted(headers.items())]
if "PODMIN_OTEL_LOGS_MTLS" == "true":
    identity_dir = os.environ["PODMIN_FLUENT_BIT_IDENTITY_DIR"]
    output["tls.crt_file"] = identity_dir + "/tls.crt"
    output["tls.key_file"] = identity_dir + "/tls.key"
if "PODMIN_OTEL_LOGS_CA":
    output["tls.ca_file"] = "/run/podmin/telemetry/server-ca.pem"
configuration = {
    "service": {
        "flush": 1,
        "log_level": "info",
        "storage.path": "/var/lib/fluent-bit/storage",
        "storage.sync": "normal",
        "storage.checksum": "on",
        "storage.max_chunks_up": 128,
        "storage.backlog.mem_limit": "5M",
    },
    "parsers": [{
        "name": "kubernetes_container_path",
        "format": "regex",
        "regex": r"^/var/log/containers/(?<pod>[^_]+)_(?<namespace>[^_]+)_(?<container>[^_]+)-(?<container_id>[0-9a-f]{64})\.log$",
    }],
    "pipeline": {
        "inputs": [{
            "name": "tail",
            "tag": "pod.*",
            "path": "/var/log/containers/*.log",
            "path_key": "log.file.path",
            "multiline.parser": "cri",
            "db": "/var/lib/fluent-bit/tail.db",
            "db.sync": "normal",
            "read_from_head": False,
            "refresh_interval": 5,
            "rotate_wait": 30,
            "skip_long_lines": "on",
            "storage.type": "filesystem",
            "processors": {
                "logs": [
                    {"name": "opentelemetry_envelope"},
                    {"name": "content_modifier", "context": "otel_resource_attributes", "action": "upsert", "key": "podmin.cluster.id", "value": "PODMIN_CLUSTER"},
                    {"name": "content_modifier", "context": "otel_resource_attributes", "action": "upsert", "key": "podmin.nodegroup.id", "value": "PODMIN_NODEGROUP"},
                    {"name": "content_modifier", "context": "otel_resource_attributes", "action": "upsert", "key": "host.name", "value": socket.gethostname()},
                ],
            },
        }],
        "filters": [{
            "name": "parser",
            "match": "pod.*",
            "key_name": "log.file.path",
            "parser": "kubernetes_container_path",
            "reserve_data": "on",
            "preserve_key": "on",
        }, {
            "name": "modify",
            "match": "pod.*",
            "rename": [
                "pod k8s.pod.name",
                "namespace k8s.namespace.name",
                "container k8s.container.name",
                "container_id container.id",
            ],
        }],
        "outputs": [output],
    },
}
with open(sys.argv[2], "w", encoding="utf-8") as destination:
    json.dump(configuration, destination, indent=2)
    destination.write("\n")
os.chmod(sys.argv[2], 0o600)
PYTHON
mv "$config_file" /etc/fluent-bit/fluent-bit.yaml
rm -f "$headers_file"
trap - EXIT
EOF
  chmod 0700 /usr/local/sbin/podmin-fluent-bit-config
fi

# Declare startup dependencies in systemd rather than relying on command order.
install_service containerd 'containerd container runtime' root notify \
  '/usr/local/bin/containerd --config /etc/containerd/config.toml' \
  'network-online.target' 'network-online.target' '' \
  'Delegate=yes' 'KillMode=process' 'TasksMax=infinity' 'LimitNPROC=infinity' 'LimitCORE=infinity' 'OOMScoreAdjust=-999'
install_service podmin-agent 'Podmin agent' root exec \
  "/usr/local/bin/podmin-agent --provider=aws --bucket=${bucket} --region=${region} --cluster=${cluster} --nodegroup=${nodegroup} --node-address=${node_ipv6} --ipv6-prefix=${pod_prefix} --workload-ca-publish=${workload_ca_publish} --otel-logs-ca=PODMIN_OTEL_LOGS_CA --otel-logs-mtls=${otel_logs_mtls}" \
  'network-online.target podmin-network.service' 'network-online.target' 'podmin-network.service'
install_service coredns 'Podmin DNS' coredns exec \
  '/usr/local/bin/coredns -conf /etc/coredns/Corefile' \
  'network-online.target podmin-agent.service' 'network-online.target podmin-agent.service' \
  '' \
  'CapabilityBoundingSet=CAP_NET_BIND_SERVICE' 'AmbientCapabilities=CAP_NET_BIND_SERVICE' 'NoNewPrivileges=true'
install_service kubelet 'Kubernetes node agent' root exec \
  "/usr/local/bin/kubelet --config=/etc/kubernetes/kubelet.yaml --node-ip=${node_ipv6}" \
  'containerd.service podmin-agent.service podmin-network.service' \
  'podmin-agent.service' \
  'containerd.service podmin-network.service' \
  "ExecStartPost=/bin/sh -c 'for attempt in \$(seq 1 60); do test -S /var/lib/kubelet/pods-api/pods-api.sock && exit 0; sleep 1; done; exit 1'"
if [ "$otel_logs_enabled" = true ]; then
  install_service fluent-bit 'Fluent Bit container log collector' root simple \
    '/opt/fluent-bit/bin/fluent-bit --config=/etc/fluent-bit/fluent-bit.yaml' \
    'network-online.target kubelet.service' \
    'network-online.target kubelet.service' \
    'kubelet.service' \
    'ExecStartPre=/usr/local/sbin/podmin-fluent-bit-config' \
    'UMask=0077'
fi
log 'Podmin runtime configuration completed successfully.'

# Enabling persists services across reboot; ordered starts avoid a dependency cycle.
log 'Enabling and starting Podmin services...'
systemctl daemon-reload
services=(containerd podmin-network podmin-agent coredns kubelet)
if [ "$otel_logs_enabled" = true ]; then
  services+=(fluent-bit)
fi
systemctl enable "${services[@]}"
systemctl start podmin-agent
systemctl start kubelet coredns
if [ "$otel_logs_enabled" = true ]; then
  log 'Installing Fluent Bit and its dependencies...'
  if dpkg --install "${destination}/libpq5.deb" && dpkg --install "${destination}/fluent-bit.deb"; then
    log 'Fluent Bit installation completed successfully.'
    systemctl start fluent-bit || log 'warning: Fluent Bit failed to start; Podmin workloads remain available'
  else
    log 'warning: Fluent Bit installation failed; Podmin workloads remain available'
  fi
fi
log 'Podmin services started successfully.'

log 'Podmin user-data completed successfully.'
