#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TEMPLATE_PATH="${TEMPLATE_PATH:-${ROOT_DIR}/examples/bootstrap-controller-cloud-init.yaml}"

require() {
  if [[ -z "${!1:-}" ]]; then
    echo "missing required env var: $1" >&2
    exit 1
  fi
}

require RKE2_TOKEN
require REGION
require INSTANCE_TYPE
require IMAGE_FAMILY
require SSH_KEY_NAME
require CLUSTER_NAME

SSH_USER="${SSH_USER:-ubuntu}"
SSH_KEY_PATH="${SSH_KEY_PATH:-}"
NODECLASS_OUT="${NODECLASS_OUT:-${ROOT_DIR}/lambdanodeclass.generated.yaml}"
RKE2_SERVER_ADDR="${RKE2_SERVER_ADDR:-}"

if [[ ! -f "${TEMPLATE_PATH}" ]]; then
  echo "cloud-init template not found: ${TEMPLATE_PATH}" >&2
  exit 1
fi

WORKDIR="$(mktemp -d)"
cleanup() { rm -rf "${WORKDIR}"; }
trap cleanup EXIT

CLOUD_INIT="${WORKDIR}/cloud-init.yaml"
sed \
  -e "s|REPLACE_WITH_RKE2_TOKEN|${RKE2_TOKEN}|g" \
  "${TEMPLATE_PATH}" > "${CLOUD_INIT}"

LAUNCH_CONFIG="${WORKDIR}/launch.yaml"
cat > "${LAUNCH_CONFIG}" <<EOF
name: ${CLUSTER_NAME}-controller
hostname: ${CLUSTER_NAME}-controller
region: ${REGION}
instanceType: ${INSTANCE_TYPE}
imageFamily: ${IMAGE_FAMILY}
sshKeyNames:
  - ${SSH_KEY_NAME}
userDataFile: ${CLOUD_INIT}
tags:
  cluster: ${CLUSTER_NAME}
  role: controller
EOF

INSTANCE_ID="$(./lambdactl launch --confirm --config "${LAUNCH_CONFIG}" | head -n 1 | tr -d '\r\n')"
if [[ -z "${INSTANCE_ID}" ]]; then
  echo "failed to get instance id from lambdactl output" >&2
  exit 1
fi

echo "launched instance: ${INSTANCE_ID}"

if [[ -z "${PUBLIC_ENDPOINT:-}" ]]; then
  echo "waiting for instance to become active and have a public IP..."
  for _ in $(seq 1 120); do
    OUT="$(./lambdactl get-instance --id "${INSTANCE_ID}" 2>/dev/null || true)"
    STATUS="$(echo "${OUT}" | awk -F' ' '{for(i=1;i<=NF;i++){if($i ~ /^status=/){sub(/^status=/,"",$i);print $i}}}')"
    IP="$(echo "${OUT}" | awk -F' ' '{for(i=1;i<=NF;i++){if($i ~ /^ip=/){sub(/^ip=/,"",$i);print $i}}}')"
    if [[ "${STATUS}" == "active" && -n "${IP}" ]]; then
      PUBLIC_ENDPOINT="${IP}"
      break
    fi
    sleep 5
  done
fi

if [[ -z "${PUBLIC_ENDPOINT:-}" ]]; then
  echo "failed to resolve PUBLIC_ENDPOINT from instance ${INSTANCE_ID}" >&2
  exit 1
fi

SSH_OPTS=(-o StrictHostKeyChecking=accept-new -o UserKnownHostsFile="${WORKDIR}/known_hosts" -o CheckHostIP=no)
if [[ -n "${SSH_KEY_PATH}" ]]; then
  SSH_OPTS+=(-i "${SSH_KEY_PATH}")
fi

echo "waiting for ssh on ${PUBLIC_ENDPOINT}..."
for _ in $(seq 1 180); do
  if ssh "${SSH_OPTS[@]}" "${SSH_USER}@${PUBLIC_ENDPOINT}" "true" >/dev/null 2>&1; then
    break
  fi
  sleep 5
done

echo "waiting for /etc/rancher/rke2/rke2.yaml to exist..."
for _ in $(seq 1 180); do
  if ssh "${SSH_OPTS[@]}" "${SSH_USER}@${PUBLIC_ENDPOINT}" "test -f /etc/rancher/rke2/rke2.yaml" >/dev/null 2>&1; then
    break
  fi
  sleep 5
done

KUBECONFIG_OUT="${KUBECONFIG_OUT:-${ROOT_DIR}/rke2.yaml}"
KUBECONFIG_PATH="${WORKDIR}/rke2.yaml"
if ! scp "${SSH_OPTS[@]}" "${SSH_USER}@${PUBLIC_ENDPOINT}:/etc/rancher/rke2/rke2.yaml" "${KUBECONFIG_PATH}" 2>/dev/null; then
  if ! ssh "${SSH_OPTS[@]}" "${SSH_USER}@${PUBLIC_ENDPOINT}" "sudo -n cat /etc/rancher/rke2/rke2.yaml" > "${KUBECONFIG_PATH}"; then
    echo "failed to download kubeconfig; ensure ${SSH_USER} can sudo without password" >&2
    exit 1
  fi
fi

sed -i.bak "s|server: https://127.0.0.1:6443|server: https://${PUBLIC_ENDPOINT}:6443|g" "${KUBECONFIG_PATH}"

echo "waiting for Kubernetes API to be ready..."
for _ in $(seq 1 120); do
  if kubectl --kubeconfig "${KUBECONFIG_PATH}" get nodes >/dev/null 2>&1; then
    break
  fi
  sleep 5
done

echo "kubeconfig written to: ${KUBECONFIG_PATH}"
cp "${KUBECONFIG_PATH}" "${KUBECONFIG_OUT}"
echo "kubeconfig written to: ${KUBECONFIG_OUT}"
echo "export KUBECONFIG=${KUBECONFIG_OUT}"

if [[ -z "${RKE2_SERVER_ADDR}" ]]; then
  RKE2_SERVER_ADDR="$(ssh "${SSH_OPTS[@]}" "${SSH_USER}@${PUBLIC_ENDPOINT}" "hostname -I | awk '{print \$1}'" 2>/dev/null || true)"
fi

if [[ -n "${RKE2_SERVER_ADDR}" ]]; then
  python3 - <<PY
import pathlib, re
src = pathlib.Path("${ROOT_DIR}") / "lambdanodeclass.yaml"
dst = pathlib.Path("${NODECLASS_OUT}")
text = src.read_text()
text = re.sub(r"^(\\s*server:\\s*).*$", r"\\1https://${RKE2_SERVER_ADDR}:9345", text, flags=re.MULTILINE)
text = re.sub(r"^(\\s*token:\\s*).*$", r"\\1${RKE2_TOKEN}", text, flags=re.MULTILINE)
dst.write_text(text)
print(f"wrote nodeclass: {dst}")
PY
else
  echo "warning: could not determine RKE2 server address; nodeclass not generated" >&2
fi
