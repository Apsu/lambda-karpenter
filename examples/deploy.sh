#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

require() {
  if [[ -z "${!1:-}" ]]; then
    echo "missing required env var: $1" >&2
    exit 1
  fi
}

require KUBECONFIG
require LAMBDA_API_TOKEN
require CLUSTER_NAME

GPU_VALUES="${GPU_VALUES:-${ROOT_DIR}/examples/gpu-operator-values.yaml}"
GPU_OPERATOR_VERSION="${GPU_OPERATOR_VERSION:-v25.10.1}"
NODECLASS_FILE="${NODECLASS_FILE:-${ROOT_DIR}/lambdanodeclass.generated.yaml}"
NODEPOOL_FILE="${NODEPOOL_FILE:-${ROOT_DIR}/nodepool.yaml}"
IMAGE_TAG="${IMAGE_TAG:-0.1.9}"

if [[ -n "${NODECLASS_FILE_OVERRIDE:-}" ]]; then
  NODECLASS_FILE="${NODECLASS_FILE_OVERRIDE}"
fi

if [[ ! -f "${GPU_VALUES}" ]]; then
  echo "gpu-operator values not found: ${GPU_VALUES}" >&2
  exit 1
fi
if [[ ! -f "${NODECLASS_FILE}" ]]; then
  echo "nodeclass file not found: ${NODECLASS_FILE}" >&2
  echo "hint: generate it with examples/bootstrap-controller.sh or create it manually" >&2
  exit 1
fi
if [[ ! -f "${NODEPOOL_FILE}" ]]; then
  echo "nodepool file not found: ${NODEPOOL_FILE}" >&2
  exit 1
fi

helm repo add nvidia https://helm.ngc.nvidia.com/nvidia || true
helm repo update

helm upgrade --install gpu-operator nvidia/gpu-operator \
  --namespace gpu-operator --create-namespace \
  --version "${GPU_OPERATOR_VERSION}" \
  -f "${GPU_VALUES}"

kubectl create namespace karpenter --dry-run=client -o yaml | kubectl apply -f -
kubectl -n karpenter create secret generic lambda-api \
  --from-literal=token="${LAMBDA_API_TOKEN}" \
  --dry-run=client -o yaml | kubectl apply -f -

helm upgrade --install lambda-karpenter "${ROOT_DIR}/charts/lambda-karpenter" \
  --namespace karpenter --create-namespace \
  --set config.clusterName="${CLUSTER_NAME}" \
  --set config.apiTokenSecret.name=lambda-api \
  --set config.apiTokenSecret.key=token \
  --set image.tag="${IMAGE_TAG}"

kubectl apply -f "${NODECLASS_FILE}"
kubectl apply -f "${NODEPOOL_FILE}"
