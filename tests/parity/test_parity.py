from __future__ import annotations

import importlib.util
import json
import pathlib
import tempfile
import unittest

ROOT = pathlib.Path(__file__).resolve().parents[2]
DATA = ROOT / "testdata/parity"
BASELINE = ROOT / "testdata/parity/baseline"


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
        self.assertEqual(frontend["method_count"], 164)
        self.assertEqual(frontend["unique_command_count"], 162)
        self.assertEqual(commands["reconciliation"]["rust_only"], ["credential_save"])
        self.assertEqual(
            commands["reconciliation"]["frontend_only"],
            [
                "ai_answer",
                "ai_circuit_status",
                "ai_edit_resend",
                "ai_hitl_events",
                "ai_hitl_snapshot",
                "ai_run_events",
                "ai_run_list",
                "ai_steer",
                "ai_usage_summary",
                "asset_probe_batch",
                "audit_count",
                "session_probe_host_key",
                "sync_bundle_read",
                "sync_bundle_write",
                "sync_token_issue",
                "sync_token_list",
                "sync_token_revoke",
                "terminal_resize_flush",
                "transcript_delete",
                "transcript_hosts",
                "transcript_list",
                "transcript_read",
                "transcript_search",
                "vault_set_autolock",
            ],
        )
        steer = {entry["name"]: entry for entry in frontend["entries"] if entry["name"] == "ai_steer"}
        self.assertEqual(
            [(steer["ai_steer"]["accessor"], steer["ai_steer"]["line"])],
            [("aiApi.steer", 559)],
        )
        hitl = {entry["name"]: entry for entry in frontend["entries"] if "hitl" in entry["name"]}
        self.assertEqual(
            [(hitl["ai_hitl_snapshot"]["accessor"], hitl["ai_hitl_snapshot"]["line"])],
            [("aiApi.hitlSnapshot", 567)],
        )
        self.assertEqual(
            [(hitl["ai_hitl_events"]["accessor"], hitl["ai_hitl_events"]["line"])],
            [("aiApi.hitlEvents", 569)],
        )
        self.assertEqual(
            commands["reconciliation"]["duplicate_frontend_commands"],
            [
                {"name": "ai_presets", "accessors": ["aiApi.presets", "modelApi.presets"]},
                {"name": "ai_test_provider", "accessors": ["aiApi.testProvider", "modelApi.test"]},
            ],
        )
        self.assertEqual(commands["reconciliation"]["sync_only_commands"], ["sync_digest", "sync_export", "sync_import"])
        self.assertEqual(frontend["entries"][0]["accessor"], "systemApi.platform")
        self.assertEqual(frontend["entries"][0]["line"], 64)
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


class RustTreeIndependenceTest(unittest.TestCase):
    def test_parity_scripts_do_not_reference_the_removed_rust_tree(self):
        for path in sorted((ROOT / "scripts/parity").glob("*.py")):
            text = path.read_text(encoding="utf-8")
            self.assertNotIn("src-tauri", text, path.name)
            self.assertNotIn("Cargo", text, path.name)

    def test_frozen_registry_is_pinned_to_the_baseline_commit(self):
        registry = load(DATA / "rust-registry.json")
        source = load(DATA / "source.json")
        self.assertEqual(registry["source"]["commit"], source["commit"])
        self.assertEqual(registry["source"]["implementation"], "rust")


class HistoricalFreezeTest(unittest.TestCase):
    def test_historical_parity_assets_stay_frozen(self):
        manifest = load(DATA / "manifest.json")
        status = {entry["path"]: entry["status"] for entry in manifest["files"]}
        self.assertEqual(status["testdata/parity/source.json"], "frozen-historical")
        self.assertEqual(status["testdata/parity/rust-registry.json"], "frozen-historical")
        self.assertEqual(status["testdata/parity/inventory.json"], "generated")
        self.assertEqual(status["testdata/parity/go-aliases.json"], "configuration")


class BaselineReportTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.data = load(BASELINE / "baseline.json")

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
        coverage = load(BASELINE / "go-coverage.json")
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
        verified = [feature["id"] for feature in coverage["commands"] if feature["status"] == "verified"]
        self.assertEqual(set(verified), {"app_info", "app_platform"})
        self.assertEqual(summary["commands_verified"], len(verified))
        for feature in coverage["commands"]:
            self.assertIn(feature["status"], {"verified", "implemented-unverified", "not-implemented"})
            if feature["status"] != "verified":
                self.assertTrue(feature["evidence"])
        report = load(BASELINE / "go-selfcheck.json")
        self.assertFalse(report["summary"]["skips_are_passes"])
        self.assertFalse(report["summary"]["feature_complete"])
        self.assertFalse(report["summary"]["installer_package_gates_passed"])
        for gate in report["size_gates"]:
            self.assertEqual(gate["smaller"], gate["go_bytes"] < gate["rust_bytes"])


class GoSelfcheckSurfaceTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.module = load_module("parity_go_selfcheck", ROOT / "scripts/parity/go_selfcheck.py")

    def valid_surface(self):
        return {
            "commands": ["app_info", "app_platform", "sync_digest", "sync_export", "sync_import"],
            "peer_commands": ["sync_digest", "sync_export", "sync_import"],
        }

    def write_surface(self, directory, value):
        path = directory / "surface.json"
        path.write_text(json.dumps(value), encoding="utf-8")
        return path

    def test_load_surface_accepts_valid_probe_output(self):
        with tempfile.TemporaryDirectory() as directory:
            surface = self.module.load_surface(self.write_surface(pathlib.Path(directory), self.valid_surface()))
        self.assertEqual(surface["commands"][:2], ["app_info", "app_platform"])
        self.assertEqual(len(surface["peer_commands"]), 3)

    def test_load_surface_rejects_incomplete_or_inconsistent_output(self):
        cases = [
            {"commands": [], "peer_commands": ["sync_digest"]},
            {"commands": ["app_platform", "app_info", "sync_digest"], "peer_commands": ["sync_digest"]},
            {"commands": ["app_info", "app_info", "app_platform"], "peer_commands": ["app_info"]},
            {"commands": ["app_info", "sync_digest"], "peer_commands": ["sync_digest"]},
            {"commands": ["app_info", "app_platform"], "peer_commands": ["sync_digest"]},
            {"commands": ["app_info", "app_platform"]},
        ]
        with tempfile.TemporaryDirectory() as directory:
            for value in cases:
                with self.assertRaises(ValueError):
                    self.module.load_surface(self.write_surface(pathlib.Path(directory), value))

    def test_expected_commands_tracks_mode(self):
        surface = self.valid_surface()
        self.assertEqual(self.module.expected_commands(surface, sync_only=False), 5)
        self.assertEqual(self.module.expected_commands(surface, sync_only=True), 3)

    def test_check_health_validates_the_live_surface(self):
        surface = self.valid_surface()
        full_health = {"ok": True, "service": "nexterm-server", "syncOnly": False, "commands": 5}
        sync_health = {"ok": True, "service": "nexterm-server", "syncOnly": True, "commands": 3}
        self.assertEqual(self.module.check_health(full_health, surface, sync_only=False), 5)
        self.assertEqual(self.module.check_health(sync_health, surface, sync_only=True), 3)
        with self.assertRaises(AssertionError):
            self.module.check_health({**full_health, "commands": 2}, surface, sync_only=False)
        with self.assertRaises(AssertionError):
            self.module.check_health({**sync_health, "commands": 5}, surface, sync_only=True)
        with self.assertRaises(AssertionError):
            self.module.check_health(full_health, surface, sync_only=True)
        with self.assertRaises(AssertionError):
            self.module.check_health({"ok": False, "service": "nexterm-server", "syncOnly": False, "commands": 5}, surface, sync_only=False)

    def test_live_smoke_skip_reason_names_the_real_dependency(self):
        surface = self.valid_surface()
        self.assertEqual(
            self.module.live_smoke_skip_reason(True, None),
            "production command surface probe failed",
        )
        self.assertEqual(
            self.module.live_smoke_skip_reason(False, surface),
            "Go server binary build failed",
        )
        self.assertEqual(
            self.module.live_smoke_skip_reason(False, None),
            "Go server binary build and production command surface probe failed",
        )
        with self.assertRaises(ValueError):
            self.module.live_smoke_skip_reason(True, surface)

    def test_surface_probe_is_wired_into_the_selfcheck(self):
        probe = ROOT / "tests/parity/go/cmd/surfaceprobe/main.go"
        self.assertTrue(probe.is_file())
        source = probe.read_text(encoding="utf-8")
        self.assertIn("package main", source)
        self.assertIn("PeerDispatcher", source)
        selfcheck = (ROOT / "scripts/parity/go_selfcheck.py").read_text(encoding="utf-8")
        self.assertIn("cmd/surfaceprobe", selfcheck)
        self.assertIn("go-surface-probe", selfcheck)


class SyncOnlyPeerSurfaceTest(unittest.TestCase):
    def test_server_mounts_peer_rpc_only_in_sync_only_with_account_boundaries(self):
        source = (ROOT / "internal/server/server.go").read_text(encoding="utf-8")
        mount = 'mux.Handle("POST /sync/rpc", s.accountGuard(s.requireAccountSession(s.requireAccountCSRF(peerRPC))))'
        self.assertIn(mount, source)
        gate = source.index("if s.options.SyncOnly {")
        mount_at = source.index(mount)
        self.assertGreater(mount_at, gate)
        self.assertLess(mount_at, source.index("return mux", mount_at))
        self.assertIn("s.peerDispatcher.Len()", source)

    def test_server_binary_wires_the_production_peer_dispatcher(self):
        source = (ROOT / "cmd/nexterm-server/main.go").read_text(encoding="utf-8")
        self.assertIn("application.Services.Sync.PeerDispatcher()", source)
        self.assertIn("PeerDispatcher: peerDispatcher", source)

    def test_baseline_records_the_three_command_sync_only_contract(self):
        report = load(BASELINE / "go-selfcheck.json")
        surface = report["runtime"]["command_surface"]
        self.assertEqual(surface["peer_commands_count"], 3)
        self.assertEqual(surface["sync_only_commands"], ["sync_digest", "sync_export", "sync_import"])
        self.assertEqual(surface["health_sync_only_commands"], 3)
        observations = report["runtime"]["sync_only_server"]
        health = next(entry["health"] for entry in observations if "health" in entry)
        self.assertTrue(health["syncOnly"])
        self.assertEqual(health["commands"], 3)
        self.assertIn({"sync_only_rpc_status": 404}, observations)


if __name__ == "__main__":
    unittest.main()
