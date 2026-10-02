#!/usr/bin/env python3
from __future__ import annotations

import argparse
import json
import pathlib
import re
from typing import Any

ROOT = pathlib.Path(__file__).resolve().parents[2]
DEFAULT = ROOT / "docs/acceptance-rwig/baseline/baseline.json"
RESULT = re.compile(r"test result: .*?([0-9]+) passed; [0-9]+ failed; ([0-9]+) ignored")
SKIP = re.compile(r"^(?:\[skip\]|跳过：)(.*)")


def account(log: pathlib.Path) -> dict[str, Any]:
    lines = log.read_text(encoding="utf-8").splitlines()
    passed = 0
    ignored = 0
    early_skips = []
    ignored_tests = []
    pending: tuple[str, int] | None = None
    for number, line in enumerate(lines, 1):
        result = RESULT.search(line)
        if result:
            passed += int(result.group(1))
            ignored += int(result.group(2))
        skip = SKIP.match(line.strip())
        if skip:
            if pending is not None:
                raise RuntimeError(f"unattached skip evidence in {log}:{pending[1]}")
            pending = (skip.group(1).strip(), number)
        test = re.match(r"test (.+) \.\.\. (ok|ignored)(?:, ?(.*))?$", line)
        if not test:
            continue
        name, outcome, note = test.groups()
        if outcome == "ignored":
            ignored_tests.append(
                {
                    "test": name,
                    "kind": "documentation-example" if name.startswith("src-") else "product-test",
                    "note": note or "",
                    "evidence": f"{log.relative_to(ROOT)}:{number}",
                }
            )
        elif pending is not None:
            reason, evidence_line = pending
            early_skips.append(
                {
                    "test": name,
                    "reason": reason,
                    "evidence": f"{log.relative_to(ROOT)}:{evidence_line}",
                    "runner_outcome": "ok (early return; not counted as an executed pass)",
                }
            )
            pending = None
    if pending is not None:
        raise RuntimeError(f"unattached skip evidence in {log}:{pending[1]}")
    if ignored != len(ignored_tests):
        raise RuntimeError(
            f"ignored-test accounting incomplete in {log}: runner={ignored}, identified={len(ignored_tests)}"
        )
    return {
        "runner_reported_passed": passed,
        "runner_reported_ignored": ignored,
        "early_return_skips": early_skips,
        "ignored_tests": ignored_tests,
        "executed_passed_excluding_early_return_skips": passed - len(early_skips),
        "accounting": f"{passed} runner-reported passes - {len(early_skips)} early returns = {passed - len(early_skips)} executed passes; {ignored} ignored entries remain unexecuted",
    }


def main() -> int:
    parser = argparse.ArgumentParser(description="Separate Rust runner-reported passes from early-return and ignored coverage")
    parser.add_argument("--baseline", type=pathlib.Path, default=DEFAULT)
    args = parser.parse_args()
    report = json.loads(args.baseline.read_text(encoding="utf-8"))
    accounting = {}
    for step in report.get("steps", []):
        if step["id"] not in {"rust-test-desktop", "rust-test-server"}:
            continue
        if step["status"] != "passed":
            continue
        accounting[step["id"]] = account(ROOT / step["log"])
    report["rust_test_accounting"] = accounting
    known_skips = {entry["id"] for entry in report.get("coverage_skips", [])}
    for entry in report.get("coverage_skips", []):
        if entry["id"].startswith("macOS Keychain roundtrip tests"):
            entry["id"] = "macOS Keychain roundtrip tests (4)"
            entry["tests"] = [
                "vault::keychain::tests::keychain_roundtrip",
                "vault::tests::mac_init_dpapi_works_on_first_run",
                "vault::tests::mac_load_survives_missing_keychain_item",
                "vault::tests::mac_load_unlocks_via_keychain",
            ]
    if "sync::client::tests::live_server_token_is_enough" not in known_skips:
        report.setdefault("coverage_skips", []).append(
            {
                "id": "sync::client::tests::live_server_token_is_enough",
                "category": "external-service",
                "status": "ignored-by-attribute",
                "reason": "requires a real server and token; it was not replaced with a mock or counted as passed",
            }
        )
    if "summary" in report:
        report["summary"]["coverage_skipped_or_not_run"] = len(report.get("coverage_skips", []))
    args.baseline.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    for step_id, value in accounting.items():
        print(f"{step_id}: {value['accounting']}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
