#!/usr/bin/env python3
from __future__ import annotations

import argparse
import importlib.util
import json
import pathlib
from typing import Any

ROOT = pathlib.Path(__file__).resolve().parents[2]
BASELINE = ROOT / "testdata/parity/baseline/baseline.json"


def load_measure():
    path = ROOT / "scripts/parity/measure_artifacts.py"
    spec = importlib.util.spec_from_file_location("parity_measure", path)
    assert spec and spec.loader
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def compare_sizes(rust_bytes: int, go_bytes: int) -> dict[str, Any]:
    delta = go_bytes - rust_bytes
    return {
        "rust_bytes": rust_bytes,
        "go_bytes": go_bytes,
        "delta_bytes": delta,
        "go_to_rust_ratio": round(go_bytes / rust_bytes, 6) if rust_bytes else None,
        "smaller": go_bytes < rust_bytes,
        "status": "passed" if go_bytes < rust_bytes else "failed",
        "rule": "Go artifact must be strictly smaller than the corresponding Rust artifact",
    }


def gate(rust: dict[str, Any], go_path: pathlib.Path) -> dict[str, Any]:
    result: dict[str, Any] = {
        "rust_label": rust["label"],
        "rust_path": rust["path"],
        "rust_sha256": rust["sha256"],
        "go_path": str(go_path),
    }
    if not go_path.is_file():
        result.update({"status": "not-produced", "smaller": False, "reason": "Go artifact does not exist"})
        return result
    measure = load_measure()
    go = measure.measure("go", go_path)
    result.update(compare_sizes(rust["size_bytes"], go["size_bytes"]))
    result["go_sha256"] = go["sha256"]
    result["go_file"] = go["file"]
    return result


def main() -> int:
    parser = argparse.ArgumentParser(description="Enforce strict Go-smaller-than-Rust artifact size gates")
    parser.add_argument("--baseline", type=pathlib.Path, default=BASELINE)
    parser.add_argument("--gate", action="append", required=True, help="RUST_LABEL=GO_PATH; repeat for each artifact")
    parser.add_argument("--output", type=pathlib.Path, required=True)
    parser.add_argument("--require", action="store_true", help="exit nonzero when any gate is not strictly smaller")
    args = parser.parse_args()
    baseline = json.loads(args.baseline.read_text(encoding="utf-8"))
    rust_by_label = {artifact["label"]: artifact for artifact in baseline["artifacts"]}
    results = []
    for item in args.gate:
        label, separator, raw_path = item.partition("=")
        if not separator or label not in rust_by_label:
            parser.error(f"unknown or invalid Rust artifact label: {item}")
        results.append(gate(rust_by_label[label], pathlib.Path(raw_path).resolve()))
    report = {
        "schema_version": 1,
        "status": "passed" if all(result["status"] == "passed" for result in results) else "failed",
        "feature_parity_claim": False,
        "gates": results,
    }
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    for result in results:
        print(f"{result['rust_label']}: {result['status']}")
    return 1 if args.require and report["status"] != "passed" else 0


if __name__ == "__main__":
    raise SystemExit(main())
