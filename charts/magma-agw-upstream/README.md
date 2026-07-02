# Magma AGW Upstream

This chart starts from the upstream Magma `v1.9.0` experimental
`lte/gateway/deploy/agwc-helm-charts` chart and keeps it as a clean Kubernetes
baseline before adding any eBPF datapath work.

It intentionally remains close to upstream:

- one Deployment per AGW component
- one replica per AGW component
- `Recreate` deployment strategy
- `hostNetwork: true`
- shared `/etc/magma`, `/var/opt/magma`, `/tmp`, and `/var/run` assumptions
- privileged datapath-related components such as `oai-mme`, `pipelined`, `sctpd`,
  and `sessiond`

Changes from upstream:

- chart name/version are repo-local: `magma-agw-upstream` `0.1.0`
- image tags default to `1.9.0-upstream`, not `latest`
- Helm rendering fails if any chart image uses `latest`
- adds missing Docker Compose services `envoy_controller` and `liagentd`
- adds `nodeSelector` and `tolerations`
- adds an idempotent optional `agw-node-prep` DaemonSet for AGW host prerequisites
- enables Magma 5G SA mconfig defaults
- adds an optional UERANSIM 5G gNB/UE simulator with Multus NADs and an
  idempotent subscriber/APN provisioning Job
- makes `pipelined` wait for sessiond before startup so native UPF node
  association is not lost to Kubernetes startup races
- overrides the upstream generated 5G AMF `T3512` timer from 10 minutes to
  54 minutes by default for stable UERANSIM datapath validation
- adds a basic Helm test hook

`liagentd` is present but disabled by default because the packaged upstream
`liagentd.yml` sets `enable: false`; deploying it in that state causes the
process to exit cleanly and Kubernetes to report a restart loop.

## AGW scale model

Magma AGW is containerized in the upstream `v1.9.0` release, but the upstream
AGWC Helm chart still models an Access Gateway as a single gateway instance, not
as a horizontally scaled replica set.

In this chart, every AGW service inherits the upstream deployment template with:

- `replicas: 1`
- `strategy.type: Recreate`
- `hostNetwork: true`
- required pod anti-affinity against AGW pods from other Helm releases
- shared host/PVC state such as `/etc/snowflake`, `/var/opt/magma`, OVS, and
  `gtp_br0`

Do not run multiple replicas of the same AGW instance on one node. To deploy
multiple gateways in one Kubernetes cluster, use separate gateway identities,
node selectors, interface/IP assignments, secrets/certificates, and persistent
state. The supported operational model for this chart is:

```text
one AGW gateway identity == one AGW instance == one selected Kubernetes node
```

Nodes that are allowed to host AGW must be labelled:

```sh
kubectl label node ebpf-bng-node-02 magma.io/agw-node=true --overwrite
```

The default `nodeSelector` requires both the participation label and the tested
node hostname:

```yaml
nodeSelector:
  magma.io/agw-node: "true"
  kubernetes.io/hostname: ebpf-bng-node-02
```

For multiple AGWs, install one Helm release per gateway and set each release's
`nodeSelector.kubernetes.io/hostname` to a different labelled AGW node. The
chart also adds anti-affinity so AGW pods from another Helm release cannot land
on the same node.

Running more than one distinct AGW on the same node is not covered by the
upstream chart and would require explicit isolation of host networking, OVS
bridges, interface names, IPs, gateway IDs, certificates, host paths, and
persistent volumes. Treat that as custom engineering rather than a supported
replica mode.

## Images

Default image values:

```yaml
image:
  repository: ghcr.io/infinitydon/telco-helm-charts
  tag: "1.9.0-upstream"
  gatewayGoRepository: ghcr.io/infinitydon/telco-helm-charts
  gatewayGoTag: "1.9.0-upstream"
  test: alpine:3.20.3
```

## Reference Dockerfiles

The chart does not build images at install time. For traceability, the tested
UERANSIM simulator Dockerfile is kept at
`docs/Dockerfile.ueransim-v3.2.4`. It builds UERANSIM from the upstream
`v3.2.4` tag on Ubuntu 22.04 and includes operational troubleshooting tools
used during validation: `ping`, `tcpdump`, `iperf3`, `iproute2`, and DNS tools.

The validated image tag for this chart is:

```yaml
simulator:
  image:
    repository: ghcr.io/infinitydon/ueransim
    tag: v3.2.4-x86-64
    pullPolicy: Always
```

Do not publish or deploy the simulator with a `latest` tag. If rebuilding the
reference image, push it with an immutable version tag such as
`v3.2.4-x86-64`.

