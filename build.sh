#!/usr/bin/env bash
set -euo pipefail

REPO="${IMAGE_REPO:-apsu/lambda-karpenter}"
TAG="${IMAGE_TAG:-$(grep '^  tag:' charts/lambda-karpenter/values.yaml | awk '{print $2}' | tr -d '"')}"
IMAGE="${REPO}:${TAG}"
PLATFORMS="${PLATFORMS:-linux/amd64,linux/arm64}"

echo "Building ${IMAGE} for ${PLATFORMS}"

docker buildx build \
  --platform "${PLATFORMS}" \
  --tag "${IMAGE}" \
  --push \
  .

echo "Pushed ${IMAGE}"
