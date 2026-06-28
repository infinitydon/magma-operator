import os
import pathlib
import subprocess
import tempfile
from typing import Any

import yaml


class HelmError(RuntimeError):
    pass


def run(cmd: list[str], timeout: int = 600) -> str:
    proc = subprocess.run(cmd, text=True, capture_output=True, timeout=timeout)
    if proc.returncode != 0:
        raise HelmError(proc.stderr.strip() or proc.stdout.strip())
    return proc.stdout.strip()


def chart_checkout(repo: str, version: str, workdir: str) -> pathlib.Path:
    safe_name = repo.rstrip("/").split("/")[-1].removesuffix(".git")
    path = pathlib.Path(workdir) / safe_name
    if path.exists():
        run(["git", "-C", str(path), "fetch", "--all", "--tags"], timeout=300)
    else:
        run(["git", "clone", repo, str(path)], timeout=600)
    run(["git", "-C", str(path), "checkout", version], timeout=300)
    return path


def helm_upgrade(
    *,
    release: str,
    namespace: str,
    chart_dir: pathlib.Path,
    values: dict[str, Any],
    timeout_seconds: int,
) -> str:
    with tempfile.NamedTemporaryFile("w", suffix=".yaml", delete=False) as handle:
        yaml.safe_dump(values, handle)
        values_file = handle.name
    try:
        return run(
            [
                "helm",
                "upgrade",
                "--install",
                release,
                str(chart_dir),
                "-n",
                namespace,
                "--create-namespace",
                "--values",
                values_file,
                "--wait",
                "--timeout",
                f"{timeout_seconds}s",
            ],
            timeout=timeout_seconds + 300,
        )
    finally:
        try:
            os.unlink(values_file)
        except FileNotFoundError:
            pass


def helm_uninstall(release: str, namespace: str) -> None:
    run(["helm", "uninstall", release, "-n", namespace, "--ignore-not-found"], timeout=300)