## Node Prep DaemonSet

`nodePrep.enabled` deploys a privileged DaemonSet on the selected AGW node. It is
idempotent: it checks for Open vSwitch, kernel modules, `gtp_br0`,
`uplink_br0`, patch ports, internal ports, interface addresses, and sysctls
before changing host state. It also probes whether OVS can create a `type=gtpu`
port before marking datapath prep successful when `requireMagmaOvsKmod=true`.

For production, review these values and map them to the real AGW N2/N3/uplink
interfaces before installing. The defaults use node-02 for Magma AGW and create
host-side macvlan interfaces for N2/N3. The N2 subnet intentionally avoids
`192.88.99.0/24` because Linux SCTP treats that old 6to4 anycast range poorly.

```yaml
nodeSelector:
  kubernetes.io/hostname: ebpf-bng-node-02

nodePrep:
  enabled: true
  installKernelModulesExtra: true
  nodeSelector:
    magma.io/agw-node: "true"
    kubernetes.io/hostname: ebpf-bng-node-02
  runMagmaOvsKmodUpgrade: true
  requireMagmaOvsKmod: false
  magmaOvsKmodUpgradePath: /usr/local/bin/ovs-kmod-upgrade.sh
  ovsKmod:
    validateGtpu: true
    installCommand: ""
    packageUrl: ""
    packageSha256: ""
    packagePath: /tmp/magma-ovs-kmod.deb
  bridge:
    name: gtp_br0
    address: 192.168.128.1/24
    datapathType: system
  uplinkBridge:
    enabled: true
    name: uplink_br0
    uplinkPort: magma-sgi
  gtpu:
    enabled: false
    cleanupPorts:
      - gtpu0
    ofport: 32768
  interfaces:
    createMissing: true
    cleanupLegacy:
      - eth1
      - eth2
    s1:
      name: magma-n2
      type: macvlan
      parent: enp8s19
      address: 10.88.99.142/24
      replaceAddresses: true
    n3:
      name: magma-n3
      type: macvlan
      parent: enp8s20
      address: 192.168.60.142/24
      replaceAddresses: true
    nat:
      name: magma-sgi
      type: macvlan
      parent: enp8s21
      address: ""
```

Keep N3 and SGi/NAT on separate host interfaces. `pipelined` manages the SGi
address and can replace it with the SGi management IP; using the same interface
for N3 would remove the 5G GTP-U endpoint address.

`pipelined` publishes the 5G UPF node state to sessiond from the configured
`enodeb_iface`/`upf_node_identifier`. The chart starts `pipelined` only after
sessiond's local gRPC port is reachable; otherwise the native association loop
can back off while UERANSIM is already creating a PDU session, causing the AMF
to advertise `0.0.0.0` and `0x7fffffff` as the UPF tunnel endpoint.

`nodePrep.natEgress` installs idempotent host iptables rules for UE subnet
egress. If `nodePrep.natEgress.interface` is empty, node prep detects the
node's default-route interface. In the tested lab, `magma-sgi` was a private
macvlan with no default route, so UE internet reachability required NAT egress
through the node default interface `eth0`.

## AMF Periodic Registration Timer

The upstream Magma 1.9 `mme.conf.template` hardcodes the 5G AMF `T3512` value
to 10 minutes. In this lab that caused UERANSIM to perform periodic
registration after 10 minutes; the periodic update was not completed cleanly,
the UE kept a stale `uesimtun0`, and sessiond removed the active PDU session.

This chart rewrites the generated `mme.conf` in the config Job:

```yaml
config:
  amf:
    t3512Minutes: 54
```

The live AGW check is:

```sh
kubectl -n magma-agw exec deploy/oai-mme -- \
  grep -n 'T3512' /var/opt/magma/tmp/mme.conf
```

Expected:

```text
T3512 = 54
```

## 5G Simulator

`simulator.enabled` deploys UERANSIM. It defaults to `false` so a full
Orc8r/NMS-backed deployment can register and configure the AGW before the UE
attempts registration. When enabled, the tested default uses Multus `macvlan`
for the simulator gNB and schedules it on node-01 so NGAP/SCTP is a real
inter-node association. Magma AGW remains on node-02.

UERANSIM must run on a completely separate worker node from the AGW. Do not
schedule the simulator onto the AGW worker node; the chart defaults enforce this
with a separate simulator node label, hostname selector, and pod anti-affinity
against AGW pods.

UERANSIM is a simulator workload, not an NMS-managed eNodeB/CBSD inventory
object. Its gNB establishes NGAP/SCTP toward the AGW and the UE/session should
be visible through gateway/session state and logs, but it should not be expected
to appear as a persistent "radio node" in the Magma NMS equipment views.

