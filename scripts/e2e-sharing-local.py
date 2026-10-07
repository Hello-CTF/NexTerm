#!/usr/bin/env python3
"""SHARE138 live sharing process-level acceptance over real processes.

Builds the real nexterm-server binary once, starts one real server with
--auth=on, initializes a superadmin, enrolls and runs the real device agent,
then drives the M127 sharing contracts through the M136 outbound bridge:

  1. public read-only link: viewer attaches to a live session, output flows,
     input is silently discarded (default read-only)
  2. public read_write link: input reaches the session (echo round trip)
  3. revoke: live viewer is stopped with an error frame, re-resolve denied
  4. expiry: short-TTL link stops the live viewer at expiry
  5. registered host share: recipient opens a NEW terminal through the host
     agent; permission shrink read_write -> read stops input but keeps output
  6. daemon offline: live stream drops with the bridge, new opens get 503
  7. disclosure guards: management/sync routes stay behind account auth and
     share views carry no token material
  8. public viewer page: plain GET /share/public/{token} serves the configured
     SPA/index (no token validation, no-store) while the WS upgrade keeps the
     token gate

No third-party Python dependencies, no external network, no service
installation (desired_autostart is pinned false before `agent run`), no
tmux/X11/WebView. The detached supervisor helper is killed via helper.pid
during cleanup.

    python3 scripts/e2e-sharing-local.py
    python3 scripts/e2e-sharing-local.py --bin target/go-build/nexterm-server-sharing-e2e --no-build
"""

from __future__ import annotations

import argparse
import base64
import hashlib
import json
import os
import pathlib
import signal
import socket
import struct
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request
import uuid

ROOT = pathlib.Path(__file__).resolve().parent.parent
SESSION_COOKIE = "nexterm_session"
CSRF_HEADER = "X-NexTerm-CSRF"
PASSED: list[str] = []
FAILED: list[str] = []

LINK_VIEW_KEYS = {
    "id", "owner_id", "device_id", "session_id", "permission",
    "created_at", "expires_at", "revoked_at", "last_accessed_at",
}
HOST_SHARE_VIEW_KEYS = {
    "id", "owner_id", "owner_username", "device_id", "recipient_id",
    "recipient_username", "permission", "created_at", "expires_at", "revoked_at",
}
READY_FRAME_KEYS = {"type", "session_id", "permission", "expires_at"}


def check(name: str, ok: bool, detail: object = "") -> None:
    if ok:
        PASSED.append(name)
        print(f"  PASS {name}", flush=True)
    else:
        FAILED.append(name)
        print(f"  FAIL {name}: {detail}", flush=True)


def free_port() -> int:
    with socket.socket() as listener:
        listener.bind(("127.0.0.1", 0))
        return int(listener.getsockname()[1])


class Client:
    """会话 cookie + CSRF 头的最小 HTTP 客户端。"""

    def __init__(self, port: int):
        self.port = port
        self.cookie: str | None = None
        self.csrf: str | None = None

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


class Instance:
    def __init__(self, binary: pathlib.Path, work: pathlib.Path, name: str, master_key: str, auth: str, web_root: pathlib.Path | None = None):
        self.binary = binary
        self.data_dir = work / name
        self.log_path = work / f"{name}.log"
        self.master_key = master_key
        self.auth = auth
        self.web_root = web_root
        self.port = free_port()
        self.process: subprocess.Popen | None = None
        self.log_handle = None

    def start(self) -> None:
        self.stop()
        self.data_dir.mkdir(parents=True, exist_ok=True)
        environment = dict(os.environ)
        environment.update(
            {
                "NEXTERM_DATA_DIR": str(self.data_dir),
                "NEXTERM_LISTEN": f"127.0.0.1:{self.port}",
                "NEXTERM_MASTER_KEY": self.master_key,
                "NEXTERM_AUTH": self.auth,
            }
        )
        arguments = [
            str(self.binary), "--listen", f"127.0.0.1:{self.port}", "--data-dir", str(self.data_dir), f"--auth={self.auth}",
        ]
        if self.web_root is not None:
            arguments += ["--web-root", str(self.web_root)]
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


