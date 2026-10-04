#!/usr/bin/env python3
"""Go-only two-process sync acceptance over real HTTP.

Builds CGO_ENABLED=0 by default, uses independent data directories and master
keys, exports from one real server into another, verifies idempotence,
re-encryption, tombstones and version rejection, then restarts the peer in
--sync-only mode. There are no Rust fixtures and no skip-as-pass paths.

    python3 scripts/e2e-sync-local.py
    python3 scripts/e2e-sync-local.py --bin target/go-build/nexterm-server-linux-amd64 --no-build
"""

from __future__ import annotations

import argparse
import json
import os
import pathlib
import shutil
import socket
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request
import uuid

ROOT = pathlib.Path(__file__).resolve().parent.parent
TOKEN_HEADER = "X-NexTerm-Sync-Token"
PASSED: list[str] = []
FAILED: list[str] = []


def check(name: str, ok: bool, detail: object = "") -> None:
    if ok:
        PASSED.append(name)
        print(f"  PASS {name}", flush=True)
    else:
        FAILED.append(name)
        print(f"  FAIL {name}: {detail}", flush=True)


def call(port: int, command: str, args: object = None, token: str | None = None, route: str = "/rpc", extra_headers: dict[str, str] | None = None) -> tuple[int, dict]:
    request = urllib.request.Request(
        f"http://127.0.0.1:{port}{route}",
        data=json.dumps({"cmd": command, "args": args}).encode(),
        headers={"content-type": "application/json"},
        method="POST",
    )
    if token is not None:
        request.add_header(TOKEN_HEADER, token)
    for name, value in (extra_headers or {}).items():
        request.add_header(name, value)
    try:
        with urllib.request.urlopen(request, timeout=20) as response:
            return response.status, json.loads(response.read())
    except urllib.error.HTTPError as error:
        raw = error.read()
        try:
            return error.code, json.loads(raw)
        except (ValueError, TypeError):
            return error.code, {"raw": raw.decode(errors="replace")[:500]}


def data(port: int, command: str, args: object = None, token: str | None = None, route: str = "/rpc") -> object:
    status, envelope = call(port, command, args, token, route)
    if status != 200 or not isinstance(envelope, dict) or envelope.get("ok") is not True:
        raise AssertionError(f"{command}: HTTP {status} {envelope}")
    return envelope.get("data")


def raw_status(port: int, path: str, method: str = "GET", body: bytes | None = None, token: str | None = None) -> int:
    request = urllib.request.Request(f"http://127.0.0.1:{port}{path}", data=body, method=method)
    if token is not None:
        request.add_header(TOKEN_HEADER, token)
    try:
        with urllib.request.urlopen(request, timeout=20) as response:
            return response.status
    except urllib.error.HTTPError as error:
        return error.code


def free_port() -> int:
    with socket.socket() as listener:
        listener.bind(("127.0.0.1", 0))
        return int(listener.getsockname()[1])


class Instance:
    def __init__(self, binary: pathlib.Path, work: pathlib.Path, name: str, master_key: str):
        self.binary = binary
        self.data_dir = work / name
        self.log_path = work / f"{name}.log"
        self.master_key = master_key
        self.port = free_port()
        self.process: subprocess.Popen | None = None
        self.log_handle = None

    def start(self, sync_only: bool = False, host: str = "127.0.0.1") -> None:
        self.stop()
        self.data_dir.mkdir(parents=True, exist_ok=True)
        environment = dict(os.environ)
        environment.update(
            {
                "NEXTERM_DATA_DIR": str(self.data_dir),
                "NEXTERM_LISTEN": f"{host}:{self.port}",
                "NEXTERM_MASTER_KEY": self.master_key,
            }
        )
        arguments = [str(self.binary), "--listen", f"{host}:{self.port}", "--data-dir", str(self.data_dir)]
        if sync_only:
            arguments.append("--sync-only")
        self.log_handle = self.log_path.open("ab")
        self.process = subprocess.Popen(arguments, cwd=ROOT, env=environment, stdout=self.log_handle, stderr=subprocess.STDOUT)
        deadline = time.monotonic() + 30
        while time.monotonic() < deadline:
            if self.process.poll() is not None:
                raise AssertionError(f"server exited with {self.process.returncode}; log={self.log_path}")
            try:
                with urllib.request.urlopen(f"http://127.0.0.1:{self.port}/healthz", timeout=1) as response:
                    if response.status == 200:
                        return
            except Exception:
                time.sleep(0.1)
        raise AssertionError(f"server readiness timed out; log={self.log_path}")

    def cli_token(self, rotate: bool = False) -> str:
        arguments = [str(self.binary), "rotate-token" if rotate else "token", "--data-dir", str(self.data_dir)]
        result = subprocess.run(arguments, cwd=ROOT, capture_output=True, text=True, timeout=30)
        if result.returncode != 0:
            raise AssertionError(f"token command failed: {result.stderr.strip()}; log={self.log_path}")
        return result.stdout.strip()

    def health(self) -> dict:
        with urllib.request.urlopen(f"http://127.0.0.1:{self.port}/healthz", timeout=5) as response:
            return json.loads(response.read())

    def stop(self) -> None:
        if self.process is not None and self.process.poll() is None:
            self.process.terminate()
            try:
                self.process.wait(timeout=8)
            except subprocess.TimeoutExpired:
                self.process.kill()
                self.process.wait(timeout=3)
        self.process = None
        if self.log_handle is not None:
            self.log_handle.close()
            self.log_handle = None


