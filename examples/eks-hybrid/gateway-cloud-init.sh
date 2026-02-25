#!/bin/bash
set -eux

# EKS Hybrid gateway node bootstrap.
# This node holds the WireGuard tunnel to the AWS jumpbox and joins the EKS
# cluster. Worker nodes provisioned by Karpenter route VPC traffic through
# this gateway instead of running their own tunnel.

# --- Configuration (templated at launch time) ---
EKS_CLUSTER_NAME="lambda-hybrid"
EKS_REGION="us-west-2"
K8S_VERSION="1.31"
SSM_ACTIVATION_CODE="<activation-code>"
SSM_ACTIVATION_ID="<activation-id>"

WG_PRIVATE_KEY="<gateway-private-key>"
WG_JUMPBOX_PUBKEY="<jumpbox-public-key>"
WG_JUMPBOX_ENDPOINT="<jumpbox-public-ip>:51820"
WG_ADDRESS="10.100.0.2/30"
VPC_CIDR="172.31.0.0/16"

# --- 1. Remove conflicting Lambda Stack services and packages ---
systemctl disable --now \
  lambda-jupyter.service \
  cloudflared.service cloudflared-update.service cloudflared-update.timer \
  docker.service docker.socket \
  glances.service \
  postfix@-.service || true
ip link delete docker0 2>/dev/null || true
apt-get purge -y -qq \
  docker-ce docker-ce-cli docker-buildx-plugin \
  docker-compose-plugin docker-ce-rootless-extras \
  cloudflared glances podman 2>/dev/null || true

# --- 2. Enable IP forwarding (for worker node traffic) ---
sysctl -w net.ipv4.ip_forward=1
echo 'net.ipv4.ip_forward=1' > /etc/sysctl.d/99-forward.conf

# --- 3. Install WireGuard and establish tunnel to AWS jumpbox ---
apt-get update -qq && apt-get install -y -qq wireguard-tools

cat > /etc/wireguard/wg0.conf <<WG
[Interface]
PrivateKey = ${WG_PRIVATE_KEY}
Address = ${WG_ADDRESS}
MTU = 1420

[Peer]
PublicKey = ${WG_JUMPBOX_PUBKEY}
Endpoint = ${WG_JUMPBOX_ENDPOINT}
AllowedIPs = ${VPC_CIDR}
PersistentKeepalive = 25
WG
chmod 600 /etc/wireguard/wg0.conf
systemctl enable --now wg-quick@wg0

# --- 4. Download and install nodeadm (must run before nodeadm init) ---
ARCH="$(uname -m)"
case "$ARCH" in
  x86_64)  ARCH="amd64" ;;
  aarch64) ARCH="arm64" ;;
esac
curl -fsSL "https://hybrid-assets.eks.amazonaws.com/releases/latest/bin/linux/${ARCH}/nodeadm" -o /usr/local/bin/nodeadm
chmod +x /usr/local/bin/nodeadm
nodeadm install ${K8S_VERSION} --credential-provider ssm

# --- 5. Resolve instance ID for provider-id ---
INSTANCE_ID=""
if [ -f /var/lib/cloud/data/instance-id ]; then
  INSTANCE_ID="$(cat /var/lib/cloud/data/instance-id | tr -d '\n')"
fi
if [ -z "$INSTANCE_ID" ] && [ -f /run/cloud-init/.instance-id ]; then
  INSTANCE_ID="$(cat /run/cloud-init/.instance-id | tr -d '\n')"
fi
if [ -z "$INSTANCE_ID" ]; then
  echo "could not determine instance id for provider-id" >&2
  exit 1
fi
INSTANCE_ID="$(echo "$INSTANCE_ID" | tr -d '-')"

# --- 6. Write nodeadm config and join cluster ---
mkdir -p /etc/eks
cat > /etc/eks/nodeadm-config.yaml <<EOF
apiVersion: node.eks.aws/v1alpha1
kind: NodeConfig
spec:
  cluster:
    name: ${EKS_CLUSTER_NAME}
    region: ${EKS_REGION}
  hybrid:
    ssm:
      activationCode: ${SSM_ACTIVATION_CODE}
      activationId: ${SSM_ACTIVATION_ID}
  kubelet:
    flags:
      - --provider-id=lambda://${INSTANCE_ID}
      - --node-labels=lambda.cloud/role=gateway
EOF

nodeadm init --config-source file:///etc/eks/nodeadm-config.yaml
