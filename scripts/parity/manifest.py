#!/usr/bin/env python3
from __future__ import annotations

import argparse
import hashlib
import json
import pathlib
import sys

ROOT = pathlib.Path(__file__).resolve().parents[2]
DATA = ROOT / "testdata/parity"
OUTPUT = DATA / "manifest.json"
ROLES = {
    "source.json": "pinned Rust feature and artifact baseline commit",
    "inventory.json": "source-derived command/event/stream feature inventory",
    "go-aliases.json": "optional canonical feature IDs to Go-internal names; no wire compatibility requirement",
}


def generate() -> dict:
    source = json.loads((DATA / "source.json").read_text(encoding="utf-8"))
    files = []
    for name, role in ROLES.items():
        data = (DATA / name).read_bytes()
        files.append(
            {
                "path": f"testdata/parity/{name}",
                "role": role,
                "bytes": len(data),
                "sha256": hashlib.sha256(data).hexdigest(),
            }
        )
    return {
        "schema_version": 1,
        "baseline": {
            "purpose": "feature inventory and Rust test/artifact size baseline plus Go self-consistency",
            "source_commit": source["commit"],
            "real_user_data": False,
            "rust_compatibility_goldens": False,
        },
        "producers": {
            "inventory": "python3 scripts/parity/inventory.py",
            "rust_baseline": "python3 scripts/parity/run_baseline.py",
            "go_selfcheck": "python3 scripts/parity/go_selfcheck.py --go-root <go-module-root>",
            "verification": "python3 scripts/parity/verify.py --go-root <go-module-root>",
        },
        "files": files,
    }


def render(value: dict) -> str:
    return json.dumps(value, ensure_ascii=False, indent=2) + "\n"


def main() -> int:
    parser = argparse.ArgumentParser(description="Generate or verify feature/baseline asset hashes and provenance")
    parser.add_argument("--check", action="store_true")
    args = parser.parse_args()
    generated = render(generate())
    if args.check:
        if not OUTPUT.is_file() or OUTPUT.read_text(encoding="utf-8") != generated:
            print("parity manifest is stale or missing; run python3 scripts/parity/manifest.py", file=sys.stderr)
            return 1
        print("feature inventory and baseline asset hashes are current")
        return 0
    OUTPUT.write_text(generated, encoding="utf-8")
    print(f"wrote {OUTPUT.relative_to(ROOT)}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
