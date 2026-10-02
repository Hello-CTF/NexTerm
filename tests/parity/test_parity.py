from __future__ import annotations

import importlib.util
import json
import pathlib
import unittest

ROOT = pathlib.Path(__file__).resolve().parents[2]
DATA = ROOT / "testdata/parity"
DOCS = ROOT / "docs/acceptance-rwig/baseline"


def load(path: pathlib.Path):
    return json.loads(path.read_text(encoding="utf-8"))


def load_module(name: str, path: pathlib.Path):
    spec = importlib.util.spec_from_file_location(name, path)
    assert spec and spec.loader
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class InventoryTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.data = load(DATA / "inventory.json")

    def test_command_accounting(self):
        commands = self.data["commands"]
        rust = commands["rust_registry"]
        frontend = commands["frontend_facade"]
        self.assertEqual(rust["count"], 139)
        self.assertEqual(rust["unique_count"], 139)
        self.assertEqual(frontend["method_count"], 139)
        self.assertEqual(frontend["unique_command_count"], 138)
        self.assertEqual(commands["reconciliation"]["rust_only"], ["credential_save"])
        self.assertEqual(commands["reconciliation"]["frontend_only"], [])
        self.assertEqual(
            commands["reconciliation"]["duplicate_frontend_commands"],
            [{"name": "ai_presets", "accessors": ["aiApi.presets", "modelApi.presets"]}],
        )
        self.assertEqual(commands["reconciliation"]["sync_only_commands"], ["sync_digest", "sync_export", "sync_import"])
        self.assertEqual(frontend["entries"][0]["accessor"], "systemApi.platform")
        self.assertEqual(frontend["entries"][0]["line"], 93)
        self.assertFalse(any(entry["accessor"].startswith("unknown") for entry in frontend["entries"]))

    def test_events_and_streams_are_explicit(self):
        events = self.data["events"]
        self.assertEqual(events["rust_declared_count"], 10)
        self.assertEqual(events["frontend_declared_count"], 9)
        self.assertEqual(events["union_count"], 10)
        self.assertEqual(events["reserved_rust_events"], ["sync://status"])
        streams = self.data["streams"]
        self.assertEqual(
            streams["binary_commands"],
            ["docker_exec_attach", "docker_logs_attach", "session_open_line_tab", "terminal_attach", "terminal_attach_tab"],
        )
        self.assertEqual(streams["ai_json_commands"], ["ai_chat", "ai_takeover_run"])
        self.assertEqual(streams["ai_event_variant_count"], 14)
        self.assertIn("toolResult", streams["ai_event_variants"])
        self.assertIn("planSubmitted", streams["ai_event_variants"])


class BaselineReportTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.data = load(DOCS / "baseline.json")

    def test_all_commands_and_artifacts_are_recorded(self):
        self.assertEqual(self.data["summary"]["commands_failed"], 0)
        self.assertEqual(self.data["summary"]["commands_passed"], 10)
        self.assertFalse(self.data["summary"]["skips_are_passes"])
        self.assertEqual(len(self.data["steps"]), 10)
        for step in self.data["steps"]:
            self.assertEqual(step["status"], "passed")
            self.assertTrue((ROOT / step["log"]).is_file())
        labels = {artifact["label"] for artifact in self.data["artifacts"]}
        self.assertEqual(labels, {"rust-desktop-macos-arm64", "rust-server-macos-arm64", "rust-desktop-dmg-macos-arm64"})
        for artifact in self.data["artifacts"]:
            self.assertGreater(artifact["size_bytes"], 0)
            self.assertEqual(len(artifact["sha256"]), 64)

    def test_skip_accounting_is_complete(self):
        for accounting in self.data["rust_test_accounting"].values():
            early = len(accounting["early_return_skips"])
            self.assertEqual(
                accounting["executed_passed_excluding_early_return_skips"],
                accounting["runner_reported_passed"] - early,
            )
            self.assertEqual(accounting["runner_reported_ignored"], len(accounting["ignored_tests"]))
            self.assertGreater(early, 0)
        self.assertGreaterEqual(self.data["summary"]["coverage_skipped_or_not_run"], 1)
        self.assertEqual(self.data["metrics"]["sync_e2e_assertions_passed"], 34)
        self.assertEqual(self.data["metrics"]["scrollback_retained_bytes"], 33554432)


class SizeGateTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.module = load_module("parity_size_gate", ROOT / "scripts/parity/size_gate.py")

    def test_gate_is_strictly_smaller(self):
        self.assertEqual(self.module.compare_sizes(100, 99)["status"], "passed")
        self.assertEqual(self.module.compare_sizes(100, 100)["status"], "failed")
        self.assertEqual(self.module.compare_sizes(100, 101)["status"], "failed")

    def test_missing_artifact_is_not_a_pass(self):
        rust = {"label": "rust", "path": "rust.bin", "sha256": "0" * 64, "size_bytes": 100}
        result = self.module.gate(rust, ROOT / "target/definitely-not-a-go-artifact")
        self.assertEqual(result["status"], "not-produced")
        self.assertFalse(result["smaller"])


class GoCoverageTest(unittest.TestCase):
    def test_coverage_and_selfcheck_never_claim_pending_features(self):
        coverage = load(DOCS / "go-coverage.json")
        summary = coverage["summary"]
        self.assertEqual(summary["commands_total"], 139)
        self.assertEqual(summary["events_total"], 10)
        self.assertEqual(summary["streams_total"], 7)
        self.assertEqual(len(coverage["commands"]), 139)
        self.assertEqual(
            summary["commands_verified"] + summary["commands_implemented_unverified"] + summary["commands_not_implemented"],
            139,
        )
        self.assertFalse(summary["feature_complete"])
        for feature in coverage["commands"]:
            self.assertIn(feature["status"], {"verified", "implemented-unverified", "not-implemented"})
            if feature["status"] != "verified":
                self.assertTrue(feature["evidence"])
        report = load(DOCS / "go-selfcheck.json")
        self.assertFalse(report["summary"]["skips_are_passes"])
        self.assertFalse(report["summary"]["feature_complete"])
        self.assertFalse(report["summary"]["installer_package_gates_passed"])
        for gate in report["size_gates"]:
            self.assertEqual(gate["smaller"], gate["go_bytes"] < gate["rust_bytes"])


if __name__ == "__main__":
    unittest.main()