Label the simulator worker:

```sh
kubectl label node ebpf-bng-node-01 magma.io/ueransim-node=true --overwrite
```

```yaml
simulator:
  enabled: true
  image:
    repository: ghcr.io/infinitydon/ueransim
    tag: v3.2.4-x86-64
    pullPolicy: Always
  nodeSelector:
    magma.io/ueransim-node: "true"
    kubernetes.io/hostname: ebpf-bng-node-01
  antiAffinity:
    separateFromAgw: true
  multus:
    type: macvlan
    n2:
      master: enp8s19
      ip: 10.88.99.150/24
    n3:
      master: enp8s20
      ip: 192.168.60.150/24
  subscriber:
    provision: false
  iperf3Server:
    enabled: false
    hostNetwork: true
    port: 5201
```

The simulator image defaults to a Magma 1.9-compatible UERANSIM v3.2.4 build
and does not use a `latest` tag. The binary paths are configurable through
`simulator.gnb.commandPath` and `simulator.ue.commandPath`; the defaults match
the GHCR image above.

Compatibility note: `ghcr.io/infinitydon/ueransim:v3.3.0-x86-64` was tested in
the same lab. The image pulled successfully and both `nr-gnb`/`nr-ue` reported
`v3.3.0`; NG setup, UE registration, PDU session establishment, and
`uesimtun0` creation succeeded. User-plane ICMP through `uesimtun0` failed,
while reverting to `v3.2.4-x86-64` restored ICMP with the same AGW and
subscriber configuration. Keep `v3.2.4-x86-64` as the default until the
v3.3.0 datapath difference is isolated.

`simulator.gnb.startDelaySeconds` defaults to `30` and
`simulator.ue.startDelaySeconds` defaults to `60` so the AGW SCTP listener and
gNB are up before the UE attempts registration. If the AGW node is slow to
initialize OVS or services, increase the gNB and UE delays together.

For the exact requirements that made `ping -I uesimtun0 8.8.8.8` work in the
validated lab, see `docs/ue-ping-validation.md`.

The chart can also run a host-networked `iperf3` server on the simulator worker
for UE throughput validation. See `docs/ue-ping-validation.md` for the tested
command, observed diagnostic success, and the remaining clean-path TCP/5201
policy/enforcement caveat.

For a full Magma deployment, create the subscriber, APN/DNN, and active policy
assignment in NMS/Orc8r. The local subscriber provisioning Job is disabled by
default and is intended only for standalone AGW datapath testing. In cluster
testing, UERANSIM completed NG setup, initial registration, PDU session
establishment, brought up `uesimtun0`, and passed internet ICMP after the
subscriber had `default_rule_1` assigned from Orc8r and node prep installed NAT
egress for `192.168.128.0/24`.

Recommended full-stack order:

1. Deploy Orc8r/NMS and confirm the GUI/API are reachable.
2. Create the network, gateway, APN/DNN, and UE subscriber in NMS/Orc8r.
3. Deploy AGW and confirm it is registered/synced with Orc8r.
4. Enable UERANSIM by setting `simulator.enabled=true`, keeping
   `simulator.subscriber.provision=false`.

If UE registration and PDU establishment succeed but `ping -I uesimtun0` fails,
check these in order:

1. `policydb_cli.py dump_data` in the AGW `policydb` pod must show the UE IMSI
   with an Orc8r-streamed policy, for example `global_policies:
   "default_rule_1"`.
2. `ovs-ofctl -O OpenFlow13 dump-flows gtp_br0` on the AGW node should show
   uplink packets matching the `qfi=9` tunnel flow, then matching the policy
   rule for the UE IP.
3. Host iptables must NAT the UE pool, default `192.168.128.0/24`, out an
   interface with working upstream routing. `nodePrep.natEgress` handles this
   idempotently.

If the gNB log reports `TEID -2147483647 not found on GTP-U Downlink`, the UPF
node association did not reach sessiond before the PDU session was created.
Restart `pipelined`/`sessiond` only after node prep is complete, then reattach
the UE.

## Installation

This chart deploys the Access Gateway only. Magma NMS GUI and Orc8r REST API
access are provided by the Orchestrator side, not by the AGW pods. In this repo,
see the `magma-ebpf` chart for the TOSSI Orc8r/NMS deployer wrapper and its
NodePort access notes.

### Prerequisites

Before installing, confirm the target cluster has:

