{{/*
Resolve instance ID — shared helper used by kubeadm, rke2, and eks-hybrid templates.
Reads cloud-init instance-id and strips dashes to match Lambda API format.
*/}}
{{- define "lambda-karpenter.resolveInstanceID" -}}
set -eu
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
{{- end -}}

{{/*
kubeadm worker join user-data.
*/}}
{{- define "lambda-karpenter.userData.kubeadm" -}}
#cloud-config
runcmd:
  # Reconfigure containerd: enable CRI, systemd cgroup, NVIDIA runtime.
  - containerd config default > /etc/containerd/config.toml
  - sed -i 's/SystemdCgroup = false/SystemdCgroup = true/' /etc/containerd/config.toml
  - nvidia-ctk runtime configure --runtime=containerd --set-as-default
  - systemctl restart containerd

  # Kernel prereqs.
  - modprobe br_netfilter
  - sysctl -w net.bridge.bridge-nf-call-iptables=1

  # Install kubeadm/kubelet.
  - curl -fsSL https://pkgs.k8s.io/core:/stable:/v1.32/deb/Release.key | gpg --dearmor -o /etc/apt/keyrings/kubernetes-apt-keyring.gpg
  - echo 'deb [signed-by=/etc/apt/keyrings/kubernetes-apt-keyring.gpg] https://pkgs.k8s.io/core:/stable:/v1.32/deb/ /' > /etc/apt/sources.list.d/kubernetes.list
  - apt-get update -qq && apt-get install -y -qq kubeadm kubelet

  # Set provider-id so Karpenter can match instance to node.
  - |
      {{ include "lambda-karpenter.resolveInstanceID" . | nindent 6 }}
      mkdir -p /etc/systemd/system/kubelet.service.d
      cat <<CFG > /etc/systemd/system/kubelet.service.d/20-provider-id.conf
      [Service]
      Environment="KUBELET_EXTRA_ARGS=--provider-id=lambda://${INSTANCE_ID}"
      CFG
      systemctl daemon-reload

  # Join cluster.
  - kubeadm join {{ .Values.cluster.controllerIP }}:6443 --token {{ .Values.cluster.joinToken }} --discovery-token-unsafe-skip-ca-verification
{{- end -}}

{{/*
RKE2 agent join user-data.
*/}}
{{- define "lambda-karpenter.userData.rke2" -}}
#cloud-config
package_update: true
package_upgrade: false

runcmd:
  - curl -sfL https://get.rke2.io | INSTALL_RKE2_TYPE="agent" sh -
  - mkdir -p /etc/rancher/rke2

  # Write RKE2 agent config with provider-id.
  - |
      {{ include "lambda-karpenter.resolveInstanceID" . | nindent 6 }}
      cat <<CFG > /etc/rancher/rke2/config.yaml
      server: https://{{ .Values.cluster.controllerIP }}:9345
      token: {{ .Values.cluster.joinToken }}
      kubelet-arg:
        - provider-id=lambda://${INSTANCE_ID}
      CFG

  - systemctl enable rke2-agent
  - systemctl start rke2-agent
{{- end -}}

{{/*
EKS hybrid node registration user-data using nodeadm.
SSM activation credentials are injected dynamically by the provider at launch
time via Go template variables ({{ "{{" }} .SSMActivationCode {{ "}}" }}, etc.).
Static config (cluster name, region, VPC CIDR) comes from Helm values.
*/}}
{{- define "lambda-karpenter.userData.eks-hybrid" -}}
#!/bin/bash
set -eux

# --- Config (static from Helm) ---
EKS_CLUSTER_NAME="{{ required "cluster.eksClusterName is required for eks-hybrid" .Values.cluster.eksClusterName }}"
EKS_REGION="{{ required "cluster.eksRegion is required for eks-hybrid" .Values.cluster.eksRegion }}"
K8S_VERSION="{{ .Values.cluster.eksK8sVersion | default "1.31" }}"

# --- Config (dynamic from provider at launch time) ---
SSM_ACTIVATION_CODE="{{ "{{" }} .SSMActivationCode {{ "}}" }}"
SSM_ACTIVATION_ID="{{ "{{" }} .SSMActivationID {{ "}}" }}"
GATEWAY_IP="{{ "{{" }} .GatewayIP {{ "}}" }}"
VPC_CIDR="{{ required "cluster.eksVPCCIDR is required for eks-hybrid" .Values.cluster.eksVPCCIDR }}"

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
ip route add ${VPC_CIDR} via ${GATEWAY_IP}

# --- 3. Download and install nodeadm ---
ARCH="$(uname -m)"
case "$ARCH" in
  x86_64)  ARCH="amd64" ;;
  aarch64) ARCH="arm64" ;;
esac
curl -fsSL "https://hybrid-assets.eks.amazonaws.com/releases/latest/bin/linux/${ARCH}/nodeadm" -o /usr/local/bin/nodeadm
chmod +x /usr/local/bin/nodeadm
nodeadm install ${K8S_VERSION} --credential-provider ssm

# --- 4. Resolve instance ID for provider-id ---
{{ include "lambda-karpenter.resolveInstanceID" . }}

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
{{- end -}}

{{/*
Dispatcher — selects user-data template based on cluster.type.
*/}}
{{- define "lambda-karpenter.defaultUserData" -}}
{{- if eq .Values.cluster.type "rke2" -}}
{{ include "lambda-karpenter.userData.rke2" . }}
{{- else if eq .Values.cluster.type "eks-hybrid" -}}
{{ include "lambda-karpenter.userData.eks-hybrid" . }}
{{- else -}}
{{ include "lambda-karpenter.userData.kubeadm" . }}
{{- end -}}
{{- end -}}
