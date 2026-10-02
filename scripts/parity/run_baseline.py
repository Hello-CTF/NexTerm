#!/usr/bin/env python3
from __future__ import annotations

import argparse
import datetime
import hashlib
import importlib.util
import json
import os
import pathlib
import platform
import re
import shlex
import subprocess
import sys
import time
from typing import Any

ROOT = pathlib.Path(__file__).resolve().parents[2]
DOCS = ROOT / "docs/acceptance-rwig/baseline"
SELECTED_CARGO = ROOT / "target/parity-toolchain/rust/bin/cargo"


def utc_now() -> str:
    return datetime.datetime.now(datetime.timezone.utc).isoformat()


def output(command: list[str], env: dict[str, str] | None = None) -> str:
    try:
        result = subprocess.run(
            command,
            cwd=ROOT,
            env=env,
            text=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,
            timeout=30,
        )
        return result.stdout.strip()
    except (OSError, subprocess.TimeoutExpired) as error:
        return f"unavailable: {error}"


def selected_environment() -> dict[str, str]:
    env = dict(os.environ)
    if SELECTED_CARGO.is_file():
        env["PATH"] = f"{SELECTED_CARGO.parent}{os.pathsep}{env.get('PATH', '')}"
    env.update(
        {
            "CARGO_HOME": str(ROOT / "target/parity-cargo-home"),
            "CARGO_TARGET_DIR": str(ROOT / "target/parity"),
            "CARGO_TERM_COLOR": "never",
            "NO_COLOR": "1",
            "CLICOLOR": "0",
            "CODEBUDDY_SESSION_ID": "",
            "CLAUDE_SESSION_ID": "",
        }
    )
    for name in list(env):
        if name.startswith("NEXTERM_SSH_") or name == "NEXTERM_TEST_KEYCHAIN":
            env.pop(name)
    return env


def environment(env: dict[str, str]) -> dict[str, Any]:
    return {
        "captured_at": utc_now(),
        "host": {
            "system": platform.system(),
            "release": platform.release(),
            "version": platform.version(),
            "machine": platform.machine(),
            "python": platform.python_version(),
            "sw_vers": output(["sw_vers"]) if platform.system() == "Darwin" else None,
        },
        "source": {
            "head": output(["git", "rev-parse", "HEAD"]),
            "rwig": output(["git", "rev-parse", "rwig"]),
        },
        "default_toolchain": {
            "rustc": output(["rustc", "--version"]),
            "cargo": output(["cargo", "--version"]),
        },
        "selected_toolchain": {
            "rustc": output(["rustc", "--version"], env),
            "cargo": output(["cargo", "--version"], env),
            "node": output(["node", "--version"], env),
            "pnpm": output(["pnpm", "--version"], env),
            "path_prefix": str(SELECTED_CARGO.parent) if SELECTED_CARGO.is_file() else "system PATH",
            "cargo_home": env["CARGO_HOME"],
            "cargo_target_dir": env["CARGO_TARGET_DIR"],
        },
        "locale": {name: os.environ.get(name) for name in ["LC_ALL", "LC_CTYPE", "LANG"]},
        "credential_guards": [
            "NEXTERM_SSH_* removed from baseline subprocesses",
            "NEXTERM_TEST_KEYCHAIN removed; personal Keychain is not touched",
            "all generated credentials are marked PARITY-SYNTHETIC",
        ],
    }


