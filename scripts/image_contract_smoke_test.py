#!/usr/bin/env python3
"""Assert the Bazel-built tunnel-client image keeps the Docker contract."""

from __future__ import annotations

import json
import stat
import sys
import tarfile
from contextlib import ExitStack
from pathlib import Path, PurePosixPath
from typing import Any, NoReturn

import zstandard


def fail(message: str) -> NoReturn:
    raise SystemExit(message)


def read_json_member(image_tar: tarfile.TarFile, name: str) -> Any:
    member = image_tar.extractfile(name)
    if member is None:
        fail(f"image tarball is missing {name}")
    return json.load(member)


def check_image_payloads(image_tar: tarfile.TarFile, layers: list[str]) -> None:
    binaries = ("usr/bin/tunnel-client", "usr/bin/cloudflared")
    directories = ("usr", "usr/bin")
    pending = {*binaries, *directories}
    entries: dict[str, tarfile.TarInfo] = {}

    # The generated payload layers are additive. Read from the top until the
    # effective binaries and parent directories are known, skipping the base.
    for layer_name in reversed(layers):
        current = {}
        with ExitStack() as stack:
            blob = image_tar.extractfile(layer_name)
            if blob is None:
                fail(f"image tarball is missing {layer_name}")
            raw = stack.enter_context(blob)
            # The native saver writes both gzip and Zstandard layers.
            magic = raw.read(4)
            raw.seek(0)
            stream = raw
            if magic == b"\x28\xb5\x2f\xfd":
                stream = stack.enter_context(zstandard.ZstdDecompressor().stream_reader(raw))
            layer = stack.enter_context(tarfile.open(fileobj=stream, mode="r|*"))
            for member in layer:
                member_path = PurePosixPath(member.name)
                name = member_path.as_posix().lstrip("/")
                if member_path.name.startswith(".wh."):
                    fail(f"unexpected whiteout in image payload layer: {name}")
                if name in pending:
                    current[name] = member
        entries.update(current)
        pending.difference_update(current)
        if not pending:
            break

    for directory in directories:
        entry = entries.get(directory)
        if entry is None or not entry.isdir():
            fail(f"image payload parent is not a directory: {directory}")
    for binary in binaries:
        entry = entries.get(binary)
        if entry is None or not entry.isfile():
            fail(f"image is missing regular file {binary}")
        if not entry.size:
            fail(f"image payload is empty: {binary}")
        if stat.S_IMODE(entry.mode) & 0o111 == 0:
            fail(f"image payload is not executable: {binary}")


def main() -> None:
    if len(sys.argv) != 2:
        fail("usage: image_contract_smoke_test.py <image-tarball>")

    with tarfile.open(Path(sys.argv[1]), "r:*") as image_tar:
        manifest = read_json_member(image_tar, "manifest.json")
        if len(manifest) != 1:
            fail(f"expected one image manifest entry, got {len(manifest)}")
        check_image_payloads(image_tar, manifest[0]["Layers"])
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