def build_server(binary: pathlib.Path) -> None:
    version = json.loads((ROOT / "wails.json").read_text(encoding="utf-8"))["info"]["version"]
    binary.parent.mkdir(parents=True, exist_ok=True)
    environment = dict(os.environ)
    environment.update({"CGO_ENABLED": "0", "GOTOOLCHAIN": "go1.26.8"})
    subprocess.run(
        [
            "go", "build", "-mod=readonly", "-trimpath", "-buildvcs=false",
            "-ldflags", f"-s -w -X github.com/ProbiusOfficial/NexTerm/internal/version.Version={version}",
            "-o", str(binary), "./cmd/nexterm-server",
        ],
        cwd=ROOT,
        env=environment,
        check=True,
    )


def synthetic_id() -> str:
    return f"e2e{uuid.uuid4().hex[:23]}"


def synthetic_bundle() -> dict:
    now = int(time.time() * 1000)
    group_id = synthetic_id()
    asset_id = synthetic_id()
    credential_id = synthetic_id()
    return {
        "protocol": 1,
        "origin": "go-e2e-a",
        "exportedAt": now,
        "groups": [{"id": group_id, "parentId": None, "name": "Go E2E 生产", "sort": 0, "createdAt": now, "updatedAt": now}],
        "assets": [{
            "id": asset_id,
            "groupId": group_id,
            "kind": "ssh",
            "name": "go-e2e-web-1",
            "host": "192.0.2.10",
            "port": 2222,
            "username": "root",
            "authKind": "password",
            "keyPath": None,
            "credId": credential_id,
            "optionsJson": json.dumps({"keepalive": 30}, separators=(",", ":")),
            "tags": "go-e2e",
            "note": "Go self-consistency only",
            "sort": 0,
            "createdAt": now,
            "updatedAt": now,
            "deletedAt": None,
        }],
        "creds": [{"id": credential_id, "name": "go-e2e-root", "kind": "password", "secret": "s3cret-中文-go-e2e"}],
    }