def coverage_skips() -> list[dict[str, str]]:
    skips = [
        {
            "id": "ssh_pty_pipeline_on_linux_target",
            "category": "external-service",
            "status": "skipped",
            "reason": "requires an SSH target and NEXTERM_SSH_HOST/USER/KEY; no real target or credentials were used",
        },
        {
            "id": "10-session RSS and throughput acceptance",
            "category": "external-service",
            "status": "not-measured",
            "reason": "requires the unavailable SSH target; the single-engine benchmark is not a substitute",
        },
    ]
    if platform.system() == "Darwin" and os.environ.get("NEXTERM_TEST_KEYCHAIN") != "1":
        skips.append(
            {
                "id": "macOS Keychain roundtrip tests (3)",
                "category": "external-os-store",
                "status": "skipped",
                "reason": "NEXTERM_TEST_KEYCHAIN=1 was deliberately not set; no personal Keychain data was touched",
            }
        )
    if any(os.environ.get(name) for name in ["LC_ALL", "LC_CTYPE", "LANG"]):
        locale = ", ".join(f"{name}={os.environ.get(name)}" for name in ["LC_ALL", "LC_CTYPE", "LANG"] if os.environ.get(name))
        skips.append(
            {
                "id": "local_pty_keeps_utf8_filenames",
                "category": "host-environment",
                "status": "skipped-by-test-precondition",
                "reason": f"the regression test requires an empty host locale; current {locale}",
            }
        )
    if platform.system() != "Windows":
        skips.append(
            {
                "id": "Windows DPAPI-specific tests",
                "category": "platform-not-compiled",
                "status": "not-run",
                "reason": f"current host is {platform.system()}; these tests require a Windows run",
            }
        )
    if not any(pathlib.Path(path).exists() for path in ["/usr/libexec/sftp-server", "/usr/lib/openssh/sftp-server"]):
        skips.append(
            {
                "id": "writing_an_existing_file_keeps_a_faithful_backup",
                "category": "external-program",
                "status": "skipped",
                "reason": "OpenSSH sftp-server is not installed at either supported path",
            }
        )
    return skips


