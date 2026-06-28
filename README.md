# Magma Operator

Kubernetes operator for managing Magma Orc8r/NMS and Access Gateway (AGW)
lifecycles.

This repo is intentionally separate from `telco-helm-charts`. The Helm charts
remain the packaging source for static Kubernetes resources; this operator owns
the lifecycle logic that was brittle in plain Helm:

- Orc8r/NMS install, upgrade, status, and URL discovery.
- AGW install, scheduling, node preparation, and status.
- Certificate reuse and admin operator registration.
- NMS-first provisioning flow.
- Idempotent reconciliation after node or pod restarts.

## Custom Resources

### `MagmaOrc8r`

Installs and monitors the upstream Magma Orc8r/NMS stack.

```yaml
apiVersion: magma.infra.don/v1alpha1
kind: MagmaOrc8r
metadata:
  name: core
  namespace: magma-system
spec:
  chart:
    repo: https://github.com/infinitydon/telco-helm-charts.git
    path: magma-fullstack-upstream
    version: main
    releaseName: magma-fullstack
  domainName: magma.local
  nms:
    serviceType: NodePort
    admin:
      organization: magma-test
      username: admin
      passwordSecretRef:
        name: magma-nms-admin
        key: password
  waitTimeoutSeconds: 3600
```

### `MagmaAGW`

Installs and monitors a Magma AGW instance and links it to an Orc8r stack.

```yaml
apiVersion: magma.infra.don/v1alpha1
kind: MagmaAGW
metadata:
  name: agw-01
  namespace: magma-agw
spec:
  chart:
    repo: https://github.com/infinitydon/telco-helm-charts.git
    path: magma-agw-upstream
    version: main
    releaseName: agwc
  orc8rRef:
    name: core
    namespace: magma-system
  agwNodeSelector:
    magma.infra.don/agw: "true"
  requireDedicatedNode: true
  ueSimulator:
    enabled: false
```

## Development

The operator is implemented with Python, Kopf, and the Kubernetes Python
client. It shells out to Helm for chart operations because the existing Helm
charts remain the source of deployable manifests.

```bash
python -m venv .venv
. .venv/bin/activate
pip install -r requirements.txt
kopf run -m magma_operator.main --verbose
```

## Install

```bash
kubectl apply -f config/crd
kubectl apply -f config/rbac.yaml
kubectl apply -f config/deployment.yaml
```

The deployment image is a placeholder until CI builds and publishes it.

