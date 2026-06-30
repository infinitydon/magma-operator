# Magma Operator

Kubebuilder operator for managing the Magma upstream Orc8r/NMS and AGW Helm charts from `infinitydon/telco-helm-charts`.

The operator reconciles:

- `MagmaOrc8r`: deploys Orc8r, NMS/MagmaLTE, PostgreSQL, bootstrap/admin jobs, and generated secrets through `magma-fullstack-upstream`.
- `MagmaAGW`: deploys containerized AGW, idempotent node preparation, Multus/UERANSIM simulator resources, and AGW node anti-affinity through `magma-agw-upstream`.

The current controller intentionally shells out to pinned Helm v3 inside the manager image. The manager image includes `helm`, `git`, and a writable `/tmp` cache for chart clones.

## Image

Default image:

```bash
ghcr.io/infinitydon/magma-operator:v0.1.9
```

No default container image uses the `latest` tag.

Build without Docker:

```bash
make build
make docker-build CONTAINER_TOOL=buildah IMG=ghcr.io/infinitydon/magma-operator:v0.1.9
buildah push ghcr.io/infinitydon/magma-operator:v0.1.9
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

The sample also provisions the `magma-test` organization for NodePort access,
creates LTE/5G network `mpk_test`, and creates subscriber
`IMSI001010000000001` for the UERANSIM UE. Set `spec.nmsCustomDomains` to every
host:port that users may use to open NMS; MagmaLTE uses this list to map the
login request to the correct organization.

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
datapath:
  enabled: true
  requireMagmaOvsKmod: true
```

With `datapath.enabled=true`, the operator performs a two-step AGW reconcile:
it first deploys the chart with `agw-node-prep` scheduled on the raw
`agwNodeSelector`, while AGW workloads are gated on
`magma.io/agw-datapath-ready=true`. Once the node-prep DaemonSet is ready, the
operator labels the selected node and reconciles the Helm release again with
normal readiness waiting. Set `datapath.requireMagmaOvsKmod=true` to make
node-prep fail fast when `/usr/local/bin/ovs-kmod-upgrade.sh` is missing.

UERANSIM is enabled in the sample and selected by:

```yaml
enableUERANSIM: true
ueransimStartPolicy: AfterAGWReady
ueransimReadyConfigMap: agwc-ueransim-ready
ueransimNodeSelector:
  magma.io/ueransim-node: "true"
```

With the default `AfterAGWReady` policy, the operator installs the AGW with
UERANSIM disabled first. Start UERANSIM only after the AGW is registered and
healthy in Orc8r/NMS and the UE subscriber has been created in NMS:

```bash
kubectl -n magma-agw create configmap agwc-ueransim-ready \
  --from-literal=ready=true \
  --dry-run=client -o yaml | kubectl apply -f -
```

This ConfigMap is an idempotent gate. Reapplying it is safe, and removing or
setting `ready=false` before UERANSIM starts keeps the simulator disabled on
the next reconciliation. Use `ueransimStartPolicy: Immediate` only for isolated
simulator testing.

The AGW chart defaults to Multus `macvlan` for UERANSIM. If a node/NIC combination cannot support macvlan, override the chart values through `spec.values`, for example:

```yaml
values:
  simulator.multus.type: host-device
  simulator.multus.n2.master: enp8s21
  simulator.multus.n3.master: enp8s22
```

Subscriber provisioning belongs in Orc8r/NMS for the full-stack deployment. The
AGW-local `subscriberdb` seeding job is not set in the sample and should remain
off when the AGW is managed by NMS. It is only a standalone AGW lab shortcut:

```yaml
ueransimStartPolicy: Immediate
values:
  simulator.subscriber.provision: "true"
```

### Validated Simulator Image

The current validated UERANSIM simulator image for the Magma 1.9 AGW chart is:

```text
ghcr.io/infinitydon/ueransim:v3.2.4-x86-64
```

The matching reference Dockerfile is stored in the Helm chart repo at
`magma-agw-upstream/docs/Dockerfile.ueransim-v3.2.4`. It is documentation and
rebuild reference only; the operator does not build simulator images during
reconciliation.

The UE ping requirements and validation procedure are captured in the Helm chart
repo at `magma-agw-upstream/docs/ue-ping-validation.md`.

`ghcr.io/infinitydon/ueransim:v3.3.0-x86-64` was also tested. It pulled and ran,
and UE registration plus PDU session establishment succeeded, but user-plane
ICMP through `uesimtun0` failed. The operator samples should therefore continue
to use `v3.2.4-x86-64` until that v3.3.0 datapath behavior is understood.

## Status

Check reconciliation:

```bash
kubectl get magmaorc8r -n magma -o wide
kubectl get magmaagw -n magma-agw -o wide
kubectl describe magmaorc8r -n magma magma-orc8r
kubectl describe magmaagw -n magma-agw agwc
```

The operator writes a `Ready` condition and release details into status. Helm errors are reflected as `Ready=False` with the Helm output in the condition message.