class WebSocketHTTPError(Exception):
    def __init__(self, status: int, body: bytes):
        super().__init__(f"websocket handshake rejected: HTTP {status}")
        self.status = status
        self.body = body


class WebSocket:
    """最小 stdlib WS 客户端: 二进制帧 + 文本帧, 自动应答 ping。"""

    def __init__(self, port: int, path: str, headers: dict[str, str] | None = None, timeout: float = 10.0):
        self.sock = socket.create_connection(("127.0.0.1", port), timeout=timeout)
        self.sock.settimeout(timeout)
        key = base64.b64encode(os.urandom(16)).decode()
        lines = [
            f"GET {path} HTTP/1.1",
            f"Host: 127.0.0.1:{port}",
            "Upgrade: websocket",
            "Connection: Upgrade",
            f"Sec-WebSocket-Key: {key}",
            "Sec-WebSocket-Version: 13",
        ]
        for name, value in (headers or {}).items():
            lines.append(f"{name}: {value}")
        self.sock.sendall(("\r\n".join(lines) + "\r\n\r\n").encode())
        status, response_headers, remainder = self._read_handshake()
        if status != 101:
            raise WebSocketHTTPError(status, remainder)
        self.buffer = remainder

    def _read_handshake(self) -> tuple[int, dict[str, str], bytes]:
        data = b""
        while b"\r\n\r\n" not in data:
            chunk = self.sock.recv(4096)
            if not chunk:
                raise AssertionError("websocket handshake connection closed")
            data += chunk
        head, _, remainder = data.partition(b"\r\n\r\n")
        lines = head.decode(errors="replace").split("\r\n")
        status = int(lines[0].split(" ", 2)[1])
        headers = {}
        for line in lines[1:]:
            name, _, value = line.partition(":")
            headers[name.strip().lower()] = value.strip()
        return status, headers, remainder

    def _read_exact(self, count: int) -> bytes:
        while len(self.buffer) < count:
            chunk = self.sock.recv(65536)
            if not chunk:
                raise AssertionError("websocket connection closed")
            self.buffer += chunk
        result, self.buffer = self.buffer[:count], self.buffer[count:]
        return result

    def send(self, opcode: int, payload: bytes) -> None:
        mask = os.urandom(4)
        header = bytes([0x80 | opcode])
        length = len(payload)
        if length < 126:
            header += bytes([0x80 | length])
        elif length < 65536:
            header += bytes([0x80 | 126]) + struct.pack(">H", length)
        else:
            header += bytes([0x80 | 127]) + struct.pack(">Q", length)
        masked = bytes(byte ^ mask[index % 4] for index, byte in enumerate(payload))
        self.sock.sendall(header + mask + masked)

    def send_binary(self, payload: bytes) -> None:
        self.send(0x2, payload)

    def recv(self, timeout: float | None = None) -> tuple[int, bytes]:
        if timeout is not None:
            self.sock.settimeout(timeout)
        while True:
            first, second = self._read_exact(2)
            opcode = first & 0x0F
            length = second & 0x7F
            if length == 126:
                length = struct.unpack(">H", self._read_exact(2))[0]
            elif length == 127:
                length = struct.unpack(">Q", self._read_exact(8))[0]
            payload = self._read_exact(length)
            if opcode == 0x9:  # ping -> pong
                self.send(0xA, payload)
                continue
            if opcode == 0xA:  # pong
                continue
            return opcode, payload

    def close(self) -> None:
        try:
            self.send(0x8, b"")
        except OSError:
            pass
        try:
            self.sock.close()
        except OSError:
            pass


