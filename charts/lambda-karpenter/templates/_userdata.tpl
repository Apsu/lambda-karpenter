{{/*
Resolve instance ID — shared helper used by both kubeadm and rke2 templates.
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
Dispatcher — selects user-data template based on cluster.type.
*/}}
{{- define "lambda-karpenter.defaultUserData" -}}
{{- if eq .Values.cluster.type "rke2" -}}
{{ include "lambda-karpenter.userData.rke2" . }}
{{- else -}}
{{ include "lambda-karpenter.userData.kubeadm" . }}
{{- end -}}
{{- end -}}
