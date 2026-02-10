# lambda-karpenter Helm Chart

This chart installs the Lambda Cloud Karpenter provider and the required CRDs:

- `LambdaNodeClass` (provider-specific)
- `NodeClaim` and `NodePool` (Karpenter core)

It does not install the AWS Karpenter controller.

## Install

```bash
kubectl create namespace karpenter
kubectl -n karpenter create secret generic lambda-api --from-literal=token=<your-token>

helm upgrade --install lambda-karpenter ./charts/lambda-karpenter \
  --namespace karpenter \
  --set config.clusterName=<cluster-name> \
  --set config.apiTokenSecret.name=lambda-api \
  --set config.apiTokenSecret.key=token
```