# supervisor 协议帧类型 (internal/supervisor/protocol.go)
FRAME_HELLO = 1
FRAME_HELLO_ACK = 2
FRAME_ERROR = 3
FRAME_CREATE = 4
FRAME_CREATED = 5
FRAME_DETACH = 18
SUPERVISOR_PROTOCOL = 2


class SupervisorStream:
    """把 relay WS 的二进制消息当作字节流, 读写 supervisor 协议帧。"""

    def __init__(self, ws: WebSocket):
        self.ws = ws
        self.buffer = b""

    def send_frame(self, kind: int, message: object) -> None:
        payload = json.dumps(message).encode()
        header = struct.pack(">I", len(payload)) + bytes([kind])
        self.ws.send_binary(header + payload)

    def read_frame(self, timeout: float = 10.0) -> tuple[int, bytes]:
        deadline = time.monotonic() + timeout
        while True:
            if len(self.buffer) >= 5:
                length = struct.unpack(">I", self.buffer[:4])[0]
                if len(self.buffer) >= 5 + length:
                    kind = self.buffer[4]
                    payload = self.buffer[5 : 5 + length]
                    self.buffer = self.buffer[5 + length :]
                    return kind, payload
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                raise AssertionError("supervisor frame read timed out")
            self.ws.sock.settimeout(remaining)
            opcode, payload = self.ws.recv()
            if opcode == 0x8:
                raise AssertionError("relay websocket closed mid-stream")
            if opcode == 0x2:
                self.buffer += payload

    def hello(self, state_digest: str) -> None:
        self.send_frame(FRAME_HELLO, {"version": SUPERVISOR_PROTOCOL, "state_digest": state_digest})
        kind, payload = self.read_frame()
        if kind != FRAME_HELLO_ACK:
            raise AssertionError(f"supervisor hello failed: frame {kind} payload={payload.decode(errors='replace')}")

    def create(self, command: list[str], cols: int = 80, rows: int = 24) -> dict:
        self.send_frame(FRAME_CREATE, {"command": command, "cols": cols, "rows": rows})
        kind, payload = self.read_frame()
        if kind == FRAME_ERROR:
            raise AssertionError(f"create failed: {payload.decode(errors='replace')}")
        if kind != FRAME_CREATED:
            raise AssertionError(f"create: unexpected frame {kind}")
        return json.loads(payload)

    def detach(self) -> None:
        self.send_frame(FRAME_DETACH, {})


class ShareViewer:
    """分享 viewer WS 的帧读取辅助: 首个文本帧是 ready, 之后二进制是输出。"""

    def __init__(self, ws: WebSocket):
        self.ws = ws

    def expect_ready(self) -> dict:
        opcode, payload = self.ws.recv(timeout=15)
        if opcode != 0x1:
            raise AssertionError(f"ready frame: opcode {opcode} payload={payload[:200]!r}")
        frame = json.loads(payload)
        if frame.get("type") != "ready":
            raise AssertionError(f"ready frame: {frame}")
        return frame

    def read_output(self, timeout: float = 10.0) -> bytes:
        opcode, payload = self.ws.recv(timeout=timeout)
        if opcode == 0x8:
            raise AssertionError("viewer websocket closed")
        if opcode == 0x1:
            frame = json.loads(payload)
            if frame.get("type") == "error":
                raise AssertionError(f"viewer error frame: {frame}")
            return b""
        return payload

    def expect_output(self, needle: bytes, timeout: float = 15.0) -> None:
        deadline = time.monotonic() + timeout
        seen = b""
        while time.monotonic() < deadline:
            seen += self.read_output(timeout=max(0.2, deadline - time.monotonic()))
            if needle in seen:
                return
        raise AssertionError(f"output never contained {needle!r}: {seen[-300:]!r}")

    def expect_silent(self, duration: float) -> None:
        deadline = time.monotonic() + duration
        while time.monotonic() < deadline:
            try:
                payload = self.read_output(timeout=max(0.2, deadline - time.monotonic()))
            except (TimeoutError, socket.timeout):
                return
            if payload:
                raise AssertionError(f"expected silence, got {payload!r}")

    def expect_error(self, timeout: float) -> dict:
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            try:
                opcode, payload = self.ws.recv(timeout=max(0.2, deadline - time.monotonic()))
            except (TimeoutError, socket.timeout):
                break
            if opcode == 0x8:
                break
            if opcode == 0x1:
                frame = json.loads(payload)
                if frame.get("type") == "error":
                    return frame
        raise AssertionError("no error frame arrived")

    def expect_closed(self, timeout: float) -> None:
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            try:
                opcode, _ = self.ws.recv(timeout=max(0.2, deadline - time.monotonic()))
            except (TimeoutError, socket.timeout):
                continue
            except AssertionError:
                return  # connection closed (empty read)
            if opcode == 0x8:
                return
        raise AssertionError("viewer websocket was not closed in time")