- Multus installed and the `NetworkAttachmentDefinition` CRD present.
- A Linux node labelled for AGW, default `ebpf-bng-node-02` with
  `magma.io/agw-node=true`.
- A separate Linux node labelled for UERANSIM, default `ebpf-bng-node-01` with
  `magma.io/ueransim-node=true`.
- Usable host interfaces for AGW N2/N3 and simulator N2/N3. The tested defaults
  are `enp8s19` and `enp8s20` on both nodes.
- SCTP and GTP kernel support. `nodePrep` attempts to load `gtp`,
  `openvswitch`, and `nf_conntrack`. When
  `nodePrep.installKernelModulesExtra=true`, a failed module load triggers an
  idempotent install of `linux-modules-extra-$(uname -r)` and retries the
  module load. It still does not build custom kernel modules.
- Open vSwitch. If it is missing and `nodePrep.installOpenvSwitch=true`, the
  node prep DaemonSet installs it with `apt-get`.
- Magma-compatible OVS/GTP kernel support. Node prep probes the live host OVS by
  trying to create a temporary `type=gtpu` port. If the probe fails and
  `nodePrep.runMagmaOvsKmodUpgrade=true`, it can run one of three approved
  installer paths: `nodePrep.ovsKmod.installCommand`,
  `nodePrep.ovsKmod.packageUrl` with optional SHA256 validation, or the existing
  executable at `nodePrep.magmaOvsKmodUpgradePath`. Set
  `nodePrep.requireMagmaOvsKmod=true` to fail fast unless the GTP-U probe passes
  after the installer runs. The default keeps the stack schedulable for control
  plane testing, but real UE datapath validation should use
  `requireMagmaOvsKmod=true`.

The tested working node was Ubuntu 20.04.6 LTS with kernel
`5.4.0-216-generic`, Magma OVS `2.15.4-10-magma`, and the patched GTP/QFI OVS
datapath loaded. Ubuntu 24.04 with stock OVS did not provide a compatible Magma
GTP-U datapath for this chart.

When this chart is driven by the Magma operator, the operator can set
`nodePrep.nodeSelector` to the raw AGW node label and set the AGW workload
`nodeSelector` to include `magma.io/agw-datapath-ready=true`. This lets node
prep run first, then schedules AGW pods only after the operator marks the node
ready.

Quick checks:

```sh
kubectl get crd network-attachment-definitions.k8s.cni.cncf.io
kubectl get pods -n kube-system -l app=multus -o wide
kubectl get nodes -o wide
kubectl get nodes -l magma.io/agw-node=true
kubectl get nodes -l magma.io/ueransim-node=true
```

On each selected node, verify the interface names before using the defaults:

```sh
kubectl debug node/<node-name> -it --image=ubuntu:22.04 -- chroot /host ip -br addr
```

### Bring-Up

```sh
kubectl create namespace magma
```

For lab use, the chart can create a self-signed cert Secret when
`secret.create=true`. For a real orc8r-backed deployment, disable that and create
the root CA Secret needed to communicate with orc8r:

```sh
kubectl create secret generic agwc-secret-certs \
  --from-file=rootCA.pem=rootCA.pem \
  --from-file=gateway.crt=gateway.crt \
  --from-file=gateway.key=gateway.key \
  --from-file=controller.crt=controller.crt \
  --from-file=controller.key=controller.key \
  --namespace magma
```

Deploy after reviewing `values.yaml`:

```sh
helm upgrade --install agwc ./magma-agw-upstream \
  --namespace magma \
  --values values.yaml \
  --wait \
  --timeout 20m
```

For the tested lab defaults:

```sh
helm upgrade --install agwc ./magma-agw-upstream \
  --namespace magma-agw-test \
  --set namespace=magma-agw-test \
  --set simulator.enabled=true \
  --wait \
  --timeout 20m
```

### Verification

Check the release and pods:

```sh
helm status agwc -n magma-agw-test
kubectl get pods -n magma-agw-test -o wide
kubectl logs -n magma-agw-test deploy/agwc-ueransim-gnb --tail=100
kubectl logs -n magma-agw-test deploy/agwc-ueransim-ue --tail=120
```

Expected successful simulator messages:

```text
NG Setup procedure is successful
Initial Registration is successful
PDU Session establishment is successful PSI[1]
TUN interface[uesimtun0, 192.168.128.x] is up.
```

Check AGW N2 SCTP listener:

```sh
kubectl exec -n magma-agw-test deploy/oai-mme -- \
  sh -c 'cat /proc/net/sctp/eps | grep 38412'
```

