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
NODECLASS_FILE="${NODECLASS_FILE:-${ROOT_DIR}/lambdanodeclass.yaml}"
NODEPOOL_FILE="${NODEPOOL_FILE:-${ROOT_DIR}/nodepool.yaml}"

if [[ -z "${NODECLASS_FILE_OVERRIDE:-}" ]]; then
  if [[ -f "${ROOT_DIR}/lambdanodeclass.generated.yaml" ]]; then
    NODECLASS_FILE="${ROOT_DIR}/lambdanodeclass.generated.yaml"
  fi
else
  NODECLASS_FILE="${NODECLASS_FILE_OVERRIDE}"
fi

if [[ ! -f "${GPU_VALUES}" ]]; then
  echo "gpu-operator values not found: ${GPU_VALUES}" >&2
  exit 1
fi
if [[ "${NODECLASS_FILE}" == "${ROOT_DIR}/lambdanodeclass.yaml" ]]; then
  if rg -q "token: tust1337" "${NODECLASS_FILE}"; then
    echo "warning: using default lambdanodeclass.yaml with placeholder token; consider NODECLASS_FILE_OVERRIDE=./lambdanodeclass.generated.yaml" >&2
  fi
fi

if [[ ! -f "${NODECLASS_FILE}" ]]; then
  echo "nodeclass file not found: ${NODECLASS_FILE}" >&2
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
  --version v25.10.1 \
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
  --set image.tag="${IMAGE_TAG:-0.1.9}"

kubectl apply -f "${NODECLASS_FILE}"
kubectl apply -f "${NODEPOOL_FILE}"
