# Magma Operator

Kubebuilder operator for managing Magma upstream Orc8r/NMS and AGW deployments with operator-bundled chart assets.

The operator reconciles:

- `MagmaOrc8r`: deploys Orc8r, NMS/MagmaLTE, PostgreSQL, bootstrap/admin jobs, and generated secrets through `magma-fullstack-upstream`.
- `MagmaAGW`: deploys containerized AGW, idempotent node preparation, Multus/UERANSIM simulator resources, and AGW node anti-affinity through `magma-agw-upstream`.

The manager image includes pinned Helm v3 and the Magma chart assets under `/opt/magma-operator/charts`, so normal reconciliation does not clone or fetch charts from an external repository. `spec.chartRepository` remains available only as an explicit override for development or controlled migration work.

## Image

Default image:

```bash
ghcr.io/infinitydon/magma-operator:v0.1.25
```

No default container image uses the `latest` tag.

Build without Docker:

```bash
make build
make docker-build CONTAINER_TOOL=buildah IMG=ghcr.io/infinitydon/magma-operator:v0.1.25
buildah push ghcr.io/infinitydon/magma-operator:v0.1.25
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

The operator manages AGW bootstrap identity and NMS gateway registration before
it deploys the AGW workloads. `spec.identity.secretName` is the Kubernetes
Secret that stores `gw_challenge.key`, its base64 form, the derived public key,
the public-key hash, and the hardware ID. If the Secret does not exist, the
operator creates it. To preserve an existing AGW identity, set
`identity.importSecretName`/`identity.importSecretKey` or pre-create the
`identity.secretName` Secret. `rotationPolicy: Never` is the production default.

With `gatewayRegistration.enabled=true`, the operator waits for Orc8r/NMS,
creates the default tier if needed, and registers the gateway through Magma's
LTE gateway API so both the generic Magmad record and the cellular gateway
record exist. This is required for subscriber sync and 5G UE registration.

```yaml
networkID: mpk_test
orc8rNamespace: magma
orc8rReleaseName: magma-fullstack
nmsAPIHost: magma-fullstack-nginx-proxy
nmsAdminCertSecretName: orc8r-secrets-certs
identity:
  secretName: agw-bootstrap-challenge-key
  hardwareID: 38001016-6de3-4d3d-927c-5d448a674bfe
  rotationPolicy: Never
gatewayRegistration:
  enabled: true
  deleteOnRemoval: false
  name: agw-node-02
  description: Kubernetes managed AGW
  tier: default
deletionPolicy:
  deletePVC: false
  deleteIdentitySecret: false
```

The AGW controller also owns the runtime glue that should not be maintained by
hand:

- discovers the live Orc8r nginx service ClusterIP and injects it into
  `orc8r.hostAliases.ip`;
- copies the live Orc8r `rootCA.pem` into the AGW cert Secret and existing AGW
  PVC when certificates are regenerated;
- annotates AGW Deployments with the root CA hash so pods roll after trust
  bundle changes;
- deletes stale unschedulable `NodeAffinity` pods left behind by older
  ReplicaSets after selector changes;
- adds a finalizer that uninstalls the AGW Helm release and removes
  operator-applied datapath-ready labels before the `MagmaAGW` object is
  removed;
- optionally removes the Orc8r/NMS gateway record, AGW PVC, and managed
  identity Secret when the CR explicitly opts into those destructive actions.

The controller preserves the managed identity Secret and AGW PVC on CR deletion
by default. That keeps gateway identity recoverable after accidental CR removal
or a controlled redeploy. Set `gatewayRegistration.deleteOnRemoval=true`,
`deletionPolicy.deletePVC=true`, or `deletionPolicy.deleteIdentitySecret=true`
only when the intended lifecycle is a full teardown.

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
ueransimValidation:
  enabled: true
  trigger: initial-validation
  pingHost: 4.2.2.2
  iperfServer: 192.168.88.230
  iperfPort: 5201
  timeoutSeconds: 240
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

UERANSIM is intentionally treated as an end-user simulator, not as part of AGW
steady-state health. It can enter idle/session states that do not prove the AGW
is broken. The operator therefore does not block `Ready=True` on UERANSIM pod
or tunnel state. If `ueransimValidation.enabled=true`, it creates a one-shot
validation Job named `<release>-ueransim-validation`. Each new
`ueransimValidation.trigger` value first recreates the UERANSIM gNB and UE
Deployments, waits for them to become ready, then the Job waits for the UE pod's
`uesimtun0`, runs ping, and optionally runs iperf3. The result is reported in
the separate `UERANSIMValidated` condition.

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

The legacy `values.config.gwChallenge` escape hatch is still honored as an
import source, but the typed `identity` block should be preferred. The operator
re-injects the managed challenge key into Helm values on every reconcile so a
fresh `agwc-claim` PVC keeps the same gateway identity and can bootstrap against
the existing NMS gateway record.

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

The operator writes a top-level `Ready` condition and detailed lifecycle
conditions into status. Helm errors are reflected as `Ready=False` with the
Helm output in the condition message. AGW conditions include:

- `IdentityReady`
- `DatapathReady`
- `TrustBundleSynced`
- `GatewayRegistered`
- `UERANSIMValidated` when one-shot validation is enabled

For AGW, status also includes:

- `identitySecretName`
- `hardwareID`
- `challengePublicKeyHash`
- `gatewayRegistered`
- `orc8rServiceIP`
- `trustBundleHash`
