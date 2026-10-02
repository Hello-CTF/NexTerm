#!/usr/bin/env python3
from __future__ import annotations

import argparse
import datetime
import importlib.util
import json
import os
import pathlib
import platform
import shlex
import shutil
import socket
import subprocess
import sys
import time
import urllib.error
import urllib.request
from typing import Any

ROOT = pathlib.Path(__file__).resolve().parents[2]
DOCS = ROOT / "docs/acceptance-rwig/baseline"


def utc_now() -> str:
    return datetime.datetime.now(datetime.timezone.utc).isoformat()


def load_module(name: str, path: pathlib.Path):
    spec = importlib.util.spec_from_file_location(name, path)
    assert spec and spec.loader
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def go_environment() -> dict[str, str]:
    env = dict(os.environ)
    env.update(
        {
            "GOPATH": str(ROOT / "target/parity-go-path"),
            "GOMODCACHE": str(ROOT / "target/parity-go-modcache"),
            "GOCACHE": str(ROOT / "target/parity-go-cache"),
            "GOTOOLCHAIN": "auto",
            "CGO_ENABLED": "1",
        }
    )
    return env


def write_json(path: pathlib.Path, value: dict[str, Any]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")


def run_step(
    step_id: str,
    command: list[str],
    cwd: pathlib.Path,
    env: dict[str, str],
    log_dir: pathlib.Path,
) -> tuple[dict[str, Any], str]:
    started = time.monotonic()
    try:
        result = subprocess.run(
            command,
            cwd=cwd,
            env=env,
            text=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,
            timeout=2400,
        )
        code = result.returncode
        text = result.stdout
    except subprocess.TimeoutExpired as error:
        code = 124
        text = (error.stdout or "") + "\ncommand timed out"
    except OSError as error:
        code = 127
        text = str(error)
    log_dir.mkdir(parents=True, exist_ok=True)
    log = log_dir / f"{step_id}.log"
    log.write_text(text, encoding="utf-8")
    record = {
        "id": step_id,
        "command": shlex.join(command),
        "cwd": str(cwd),
        "status": "passed" if code == 0 else "failed",
        "exit_code": code,
        "duration_seconds": round(time.monotonic() - started, 3),
        "log": log.relative_to(ROOT).as_posix(),
    }
    print(f"{step_id}: {record['status']}", flush=True)
    return record, text


def free_port() -> int:
    with socket.socket() as listener:
        listener.bind(("127.0.0.1", 0))
        return listener.getsockname()[1]


def rpc(port: int, command: str, args: Any = None) -> dict[str, Any]:
    body = json.dumps({"cmd": command, "args": args}).encode()
    request = urllib.request.Request(
        f"http://127.0.0.1:{port}/rpc",
        data=body,
        headers={"content-type": "application/json"},
    )
    with urllib.request.urlopen(request, timeout=3) as response:
        return json.loads(response.read())


def wait_health(port: int, process: subprocess.Popen, timeout: float = 20) -> dict[str, Any]:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if process.poll() is not None:
            raise RuntimeError(f"server exited early with {process.returncode}")
        try:
            with urllib.request.urlopen(f"http://127.0.0.1:{port}/healthz", timeout=0.5) as response:
                return json.loads(response.read())
        except (urllib.error.URLError, TimeoutError):
            time.sleep(0.1)
    raise TimeoutError("server did not become healthy")


def server_smoke(
    binary: pathlib.Path,
    work: pathlib.Path,
    log_dir: pathlib.Path,
    sync_only: bool = False,
) -> dict[str, Any]:
    port = free_port()
    data_dir = work / ("sync-only-data" if sync_only else "full-data")
    data_dir.mkdir(parents=True, exist_ok=True)
    log = log_dir / ("go-server-sync-only-smoke.log" if sync_only else "go-server-smoke.log")
    command = [str(binary), "--listen", f"127.0.0.1:{port}", "--data-dir", str(data_dir)]
    if sync_only:
        command.append("--sync-only")
    evidence = []
    runtime: dict[str, Any] = {"verified_commands": [], "implemented_commands": []}
    with log.open("w", encoding="utf-8") as output:
        process = subprocess.Popen(command, cwd=work, stdout=output, stderr=subprocess.STDOUT)
        try:
            health = wait_health(port, process)
            evidence.append({"health": health})
            expected_commands = 2
            if not health.get("ok") or health.get("service") != "nexterm-server":
                raise AssertionError(f"invalid health response: {health}")
            if health.get("commands") != expected_commands or health.get("syncOnly") != sync_only:
                raise AssertionError(f"health deployment shape mismatch: {health}")
            runtime["implemented_commands"] = ["app_info", "app_platform"]
            if sync_only:
                try:
                    urllib.request.urlopen(f"http://127.0.0.1:{port}/rpc", timeout=2)
                    raise AssertionError("sync-only /rpc unexpectedly exists")
                except urllib.error.HTTPError as error:
                    if error.code != 404:
                        raise
                    evidence.append({"sync_only_rpc_status": 404})
            else:
                platform_response = rpc(port, "app_platform")
                expected_goos = {"Darwin": "darwin", "Linux": "linux", "Windows": "windows"}[platform.system()]
                if platform_response.get("ok") is not True or platform_response.get("data") != expected_goos:
                    raise AssertionError(f"app_platform selfcheck failed: {platform_response}")
                runtime["verified_commands"].append(
                    {
                        "name": "app_platform",
                        "evidence": f"{log.relative_to(ROOT).as_posix()}#app_platform",
                    }
                )
                evidence.append({"app_platform": platform_response})
                info_response = rpc(port, "app_info")
                info = info_response.get("data", {})
                if info_response.get("ok") is not True or info.get("name") != "NexTerm" or not info.get("version"):
                    raise AssertionError(f"app_info selfcheck failed: {info_response}")
                runtime["verified_commands"].append(
                    {
                        "name": "app_info",
                        "evidence": f"{log.relative_to(ROOT).as_posix()}#app_info",
                    }
                )
                evidence.append({"app_info": info_response})
                missing = rpc(port, "self.missing")
                if missing.get("ok") is not False or missing.get("error", {}).get("code") != "not_found":
                    raise AssertionError(f"unknown command did not return not_found: {missing}")
                evidence.append({"unknown_command": missing})
        finally:
            process.terminate()
            try:
                process.wait(timeout=8)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait(timeout=3)
        output.write("\nSELFCheck observations (Go runtime only; no Rust expected payloads):\n")
        output.write(json.dumps(evidence, ensure_ascii=False, indent=2) + "\n")
    runtime["observations"] = evidence
    runtime["log"] = log.relative_to(ROOT).as_posix()
    return runtime


def main() -> int:
    parser = argparse.ArgumentParser(description="Run Go-native IPC/deployment self-consistency checks and explicit coverage/size gates")
    parser.add_argument("--go-root", type=pathlib.Path, default=ROOT)
    parser.add_argument("--baseline", type=pathlib.Path, default=DOCS / "baseline.json")
    parser.add_argument("--output", type=pathlib.Path, default=DOCS / "go-selfcheck.json")
    parser.add_argument("--coverage-output", type=pathlib.Path, default=DOCS / "go-coverage.json")
    parser.add_argument("--require-size", action="store_true")
    parser.add_argument("--require-features", action="store_true")
    args = parser.parse_args()
    go_root = args.go_root.resolve()
    if not (go_root / "go.mod").is_file():
        parser.error(f"Go module root does not contain go.mod: {go_root}")
    env = go_environment()
    work = ROOT / "target/parity-go-selfcheck"
    shutil.rmtree(work, ignore_errors=True)
    harness = work / "harness"
    harness.parent.mkdir(parents=True, exist_ok=True)
    shutil.copytree(ROOT / "tests/parity/go", harness)
    artifacts = ROOT / "target/parity-go-artifacts"
    artifacts.mkdir(parents=True, exist_ok=True)
    log_dir = DOCS / "logs"
    steps = []
    runtime: dict[str, Any] = {
        "verified_commands": [],
        "implemented_commands": [],
        "verified_events": [],
        "verified_streams": [],
    }

    step, _ = run_step(
        "go-harness-resolve",
        ["go", "mod", "edit", f"-replace=github.com/ProbiusOfficial/NexTerm={go_root}"],
        harness,
        env,
        log_dir,
    )
    steps.append(step)
    step, _ = run_step("go-harness-tidy", ["go", "mod", "tidy"], harness, env, log_dir)
    steps.append(step)
    step, version_output = run_step("go-toolchain", ["go", "env", "GOVERSION"], harness, env, log_dir)
    steps.append(step)
    step, _ = run_step(
        "go-ipc-self-tests",
        ["go", "test", "-mod=readonly", "-race", "-count=1", "./..."],
        harness,
        env,
        log_dir,
    )
    steps.append(step)

    server_binary = artifacts / "nexterm-server"
    server_env = dict(env)
    server_env["CGO_ENABLED"] = "0"
    step, _ = run_step(
        "go-server-build",
        ["go", "build", "-mod=readonly", "-trimpath", "-ldflags=-s -w", "-o", str(server_binary), "./cmd/nexterm-server"],
        go_root,
        server_env,
        log_dir,
    )
    steps.append(step)
    if step["status"] == "passed":
        started = time.monotonic()
        try:
            full = server_smoke(server_binary, work, log_dir)
            runtime["verified_commands"] = full["verified_commands"]
            runtime["implemented_commands"] = full["implemented_commands"]
            runtime["full_server"] = full["observations"]
            runtime["sync_only_server"] = server_smoke(server_binary, work, log_dir, sync_only=True)["observations"]
            smoke_status = "passed"
            smoke_code = 0
        except Exception as error:
            smoke_status = "failed"
            smoke_code = 1
            runtime["server_smoke_error"] = str(error)
        steps.append(
            {
                "id": "go-server-live-smoke",
                "command": "full and sync-only loopback servers with synthetic requests",
                "status": smoke_status,
                "exit_code": smoke_code,
                "duration_seconds": round(time.monotonic() - started, 3),
                "log": "docs/acceptance-rwig/baseline/logs/go-server-smoke.log",
            }
        )
        print(f"go-server-live-smoke: {smoke_status}", flush=True)
    else:
        steps.append(
            {
                "id": "go-server-live-smoke",
                "status": "not-run-dependency-failed",
                "reason": "Go server binary build failed",
            }
        )

    desktop_binary = artifacts / "nexterm-desktop"
    step, _ = run_step(
        "go-desktop-build",
        ["go", "build", "-mod=readonly", "-trimpath", "-ldflags=-s -w", "-o", str(desktop_binary), "./cmd/nexterm-desktop"],
        go_root,
        env,
        log_dir,
    )
    steps.append(step)

    baseline = json.loads(args.baseline.read_text(encoding="utf-8"))
    rust = {artifact["label"]: artifact for artifact in baseline["artifacts"]}
    size_module = load_module("parity_size_gate", ROOT / "scripts/parity/size_gate.py")
    size_gates = []
    for label, path in [
        ("rust-desktop-macos-arm64", desktop_binary),
        ("rust-server-macos-arm64", server_binary),
    ]:
        if label in rust:
            size_gates.append(size_module.gate(rust[label], path))
    size_passed = bool(size_gates) and all(gate["status"] == "passed" for gate in size_gates)
    report = {
        "schema_version": 1,
        "purpose": "Go self-consistency and acceptance evidence; no Rust payload goldens or differential compatibility claims",
        "captured_at": utc_now(),
        "go_source": {
            "root": str(go_root),
            "commit": subprocess.check_output(["git", "-C", str(go_root), "rev-parse", "HEAD"], text=True).strip(),
            "go_version": version_output.strip(),
        },
        "steps": steps,
        "runtime": runtime,
        "size_gates": size_gates,
        "not_produced": [
            {
                "item": "Go DMG/NSIS/LPK package-size acceptance",
                "reason": "this skeleton selfcheck builds raw desktop/server binaries only; no installer-package pass is claimed",
            }
        ],
    }
    write_json(args.output, report)
    coverage = subprocess.run(
        [
            sys.executable,
            "scripts/parity/coverage.py",
            "--go-report",
            str(args.output),
            "--output",
            str(args.coverage_output),
        ],
        cwd=ROOT,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
    )
    print(coverage.stdout, end="")
    if coverage.returncode != 0:
        report["coverage_error"] = coverage.stdout
        feature_complete = False
        coverage_summary = {}
    else:
        coverage_data = json.loads(args.coverage_output.read_text(encoding="utf-8"))
        coverage_summary = coverage_data["summary"]
        feature_complete = coverage_summary["feature_complete"]
    failed_steps = [step for step in steps if step["status"] != "passed"]
    report["summary"] = {
        "selfcheck_steps_passed": sum(step["status"] == "passed" for step in steps),
        "selfcheck_steps_failed_or_not_run": len(failed_steps),
        "feature_complete": feature_complete,
        "raw_binary_size_gates_passed": size_passed,
        "installer_package_gates_passed": False,
        "feature_coverage": coverage_summary,
        "skips_are_passes": False,
    }
    report["status"] = (
        "failed"
        if failed_steps or coverage.returncode != 0
        else "selfcheck-passed-acceptance-gaps-remain"
        if not (feature_complete and size_passed)
        else "selfcheck-passed-package-gates-remain"
    )
    write_json(args.output, report)
    failed = bool(failed_steps) or coverage.returncode != 0
    if args.require_size and not size_passed:
        failed = True
    if args.require_features and not feature_complete:
        failed = True
    return 1 if failed else 0


if __name__ == "__main__":
    raise SystemExit(main())
