#!/usr/bin/env python3
from __future__ import annotations

import argparse
import pathlib
import subprocess
import sys

ROOT = pathlib.Path(__file__).resolve().parents[2]


def run(command: list[str]) -> None:
    print("+", " ".join(command), flush=True)
    subprocess.run(command, cwd=ROOT, check=True)


def static_checks() -> None:
    run(["python3", "scripts/parity/inventory.py", "--check"])
    run(["python3", "scripts/parity/manifest.py", "--check"])
    run(["python3", "-m", "py_compile", *sorted(str(path.relative_to(ROOT)) for path in (ROOT / "scripts/parity").glob("*.py"))])
    run(["python3", "-m", "unittest", "discover", "-s", "tests/parity", "-v"])


def main() -> int:
    parser = argparse.ArgumentParser(description="Verify baseline integrity and Go-native self-consistency without hiding unrun checks")
    parser.add_argument("--go-root", type=pathlib.Path)
    parser.add_argument("--static-only", action="store_true", help="explicitly validate only files/reports; not a Go runtime pass")
    args = parser.parse_args()
    go_root = args.go_root or ROOT
    if not args.static_only and not (go_root / "go.mod").is_file():
        print(
            f"Go runtime verification cannot run: {go_root} has no go.mod. "
            "Use --go-root or explicitly choose --static-only; neither is a skipped pass.",
            file=sys.stderr,
        )
        return 2
    if not args.static_only:
        run(["python3", "scripts/parity/go_selfcheck.py", "--go-root", str(go_root)])
    static_checks()
    if not args.static_only:
        run(["python3", "scripts/parity/coverage.py", "--check"])
    print(
        "static baseline checks verified; Go runtime selfcheck was not requested"
        if args.static_only
        else "static baseline and Go runtime selfchecks verified; feature/package gates remain separately reported"
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
