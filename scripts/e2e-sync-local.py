#!/usr/bin/env python3
"""v2 full-payload object protocol acceptance over real HTTP.

Builds CGO_ENABLED=0 by default, starts one real server with --auth=on,
initializes a real superadmin account through the console init-code flow,
then drives two device clients over the v2 object protocol: push/pull/ids
with session cookies and CSRF, idempotent re-push, stale-head fork
detection, per-user isolation, ciphertext-at-rest checks straight from the
server SQLite file, and a restart persistence pass. Object envelopes are
sealed/opened with scripts/syncv2-helper (real AES-GCM + Argon2id from the
same module), so the server is verified blind end to end. There are no
third-party Python dependencies and no skip-as-pass paths. Every run writes
target/e2e-sync/report.json (status, passed/failed counts, per-check details,
harness errors) and the server log; the Release gate accepts only a clean pass.

    python3 scripts/e2e-sync-local.py
    python3 scripts/e2e-sync-local.py --bin target/go-build/nexterm-server-linux-amd64 --no-build
"""

from __future__ import annotations

import argparse
import base64
import hashlib
import json
import os
import pathlib
import shutil
import socket
import sqlite3
import subprocess
import sys
import tempfile
import time
import traceback
import urllib.error
import urllib.request
import uuid
from datetime import datetime, timezone

ROOT = pathlib.Path(__file__).resolve().parent.parent
SESSION_COOKIE = "nexterm_session"
CSRF_HEADER = "X-NexTerm-CSRF"
GENESIS_PREFIX = "nexterm/go/sync-head/v1"
CHECKS: list[dict] = []
REPORT_DIR = ROOT / "target" / "e2e-sync"


def check(name: str, ok: bool, detail: object = "") -> None:
    if ok:
        CHECKS.append({"name": name, "status": "passed", "detail": ""})
        print(f"  PASS {name}", flush=True)
    else:
        CHECKS.append({"name": name, "status": "failed", "detail": str(detail)})
        print(f"  FAIL {name}: {detail}", flush=True)


def free_port() -> int:
    with socket.socket() as listener:
        listener.bind(("127.0.0.1", 0))
        return int(listener.getsockname()[1])


def genesis_head(user_id: str) -> str:
    digest = hashlib.sha256(GENESIS_PREFIX.encode() + b"\x00" + user_id.encode()).hexdigest()
    return digest


class Client:
    """一个 v2 同步设备: 会话 cookie + CSRF 头的最小 HTTP 客户端。"""

    def __init__(self, port: int):
        self.port = port
        self.cookie: str | None = None
        self.csrf: str | None = None
        self.user_id: str | None = None

    def request(self, method: str, path: str, payload: object = None, csrf: bool = False) -> tuple[int, dict]:
        body = json.dumps(payload).encode() if payload is not None else None
        request = urllib.request.Request(f"http://127.0.0.1:{self.port}{path}", data=body, method=method)
        request.add_header("content-type", "application/json")
        if self.cookie:
            request.add_header("Cookie", f"{SESSION_COOKIE}={self.cookie}")
        if csrf and self.csrf:
            request.add_header(CSRF_HEADER, self.csrf)
        try:
            with urllib.request.urlopen(request, timeout=30) as response:
                return response.status, json.loads(response.read())
        except urllib.error.HTTPError as error:
            raw = error.read()
            try:
                return error.code, json.loads(raw)
            except (ValueError, TypeError):
                return error.code, {"raw": raw.decode(errors="replace")[:500]}

    def login(self, username: str, password: str) -> None:
        request = urllib.request.Request(
            f"http://127.0.0.1:{self.port}/auth/login",
            data=json.dumps({"username": username, "password": password}).encode(),
            headers={"content-type": "application/json"},
            method="POST",
        )
        with urllib.request.urlopen(request, timeout=30) as response:
            body = json.loads(response.read())
            for header, value in response.headers.items():
                if header.lower() == "set-cookie" and value.startswith(f"{SESSION_COOKIE}="):
                    self.cookie = value.split(";", 1)[0].split("=", 1)[1]
        if not self.cookie:
            raise AssertionError("login response has no session cookie")
        self.csrf = body.get("csrf_token", "")
        self.user_id = body.get("user", {}).get("id", "")

    def me(self) -> dict:
        status, body = self.request("GET", "/auth/me")
        if status != 200:
            raise AssertionError(f"me: HTTP {status} {body}")
        self.user_id = body.get("user", {}).get("id", "")
        return body