def load_measure():
    path = ROOT / "scripts/parity/measure_artifacts.py"
    spec = importlib.util.spec_from_file_location("parity_measure", path)
    assert spec and spec.loader
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def write_report(path: pathlib.Path, report: dict[str, Any]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")


def main() -> int:
    parser = argparse.ArgumentParser(description="Run and record the Rust/frontend baseline without hiding coverage skips")
    parser.add_argument("--output", type=pathlib.Path, default=DOCS / "baseline.json")
    parser.add_argument("--log-dir", type=pathlib.Path, default=DOCS / "logs")
    parser.add_argument("--only", help="comma-separated step IDs")
    args = parser.parse_args()
    env = selected_environment()
    steps = [
        ("rust-test-desktop", ["cargo", "test", "--workspace", "--", "--nocapture"]),
        (
            "rust-test-server",
            ["cargo", "test", "--workspace", "--no-default-features", "--features", "server", "--", "--nocapture"],
        ),
        ("frontend-typecheck", ["pnpm", "typecheck"]),
        ("frontend-lint", ["pnpm", "lint"]),
        ("frontend-build", ["pnpm", "build"]),
        ("terminal-throughput", ["cargo", "bench", "-p", "nexterm", "--bench", "terminal_throughput"]),
        ("rust-release-desktop", ["node", "scripts/build.mjs", "release"]),
        ("rust-bundle-desktop", ["pnpm", "tauri", "build"]),
        (
            "rust-release-server",
            ["cargo", "build", "--release", "-p", "nexterm", "--no-default-features", "--features", "server", "--bin", "nexterm-server"],
        ),
        (
            "sync-e2e",
            [
                "python3",
                "scripts/e2e-sync-local.py",
                "--bin",
                "target/parity/release/nexterm-server",
                "--no-build",
            ],
        ),
    ]
    only = set(args.only.split(",")) if args.only else None
    if only is not None:
        unknown = only - {step[0] for step in steps}
        if unknown:
            parser.error(f"unknown step IDs: {sorted(unknown)}")
        steps = [step for step in steps if step[0] in only]
    args.log_dir.mkdir(parents=True, exist_ok=True)
    report: dict[str, Any] = {
        "schema_version": 1,
        "purpose": "Rust baseline for later Go parity comparisons; not a claim of complete parity",
        "status": "running",
        "environment": environment(env),
        "steps": [],
        "coverage_skips": coverage_skips(),
        "metrics": {},
        "artifacts": [],
        "not_produced": [
            {
                "item": "Windows and Linux same-host artifacts",
                "reason": f"only {platform.system()}/{platform.machine()} is available; cross-compiled output is not substituted for a native run",
            },
            {
                "item": "LazyCat LPK from this baseline run",
                "reason": "requires the external LazyCat remote builder/box; documented historical LPK sizes are retained separately",
            },
        ],
    }
    write_report(args.output, report)
    outputs: dict[str, str] = {}
    statuses: dict[str, str] = {}
    for step_id, command in steps:
        if step_id == "sync-e2e" and "rust-release-server" in statuses and statuses["rust-release-server"] != "passed":
            report["coverage_skips"].append(
                {
                    "id": step_id,
                    "category": "dependency-failure",
                    "status": "not-run",
                    "reason": "rust-release-server did not produce a usable binary",
                }
            )
            write_report(args.output, report)
            continue
        started = utc_now()
        started_clock = time.monotonic()
        try:
            result = subprocess.run(
                command,
                cwd=ROOT,
                env=env,
                text=True,
                stdout=subprocess.PIPE,
                stderr=subprocess.STDOUT,
                timeout=2400,
            )
            exit_code = result.returncode
            text = result.stdout
        except subprocess.TimeoutExpired as error:
            exit_code = 124
            text = (error.stdout or "") + "\ncommand timed out after 2400 seconds"
        except OSError as error:
            exit_code = 127
            text = str(error)
        duration = time.monotonic() - started_clock
        status = "passed" if exit_code == 0 else "failed"
        statuses[step_id] = status
        outputs[step_id] = text
        log_path = args.log_dir / f"{step_id}.log"
        log_path.write_text(text, encoding="utf-8")
        try:
            log_display = log_path.relative_to(ROOT).as_posix()
        except ValueError:
            log_display = str(log_path)
        report["steps"].append(
            {
                "id": step_id,
                "command": shlex.join(command),
                "status": status,
                "exit_code": exit_code,
                "duration_seconds": round(duration, 3),
                "started_at": started,
                "finished_at": utc_now(),
                "log": log_display,
            }
        )
        write_report(args.output, report)
        print(f"{step_id}: {status} ({duration:.1f}s)", flush=True)

    bench = outputs.get("terminal-throughput", "")
    throughput = re.search(r"=> ([0-9.]+) MB/s", bench)
    gbk = re.search(r"gbk transcode throughput: ([0-9.]+) MB/s", bench)
    retained = re.search(r"scrollback retained: ([0-9]+) bytes", bench)
    screen = re.search(r"screen after feed: ([^\n]+)", bench)
    if throughput:
        report["metrics"]["terminal_utf8_mb_per_s"] = float(throughput.group(1))
    if gbk:
        report["metrics"]["terminal_gbk_mb_per_s"] = float(gbk.group(1))
    if retained:
        report["metrics"]["scrollback_retained_bytes"] = int(retained.group(1))
    if screen:
        report["metrics"]["screen_after_feed"] = screen.group(1)
    sync = outputs.get("sync-e2e", "")
    assertions = re.search(r"全部 ([0-9]+) 条断言通过", sync)
    if assertions:
        report["metrics"]["sync_e2e_assertions_passed"] = int(assertions.group(1))

    measure = load_measure()
    candidates = [
        ("rust-release-desktop", "rust-desktop-macos-arm64", ROOT / "target/parity/release/nexterm"),
        ("rust-release-server", "rust-server-macos-arm64", ROOT / "target/parity/release/nexterm-server"),
    ]
    if statuses.get("rust-bundle-desktop") == "passed":
        candidates.extend(
            ("rust-bundle-desktop", "rust-desktop-dmg-macos-arm64", path)
            for path in sorted((ROOT / "target/parity/release/bundle/dmg").glob("*.dmg"))
        )
    for step_id, label, path in candidates:
        if statuses.get(step_id) != "passed" or not path.is_file():
            continue
        artifact = measure.measure(label, path)
        try:
            artifact["path"] = path.relative_to(ROOT).as_posix()
        except ValueError:
            pass
        report["artifacts"].append(artifact)

    write_report(args.output, report)
    subprocess.run(
        [sys.executable, "scripts/parity/account_tests.py", "--baseline", str(args.output)],
        cwd=ROOT,
        check=True,
    )
    report = json.loads(args.output.read_text(encoding="utf-8"))
    failures = [step for step in report["steps"] if step["status"] == "failed"]
    report["summary"] = {
        "commands_passed": sum(step["status"] == "passed" for step in report["steps"]),
        "commands_failed": len(failures),
        "coverage_skipped_or_not_run": len(report["coverage_skips"]),
        "skips_are_passes": False,
    }
    report["status"] = "failed" if failures else "commands-passed-coverage-gaps-remain"
    report["finished_at"] = utc_now()
    write_report(args.output, report)
    print(json.dumps(report["summary"], ensure_ascii=False))
    return 1 if failures else 0


if __name__ == "__main__":
    raise SystemExit(main())
