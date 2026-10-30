#!/usr/bin/env python3
"""Tests that Android license scope follows the actual binary/package graph."""

import importlib.util
import json
import pathlib
import sys
import unittest

sys.dont_write_bytecode = True
spec = importlib.util.spec_from_file_location(
    "build_android_license_report", pathlib.Path(__file__).with_name("build_android_license_report.py")
)
assert spec is not None and spec.loader is not None
helper = importlib.util.module_from_spec(spec)
spec.loader.exec_module(helper)
HEADER, MAIN_MODULE, render_report = helper.HEADER, helper.MAIN_MODULE, helper.render_report


MODULE = "example.com/dependency"
VERSION = "v1.2.3"


def build_info() -> str:
    return (
        "runtime: go1.27.3\n"
        f"\tmod\t{MAIN_MODULE}\t(devel)\n"
        f"\tdep\t{MODULE}\t{VERSION}\th1:fixture\n"
        "\tbuild\tGOOS=android\n\tbuild\tGOARCH=arm64\n\tbuild\tCGO_ENABLED=1\n"
    )


def package(import_path: str, **module_changes: object) -> dict:
    return {"ImportPath": import_path, "Module": {"Path": MODULE, "Version": VERSION, **module_changes}}


def graph(*packages: dict) -> str:
    return "\n".join(json.dumps(item) for item in [
        {"ImportPath": "net", "Standard": True},
        {"ImportPath": MAIN_MODULE + "/cmd/client-runtime", "Module": {"Path": MAIN_MODULE, "Main": True}},
        *packages,
    ])


def catalog() -> str:
    return HEADER + "\n" + "\n".join([
        f"{MODULE}|direct|{VERSION}|MIT|{MODULE}@{VERSION}/LICENSE|linux/arm64",
        f"{MODULE}/special|transitive|{VERSION}|BSD-3-Clause|{MODULE}@{VERSION}/special/LICENSE|linux/arm64",
        "example.com/windows|transitive|v1.0.0|MIT|example.com/windows@v1.0.0/LICENSE|windows/amd64",
    ]) + "\n"


class AndroidLicenseReportTest(unittest.TestCase):
    def test_actual_packages_select_nested_license_and_exclude_other_platform_dependency(self):
        result = render_report(build_info(), graph(package(MODULE + "/api"), package(MODULE + "/special/codec")), catalog())
        self.assertIn(f"{MODULE}|direct|{VERSION}|MIT|", result)
        self.assertIn(f"{MODULE}/special|transitive|{VERSION}|BSD-3-Clause|", result)
        self.assertNotIn("example.com/windows", result)
        self.assertNotIn("linux/arm64", result)
        rows = result.split(HEADER + "\n", 1)[1].splitlines()
        self.assertEqual(len(rows), 2)
        self.assertTrue(all(row.endswith("|android/arm64") for row in rows))
        self.assertEqual(result, render_report(build_info(), graph(package(MODULE + "/special/codec"), package(MODULE + "/api")), catalog()))

    def test_unused_general_record_is_not_included(self):
        result = render_report(build_info(), graph(package(MODULE + "/special")), catalog())
        self.assertNotIn(f"{MODULE}|direct|", result)
        self.assertIn(f"{MODULE}/special|", result)

    def test_wrong_binary_target_or_cgo_rejected(self):
        for setting, wrong in [("GOOS=android", "GOOS=linux"), ("GOARCH=arm64", "GOARCH=amd64"), ("CGO_ENABLED=1", "CGO_ENABLED=0")]:
            with self.subTest(setting=setting), self.assertRaisesRegex(ValueError, "binary must have"):
                render_report(build_info().replace(setting, wrong), graph(package(MODULE)), catalog())

    def test_binary_and_graph_version_mismatch_rejected(self):
        with self.assertRaisesRegex(ValueError, "binary module versions differ"):
            render_report(build_info(), graph(package(MODULE, Version="v1.2.4")), catalog())

    def test_stale_specific_catalog_does_not_fall_back_to_general_license(self):
        stale = catalog().replace(f"special|transitive|{VERSION}|", "special|transitive|v1.0.0|")
        with self.assertRaisesRegex(ValueError, "license record does not match"):
            render_report(build_info(), graph(package(MODULE + "/special")), stale)

    def test_uncovered_package_rejected(self):
        no_general_record = "\n".join(line for line in catalog().splitlines() if not line.startswith(MODULE + "|"))
        with self.assertRaisesRegex(ValueError, "no reviewed license record"):
            render_report(build_info(), graph(package(MODULE + "/uncovered")), no_general_record)

    def test_catalog_license_file_must_belong_to_exact_module_version(self):
        wrong = catalog().replace(f"{MODULE}@{VERSION}/LICENSE", "example.com/other@v1.2.3/LICENSE")
        with self.assertRaisesRegex(ValueError, "license record does not match"):
            render_report(build_info(), graph(package(MODULE)), wrong)

    def test_replaced_modules_rejected_in_both_evidence_sources(self):
        with self.assertRaisesRegex(ValueError, "replaced modules"):
            render_report(build_info() + "\t=>\t../local\t(devel)\n", graph(package(MODULE)), catalog())
        with self.assertRaisesRegex(ValueError, "replaced modules"):
            render_report(build_info(), graph(package(MODULE, Replace={"Path": "../local"})), catalog())

    def test_binary_module_missing_from_graph_rejected(self):
        extra = build_info() + "\tdep\texample.com/missing\tv1.0.0\th1:fixture\n"
        with self.assertRaisesRegex(ValueError, "does not cover binary modules"):
            render_report(extra, graph(package(MODULE)), catalog())

    def test_malformed_or_ambiguous_catalog_rejected(self):
        for invalid in [catalog() + "bad|row\n", catalog() + catalog().splitlines()[1] + "\n"]:
            with self.subTest(catalog=invalid), self.assertRaises(ValueError):
                render_report(build_info(), graph(package(MODULE)), invalid)

    def test_incomplete_and_test_graphs_rejected(self):
        for extra in [{"Incomplete": True}, {"Error": {"Err": "unavailable"}}, {"ForTest": "tests"}]:
            with self.subTest(extra=extra), self.assertRaises(ValueError):
                render_report(build_info(), graph({**package(MODULE), **extra}), catalog())


if __name__ == "__main__":
    unittest.main()
