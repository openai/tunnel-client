from __future__ import annotations

import hashlib
import io
import os
import pathlib
import subprocess
import sys
import tarfile
import tempfile
import unittest

SCRIPT = pathlib.Path(__file__).with_name("extract_sbom_vendor.py")


def vendor_digest(files: dict[str, bytes]) -> str:
    digest = hashlib.sha256()
    for relative_path, contents in sorted(files.items()):
        digest.update(b"F\0")
        digest.update(relative_path.encode("utf-8"))
        digest.update(b"\0")
        digest.update(hashlib.sha256(contents).digest())
    return digest.hexdigest()


class ExtractSbomVendorTest(unittest.TestCase):
    def make_source_root(self, root: pathlib.Path, expected_digest: str) -> pathlib.Path:
        source_root = root / "client"
        (source_root / "compliance").mkdir(parents=True)
        (source_root / "compliance" / "sbom-vendor-tree.sha256").write_text(
            f"{expected_digest}\n",
            encoding="utf-8",
        )
        return source_root

    def add_file(self, archive: tarfile.TarFile, name: str, contents: bytes) -> None:
        member = tarfile.TarInfo(name)
        member.size = len(contents)
        member.mode = 0o644
        archive.addfile(member, io.BytesIO(contents))

    def make_archive(
        self,
        root: pathlib.Path,
        files: dict[str, bytes],
        extra_members: list[tuple[tarfile.TarInfo, bytes | None]] | None = None,
    ) -> pathlib.Path:
        archive_path = root / "vendor.tar.gz"
        with tarfile.open(archive_path, "w:gz") as archive:
            for relative_path, contents in sorted(files.items()):
                self.add_file(archive, f"vendor/{relative_path}", contents)
            for member, contents in extra_members or []:
                archive.addfile(member, io.BytesIO(contents) if contents is not None else None)
        return archive_path

    def run_script(
        self,
        root: pathlib.Path,
        source_root: pathlib.Path,
        archive: pathlib.Path,
    ) -> subprocess.CompletedProcess[str]:
        environment = os.environ.copy()
        environment.update({"BAZEL_TEST": "1", "TEST_TMPDIR": str(root)})
        return subprocess.run(
            [
                sys.executable,
                str(SCRIPT),
                "--source-root",
                str(source_root),
                "--vendor-archive",
                str(archive),
            ],
            check=False,
            capture_output=True,
            text=True,
            env=environment,
        )

    def test_extracts_regular_archive_and_verifies_digest(self) -> None:
        files = {
            "modules.txt": b"# vendor\n",
            "example.com/module/example.go": b"package module\n",
        }
        with tempfile.TemporaryDirectory() as temporary_directory:
            root = pathlib.Path(temporary_directory)
            source_root = self.make_source_root(root, vendor_digest(files))
            archive = self.make_archive(root, files)

            result = self.run_script(root, source_root, archive)

            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(
                (source_root / "vendor" / "example.com" / "module" / "example.go").read_bytes(),
                files["example.com/module/example.go"],
            )
            self.assertIn("verified SBOM vendor tree", result.stderr)

    def test_rejects_links_and_non_regular_members_before_writing(self) -> None:
        files = {"modules.txt": b"# vendor\n"}
        cases: list[tuple[str, tarfile.TarInfo, str]] = []

        symlink = tarfile.TarInfo("vendor/link")
        symlink.type = tarfile.SYMTYPE
        symlink.linkname = "../../outside"
        cases.append(("symlink", symlink, "must not contain links"))

        hardlink = tarfile.TarInfo("vendor/hardlink")
        hardlink.type = tarfile.LNKTYPE
        hardlink.linkname = "../../outside"
        cases.append(("hardlink", hardlink, "must not contain links"))

        fifo = tarfile.TarInfo("vendor/fifo")
        fifo.type = tarfile.FIFOTYPE
        cases.append(("fifo", fifo, "unsupported entry"))

        with tempfile.TemporaryDirectory() as temporary_directory:
            root = pathlib.Path(temporary_directory)
            for case_name, unsafe_member, expected_error in cases:
                with self.subTest(case=case_name):
                    case_root = root / case_name
                    case_root.mkdir()
                    source_root = self.make_source_root(case_root, "0" * 64)
                    archive = self.make_archive(
                        case_root,
                        files,
                        extra_members=[(unsafe_member, None)],
                    )

                    result = self.run_script(case_root, source_root, archive)

                    self.assertNotEqual(result.returncode, 0)
                    self.assertIn(expected_error, result.stderr)
                    self.assertFalse((source_root / "vendor").exists())

    def test_rejects_unsafe_paths_and_duplicate_members_before_writing(self) -> None:
        files = {"modules.txt": b"# vendor\n"}
        with tempfile.TemporaryDirectory() as temporary_directory:
            root = pathlib.Path(temporary_directory)
            for case_name, member_name, expected_error in (
                ("parent", "vendor/../escape", "unsafe path"),
                ("absolute", "/vendor/escape", "unsafe path"),
                ("dot", "vendor/./escape", "unsafe path"),
                ("duplicate", "vendor/modules.txt", "duplicate path"),
            ):
                with self.subTest(case=case_name):
                    case_root = root / case_name
                    case_root.mkdir()
                    source_root = self.make_source_root(case_root, "0" * 64)
                    member = tarfile.TarInfo(member_name)
                    member.size = len(b"escape\n")
                    archive = self.make_archive(
                        case_root,
                        files,
                        extra_members=[(member, b"escape\n")],
                    )

                    result = self.run_script(case_root, source_root, archive)

                    self.assertNotEqual(result.returncode, 0)
                    self.assertIn(expected_error, result.stderr)
                    self.assertFalse((source_root / "vendor").exists())

    def test_rejects_file_parent_conflict_before_writing(self) -> None:
        files = {"modules.txt": b"# vendor\n"}
        with tempfile.TemporaryDirectory() as temporary_directory:
            root = pathlib.Path(temporary_directory)
            source_root = self.make_source_root(root, "0" * 64)
            parent = tarfile.TarInfo("vendor/parent")
            parent.size = len(b"parent\n")
            child = tarfile.TarInfo("vendor/parent/child")
            child.size = len(b"child\n")
            archive = self.make_archive(
                root,
                files,
                extra_members=[
                    (parent, b"parent\n"),
                    (child, b"child\n"),
                ],
            )

            result = self.run_script(root, source_root, archive)

            self.assertNotEqual(result.returncode, 0)
            self.assertIn("file/directory conflict", result.stderr)
            self.assertFalse((source_root / "vendor").exists())

    def test_rejects_source_root_escape_and_symlink_parent_under_bazel(self) -> None:
        files = {"modules.txt": b"# vendor\n"}
        with tempfile.TemporaryDirectory() as temporary_directory:
            root = pathlib.Path(temporary_directory)
            test_tmpdir = root / "test-tmpdir"
            outside = root / "outside"
            test_tmpdir.mkdir()
            outside.mkdir()
            source_root = self.make_source_root(outside, vendor_digest(files))
            archive = self.make_archive(outside, files)

            escaped_source_root = test_tmpdir / ".." / "outside" / "client"
            escaped = self.run_script(test_tmpdir, escaped_source_root, archive)

            self.assertNotEqual(escaped.returncode, 0)
            self.assertIn("TEST_TMPDIR", escaped.stderr)
            self.assertFalse((source_root / "vendor").exists())

            symlink_parent = test_tmpdir / "linked-outside"
            symlink_parent.symlink_to(outside, target_is_directory=True)
            symlinked_source_root = symlink_parent / "client"
            symlinked = self.run_script(test_tmpdir, symlinked_source_root, archive)

            self.assertNotEqual(symlinked.returncode, 0)
            self.assertIn("TEST_TMPDIR", symlinked.stderr)
            self.assertFalse((source_root / "vendor").exists())


if __name__ == "__main__":
    unittest.main()
