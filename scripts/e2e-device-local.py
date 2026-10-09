#!/usr/bin/env python3
"""FLEET136 device agent process-level acceptance over real processes.

Builds the real nexterm-server binary once, starts one real server with
--auth=on (plus a second one with --auth=off), initializes a superadmin
through the console init-code flow, then drives the M124/M126 contracts end
to end:

  1. enroll: enroll-code issue (session+CSRF), agent CLI enroll, code single-use
  2. agent run: control channel online, current-url report, natural sync tick
  3. sync metrics: direct POST /agent/sync sample read back via /fleet API
  4. outbound bridge/resume: supervisor protocol (real create/attach/detach/
     re-attach with expect incarnation) through GET /fleet/devices/{id}/bridge;
     the device list must expose the same state digest the browser needs for
     the supervisor hello (FLEET156)
  5. revocation: agent control channel kicked, agent exits with code 3
  6. protocol errors: version_mismatch and forbidden hello frames
  7. --auth=off: every fleet route rejected with 403

No third-party Python dependencies, no external network, no service
installation (desired_autostart is pinned false before `agent run`), no
tmux/X11/WebView. The detached supervisor helper is killed via helper.pid
during cleanup.

    python3 scripts/e2e-device-local.py
    python3 scripts/e2e-device-local.py --bin target/go-build/nexterm-server-device-e2e --no-build
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
from datetime import datetime, timezone

ROOT = pathlib.Path(__file__).resolve().parent.parent
SESSION_COOKIE = "nexterm_session"
CSRF_HEADER = "X-NexTerm-CSRF"
PASSED: list[str] = []
FAILED: list[str] = []

AGENT_EXIT_REVOKED = 3


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
    def __init__(self, binary: pathlib.Path, work: pathlib.Path, name: str, master_key: str, auth: str):
        self.binary = binary
        self.data_dir = work / name
        self.log_path = work / f"{name}.log"
        self.master_key = master_key
        self.auth = auth
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
        environment.update(
            {
                "NEXTERM_DATA_DIR": str(self.data_dir),
                "NEXTERM_LISTEN": f"127.0.0.1:{self.port}",
                "NEXTERM_MASTER_KEY_FILE": str(key_file),
                "NEXTERM_AUTH": self.auth,
            }
        )
        arguments = [
            str(self.binary), "--listen", f"127.0.0.1:{self.port}", "--data-dir", str(self.data_dir), f"--auth={self.auth}",
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
    version = os.environ.get("NEXTERM_RELEASE_VERSION") or json.loads((ROOT / "wails.json").read_text(encoding="utf-8"))["info"]["version"]
    binary.parent.mkdir(parents=True, exist_ok=True)
    environment = dict(os.environ)
    environment.update({"CGO_ENABLED": "0", "GOTOOLCHAIN": "go1.26.8"})
    command = [
        "go", "build", "-mod=readonly", "-trimpath", "-buildvcs=false",
        "-o", str(binary), package,
    ]
    if package == "./cmd/nexterm-server":
        command[5:5] = ["-ldflags", f"-s -w -X github.com/Hello-CTF/NexTerm/internal/version.Version={version}"]
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

    def recv(self) -> tuple[int, bytes]:
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
FRAME_ATTACH = 8
FRAME_ATTACHED = 9
FRAME_OK = 13
FRAME_DETACH = 18
FRAME_OUTPUT = 19
FRAME_KILL_SESSION = 22
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

    def attach(self, session_id: str, expect_created_at_ns: int = 0, expect_incarnation: str = "") -> dict:
        message: dict[str, object] = {"id": session_id}
        if expect_created_at_ns:
            message["expect_created_at_unix_nano"] = expect_created_at_ns
        if expect_incarnation:
            message["expect_incarnation"] = expect_incarnation
        self.send_frame(FRAME_ATTACH, message)
        kind, payload = self.read_frame()
        if kind == FRAME_ERROR:
            error = json.loads(payload)
            raise SupervisorWireError(error.get("code", ""), error.get("message", ""))
        if kind != FRAME_ATTACHED:
            raise AssertionError(f"attach: unexpected frame {kind}")
        return json.loads(payload)

    def detach(self) -> None:
        self.send_frame(FRAME_DETACH, {})

    def kill_session(self, session_id: str) -> None:
        self.send_frame(FRAME_KILL_SESSION, {"id": session_id})
        kind, payload = self.read_frame()
        if kind == FRAME_ERROR:
            raise AssertionError(f"kill_session failed: {payload.decode(errors='replace')}")
        if kind != FRAME_OK:
            raise AssertionError(f"kill_session: unexpected frame {kind}")

    def read_output(self, timeout: float = 10.0) -> bytes:
        kind, payload = self.read_frame(timeout)
        if kind == FRAME_OUTPUT:
            return payload[8:]
        if kind == FRAME_ERROR:
            raise AssertionError(f"stream error: {payload.decode(errors='replace')}")
        raise AssertionError(f"unexpected frame {kind} while reading output")


class SupervisorWireError(Exception):
    def __init__(self, code: str, message: str):
        super().__init__(f"{code}: {message}")
        self.code = code


def rfc3339_to_unix_nano(value: str) -> int:
    # Go time.Time JSON (RFC3339Nano, 如 2026-10-07T05:34:56.123456789+08:00)
    # 转 Unix 纳秒; datetime 只有微秒精度且 float 秒会丢纳秒, 必须按整数拼,
    # 数值时区偏移必须参与换算 (本机非 UTC 时 Go 会带偏移输出)。
    text = value
    offset_seconds = 0
    if text.endswith("Z"):
        text = text[:-1]
    else:
        for index in range(len(text) - 1, 18, -1):
            if text[index] in "+-":
                sign = 1 if text[index] == "+" else -1
                hours, minutes = text[index + 1 :].split(":")
                offset_seconds = sign * (int(hours) * 3600 + int(minutes) * 60)
                text = text[:index]
                break
    head, dot, fraction = text.partition(".")
    fraction_ns = 0
    if dot:
        digits = ""
        for char in fraction:
            if char.isdigit():
                digits += char
            else:
                break
        fraction_ns = int((digits + "000000000")[:9])
    seconds = int(datetime.fromisoformat(head).replace(tzinfo=timezone.utc).timestamp()) - offset_seconds
    return seconds * 1_000_000_000 + fraction_ns


def wait_until(deadline_s: float, what: str, condition) -> None:
    deadline = time.monotonic() + deadline_s
    while time.monotonic() < deadline:
        if condition():
            return
        time.sleep(0.2)
    raise AssertionError(f"timed out waiting for {what}")


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--bin", type=pathlib.Path, default=ROOT / "target/go-build/nexterm-server-device-e2e")
    parser.add_argument("--helper", type=pathlib.Path, default=ROOT / "target/go-build/syncv2-helper-device-e2e")
    parser.add_argument("--no-build", action="store_true", help="reuse existing binaries")
    arguments = parser.parse_args()

    if not arguments.no_build:
        build(arguments.bin, "./cmd/nexterm-server")
        build(arguments.helper, "./scripts/syncv2-helper")

    work = pathlib.Path(tempfile.mkdtemp(prefix="nexterm-device-e2e-"))
    master_key = f"e2e-master-{uuid.uuid4().hex}"
    server = Instance(arguments.bin, work, "server", master_key, "on")
    agent_dir = work / "agent"
    agent_log = (work / "agent.log").open("ab")
    agent_process: subprocess.Popen | None = None
    alice_password = "alice-e2e-pw-123"

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

        print("== enrollment ==", flush=True)
        status, body = alice.request("PUT", "/fleet/base-urls", {"base_urls": [{"url": f"http://127.0.0.1:{server.port}"}]}, csrf=True)
        check("配置接入地址", status == 200 and len(body.get("base_urls", [])) == 1, f"HTTP {status} {body}")

        status, body = alice.request("POST", "/device/enroll-codes", {"ttl_ms": 600000})
        check("写操作缺 CSRF 被拒绝", status == 403, f"HTTP {status} {body}")
        status, body = alice.request("POST", "/device/enroll-codes", {"ttl_ms": 600000}, csrf=True)
        check("签发接入码", status == 200 and bool(body.get("code")), f"HTTP {status} {body}")
        enroll_code = body.get("code", "")

        enroll = subprocess.run(
            [str(arguments.bin), "agent", "enroll", "--server", f"http://127.0.0.1:{server.port}",
             "--code", enroll_code, "--data-dir", str(agent_dir), "--name", "e2e-box"],
            capture_output=True, text=True, timeout=60, cwd=ROOT,
        )
        check("agent enroll 子命令", enroll.returncode == 0 and "注册成功" in enroll.stdout, f"rc={enroll.returncode} {enroll.stdout} {enroll.stderr}")
        config_path = agent_dir / "fleet" / "agent.json"
        mode = config_path.stat().st_mode & 0o777 if config_path.exists() else 0
        check("enroll 配置 0600", mode == 0o600, f"mode={oct(mode)}")
        agent_config = json.loads(config_path.read_text())
        device_id = agent_config["device_id"]
        device_secret = agent_config["secret"]

        status, body = Client(server.port).request("POST", "/device/enroll", {"code": enroll_code, "name": "reuse"})
        check("接入码单次性", status == 403, f"HTTP {status} {body}")

        # 钉住 desired_autostart=false (服务端记录 + 设备本地配置), 保证验收不安装任何服务。
        status, body = alice.request("POST", f"/fleet/devices/{device_id}/autostart", {"desired": False}, csrf=True)
        check("关闭期望自启动", status == 200 and body.get("desired_autostart") is False, f"HTTP {status} {body}")
        agent_config["desired_autostart"] = False
        agent_config["metrics_interval_ms"] = 5000
        config_path.write_text(json.dumps(agent_config))

        print("== agent run 在线 ==", flush=True)
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

        def agent_online() -> bool:
            row = device_row()
            return bool(row.get("agent", {}).get("current_url"))

        wait_until(20, "agent control channel online", agent_online)
        row = device_row()
        check("控制通道上线并上报 current_url", row.get("agent", {}).get("current_url") == f"http://127.0.0.1:{server.port}",
              f"row={row}")

        print("== sync 指标 ==", flush=True)
        status, body = Client(server.port).request("POST", "/agent/sync", {
            "device_id": device_id, "secret": device_secret,
            "sample": {"ts": int(time.time() * 1000), "cpu_pct": 42.5, "mem_used": 1, "mem_total": 2, "disk_used": 3, "disk_total": 4, "uptime_s": 5},
        })
        check("直接 sync 上报", status == 200 and body.get("desired_autostart") is False and body.get("terminal_enabled") is True, f"HTTP {status} {body}")

        def metrics_samples() -> list:
            _, body = alice.request("GET", f"/fleet/devices/{device_id}/metrics")
            return body.get("samples", [])

        def saw_direct_sample() -> bool:
            return any(sample.get("cpu_pct") == 42.5 for sample in metrics_samples())

        wait_until(15, "direct sync sample visible", saw_direct_sample)
        check("sync 指标可读回", saw_direct_sample(), "samples=%s" % (metrics_samples(),)[:300])

        def saw_natural_sample() -> bool:
            return any(sample.get("cpu_pct") != 42.5 for sample in metrics_samples())

        wait_until(20, "natural agent sync sample", saw_natural_sample)
        check("agent 自然 sync 落库", saw_natural_sample(), "samples=%s" % (metrics_samples(),)[:300])

        print("== 出站桥接与 resume ==", flush=True)
        relay_headers = {"Cookie": f"{SESSION_COOKIE}={alice.cookie}"}
        # supervisor hello 的 state digest 与设备端 helper 对齐 (对端代理模型:
        # 调用方按守护进程所在主机视角计算)。
        state_dir = str((agent_dir / "durable" / "supervisor").resolve())
        state_digest = hashlib.sha256(f"{os.geteuid()}\x00{state_dir}".encode()).hexdigest()

        # FLEET156: 浏览器经设备列表拿到同一份摘要去做 supervisor hello;
        # 这里钉住列表下发值与设备端现算值一致, 且桥接 hello 合同不回退。
        def listed_state_digest() -> str:
            return device_row().get("agent", {}).get("state_digest", "")

        wait_until(10, "device list exposes state digest", lambda: listed_state_digest() == state_digest)
        check("设备列表下发 state digest", listed_state_digest() == state_digest, f"listed={listed_state_digest()!r}")

        def open_relay() -> SupervisorStream:
            ws = WebSocket(server.port, f"/fleet/devices/{device_id}/bridge", relay_headers)
            stream = SupervisorStream(ws)
            stream.hello(state_digest)
            return stream

        # create 与 attach 是两条独立桥接 (与 Go 客户端同一模型: 每条桥接一条连接)。
        creator = open_relay()
        info = creator.create(["sh", "-c", "echo READY-42; sleep 3; echo LATE-7; sleep 60"])
        session_id = info["id"]
        incarnation = info["incarnation"]
        created_at_ns = rfc3339_to_unix_nano(info["created_at"])
        creator.detach()
        creator.ws.close()

        first = open_relay()
        attached = first.attach(session_id)
        check("桥接 attach 会话", attached.get("id") == session_id, f"info={attached}")
        output = b""
        deadline = time.monotonic() + 15
        while b"READY-42" not in output and time.monotonic() < deadline:
            output += first.read_output(timeout=5)
        check("桥接读取会话输出", b"READY-42" in output, f"output={output[-200:]!r}")
        first.detach()
        first.ws.close()

        time.sleep(4)  # LATE-7 在断开期间产出, resume 后必须在 backlog 里
        second = open_relay()
        resumed = second.attach(session_id, created_at_ns, incarnation)
        check("expect incarnation resume 成功", resumed.get("id") == session_id, f"info={resumed}")
        backlog = b""
        deadline = time.monotonic() + 10
        while b"LATE-7" not in backlog and time.monotonic() < deadline:
            backlog += second.read_output(timeout=5)
        check("resume 补读断开期间输出", b"LATE-7" in backlog, f"backlog={backlog[-200:]!r}")
        second.detach()
        second.ws.close()

        # 错误 incarnation 的 attach 被 helper 拒绝并拆线, 单独开一条桥接验证
        # (会话仍然存活, 拒绝码必须是 identity)。
        third = open_relay()
        try:
            third.attach(session_id, created_at_ns, "wrong-incarnation")
            check("错误 incarnation 被拒绝", False, "attach succeeded with wrong incarnation")
        except SupervisorWireError as error:
            check("错误 incarnation 被拒绝", error.code == "identity", f"code={error.code}")
        third.ws.close()

        killer = open_relay()
        killer.kill_session(session_id)
        killer.ws.close()
        check("kill_session 清理会话", True)

        print("== 协议错误 ==", flush=True)
        ws = WebSocket(server.port, "/ws/device")
        ws.send(0x1, json.dumps({"type": "hello", "protocol": 99, "device_id": device_id, "secret": device_secret}).encode())
        opcode, payload = ws.recv()
        message = json.loads(payload) if opcode == 0x1 else {}
        check("协议版本不匹配回 version_mismatch", message.get("code") == "version_mismatch", f"frame={message}")
        ws.close()

        ws = WebSocket(server.port, "/ws/device")
        ws.send(0x1, json.dumps({"type": "hello", "protocol": 1, "device_id": device_id, "secret": "wrong"}).encode())
        opcode, payload = ws.recv()
        message = json.loads(payload) if opcode == 0x1 else {}
        check("错误凭证回 forbidden", message.get("code") == "forbidden", f"frame={message}")
        ws.close()

        print("== 吊销 ==", flush=True)
        status, body = alice.request("POST", f"/fleet/devices/{device_id}/revoke", {}, csrf=True)
        check("吊销设备", status == 200 and body.get("ok") is True, f"HTTP {status} {body}")
        exit_code = agent_process.wait(timeout=15)
        check("agent 收到 revoke 并以退出码 3 停止", exit_code == AGENT_EXIT_REVOKED, f"exit={exit_code}")
        status, body = Client(server.port).request("POST", "/agent/sync", {"device_id": device_id, "secret": device_secret})
        check("吊销后 sync 403", status == 403, f"HTTP {status} {body}")

        print("== --auth=off 关闭 fleet ==", flush=True)
        off_server = Instance(arguments.bin, work, "server-off", master_key, "off")
        off_server.start()
        try:
            status, body = Client(off_server.port).request("POST", "/device/enroll", {"code": "x", "name": "y"})
            check("off 下 enroll 403", status == 403, f"HTTP {status} {body}")
            status, body = Client(off_server.port).request("POST", "/agent/sync", {"device_id": "x", "secret": "y"})
            check("off 下 agent/sync 403", status == 403, f"HTTP {status} {body}")
            status, body = Client(off_server.port).request("GET", "/fleet/devices")
            check("off 下 fleet 管理 403", status == 403, f"HTTP {status} {body}")
            try:
                WebSocket(off_server.port, "/ws/device")
                check("off 下 /ws/device 403", False, "upgrade succeeded")
            except WebSocketHTTPError as error:
                check("off 下 /ws/device 403", error.status == 403, f"status={error.status}")
        finally:
            off_server.stop()

        print(f"\n通过 {len(PASSED)} 项, 失败 {len(FAILED)} 项", flush=True)
        return 0 if not FAILED else 1
    finally:
        stop_agent()
        cleanup_helper()
        agent_log.close()
        server.stop()


if __name__ == "__main__":
    raise SystemExit(main())
