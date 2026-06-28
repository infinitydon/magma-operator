from typing import Any

from kubernetes import client, config


def load_config() -> None:
    try:
        config.load_incluster_config()
    except config.ConfigException:
        config.load_kube_config()


def service_node_port(namespace: str, name: str, port_name: str | None = None) -> int | None:
    svc = client.CoreV1Api().read_namespaced_service(name, namespace)
    for port in svc.spec.ports or []:
        if port_name is None or port.name == port_name:
            return port.node_port
    return None


def first_node_internal_ip() -> str | None:
    nodes = client.CoreV1Api().list_node().items
    for node in nodes:
        for address in node.status.addresses or []:
            if address.type == "InternalIP":
                return address.address
    return None


def pods_ready(namespace: str, labels: dict[str, str]) -> bool:
    selector = ",".join(f"{key}={value}" for key, value in labels.items())
    pods = client.CoreV1Api().list_namespaced_pod(namespace, label_selector=selector).items
    if not pods:
        return False
    for pod in pods:
        if pod.status.phase not in ("Running", "Succeeded"):
            return False
        conditions = pod.status.conditions or []
        ready = any(c.type == "Ready" and c.status == "True" for c in conditions)
        if pod.status.phase == "Running" and not ready:
            return False
    return True


def merge_dict(base: dict[str, Any], override: dict[str, Any]) -> dict[str, Any]:
    result = dict(base)
    for key, value in override.items():
        if isinstance(value, dict) and isinstance(result.get(key), dict):
            result[key] = merge_dict(result[key], value)
        else:
            result[key] = value
    return result