The tested default binds `10.88.99.142:38412`. Avoid `192.88.99.0/24` for N2;
it caused SCTP client failures even though ICMP worked.

## Provisioning

For the full deployment path, provisioning should be done through NMS/Orc8r:

- create the LTE/5G network
- register the gateway
- configure APNs/DNNs
- add subscribers/SIM credentials
- keep UERANSIM SIM values aligned with the NMS subscriber

When `simulator.subscriber.provision=true`, the chart creates a revision-scoped
Job named like `agwc-subscriber-provision-<revision>`. This is a standalone lab
shortcut and should stay disabled when Orc8r/NMS is managing the gateway.

The Job is idempotent:

- waits for local `subscriberdb`
- checks whether the subscriber exists
- adds the subscriber if missing
- updates the APN/DNN profile on every run

Standalone lab subscriber override:

```yaml
simulator:
  ue:
    supi: imsi-001010000000001
    key: 465B5CE8B199B49FAA5F0A2EE238A6BC
    opc: E8ED289DEBA952E4283B54E88E6183CA
    apn: magma.ipv4
  subscriber:
    provision: true
    nextSeq: "000000000000"
    apnConfig: "magma.ipv4,9,1,0,0,300000000,300000000,0,,,,"
```

Manual verification:

```sh
kubectl logs -n magma-agw-test job/agwc-subscriber-provision-<revision>
kubectl exec -n magma-agw-test deploy/subscriberdb -- \
  /usr/local/bin/subscriber_cli.py get IMSI001010000000001
```

To use your own UE/SIM values, update `simulator.ue.*` and
`simulator.subscriber.apnConfig` together. If using an external orchestrator for
subscribers, set `simulator.subscriber.provision=false`.

## Certificates

The chart supports two certificate modes.

Lab mode:

```yaml
secret:
  create: true
  certs: agwc-secret-certs
```

Helm generates a self-signed CA plus gateway/controller certificates and stores
them in `agwc-secret-certs`. This is suitable for local chart testing and for
the standalone AGW workflow in this repo. Because Helm `genCA`/`genSignedCert`
generates new material on render, do not treat lab certificates as stable
identity for production.

Production/orc8r mode:

```yaml
secret:
  create: false
  certs: agwc-secret-certs
config:
  # Base64-encoded PEM contents of /var/opt/magma/certs/gw_challenge.key.
  # Keep this paired with the public challenge key stored on the NMS gateway.
  gwChallenge: LS0tLS1CRUdJTi...
```

Create `agwc-secret-certs` yourself with the controller trust chain:

- `rootCA.pem`

For a bootstrap-based AGW, `gateway.crt` and `gateway.key` are issued into the
AGW PVC by Orc8r after `magmad` proves possession of
`/var/opt/magma/certs/gw_challenge.key`. If you delete the PVC and reuse the
same NMS gateway object, set `config.gwChallenge` to the base64-encoded PEM
private challenge key that matches the public key registered in NMS. Otherwise
`magmad` will generate a new private key and bootstrap will fail with
`signed challenge doesn't match`.

For a fully pre-provisioned certificate workflow, include the static gateway
certificate material in `agwc-secret-certs` as well:

- `gateway.crt`
- `gateway.key`

Update or rotate certificates when:

- certificates are expired or near expiry
- the gateway is re-enrolled or its identity changes
- the orc8r/controller CA changes
- a private key is suspected to be exposed
- moving from lab mode to real orc8r mode

Rotate `config.gwChallenge` together with the NMS gateway device public key.
Treat it as secret material even though it is carried as a Helm value.

After updating the Secret, rerun Helm so the revision-scoped config Job copies
the files into the AGW PVC, then restart the affected control-plane pods:

```sh
helm upgrade --install agwc ./magma-agw-upstream \
  --namespace magma \
  --values values.yaml \
  --wait \
  --timeout 20m

kubectl rollout restart -n magma \
  deployment/control-proxy deployment/oai-mme deployment/sctpd
```

If switching from `secret.create=true` to externally managed certificates,
delete and recreate the Secret before the Helm upgrade:

```sh
kubectl delete secret agwc-secret-certs -n magma
kubectl create secret generic agwc-secret-certs \
  --from-file=rootCA.pem=rootCA.pem \
  --from-file=gateway.crt=gateway.crt \
  --from-file=gateway.key=gateway.key \
  --from-file=controller.crt=controller.crt \
  --from-file=controller.key=controller.key \
  --namespace magma
```

Delete:

```sh
helm uninstall agwc --namespace magma
kubectl delete -n magma secret agwc-secret-certs
kubectl delete namespace magma
```
