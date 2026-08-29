#!/usr/bin/env python3
"""Assert the Bazel-built tunnel-client image keeps the Docker contract."""

from __future__ import annotations

import json
import stat
import sys
import tarfile
from pathlib import Path
from typing import Any, NoReturn


def fail(message: str) -> NoReturn:
    raise SystemExit(message)


def read_json_member(image_tar: tarfile.TarFile, name: str) -> Any:
    member = image_tar.extractfile(name)
    if member is None:
        fail(f"image tarball is missing {name}")
    return json.load(member)


def main() -> None:
    if len(sys.argv) != 3:
        fail("usage: image_contract_smoke_test.py <unpacked-image-root> <image-tarball>")

    root = Path(sys.argv[1])
    image_tarball = Path(sys.argv[2])
    for binary in ("usr/bin/tunnel-client", "usr/bin/cloudflared"):
        path = root / binary
        if not path.is_file():
            fail(f"image is missing {binary}")
        if not path.stat().st_size:
            fail(f"image payload is empty: {binary}")
        if stat.S_IMODE(path.stat().st_mode) & 0o111 == 0:
            fail(f"image payload is not executable: {binary}")

    with tarfile.open(image_tarball, "r:*") as image_tar:
        manifest = read_json_member(image_tar, "manifest.json")
        if len(manifest) != 1:
            fail(f"expected one image manifest entry, got {len(manifest)}")
        config = read_json_member(image_tar, manifest[0]["Config"])

    image_config = config["config"]
    expected = {
        # `run` must stay in Entrypoint: Docker and Kubernetes replace Cmd
        # when callers supply runtime flags.
        "Entrypoint": ["/usr/bin/tunnel-client", "run"],
        "Cmd": None,
        "ExposedPorts": {"8080/tcp": {}},
        "User": "0",
        "WorkingDir": "/app",
    }
    for field, value in expected.items():
        if image_config.get(field) != value:
            fail(f"image config {field} mismatch: got {image_config.get(field)!r}, want {value!r}")
    if config.get("os") != "linux":
        fail(f"image os mismatch: got {config.get('os')!r}")
    if config.get("architecture") not in {"amd64", "arm64"}:
        fail(f"image architecture mismatch: got {config.get('architecture')!r}")
    if not any(entry.startswith("DD_GIT_COMMIT_SHA=") for entry in image_config.get("Env", [])):
        fail("image config is missing stamped DD_GIT_COMMIT_SHA metadata")


if __name__ == "__main__":
    main()
