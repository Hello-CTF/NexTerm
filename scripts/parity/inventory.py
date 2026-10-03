#!/usr/bin/env python3
from __future__ import annotations

import argparse
import collections
import difflib
import json
import pathlib
import re
import sys
from typing import Any

ROOT = pathlib.Path(__file__).resolve().parents[2]
DEFAULT_OUTPUT = ROOT / "testdata/parity/inventory.json"
RUST_REGISTRY = ROOT / "testdata/parity/rust-registry.json"
FRONTEND_COMMANDS = ROOT / "src/ipc/commands.ts"
FRONTEND_EVENTS = ROOT / "src/ipc/events.ts"
FRONTEND_LAYOUT = ROOT / "src/app/layout.ts"


def rel(path: pathlib.Path) -> str:
    return path.relative_to(ROOT).as_posix()


def line_at(text: str, offset: int) -> int:
    return text.count("\n", 0, offset) + 1


def source_commit() -> str:
    source = json.loads((ROOT / "testdata/parity/source.json").read_text(encoding="utf-8"))
    return source["commit"]


def load_rust_registry() -> dict[str, Any]:
    return json.loads(RUST_REGISTRY.read_text(encoding="utf-8"))


def rust_commands(registry: dict[str, Any]) -> list[dict[str, Any]]:
    entries = [dict(entry) for entry in registry["commands"]["entries"]]
    if not entries:
        raise RuntimeError("frozen Rust command registry is empty")
    return entries


def frontend_commands() -> list[dict[str, Any]]:
    text = FRONTEND_COMMANDS.read_text(encoding="utf-8")
    calls = []
    for match in re.finditer(r"\bcall\b", text):
        index = match.end()
        while index < len(text) and text[index].isspace():
            index += 1
        if index < len(text) and text[index] == "<":
            depth = 1
            index += 1
            while index < len(text) and depth:
                if text[index] == "<":
                    depth += 1
                elif text[index] == ">":
                    depth -= 1
                index += 1
            if depth:
                continue
        while index < len(text) and text[index].isspace():
            index += 1
        if index >= len(text) or text[index] != "(":
            continue
        index += 1
        while index < len(text) and text[index].isspace():
            index += 1
        if index >= len(text) or text[index] != '"':
            continue
        end = text.find('"', index + 1)
        if end < 0:
            continue
        command = text[index + 1 : end]
        if not re.fullmatch(r"[a-z][a-z0-9_]+", command):
            continue
        prefix = text[: match.start()]
        api_matches = list(re.finditer(r"export const (\w+Api) = \{", prefix))
        api = api_matches[-1].group(1) if api_matches else "unknownApi"
        api_start = api_matches[-1].start() if api_matches else 0
        method_matches = list(
            re.finditer(r"^  (\w+):", text[api_start : match.start()], re.MULTILINE)
        )
        method = method_matches[-1].group(1) if method_matches else "unknown"
        calls.append(
            {
                "name": command,
                "accessor": f"{api}.{method}",
                "source": rel(FRONTEND_COMMANDS),
                "line": line_at(text, match.start()),
            }
        )
    if not calls:
        raise RuntimeError("frontend command extraction returned no entries")
    return calls


def named_events(registry: dict[str, Any]) -> dict[str, Any]:
    rust = [dict(entry) for entry in registry["events"]["declared"]]
    layout_name = next(
        (entry["name"] for entry in rust if entry["constant"] == "LAYOUT_CHANGED"), None
    )

    frontend_text = FRONTEND_EVENTS.read_text(encoding="utf-8")
    frontend = [
        {
            "constant": match.group(1),
            "name": match.group(2),
            "source": rel(FRONTEND_EVENTS),
            "line": line_at(frontend_text, match.start()),
        }
        for match in re.finditer(r"^  (\w+): \"([^\"]+)\",", frontend_text, re.MULTILINE)
    ]
    if layout_name:
        frontend.append(
            {
                "constant": "layoutChanged",
                "name": layout_name,
                "source": rel(FRONTEND_LAYOUT),
                "line": next(
                    (
                        index
                        for index, line in enumerate(
                            FRONTEND_LAYOUT.read_text(encoding="utf-8").splitlines(),
                            1,
                        )
                        if layout_name in line
                    ),
                    None,
                ),
            }
        )

    frontend_by_name = {entry["name"]: entry for entry in frontend}
    reserved = registry["events"]["reserved"]
    for entry in rust:
        entry["frontend_constant"] = frontend_by_name.get(entry["name"], {}).get(
            "constant"
        )
        entry["declared_only"] = entry["name"] in reserved
    return {
        "rust_declared": sorted(rust, key=lambda entry: entry["name"]),
        "frontend_declared": sorted(frontend, key=lambda entry: entry["name"]),
        "rust_declared_count": len(rust),
        "frontend_declared_count": len(frontend),
        "union_count": len({entry["name"] for entry in rust + frontend}),
        "reserved_rust_events": list(reserved),
    }


