#!/bin/bash
set -eux

# EKS Hybrid worker node bootstrap.
# Launched by the Karpenter provider. Pod-level VPC traffic is handled by
# Cilium egress gateway (tunneled through the gateway node's wg0 interface).
# The static route below is only needed for host-level nodeadm bootstrap
# before Cilium is running.

# --- Configuration (templated at launch time) ---
EKS_CLUSTER_NAME="lambda-hybrid"
EKS_REGION="us-west-2"
K8S_VERSION="1.31"
SSM_ACTIVATION_CODE="<activation-code>"
SSM_ACTIVATION_ID="<activation-id>"

GATEWAY_IP="<gateway-lambda-ip>"
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

# --- 2. Add static route to VPC through gateway node ---
# Needed for host-level nodeadm bootstrap (reaching EKS API before Cilium starts).
# Once Cilium is running, pod traffic to VPC uses the egress gateway instead.
ip route add ${VPC_CIDR} via ${GATEWAY_IP}

# --- 3. Download and install nodeadm (must run before nodeadm init) ---
ARCH="$(uname -m)"
case "$ARCH" in
  x86_64)  ARCH="amd64" ;;
  aarch64) ARCH="arm64" ;;
esac
curl -fsSL "https://hybrid-assets.eks.amazonaws.com/releases/latest/bin/linux/${ARCH}/nodeadm" -o /usr/local/bin/nodeadm
chmod +x /usr/local/bin/nodeadm
nodeadm install ${K8S_VERSION} --credential-provider ssm

# --- 4. Resolve instance ID for provider-id ---
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

# --- 5. Write nodeadm config and join cluster ---
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
      - --register-with-taints=karpenter.sh/unregistered:NoExecute
EOF

nodeadm init --config-source file:///etc/eks/nodeadm-config.yaml