class Instance:
    def __init__(self, binary: pathlib.Path, work: pathlib.Path, name: str, master_key: str):
        self.binary = binary
        self.data_dir = work / name
        self.log_path = work / f"{name}.log"
        self.master_key = master_key
        self.port = free_port()
        self.process: subprocess.Popen | None = None
        self.log_handle = None

    def start(self) -> None:
        self.stop()
        self.data_dir.mkdir(parents=True, exist_ok=True)
        key_file = self.data_dir.parent / f"{self.data_dir.name}.master.key"
        key_file.write_text(self.master_key + "\n", encoding="utf-8")
        key_file.chmod(0o600)
        environment = dict(os.environ)
        environment.pop("NEXTERM_MASTER_KEY", None)
        environment.update(
            {
                "NEXTERM_DATA_DIR": str(self.data_dir),
                "NEXTERM_LISTEN": f"127.0.0.1:{self.port}",
                "NEXTERM_MASTER_KEY_FILE": str(key_file),
                "NEXTERM_AUTH": "on",
            }
        )
        arguments = [
            str(self.binary), "--listen", f"127.0.0.1:{self.port}", "--data-dir", str(self.data_dir), "--auth=on",
        ]
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

    def init_code(self) -> str:
        deadline = time.monotonic() + 15
        marker = "一次性初始化码: "
        while time.monotonic() < deadline:
            text = self.log_path.read_text(encoding="utf-8", errors="replace") if self.log_path.exists() else ""
            for line in text.splitlines():
                if marker in line:
                    return line.split(marker, 1)[1].strip()
            time.sleep(0.2)
        raise AssertionError(f"init code not found; log={self.log_path}")

    def health(self) -> dict:
        with urllib.request.urlopen(f"http://127.0.0.1:{self.port}/healthz", timeout=5) as response:
            return json.loads(response.read())

    def db_path(self) -> pathlib.Path:
        return self.data_dir / "data.db"

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


def build(binary: pathlib.Path, package: str) -> None:
    version = json.loads((ROOT / "wails.json").read_text(encoding="utf-8"))["info"]["version"]
    binary.parent.mkdir(parents=True, exist_ok=True)
    environment = dict(os.environ)
    environment.update({"CGO_ENABLED": "0", "GOTOOLCHAIN": "go1.26.8"})
    command = [
        "go", "build", "-mod=readonly", "-trimpath", "-buildvcs=false",
        "-o", str(binary), package,
    ]
    if package == "./cmd/nexterm-server":
        command[5:5] = ["-ldflags", f"-s -w -X github.com/ProbiusOfficial/NexTerm/internal/version.Version={version}"]
    subprocess.run(command, cwd=ROOT, env=environment, check=True)


def helper_run(helper: pathlib.Path, arguments: list[str], stdin: str = "") -> str:
    result = subprocess.run(
        [str(helper), *arguments], input=stdin, capture_output=True, text=True, timeout=120, cwd=ROOT,
    )
    if result.returncode != 0:
        raise AssertionError(f"helper {arguments[0]} failed: {result.stderr.strip()}")
    return result.stdout.strip()


def synthetic_id() -> str:
    return f"e2e{uuid.uuid4().hex[:23]}"


def write_report(binary: pathlib.Path, helper: pathlib.Path, started_at: str, started: float, harness_errors: list[str]) -> pathlib.Path:
    failed = [entry for entry in CHECKS if entry["status"] != "passed"]
    report = {
        "schema_version": 1,
        "mode": "sync-v2-local",
        "status": "failed" if failed or harness_errors else "passed",
        "passed": len(CHECKS) - len(failed),
        "failed": len(failed),
        "checks": CHECKS,
        "harness_errors": harness_errors,
        "binary": str(binary),
        "helper": str(helper),
        "started_at": started_at,
        "finished_at": datetime.now(timezone.utc).isoformat(timespec="seconds"),
        "duration_seconds": round(time.monotonic() - started, 3),
        "log": "target/e2e-sync/server.log",
        "skip_as_pass": False,
    }
    REPORT_DIR.mkdir(parents=True, exist_ok=True)
    temporary = REPORT_DIR / "report.json.tmp"
    temporary.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    os.replace(temporary, REPORT_DIR / "report.json")
    return REPORT_DIR / "report.json"