def streams(registry: dict[str, Any]) -> dict[str, Any]:
    data = registry["streams"]
    references = [dict(entry) for entry in data["references"]]
    variants = list(data["ai_event_variants"])
    return {
        "references": references,
        "binary_commands": sorted(
            {entry["command"] for entry in references if entry["kind"] == "binary"}
        ),
        "ai_json_commands": sorted(
            {entry["command"] for entry in references if entry["kind"] == "ai_json"}
        ),
        "ai_event_variants": variants,
        "ai_event_variant_count": len(variants),
        "ordering": data["ordering"],
        "binary_encoding": data["binary_encoding"],
    }


def generate() -> dict[str, Any]:
    registry = load_rust_registry()
    rust = rust_commands(registry)
    frontend = frontend_commands()
    rust_names = collections.Counter(entry["name"] for entry in rust)
    frontend_names = collections.Counter(entry["name"] for entry in frontend)
    by_command: dict[str, list[str]] = collections.defaultdict(list)
    for entry in frontend:
        by_command[entry["name"]].append(entry["accessor"])
    for entries in by_command.values():
        entries.sort()
    for entry in rust:
        entry["frontend_wrappers"] = by_command.get(entry["name"], [])

    rust_only = sorted(set(rust_names) - set(frontend_names))
    frontend_only = sorted(set(frontend_names) - set(rust_names))
    duplicates = [
        {"name": name, "accessors": by_command[name]}
        for name, count in sorted(frontend_names.items())
        if count > 1
    ]
    duplicate_rust = sorted(name for name, count in rust_names.items() if count > 1)
    sync_only = sorted(entry["name"] for entry in rust if entry["sync_only"])
    return {
        "schema_version": 1,
        "baseline": {
            "implementation": "rust",
            "source_commit": source_commit(),
            "generator": "scripts/parity/inventory.py",
        },
        "commands": {
            "rust_registry": {
                "source": registry["commands"]["registry_source"],
                "count": len(rust),
                "unique_count": len(rust_names),
                "duplicate_names": duplicate_rust,
                "entries": rust,
            },
            "frontend_facade": {
                "source": rel(FRONTEND_COMMANDS),
                "method_count": len(frontend),
                "unique_command_count": len(frontend_names),
                "entries": frontend,
            },
            "reconciliation": {
                "rust_only": rust_only,
                "frontend_only": frontend_only,
                "duplicate_frontend_commands": duplicates,
                "equation": (
                    f"{len(rust_names)} Rust = {len(set(rust_names) & set(frontend_names))} frontend unique"
                    f" + rust_only {rust_only}; {len(frontend)} frontend methods = {len(frontend_names)} unique"
                    + (
                        " + duplicate " + ", ".join(entry["name"] for entry in duplicates) + " wrapper"
                        if duplicates
                        else ""
                    )
                    + f"; frontend_only {frontend_only}"
                ),
                "sync_only_commands": sync_only,
            },
        },
        "events": named_events(registry),
        "streams": streams(registry),
    }


def render(value: Any) -> str:
    return json.dumps(value, ensure_ascii=False, indent=2, sort_keys=True) + "\n"


def main() -> int:
    parser = argparse.ArgumentParser(
        description="Generate or verify the IPC parity inventory from the frozen Rust registry and the live frontend facade"
    )
    parser.add_argument("--output", type=pathlib.Path, default=DEFAULT_OUTPUT)
    parser.add_argument("--check", action="store_true")
    args = parser.parse_args()
    generated = render(generate())
    if args.check:
        if not args.output.is_file():
            print(f"missing inventory: {args.output}", file=sys.stderr)
            return 1
        current = args.output.read_text(encoding="utf-8")
        if current == generated:
            print(f"inventory current: {args.output.relative_to(ROOT)}")
            return 0
        diff = difflib.unified_diff(
            current.splitlines(), generated.splitlines(), fromfile="current", tofile="generated"
        )
        print("\n".join(diff), file=sys.stderr)
        return 1
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(generated, encoding="utf-8")
    data = json.loads(generated)
    print(
        f"wrote {args.output.relative_to(ROOT)}: "
        f"Rust={data['commands']['rust_registry']['count']}, "
        f"frontend methods={data['commands']['frontend_facade']['method_count']}, "
        f"frontend unique={data['commands']['frontend_facade']['unique_command_count']}"
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