def run_acceptance(binary: pathlib.Path, work: pathlib.Path) -> None:
    a = Instance(binary, work, "a", "go-e2e-master-a")
    b = Instance(binary, work, "b", "go-e2e-master-b-with-a-different-key")
    c = Instance(binary, work, "c", "go-e2e-master-c")
    try:
        print("[1] Start independent full Go servers", flush=True)
        a.start()
        b.start()
        health_a = a.health()
        health_b = b.health()
        check("both health endpoints identify the Go server", health_a.get("service") == health_b.get("service") == "nexterm-server", (health_a, health_b))
        check("full mode is not sync-only", health_a.get("syncOnly") is False and health_b.get("syncOnly") is False)
        check("full command surface includes sync commands", health_a.get("commands", 0) >= 3, health_a.get("commands"))

        print("[2] Token gateway and instance isolation", flush=True)
        token_a = str(data(a.port, "sync_token"))
        token_b = str(data(b.port, "sync_token"))
        check("tokens are present and instance-specific", bool(token_a) and bool(token_b) and token_a != token_b)
        check("CLI token command matches the RPC token", a.cli_token() == token_a, "cli token differs from /rpc sync_token")
        status, _ = call(b.port, "sync_digest", route="/sync/rpc")
        check("missing peer token returns 401", status == 401, status)
        status, _ = call(b.port, "sync_digest", token="wrong", route="/sync/rpc")
        check("wrong peer token returns 401", status == 401, status)
        status, _ = call(b.port, "sync_digest", token=token_a, route="/sync/rpc")
        check("A token is invalid on B", status == 401, status)
        status, envelope = call(b.port, "sync_digest", token=token_b, route="/sync/rpc")
        check("B token admits B peer RPC", status == 200 and envelope.get("ok") is True, envelope)

        print("[3] Real A export to real B import with credential re-encryption", flush=True)
        seed = synthetic_bundle()
        asset_id = seed["assets"][0]["id"]
        created_a = data(a.port, "sync_import", {"args": {"bundle": seed, "force": False}})
        check("A creates the synthetic Go bundle", created_a.get("assetsCreated") == 1 and created_a.get("credsCreated") == 1, created_a)
        exported_a = data(a.port, "sync_export", {"args": {"assetIds": [asset_id], "withCreds": True}})
        check("A export contains one asset/group/credential", tuple(len(exported_a.get(key, [])) for key in ("assets", "groups", "creds")) == (1, 1, 1), exported_a)
        created_b = data(b.port, "sync_import", {"args": {"bundle": exported_a, "force": False}}, token_b, "/sync/rpc")
        check("B creates one asset/group/credential", (created_b.get("assetsCreated"), created_b.get("groupsCreated"), created_b.get("credsCreated")) == (1, 1, 1), created_b)
        exported_b = data(b.port, "sync_export", {"args": {"assetIds": [asset_id], "withCreds": True}}, token_b, "/sync/rpc")
        check("B can decrypt and re-export the secret with its own master key", exported_b["creds"][0].get("secret") == seed["creds"][0]["secret"], exported_b)
        check("Go options JSON survives as JSON text", json.loads(exported_b["assets"][0]["optionsJson"]) == {"keepalive": 30}, exported_b["assets"][0])

        print("[4] Idempotent update and version gate", flush=True)
        repeated = data(b.port, "sync_import", {"args": {"bundle": exported_a, "force": False}}, token_b, "/sync/rpc")
        check("repeat import updates rather than duplicates", repeated.get("assetsCreated") == 0 and repeated.get("assetsUpdated") == 1, repeated)
        digest = data(b.port, "sync_digest", token=token_b, route="/sync/rpc")
        check("digest still has exactly one copy", len([entry for entry in digest.get("assets", []) if entry.get("id") == asset_id]) == 1, digest)
        forged = dict(exported_a)
        forged["protocol"] = 999
        status, envelope = call(b.port, "sync_import", {"args": {"bundle": forged, "force": True}}, token_b, "/sync/rpc")
        check("unsupported protocol is rejected", status == 200 and envelope.get("ok") is False and envelope.get("error", {}).get("code") == "unsupported", envelope)

        print("[5] Tombstone propagates in the reverse direction", flush=True)
        tombstone = dict(exported_b)
        tombstone["creds"] = []
        tombstone["exportedAt"] = int(time.time() * 1000) + 1000
        tombstone["assets"] = [dict(exported_b["assets"][0])]
        tombstone["assets"][0]["updatedAt"] = tombstone["exportedAt"]
        tombstone["assets"][0]["deletedAt"] = tombstone["exportedAt"]
        data(a.port, "sync_import", {"args": {"bundle": tombstone, "force": True}}, token_a, "/sync/rpc")
        deleted_a = data(a.port, "sync_export", {"args": {"assetIds": [asset_id], "withCreds": False}})
        check("A retains the propagated deletion marker", deleted_a["assets"][0].get("deletedAt") is not None, deleted_a)

        print("[6] Restart B as sync-only and verify the reduced attack surface", flush=True)
        b.start(sync_only=True)
        sync_health = b.health()
        check("sync-only health reports exactly three commands", sync_health.get("syncOnly") is True and sync_health.get("commands") == 3, sync_health)
        status, _ = call(b.port, "sync_digest")
        check("sync-only /rpc is absent", status == 404, status)
        status, envelope = call(b.port, "sync_digest", token=token_b, route="/sync/rpc")
        check("existing token survives the sync-only restart", status == 200 and envelope.get("ok") is True, envelope)
        status, envelope = call(b.port, "sync_token", token=token_b, route="/sync/rpc")
        check("peer route cannot enumerate full-server token command", status == 200 and envelope.get("ok") is False and envelope.get("error", {}).get("code") == "not_found", envelope)
        try:
            urllib.request.urlopen(f"http://127.0.0.1:{b.port}/", timeout=5)
        except urllib.error.HTTPError as error:
            status = error.code
        else:
            status = 200
        check("sync-only does not serve a browser UI", status == 404, status)

        print("[7] Exposed listener requires the token everywhere except healthz", flush=True)
        c.start(host="0.0.0.0")
        token_c = c.cli_token()
        check("CLI provisions the exposed instance token", bool(token_c))
        status, _ = call(c.port, "sync_digest")
        check("exposed /rpc rejects a missing token", status == 401, status)
        status, _ = call(c.port, "sync_digest", token="wrong")
        check("exposed /rpc rejects a wrong token", status == 401, status)
        status, envelope = call(c.port, "sync_digest", token=token_c)
        check("exposed /rpc admits the valid token", status == 200 and envelope.get("ok") is True, envelope)
        status, _ = call(c.port, "sync_digest", extra_headers={"X-HC-User-ID": "forged"})
        check("exposed /rpc rejects a forged platform identity header", status == 401, status)
        status, _ = call(c.port, "sync_digest", route="/sync/rpc")
        check("exposed /sync/rpc rejects a missing token", status == 401, status)
        status, envelope = call(c.port, "sync_digest", token=token_c, route="/sync/rpc")
        check("exposed /sync/rpc admits the valid token", status == 200 and envelope.get("ok") is True, envelope)
        status = raw_status(c.port, "/files/blob?name=e2e.txt", method="POST", body=b"payload")
        check("exposed /files/blob rejects a missing token", status == 401, status)
        status = raw_status(c.port, "/files/blob?name=e2e.txt", method="POST", body=b"payload", token=token_c)
        check("exposed /files/blob admits the valid token", status == 200, status)
        status = raw_status(c.port, "/healthz")
        check("exposed /healthz stays public", status == 200, status)
        rotated_c = c.cli_token(rotate=True)
        check("CLI rotate-token returns a fresh token", bool(rotated_c) and rotated_c != token_c)
        status, _ = call(c.port, "sync_digest", token=token_c)
        check("rotated-out token is rejected", status == 401, status)
        status, envelope = call(c.port, "sync_digest", token=rotated_c)
        check("rotated token is admitted", status == 200 and envelope.get("ok") is True, envelope)
    finally:
        a.stop()
        b.stop()
        c.stop()


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--bin", type=pathlib.Path, default=ROOT / "target/go-build/nexterm-server-e2e")
    parser.add_argument("--no-build", action="store_true")
    parser.add_argument("--keep", action="store_true", help="preserve synthetic data and logs even after success")
    arguments = parser.parse_args()
    binary = arguments.bin.resolve()
    if not arguments.no_build:
        build_server(binary)
    if not binary.is_file():
        parser.error(f"Go server binary does not exist: {binary}")
    work = ROOT / "target/e2e-sync"
    shutil.rmtree(work, ignore_errors=True)
    work.mkdir(parents=True)
    succeeded = False
    try:
        run_acceptance(binary, work)
        succeeded = not FAILED
    except Exception as error:
        FAILED.append(f"unhandled acceptance error: {error}")
        print(f"FAIL unhandled acceptance error: {error}", file=sys.stderr)
    total = len(PASSED) + len(FAILED)
    report = {
        "schema_version": 1,
        "mode": "go-self-consistency",
        "status": "passed" if succeeded else "failed",
        "passed": len(PASSED),
        "failed": len(FAILED),
        "assertions": {"passed": PASSED, "failed": FAILED},
        "skip_as_pass": False,
    }
    (work / "report.json").write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    print(f"Go sync acceptance: {len(PASSED)}/{total} passed; logs and report: {work}")
    if succeeded and not arguments.keep:
        for name in ("a", "b"):
            shutil.rmtree(work / name, ignore_errors=True)
    return 0 if succeeded else 1


if __name__ == "__main__":
    raise SystemExit(main())