def wait_until(deadline_s: float, what: str, condition) -> None:
    deadline = time.monotonic() + deadline_s
    while time.monotonic() < deadline:
        if condition():
            return
        time.sleep(0.2)
    raise AssertionError(f"timed out waiting for {what}")


def ws_handshake_status(port: int, path: str) -> int:
    """WS 握手结果状态码: 握手被拒返回服务端 HTTP 状态, 意外成功返回 101。"""
    try:
        ws = WebSocket(port, path)
    except WebSocketHTTPError as error:
        return error.status
    ws.close()
    return 101


def plain_get(port: int, path: str) -> tuple[int, str, str]:
    """无 upgrade 头的普通 GET, 返回 (状态码, Cache-Control, 响应体文本)。"""
    request = urllib.request.Request(f"http://127.0.0.1:{port}{path}", method="GET")
    with urllib.request.urlopen(request, timeout=30) as response:
        return response.status, response.headers.get("Cache-Control", ""), response.read().decode()


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--bin", type=pathlib.Path, default=ROOT / "target/go-build/nexterm-server-sharing-e2e")
    parser.add_argument("--helper", type=pathlib.Path, default=ROOT / "target/go-build/syncv2-helper-sharing-e2e")
    parser.add_argument("--no-build", action="store_true", help="reuse existing binaries")
    arguments = parser.parse_args()

    if not arguments.no_build:
        build(arguments.bin, "./cmd/nexterm-server")
        build(arguments.helper, "./scripts/syncv2-helper")

    work = pathlib.Path(tempfile.mkdtemp(prefix="nexterm-sharing-e2e-"))
    master_key = f"e2e-master-{uuid.uuid4().hex}"
    # 真实 server 的静态入口: 一个最小 web root, 断言 plain GET 服务的就是它。
    web_root = work / "web"
    web_root.mkdir()
    (web_root / "index.html").write_text(
        "<html><head><title>NexTerm</title></head><body>share-e2e</body></html>", encoding="utf-8"
    )
    server = Instance(arguments.bin, work, "server", master_key, "on", web_root)
    agent_dir = work / "agent"
    agent_log = (work / "agent.log").open("ab")
    agent_process: subprocess.Popen | None = None
    alice_password = "alice-e2e-pw-123"
    bob_password = "bob-e2e-pw-123"
    carol_password = "carol-e2e-pw-123"

    def stop_agent() -> None:
        nonlocal agent_process
        if agent_process is not None and agent_process.poll() is None:
            agent_process.terminate()
            try:
                agent_process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                agent_process.kill()
                agent_process.wait(timeout=3)
        agent_process = None

    def cleanup_helper() -> None:
        pid_file = agent_dir / "durable" / "supervisor" / "helper.pid"
        try:
            pid = int(pid_file.read_text().strip())
        except (OSError, ValueError):
            return
        try:
            os.kill(pid, signal.SIGTERM)
        except OSError:
            pass

    try:
        server.start()
        print("== 超管初始化 ==", flush=True)
        init_code = server.init_code()
        dek_bundle = json.loads(helper_run(arguments.helper, ["gen-dek", alice_password]))
        status, body = Client(server.port).request("POST", "/auth/init", {
            "code": init_code,
            "username": "alice",
            "password": alice_password,
            "dek_envelope": dek_bundle["dek_envelope"],
            "kdf_salt": dek_bundle["kdf_salt"],
            "kdf_params": dek_bundle["kdf_params"],
            "recovery_envelope": dek_bundle["recovery_envelope"],
            "recovery_hash": dek_bundle["recovery_hash"],
        })
        check("超管初始化", status == 200 and body.get("user", {}).get("username") == "alice", f"HTTP {status} {body}")
        alice = Client(server.port)
        alice.login("alice", alice_password)

        print("== 用户与设备准备 ==", flush=True)
        status, body = alice.request("POST", "/admin/users", {"username": "bob", "password": bob_password}, csrf=True)
        check("超管创建 bob", status == 200 and body.get("user", {}).get("username") == "bob", f"HTTP {status} {body}")
        bob_id = body.get("user", {}).get("id", "")
        status, body = alice.request("POST", "/admin/users", {"username": "carol", "password": carol_password}, csrf=True)
        check("超管创建 carol", status == 200 and body.get("user", {}).get("username") == "carol", f"HTTP {status} {body}")
        bob = Client(server.port)
        bob.login("bob", bob_password)
        carol = Client(server.port)
        carol.login("carol", carol_password)

        status, body = alice.request("PUT", "/fleet/base-urls", {"base_urls": [{"url": f"http://127.0.0.1:{server.port}"}]}, csrf=True)
        check("配置接入地址", status == 200 and len(body.get("base_urls", [])) == 1, f"HTTP {status} {body}")
        status, body = alice.request("POST", "/device/enroll-codes", {"ttl_ms": 600000}, csrf=True)
        check("签发接入码", status == 200 and bool(body.get("code")), f"HTTP {status} {body}")
        enroll_code = body.get("code", "")

        enroll = subprocess.run(
            [str(arguments.bin), "agent", "enroll", "--server", f"http://127.0.0.1:{server.port}",
             "--code", enroll_code, "--data-dir", str(agent_dir), "--name", "sharing-box"],
            capture_output=True, text=True, timeout=60, cwd=ROOT,
        )
        check("agent enroll 子命令", enroll.returncode == 0 and "注册成功" in enroll.stdout, f"rc={enroll.returncode} {enroll.stdout} {enroll.stderr}")
        config_path = agent_dir / "fleet" / "agent.json"
        agent_config = json.loads(config_path.read_text())
        device_id = agent_config["device_id"]

        # 钉住 desired_autostart=false, 保证验收不安装任何服务。
        status, body = alice.request("POST", f"/fleet/devices/{device_id}/autostart", {"desired": False}, csrf=True)
        check("关闭期望自启动", status == 200 and body.get("desired_autostart") is False, f"HTTP {status} {body}")
        agent_config["desired_autostart"] = False
        agent_config["metrics_interval_ms"] = 5000
        config_path.write_text(json.dumps(agent_config))

        agent_process = subprocess.Popen(
            [str(arguments.bin), "agent", "run", "--data-dir", str(agent_dir)],
            stdout=agent_log, stderr=subprocess.STDOUT, cwd=ROOT,
        )

        def device_row() -> dict:
            _, body = alice.request("GET", "/fleet/devices")
            for row in body.get("devices", []):
                if row.get("id") == device_id:
                    return row
            return {}

        wait_until(20, "agent control channel online", lambda: bool(device_row().get("agent", {}).get("current_url")))
        check("agent 控制通道上线", device_row().get("agent", {}).get("current_url") == f"http://127.0.0.1:{server.port}",
              f"row={device_row()}")

        print("== owner 经桥接建会话 ==", flush=True)
        relay_headers = {"Cookie": f"{SESSION_COOKIE}={alice.cookie}"}
        state_dir = str((agent_dir / "durable" / "supervisor").resolve())
        state_digest = hashlib.sha256(f"{os.geteuid()}\x00{state_dir}".encode()).hexdigest()

        relay = WebSocket(server.port, f"/fleet/devices/{device_id}/bridge", relay_headers)
        stream = SupervisorStream(relay)
        stream.hello(state_digest)
        info = stream.create(["sh", "-c", "echo READY-42; exec cat"])
        session_id = info["id"]
        stream.detach()
        relay.close()
        check("owner 桥接创建会话", bool(session_id), f"info={info}")

        print("== 公开只读链接 ==", flush=True)
        status, body = alice.request("POST", "/share/links", {
            "device_id": device_id, "session_id": session_id, "write": False, "ttl_ms": 3600000,
        }, csrf=True)
        check("创建只读链接", status == 200 and body.get("permission") == "read" and bool(body.get("token")), f"HTTP {status} {body}")
        read_link_id = body.get("id", "")
        read_token = body.get("token", "")

        print("== 公开页面 plain GET ==", flush=True)
        status, _, index_body = plain_get(server.port, "/")
        check("根路径 SPA 服务", status == 200 and "<title>NexTerm</title>" in index_body
              and 'window.__NEXTERM_TRANSPORT__="web"' in index_body, f"HTTP {status} {index_body[:120]}")
        status, cache_control, token_body = plain_get(server.port, f"/share/public/{read_token}")
        check("有效 token plain GET 服务同一 SPA", status == 200 and token_body == index_body, f"HTTP {status}")
        check("token URL 响应 no-store", cache_control == "no-store", f"Cache-Control={cache_control!r}")
        status, _, invalid_body = plain_get(server.port, "/share/public/not-a-real-token")
        check("无效 token plain GET 同样服务 SPA (不校验 token)", status == 200 and invalid_body == index_body, f"HTTP {status}")
        check("无效 token WS 握手仍被拒", ws_handshake_status(server.port, "/share/public/not-a-real-token") == 403)
        post_request = urllib.request.Request(
            f"http://127.0.0.1:{server.port}/share/public/{read_token}", data=b"{}", method="POST",
        )
        try:
            urllib.request.urlopen(post_request, timeout=30)
            post_status = 200
        except urllib.error.HTTPError as error:
            post_status = error.code
        check("POST 公开 token URL 不落入 SPA (405)", post_status == 405, f"HTTP {post_status}")

        viewer = ShareViewer(WebSocket(server.port, f"/share/public/{read_token}"))
        ready = viewer.expect_ready()
        check("只读 ready 帧", ready.get("permission") == "read" and ready.get("session_id") == session_id
              and set(ready.keys()) <= READY_FRAME_KEYS, f"ready={ready}")
        viewer.expect_output(b"READY-42")
        check("只读 viewer 收到会话输出", True)
        viewer.ws.send_binary(b"echo nope-42\n")
        try:
            viewer.expect_silent(2.0)
            check("只读输入被静默丢弃", True)
        except AssertionError as error:
            check("只读输入被静默丢弃", False, error)
        viewer.ws.close()

        print("== 公开读写链接 ==", flush=True)
        status, body = alice.request("POST", "/share/links", {
            "device_id": device_id, "session_id": session_id, "write": True, "ttl_ms": 3600000,
        }, csrf=True)
        check("创建读写链接", status == 200 and body.get("permission") == "read_write", f"HTTP {status} {body}")
        write_token = body.get("token", "")
        rw_viewer = ShareViewer(WebSocket(server.port, f"/share/public/{write_token}"))
        ready = rw_viewer.expect_ready()
        check("读写 ready 帧", ready.get("permission") == "read_write", f"ready={ready}")
        rw_viewer.expect_output(b"READY-42")
        rw_viewer.ws.send_binary(b"echo hello-42\n")
        try:
            rw_viewer.expect_output(b"hello-42")
            check("读写输入到达会话并回显", True)
        except AssertionError as error:
            check("读写输入到达会话并回显", False, error)

        print("== 吊销 ==", flush=True)
        status, body = alice.request("POST", f"/share/links/{read_link_id}/revoke", {}, csrf=True)
        check("吊销只读链接", status == 200 and body.get("ok") is True, f"HTTP {status} {body}")
        # 吊销只读链接不影响读写 viewer; 吊销读写链接后活跃 viewer 必须停止。
        status, body = alice.request("GET", "/share/links")
        write_link_id = ""
        for row in body.get("links", []):
            if row.get("permission") == "read_write":
                write_link_id = row.get("id", "")
        status, body = alice.request("POST", f"/share/links/{write_link_id}/revoke", {}, csrf=True)
        check("吊销读写链接", status == 200 and body.get("ok") is True, f"HTTP {status} {body}")
        try:
            error_frame = rw_viewer.expect_error(timeout=30)
            check("吊销停止活跃 viewer (错误帧)", "吊销" in error_frame.get("message", ""), f"frame={error_frame}")
        except AssertionError as error:
            check("吊销停止活跃 viewer (错误帧)", False, error)
        rw_viewer.expect_closed(10)
        check("吊销后 token 不可再用", ws_handshake_status(server.port, f"/share/public/{write_token}") == 403)

        print("== 过期 ==", flush=True)
        status, body = alice.request("POST", "/share/links", {
            "device_id": device_id, "session_id": session_id, "write": False, "ttl_ms": 60000,
        }, csrf=True)
        check("创建 60s 短时效链接", status == 200, f"HTTP {status} {body}")
        expiring_token = body.get("token", "")
        expiring_viewer = ShareViewer(WebSocket(server.port, f"/share/public/{expiring_token}"))
        expiring_viewer.expect_ready()
        try:
            error_frame = expiring_viewer.expect_error(timeout=100)
            check("过期停止活跃 viewer", "过期" in error_frame.get("message", ""), f"frame={error_frame}")
        except AssertionError as error:
            check("过期停止活跃 viewer", False, error)
        expiring_viewer.expect_closed(10)
        check("过期后 token 不可再用", ws_handshake_status(server.port, f"/share/public/{expiring_token}") == 403)

        print("== 注册分享打开终端 ==", flush=True)
        status, body = carol.request("GET", f"/share/devices/{device_id}/terminal")
        check("无分享用户打开被拒", status == 403, f"HTTP {status} {body}")
        status, body = alice.request("POST", "/share/host-shares", {
            "device_id": device_id, "recipient_id": bob_id, "write": True, "ttl_ms": 3600000,
        }, csrf=True)
        check("授予 bob 主机分享", status == 200 and body.get("permission") == "read_write", f"HTTP {status} {body}")
        share_id = body.get("id", "")
        if set(body.keys()) - HOST_SHARE_VIEW_KEYS:
            check("主机分享视图无多余字段", False, f"keys={sorted(body.keys())}")
        else:
            check("主机分享视图无多余字段", True)

        bob_headers = {"Cookie": f"{SESSION_COOKIE}={bob.cookie}"}
        bob_viewer = ShareViewer(WebSocket(server.port, f"/share/devices/{device_id}/terminal", bob_headers))
        ready = bob_viewer.expect_ready()
        check("bob 打开新终端", ready.get("permission") == "read_write" and bool(ready.get("session_id")), f"ready={ready}")
        bob_viewer.ws.send_binary(b"echo hi-42\n")
        try:
            bob_viewer.expect_output(b"hi-42")
            check("bob 终端输入输出", True)
        except AssertionError as error:
            check("bob 终端输入输出", False, error)

        print("== 权限收缩 read_write -> read ==", flush=True)
        bob_viewer.ws.send_binary(b"while true; do echo tick; sleep 0.5; done\n")
        try:
            bob_viewer.expect_output(b"tick")
        except AssertionError as error:
            check("收缩前终端持续输出", False, error)
        status, body = alice.request("POST", "/share/host-shares", {
            "device_id": device_id, "recipient_id": bob_id, "write": False, "ttl_ms": 3600000,
        }, csrf=True)
        check("收缩为只读分享", status == 200 and body.get("permission") == "read", f"HTTP {status} {body}")
        time.sleep(3)  # 等逐块复查吃到新权限 (周期复查兜底 10s)
        bob_viewer.ws.send_binary(b"echo nope-42\n")
        output = b""
        deadline = time.monotonic() + 5
        while time.monotonic() < deadline:
            try:
                output += bob_viewer.read_output(timeout=max(0.2, deadline - time.monotonic()))
            except (TimeoutError, socket.timeout):
                break
        check("收缩后输出继续", b"tick" in output, f"output={output[-200:]!r}")
        check("收缩后输入被丢弃", b"nope-42" not in output, f"output={output[-200:]!r}")
        bob_viewer.ws.close()

        status, body = alice.request("POST", f"/share/host-shares/{share_id}/revoke", {}, csrf=True)
        check("吊销主机分享", status == 200 and body.get("ok") is True, f"HTTP {status} {body}")
        status, body = bob.request("GET", f"/share/devices/{device_id}/terminal")
        check("吊销后 bob 打开被拒", status == 403, f"HTTP {status} {body}")

        print("== 守护代理离线 ==", flush=True)
        # 重新授予 bob 并建立一条活跃分享流, 然后停掉 agent。
        status, body = alice.request("POST", "/share/host-shares", {
            "device_id": device_id, "recipient_id": bob_id, "write": True, "ttl_ms": 3600000,
        }, csrf=True)
        check("重新授予 bob", status == 200, f"HTTP {status} {body}")
        status, body = alice.request("POST", "/share/links", {
            "device_id": device_id, "session_id": session_id, "write": True, "ttl_ms": 3600000,
        }, csrf=True)
        check("离线前创建读写链接", status == 200, f"HTTP {status} {body}")
        offline_token = body.get("token", "")
        offline_viewer = ShareViewer(WebSocket(server.port, f"/share/public/{offline_token}"))
        offline_viewer.expect_ready()

        stop_agent()
        try:
            offline_viewer.expect_closed(15)
            check("agent 离线停止活跃分享流", True)
        except AssertionError as error:
            check("agent 离线停止活跃分享流", False, error)
        check("离线后公开链接 503", ws_handshake_status(server.port, f"/share/public/{offline_token}") == 503)
        status, body = bob.request("GET", f"/share/devices/{device_id}/terminal")
        check("离线后注册打开 503", status == 503, f"HTTP {status} {body}")
        status, body = alice.request("POST", "/share/links", {
            "device_id": device_id, "session_id": session_id, "write": False, "ttl_ms": 3600000,
        }, csrf=True)
        check("离线后创建链接 503", status == 503, f"HTTP {status} {body}")

        print("== 披露防线 ==", flush=True)
        status, body = Client(server.port).request("GET", "/share/links")
        check("匿名访问分享管理 401", status == 401, f"HTTP {status} {body}")
        status, body = Client(server.port).request("POST", "/sync/v2/pull", {"since": 0})
        check("匿名访问同步协议被拒", status in (401, 403), f"HTTP {status} {body}")
        status, body = alice.request("GET", "/share/links")
        leaked = [sorted(set(row.keys()) - LINK_VIEW_KEYS) for row in body.get("links", [])]
        check("链接列表无 token 字段", status == 200 and all(not extra for extra in leaked), f"extra={leaked}")

        print(f"\n通过 {len(PASSED)} 项, 失败 {len(FAILED)} 项", flush=True)
        return 0 if not FAILED else 1
    finally:
        stop_agent()
        cleanup_helper()
        agent_log.close()
        server.stop()


if __name__ == "__main__":
    raise SystemExit(main())
