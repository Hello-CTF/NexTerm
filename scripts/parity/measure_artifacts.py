#!/usr/bin/env python3
from __future__ import annotations

import argparse
import hashlib
import json
import pathlib
import platform
import subprocess
from typing import Any

ROOT = pathlib.Path(__file__).resolve().parents[2]


def command_output(command: list[str]) -> str:
    try:
        result = subprocess.run(command, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=30)
        return result.stdout.strip()
    except (OSError, subprocess.TimeoutExpired) as error:
        return f"unavailable: {error}"


def measure(label: str, path: pathlib.Path) -> dict[str, Any]:
    data = path.read_bytes()
    result: dict[str, Any] = {
        "label": label,
        "path": str(path),
        "size_bytes": len(data),
        "size_mib": round(len(data) / 1024 / 1024, 3),
        "sha256": hashlib.sha256(data).hexdigest(),
        "file": command_output(["file", "-b", str(path)]),
    }
    if platform.system() == "Darwin" and result["file"].startswith("Mach-O"):
        result["linked_libraries"] = command_output(["otool", "-L", str(path)]).splitlines()
    elif platform.system() == "Linux" and result["file"].startswith("ELF"):
        result["linked_libraries"] = command_output(["ldd", str(path)]).splitlines()
    return result


def main() -> int:
    parser = argparse.ArgumentParser(description="Measure existing Rust/Go artifacts without rebuilding them")
    parser.add_argument("artifacts", nargs="+", help="LABEL=PATH")
    parser.add_argument("--output", type=pathlib.Path, required=True)
    args = parser.parse_args()
    artifacts = []
    for item in args.artifacts:
        label, separator, raw_path = item.partition("=")
        if not separator:
            parser.error(f"expected LABEL=PATH: {item}")
        path = pathlib.Path(raw_path).resolve()
        if not path.is_file():
            raise FileNotFoundError(path)
        artifacts.append(measure(label, path))
    report = {
        "schema_version": 1,
        "host": {
            "system": platform.system(),
            "release": platform.release(),
            "machine": platform.machine(),
        },
        "artifacts": artifacts,
    }
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    for artifact in artifacts:
        print(f"{artifact['label']}: {artifact['size_bytes']} bytes sha256={artifact['sha256']}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
