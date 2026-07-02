# UERANSIM Validation Runbook

This operator treats UERANSIM as a simulator owned by the user, not as part of
steady-state AGW health. AGW readiness means the operator has reconciled AGW
resources, datapath preparation, trust bundle sync, and gateway registration.
UERANSIM validation is a separate one-shot check that can be triggered when the
user wants to prove simulator attach and user-plane traffic.

## What The Operator Does

When `spec.enableUERANSIM=true`, the operator can create UERANSIM gNB, UE, and
optional iperf3 server resources. With the recommended
`spec.ueransimStartPolicy=AfterAGWReady`, the operator keeps those simulator
resources disabled until a readiness ConfigMap is set.

When `spec.ueransimValidation.enabled=true`, every new
`spec.ueransimValidation.trigger` value causes the operator to:

1. Recreate the UERANSIM gNB and UE deployments.
2. Wait for those deployments to become ready.
3. Find the running UE pod.
4. Create a one-shot Job named `<releaseName>-ueransim-validation`.
5. Exec into the UE pod and wait for `uesimtun0`.
6. Run ping over `uesimtun0`.
7. Optionally run iperf3 over `uesimtun0`.
8. Write the result to the `UERANSIMValidated` condition.

The validation Job uses `registry.k8s.io/kubectl:v1.36.2` and execs into the UE
pod. The actual iperf3 command inside the UE pod is:

```bash
iperf3 -c "$IPERF_SERVER" -p "$IPERF_PORT" -B "$ue_ip" -t 5
```

`$ue_ip` is discovered from `uesimtun0`.

## Prerequisites

Before triggering validation, confirm:

- Orc8r is ready:

  ```bash
  kubectl -n magma get magmaorc8r magma-orc8r
  ```

- AGW is ready or waiting only for the UERANSIM gate:

  ```bash
  kubectl -n magma-agw get magmaagw agwc
  kubectl -n magma-agw describe magmaagw agwc
  ```

- The UE subscriber exists in Orc8r/NMS. The sample Orc8r CR creates
  `IMSI001010000000001`.

- The UERANSIM node selector matches at least one healthy node:

  ```bash
  kubectl get nodes -l magma.io/ueransim-node=true
  ```

- AGW and UERANSIM are on different nodes.

- If `iperfServer` is set, the server is reachable from the UE user-plane
  network and listens on `iperfPort`. To skip iperf3 and run only ping, leave
  `spec.ueransimValidation.iperfServer` empty.

## Recommended Sample Settings

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

## Start UERANSIM

With `AfterAGWReady`, create the gate ConfigMap after AGW is registered and the
subscriber is provisioned:

```bash
kubectl -n magma-agw create configmap agwc-ueransim-ready \
  --from-literal=ready=true \
  --dry-run=client -o yaml | kubectl apply -f -
```

Watch the simulator pods:

```bash
kubectl -n magma-agw get pods -l app.kubernetes.io/instance=agwc -o wide
kubectl -n magma-agw get deploy agwc-ueransim-gnb agwc-ueransim-ue
```

## Trigger A Validation Run

Validation is triggered by changing `spec.ueransimValidation.trigger` to a new
string. The value is just an idempotency key; it is not a special keyword. Use a
timestamp or descriptive name.

Bash:

```bash
kubectl -n magma-agw patch magmaagw agwc --type=merge \
  -p "{\"spec\":{\"ueransimValidation\":{\"trigger\":\"validation-$(date +%Y%m%d%H%M%S)\"}}}"
```

PowerShell:

```powershell
$trigger = "validation-$(Get-Date -Format yyyyMMddHHmmss)"
kubectl -n magma-agw patch magmaagw agwc --type=merge `
  -p "{`"spec`":{`"ueransimValidation`":{`"trigger`":`"$trigger`"}}}"
```

The operator will recreate the gNB and UE deployments before creating the
validation Job. This is intentional; it gives each validation run a clean
simulator attach attempt.

## Watch The Result

Watch the AGW conditions:

```bash
kubectl -n magma-agw get magmaagw agwc \
  -o custom-columns=READY:.status.conditions[0].status,REASON:.status.conditions[0].reason,MESSAGE:.status.conditions[0].message

kubectl -n magma-agw describe magmaagw agwc
```

Watch the validation Job:

```bash
kubectl -n magma-agw get job agwc-ueransim-validation -o wide
kubectl -n magma-agw get pods -l app.kubernetes.io/component=ueransim-validation -o wide
kubectl -n magma-agw logs job/agwc-ueransim-validation
```

Successful validation sets:

```text
Type:    UERANSIMValidated
Status:  True
Reason:  UERANSIMValidated
Message: one-shot UERANSIM validation passed
```

If validation is still running, the condition is usually `Unknown` with a reason
such as `UERANSIMValidationRunning`, `WaitingForUERANSIMValidationRollout`, or
`WaitingForUERANSIMUEPod`.

If validation fails, the condition is `False` with reason
`UERANSIMValidationFailed`. Inspect the Job logs first.

## Rerun Validation

Rerun validation by changing only the trigger.

Bash:

```bash
kubectl -n magma-agw patch magmaagw agwc --type=merge \
  -p "{\"spec\":{\"ueransimValidation\":{\"trigger\":\"rerun-$(date +%Y%m%d%H%M%S)\"}}}"
```

PowerShell:

```powershell
$trigger = "rerun-$(Get-Date -Format yyyyMMddHHmmss)"
kubectl -n magma-agw patch magmaagw agwc --type=merge `
  -p "{`"spec`":{`"ueransimValidation`":{`"trigger`":`"$trigger`"}}}"
```

The operator detects the changed trigger, deletes/replaces the old validation
Job if necessary, recreates the gNB/UE deployments, and creates a new validation
Job.

## Stop UERANSIM

To keep AGW running but stop simulator resources on the next reconciliation,
set the gate to `false` or delete it:

```bash
kubectl -n magma-agw create configmap agwc-ueransim-ready \
  --from-literal=ready=false \
  --dry-run=client -o yaml | kubectl apply -f -
```

or:

```bash
kubectl -n magma-agw delete configmap agwc-ueransim-ready
```

## Troubleshooting

No validation Job appears:

```bash
kubectl -n magma-agw describe magmaagw agwc
kubectl -n magma-agw get deploy agwc-ueransim-gnb agwc-ueransim-ue
```

The operator waits for the gNB and UE deployments to be ready before creating
the Job.

UE pod has no `uesimtun0`:

```bash
kubectl -n magma-agw logs deploy/agwc-ueransim-ue
kubectl -n magma-agw exec deploy/agwc-ueransim-ue -- ip addr
```

Check that the subscriber IMSI in NMS matches the UE config, AGW is registered,
and the UERANSIM node has the required Multus/NIC setup.

Ping fails:

```bash
kubectl -n magma-agw exec deploy/agwc-ueransim-ue -- ip route
kubectl -n magma-agw logs deploy/agwc-ueransim-gnb
kubectl -n magma-agw logs deploy/oai-mme
kubectl -n magma-agw logs deploy/pipelined
```

Check user-plane routing, NAT, SGi/N3 interfaces, and datapath node-prep.

iperf3 fails:

```bash
kubectl -n magma-agw exec deploy/agwc-ueransim-ue -- sh -c \
  'ip -4 -o addr show uesimtun0; iperf3 -c <server> -p <port> -B <ue-ip> -t 5'
```

Confirm the external iperf3 server is listening, reachable from the UE address,
and not blocked by firewall rules.

Pods stay Pending or ContainerCreating:

```bash
kubectl get pods -A --field-selector=status.phase=Pending -o wide
kubectl -n magma-agw describe pod <pod-name>
kubectl -n kube-system get pods -o wide | grep -E 'calico|multus'
```

If the event mentions Multus or CNI authorization, repair the node CNI first and
then let the operator reconcile again.