def record_harness_error(harness_errors: list[str], stage: str, error: Exception) -> None:
    traceback.print_exc()
    message = f"{stage}: {type(error).__name__}: {error}"
    harness_errors.append(message)
    check("e2e 执行异常", False, message)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--bin", type=pathlib.Path, default=ROOT / "target/go-build/nexterm-server-e2e")
    parser.add_argument("--helper", type=pathlib.Path, default=ROOT / "target/go-build/syncv2-helper-e2e")
    parser.add_argument("--no-build", action="store_true", help="reuse existing binaries")
    arguments = parser.parse_args()

    started = time.monotonic()
    started_at = datetime.now(timezone.utc).isoformat(timespec="seconds")
    harness_errors: list[str] = []
    server: Instance | None = None
    work: pathlib.Path | None = None
    try:
        if arguments.no_build:
            if not arguments.helper.exists():
                print(f"--no-build: helper missing, building {arguments.helper}", flush=True)
                build(arguments.helper, "./scripts/syncv2-helper")
        else:
            build(arguments.bin, "./cmd/nexterm-server")
            build(arguments.helper, "./scripts/syncv2-helper")

        work = pathlib.Path(tempfile.mkdtemp(prefix="nexterm-sync-v2-e2e-"))
        master_key = f"e2e-master-{uuid.uuid4().hex}"
        server = Instance(arguments.bin, work, "server", master_key)
        alice_password = "alice-e2e-pw-123"
        bob_password = "bob-e2e-pw-1234"
        server.start()
        print("== 账号初始化 ==", flush=True)
        init_code = server.init_code()
        alice_dek_bundle = json.loads(helper_run(arguments.helper, ["gen-dek", alice_password]))
        init_payload = {
            "code": init_code,
            "username": "alice",
            "password": alice_password,
            "dek_envelope": alice_dek_bundle["dek_envelope"],
            "kdf_salt": alice_dek_bundle["kdf_salt"],
            "kdf_params": alice_dek_bundle["kdf_params"],
            "recovery_envelope": alice_dek_bundle["recovery_envelope"],
            "recovery_hash": alice_dek_bundle["recovery_hash"],
        }
        status, body = Client(server.port).request("POST", "/auth/init", init_payload)
        check("超管初始化", status == 200 and body.get("user", {}).get("username") == "alice", f"HTTP {status} {body}")

        alice = Client(server.port)
        alice.login("alice", alice_password)
        alice.me()
        check("登录并获取用户 ID", bool(alice.user_id), f"user_id={alice.user_id}")
        alice_dek = alice_dek_bundle["dek"]

        print("== 设备一推送密文对象 ==", flush=True)
        now = int(time.time() * 1000)
        group_id = synthetic_id()
        asset_id = synthetic_id()
        credential_id = synthetic_id()
        snippet_id = synthetic_id()
        known_host_id = synthetic_id()
        ai_profile_id = synthetic_id()
        payloads = {
            group_id: ("group", {"id": group_id, "name": "E2E 生产-canary-组", "sort": 0, "createdAt": now, "updatedAt": now}),
            credential_id: ("credential", {"id": credential_id, "name": "e2e-canary-凭据", "kind": "password", "secret": "e2e-canary-secret-值", "updatedAt": now}),
            snippet_id: ("snippet", {"id": snippet_id, "name": "e2e-canary-片段", "body": "echo e2e-canary", "sort": 0, "createdAt": now, "updatedAt": now}),
            asset_id: ("asset", {"id": asset_id, "groupId": group_id, "kind": "ssh", "name": "e2e-canary-资产", "host": "192.0.2.10", "port": 2222, "username": "root", "credId": credential_id, "optionsJson": "{}", "tags": "", "note": "", "sort": 0, "createdAt": now, "updatedAt": now}),
            known_host_id: ("known_host", {"id": known_host_id, "host": "e2e-canary-host.internal", "port": 22, "keyType": "ssh-ed25519", "fingerprint": "SHA256:e2e-canary-fingerprint-值", "addedAt": now}),
            ai_profile_id: ("ai_profile", {"id": ai_profile_id, "name": "e2e-canary-档案", "baseUrl": "https://e2e-canary-ai.example.com", "apiKey": "e2e-canary-api-key-值", "model": "e2e-model", "fallbackModel": "e2e-fallback", "temperature": 0.5, "contextWindow": 64000, "maxTokens": 2048, "proxy": None, "stream": True, "requestTimeoutSeconds": 60, "idleTimeoutSeconds": 15, "circuitFailureThreshold": 3, "circuitCooldownSeconds": 30, "updatedAt": now}),
        }
        objects = []
        for object_id, (kind, payload) in payloads.items():
            blob = helper_run(arguments.helper, ["seal", alice_dek, object_id, kind], json.dumps(payload, ensure_ascii=False))
            objects.append({"id": object_id, "blob": blob})
        status, body = alice.request("POST", "/sync/v2/push", {"protocol": 2, "known_head": genesis_head(alice.user_id), "objects": objects}, csrf=True)
        pushed = body if isinstance(body, dict) else {}
        check("设备一首批推送", status == 200 and pushed.get("applied") == 6, f"HTTP {status} {body}")
        head = pushed.get("head", "")

        print("== 服务端密文与盲存检查 ==", flush=True)
        canaries = ["E2E 生产-canary-组", "e2e-canary-凭据", "e2e-canary-secret-值", "e2e-canary-片段", "e2e-canary-资产", "192.0.2.10", "e2e-canary-host.internal", "e2e-canary-fingerprint-值", "e2e-canary-档案", "e2e-canary-api-key-值"]
        connection = sqlite3.connect(server.db_path())
        rows = connection.execute("SELECT id, seq, blob FROM user_sync_object ORDER BY seq").fetchall()
        blobs = {row[0]: row[2] for row in rows}
        check("服务端对象行数", len(rows) == 6, f"rows={len(rows)}")
        leak = [canary for canary in canaries for blob in blobs.values() if canary.encode() in blob]
        check("服务端行不含明文", not leak, f"leaks={leak}")
        seqs = [row[1] for row in rows]
        check("seq 单调连续", seqs == sorted(seqs) and len(set(seqs)) == 6, f"seqs={seqs}")
        head_row = connection.execute("SELECT head_hash FROM user_sync_head WHERE user_id = ?", (alice.user_id,)).fetchone()
        check("head 行存在且为哈希", bool(head_row) and head_row[0] == head and len(head_row[0]) == 64, f"head_row={head_row}")
        connection.close()

        print("== 幂等重推 ==", flush=True)
        status, body = alice.request("POST", "/sync/v2/push", {"protocol": 2, "known_head": head, "objects": objects}, csrf=True)
        check("重复推送幂等", status == 200 and body.get("applied") == 0 and body.get("skipped") == 6 and body.get("head") == head, f"HTTP {status} {body}")

        print("== 设备二拉取与端到端解密 ==", flush=True)
        device2 = Client(server.port)
        device2.login("alice", alice_password)
        device2.me()
        status, body = device2.request("POST", "/sync/v2/pull", {"protocol": 2, "since_seq": 0})
        pulled = body.get("objects", []) if isinstance(body, dict) else []
        check("设备二全量拉取", status == 200 and len(pulled) == 6, f"HTTP {status} {body}")
        roundtrip_ok = True
        for object_id, (kind, payload) in payloads.items():
            entry = next((item for item in pulled if item.get("id") == object_id), None)
            if entry is None:
                roundtrip_ok = False
                continue
            opened = helper_run(arguments.helper, ["open", alice_dek, object_id, kind], entry.get("blob", ""))
            if json.loads(opened) != payload:
                roundtrip_ok = False
        check("拉取对象可端到端解密", roundtrip_ok, "open mismatch")

        print("== 分叉/回滚检测 ==", flush=True)
        stale_head = genesis_head(alice.user_id)
        status, body = device2.request("POST", "/sync/v2/push", {"protocol": 2, "known_head": stale_head, "objects": objects[:1]}, csrf=True)
        check("过期 head 推送被拒绝(409)", status == 409, f"HTTP {status} {body}")
        status, body = device2.request("POST", "/sync/v2/pull", {"protocol": 2, "since_seq": 0})
        fresh_head = body.get("head", "") if isinstance(body, dict) else ""
        status, body = device2.request("POST", "/sync/v2/push", {"protocol": 2, "known_head": fresh_head, "objects": objects[:1]}, csrf=True)
        check("拉取合并后重推成功", status == 200 and body.get("skipped") == 1, f"HTTP {status} {body}")

        print("== ids 清单 ==", flush=True)
        status, body = device2.request("POST", "/sync/v2/ids", {"protocol": 2})
        entries = body.get("entries", []) if isinstance(body, dict) else []
        id_ok = status == 200 and len(entries) == 6 and all(len(entry.get("blob_hash", "")) == 64 for entry in entries)
        check("ids 清单返回 blob 哈希", id_ok, f"HTTP {status} {body}")

        print("== 多用户隔离 ==", flush=True)
        status, body = alice.request("POST", "/admin/users", {"username": "bob", "password": bob_password, "display_name": "bob"}, csrf=True)
        check("超管创建 bob", status == 200 and body.get("user", {}).get("username") == "bob", f"HTTP {status} {body}")
        bob_dek_bundle = json.loads(helper_run(arguments.helper, ["gen-dek", bob_password]))
        bob = Client(server.port)
        bob.login("bob", bob_password)
        bob.me()
        status, body = bob.request("POST", "/auth/dek", {
            "dek_envelope": bob_dek_bundle["dek_envelope"],
            "kdf_salt": bob_dek_bundle["kdf_salt"],
            "kdf_params": bob_dek_bundle["kdf_params"],
            "recovery_envelope": bob_dek_bundle["recovery_envelope"],
            "recovery_hash": bob_dek_bundle["recovery_hash"],
        }, csrf=True)
        check("bob 上传 DEK 信封", status == 200, f"HTTP {status} {body}")
        status, body = bob.request("POST", "/sync/v2/pull", {"protocol": 2, "since_seq": 0})
        bob_entries = body.get("objects", []) if isinstance(body, dict) else []
        check("bob 拉取为空(隔离)", status == 200 and len(bob_entries) == 0, f"HTTP {status} {body}")
        alice_blob = blobs[asset_id]
        wrong_key_failed = False
        try:
            helper_run(arguments.helper, ["open", bob_dek_bundle["dek"], asset_id, "asset"], base64.b64encode(alice_blob).decode())
        except AssertionError:
            wrong_key_failed = True
        check("bob 的 DEK 无法解开 alice 对象", wrong_key_failed, "wrong key opened alice blob")

        print("== 重启持久化 ==", flush=True)
        server.stop()
        server.start()
        device3 = Client(server.port)
        device3.login("alice", alice_password)
        device3.me()
        status, body = device3.request("POST", "/sync/v2/pull", {"protocol": 2, "since_seq": 0})
        check("重启后对象仍在", status == 200 and len(body.get("objects", [])) == 6, f"HTTP {status} {body}")
    except Exception as error:
        record_harness_error(harness_errors, "run", error)
    finally:
        if server is not None:
            try:
                server.stop()
            except Exception as error:
                record_harness_error(harness_errors, "server.stop", error)
            try:
                if server.log_path.exists():
                    REPORT_DIR.mkdir(parents=True, exist_ok=True)
                    shutil.copy(server.log_path, REPORT_DIR / "server.log")
            except Exception as error:
                record_harness_error(harness_errors, "server.log", error)
        if work is not None:
            try:
                shutil.rmtree(work, ignore_errors=True)
            except Exception as error:
                record_harness_error(harness_errors, "workdir", error)
        report_path = write_report(arguments.bin, arguments.helper, started_at, started, harness_errors)
    failed = [entry["name"] for entry in CHECKS if entry["status"] != "passed"]
    print(f"\n通过 {len(CHECKS) - len(failed)} 项, 失败 {len(failed)} 项", flush=True)
    print(f"报告: {report_path}", flush=True)
    if failed:
        for name in failed:
            print(f"  FAILED: {name}", flush=True)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
