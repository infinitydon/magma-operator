# Magma Operator

Kubebuilder operator for managing the Magma upstream Orc8r/NMS and AGW Helm charts from `infinitydon/telco-helm-charts`.

The operator reconciles:

- `MagmaOrc8r`: deploys Orc8r, NMS/MagmaLTE, PostgreSQL, bootstrap/admin jobs, and generated secrets through `magma-fullstack-upstream`.
- `MagmaAGW`: deploys containerized AGW, idempotent node preparation, Multus/UERANSIM simulator resources, and AGW node anti-affinity through `magma-agw-upstream`.

The current controller intentionally shells out to pinned Helm v3 inside the manager image. The manager image includes `helm`, `git`, and a writable `/tmp` cache for chart clones.

## Image

Default image:

```bash
ghcr.io/infinitydon/magma-operator:v0.1.5
```

No default container image uses the `latest` tag.

Build without Docker:

```bash
make build
make docker-build CONTAINER_TOOL=buildah IMG=ghcr.io/infinitydon/magma-operator:v0.1.5
buildah push ghcr.io/infinitydon/magma-operator:v0.1.5
```

## Install

If the GHCR package is private, create an image pull secret before or immediately after installing the manifests:

```bash
kubectl -n magma-operator-system create secret docker-registry ghcr-pull-secret \
  --docker-server=ghcr.io \
  --docker-username=<github-user> \
  --docker-password=<github-token>

kubectl -n magma-operator-system patch serviceaccount magma-operator-controller-manager \
  --type=merge \
  --patch '{"imagePullSecrets":[{"name":"ghcr-pull-secret"}]}'
```

```bash
kubectl apply -k config/default
```

The default manager deployment runs in `magma-operator-system`.

## Node Labels

Label only nodes that may host AGW workloads:

```bash
kubectl label node ebpf-bng-node-02 magma.io/agw-node=true --overwrite
```

Label a separate worker node for UERANSIM:

```bash
kubectl label node ebpf-bng-node-01 magma.io/ueransim-node=true --overwrite
```

Do not schedule UERANSIM on the same worker node as AGW. The AGW chart also enables pod anti-affinity so multiple AGW releases are pushed to separate nodes when capacity is available.

## Orc8r/NMS

Create the namespace and Orc8r CR:

```bash
kubectl create ns magma --dry-run=client -o yaml | kubectl apply -f -
kubectl apply -f config/samples/magma_v1alpha1_magmaorc8r.yaml
```

The sample exposes MagmaLTE/NMS through NodePort `31316`:

```bash
http://<node-ip>:31316/user/login
```

Default sample login:

```text
admin / admin
```

For production, override `spec.nmsAdminPassword` and manage certificate rotation deliberately. The Helm chart reuses existing generated secrets by default to avoid accidental cert replacement during upgrades.

## AGW And 5G Simulator

Create the namespace and AGW CR:

```bash
kubectl create ns magma-agw --dry-run=client -o yaml | kubectl apply -f -
kubectl apply -f config/samples/magma_v1alpha1_magmaagw.yaml
```

The sample uses node-02 for AGW and the tested node-02 interfaces:

```yaml
s1Interface: enp8s19
sgiInterface: enp8s20
agwNodeSelector:
  magma.io/agw-node: "true"
  kubernetes.io/hostname: ebpf-bng-node-02
```

UERANSIM is enabled in the sample and selected by:

```yaml
ueransimNodeSelector:
  magma.io/ueransim-node: "true"
```

The AGW chart defaults to Multus `macvlan` for UERANSIM. If a node/NIC combination cannot support macvlan, override the chart values through `spec.values`, for example:

```yaml
values:
  simulator.multus.type: host-device
  simulator.multus.n2.master: enp8s21
  simulator.multus.n3.master: enp8s22
```

## Status

Check reconciliation:

```bash
kubectl get magmaorc8r -n magma -o wide
kubectl get magmaagw -n magma-agw -o wide
kubectl describe magmaorc8r -n magma magma-orc8r
kubectl describe magmaagw -n magma-agw agwc
```

The operator writes a `Ready` condition and release details into status. Helm errors are reflected as `Ready=False` with the Helm output in the condition message.
