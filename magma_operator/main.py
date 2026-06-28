import os
from typing import Any

import kopf

from magma_operator.helm import chart_checkout, helm_uninstall, helm_upgrade
from magma_operator.kube import first_node_internal_ip, load_config, merge_dict, pods_ready, service_node_port


GROUP = "magma.infra.don"
VERSION = "v1alpha1"
WORKDIR = os.environ.get("MAGMA_OPERATOR_WORKDIR", "/tmp/magma-operator")


@kopf.on.startup()
def startup(**_: Any) -> None:
    load_config()
    os.makedirs(WORKDIR, exist_ok=True)


def chart_spec(spec: dict[str, Any]) -> dict[str, Any]:
    chart = spec.get("chart") or {}
    return {
        "repo": chart["repo"],
        "path": chart["path"],
        "version": chart.get("version", "main"),
        "releaseName": chart.get("releaseName"),
        "values": chart.get("values") or {},
    }


@kopf.on.create(GROUP, VERSION, "magmaorc8rs")
@kopf.on.update(GROUP, VERSION, "magmaorc8rs")
def reconcile_orc8r(
    spec: dict[str, Any],
    namespace: str,
    patch: Any,
    body: dict[str, Any],
    **_: Any,
) -> dict[str, Any]:
    chart = chart_spec(spec)
    release = chart["releaseName"] or "magma-fullstack"
    timeout = int(spec.get("waitTimeoutSeconds", 3600))
    root = chart_checkout(chart["repo"], chart["version"], WORKDIR)
    values = merge_dict(
        {
            "global": {"domainName": spec.get("domainName", "magma.local")},
            "orc8r": {
                "nms": {
                    "magmalte": {"service": {"type": (spec.get("nms") or {}).get("serviceType", "NodePort")}},
                    "nginx": {"create": False},
                }
            },
        },
        chart["values"],
    )
    helm_upgrade(
        release=release,
        namespace=namespace,
        chart_dir=root / chart["path"],
        values=values,
        timeout_seconds=timeout,
    )
    node_ip = first_node_internal_ip()
    node_port = service_node_port(namespace, "magmalte")
    nms_url = f"http://{node_ip}:{node_port}/user/login" if node_ip and node_port else ""
    ready = pods_ready(namespace, {"app.kubernetes.io/instance": release})
    status = {
        "ready": str(ready).lower(),
        "releaseName": release,
        "nmsUrl": nms_url,
        "observedGeneration": body["metadata"].get("generation"),
    }
    patch.status.update(status)
    return status


@kopf.on.delete(GROUP, VERSION, "magmaorc8rs")
def delete_orc8r(spec: dict[str, Any], namespace: str, **_: Any) -> None:
    chart = chart_spec(spec)
    helm_uninstall(chart["releaseName"] or "magma-fullstack", namespace)


@kopf.on.create(GROUP, VERSION, "magmaagws")
@kopf.on.update(GROUP, VERSION, "magmaagws")
def reconcile_agw(
    spec: dict[str, Any],
    namespace: str,
    patch: Any,
    body: dict[str, Any],
    **_: Any,
) -> dict[str, Any]:
    chart = chart_spec(spec)
    release = chart["releaseName"] or "agwc"
    timeout = int(spec.get("waitTimeoutSeconds", 1800))
    root = chart_checkout(chart["repo"], chart["version"], WORKDIR)
    node_selector = spec.get("agwNodeSelector") or {}
    values = merge_dict(
        {
            "namespace": namespace,
            "scheduling": {
                "nodeSelector": node_selector,
                "requireDedicatedNode": bool(spec.get("requireDedicatedNode", True)),
            },
            "simulator": {
                "enabled": bool((spec.get("ueSimulator") or {}).get("enabled", False)),
                "subscriber": {"provision": False},
            },
        },
        chart["values"],
    )
    helm_upgrade(
        release=release,
        namespace=namespace,
        chart_dir=root / chart["path"],
        values=values,
        timeout_seconds=timeout,
    )
    ready = pods_ready(namespace, {"app.kubernetes.io/instance": release})
    orc8r_ref = spec.get("orc8rRef") or {}
    status = {
        "ready": str(ready).lower(),
        "releaseName": release,
        "orc8rRef": f"{orc8r_ref.get('namespace', namespace)}/{orc8r_ref.get('name', '')}",
        "observedGeneration": body["metadata"].get("generation"),
    }
    patch.status.update(status)
    return status


@kopf.on.delete(GROUP, VERSION, "magmaagws")
def delete_agw(spec: dict[str, Any], namespace: str, **_: Any) -> None:
    chart = chart_spec(spec)
    helm_uninstall(chart["releaseName"] or "agwc", namespace)
