#!/usr/bin/env python3
from __future__ import annotations

import argparse
import difflib
import json
import pathlib
import sys
from typing import Any

ROOT = pathlib.Path(__file__).resolve().parents[2]
INVENTORY = ROOT / "testdata/parity/inventory.json"
ALIASES = ROOT / "testdata/parity/go-aliases.json"
GO_REPORT = ROOT / "testdata/parity/baseline/go-selfcheck.json"
OUTPUT = ROOT / "testdata/parity/baseline/go-coverage.json"


def alias_for(aliases: dict[str, Any], kind: str, canonical: str) -> str:
    return aliases.get(kind, {}).get(canonical, canonical)


def verified_index(items: list[dict[str, Any]], key: str) -> dict[str, str]:
    return {item[key]: item.get("evidence", "go-selfcheck.json") for item in items}


def display_path(path: pathlib.Path) -> str:
    try:
        return path.resolve().relative_to(ROOT).as_posix()
    except ValueError:
        return str(path)


def generate(inventory_path: pathlib.Path, aliases_path: pathlib.Path, report_path: pathlib.Path) -> dict[str, Any]:
    inventory = json.loads(inventory_path.read_text(encoding="utf-8"))
    aliases = json.loads(aliases_path.read_text(encoding="utf-8"))
    report = json.loads(report_path.read_text(encoding="utf-8"))
    runtime = report.get("runtime", {})
    verified_commands = verified_index(runtime.get("verified_commands", []), "name")
    implemented_commands = set(runtime.get("implemented_commands", []))
    features = []
    for entry in inventory["commands"]["rust_registry"]["entries"]:
        canonical = entry["name"]
        go_name = alias_for(aliases, "commands", canonical)
        if go_name in verified_commands:
            status = "verified"
            evidence = verified_commands[go_name]
        elif go_name in implemented_commands:
            status = "implemented-unverified"
            evidence = "registered in Go runtime; no passing feature-specific evidence supplied"
        else:
            status = "not-implemented"
            evidence = "no Go registration or passing feature-specific evidence supplied"
        features.append(
            {
                "id": canonical,
                "go_name": go_name,
                "status": status,
                "frontend_accessors": entry["frontend_wrappers"],
                "sync_only_feature": entry["sync_only"],
                "evidence": evidence,
            }
        )

    verified_events = verified_index(runtime.get("verified_events", []), "name")
    events = []
    for entry in inventory["events"]["rust_declared"]:
        canonical = entry["name"]
        go_name = alias_for(aliases, "events", canonical)
        events.append(
            {
                "id": canonical,
                "go_name": go_name,
                "status": "verified" if go_name in verified_events else "not-verified",
                "evidence": verified_events.get(go_name, "no production-event evidence supplied"),
            }
        )

    verified_streams = verified_index(runtime.get("verified_streams", []), "command")
    stream_commands = [(name, "binary") for name in inventory["streams"]["binary_commands"]]
    stream_commands += [(name, "ai_json") for name in inventory["streams"]["ai_json_commands"]]
    streams = []
    for canonical, kind in stream_commands:
        go_name = alias_for(aliases, "streams", canonical)
        streams.append(
            {
                "id": canonical,
                "kind": kind,
                "go_name": go_name,
                "status": "verified" if go_name in verified_streams else "not-verified",
                "evidence": verified_streams.get(go_name, "no production-stream evidence supplied"),
            }
        )

    command_verified = sum(feature["status"] == "verified" for feature in features)
    event_verified = sum(event["status"] == "verified" for event in events)
    stream_verified = sum(stream["status"] == "verified" for stream in streams)
    complete = command_verified == len(features) and event_verified == len(events) and stream_verified == len(streams)
    return {
        "schema_version": 1,
        "purpose": "Go feature acceptance coverage; canonical IDs are checklist labels, not required Rust wire compatibility",
        "go_source": {
            "root": report.get("go_source", {}).get("root"),
            "commit": report.get("go_source", {}).get("commit"),
            "report": display_path(report_path),
        },
        "rules": {
            "verified_requires_passing_feature_evidence": True,
            "registration_alone_is_not_a_pass": True,
            "generic_adapter_tests_are_not_production_feature_passes": True,
            "not_implemented_and_not_verified_are_not_passes": True,
        },
        "summary": {
            "feature_complete": complete,
            "commands_total": len(features),
            "commands_verified": command_verified,
            "commands_implemented_unverified": sum(feature["status"] == "implemented-unverified" for feature in features),
            "commands_not_implemented": sum(feature["status"] == "not-implemented" for feature in features),
            "events_total": len(events),
            "events_verified": event_verified,
            "streams_total": len(streams),
            "streams_verified": stream_verified,
        },
        "commands": features,
        "events": events,
        "streams": streams,
    }


def render(value: dict[str, Any]) -> str:
    return json.dumps(value, ensure_ascii=False, indent=2) + "\n"


def main() -> int:
    parser = argparse.ArgumentParser(description="Generate Go feature-coverage evidence without turning pending work into passes")
    parser.add_argument("--inventory", type=pathlib.Path, default=INVENTORY)
    parser.add_argument("--aliases", type=pathlib.Path, default=ALIASES)
    parser.add_argument("--go-report", type=pathlib.Path, default=GO_REPORT)
    parser.add_argument("--output", type=pathlib.Path, default=OUTPUT)
    parser.add_argument("--check", action="store_true")
    parser.add_argument("--require-complete", action="store_true")
    args = parser.parse_args()
    generated = generate(args.inventory, args.aliases, args.go_report)
    rendered = render(generated)
    if args.check:
        current = args.output.read_text(encoding="utf-8") if args.output.is_file() else ""
        if current != rendered:
            print("\n".join(difflib.unified_diff(current.splitlines(), rendered.splitlines())), file=sys.stderr)
            return 1
    else:
        args.output.parent.mkdir(parents=True, exist_ok=True)
        args.output.write_text(rendered, encoding="utf-8")
    summary = generated["summary"]
    print(
        f"Go feature coverage: commands {summary['commands_verified']}/{summary['commands_total']}, "
        f"events {summary['events_verified']}/{summary['events_total']}, "
        f"streams {summary['streams_verified']}/{summary['streams_total']}; "
        f"feature_complete={summary['feature_complete']}"
    )
    return 1 if args.require_complete and not summary["feature_complete"] else 0


if __name__ == "__main__":
    raise SystemExit(main())
