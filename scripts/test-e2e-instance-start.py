#!/usr/bin/env python3
"""Instance.start 端口竞争验收: 真实占用 socket 与子进程, 无外部网络。"""

from __future__ import annotations

import importlib.util
import os
import pathlib
import socket
import tempfile
import unittest
from unittest import mock

ROOT = pathlib.Path(__file__).resolve().parent.parent
STUB_EXIT = "NEXTERM_INSTANCE_START_STUB_EXIT"
FAKE_SERVER = """#!/usr/bin/env python3
import os
import sys
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

reason = os.environ.get("NEXTERM_INSTANCE_START_STUB_EXIT")
if reason:
    print(reason, flush=True)
    raise SystemExit(1)

port = int(sys.argv[sys.argv.index("--listen") + 1].rsplit(":", 1)[1])

class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path != "/healthz":
            self.send_response(404)
            self.end_headers()
            return
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", "2")
        self.end_headers()
        self.wfile.write(b"{}")

    def log_message(self, *args):
        pass

try:
    server = ThreadingHTTPServer(("127.0.0.1", port), Handler)
except OSError as error:
    print(error, flush=True)
    raise SystemExit(1)
server.serve_forever(poll_interval=0.05)
"""


def load_module(path: pathlib.Path):
    spec = importlib.util.spec_from_file_location(f"instance_start_{path.stem}", path)
    assert spec is not None and spec.loader is not None
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


MODULES = {
    "device": load_module(ROOT / "scripts" / "e2e-device-local.py"),
    "sharing": load_module(ROOT / "scripts" / "e2e-sharing-local.py"),
    "sync": load_module(ROOT / "scripts" / "e2e-sync-local.py"),
}


def unused_port() -> int:
    with socket.socket() as listener:
        listener.bind(("127.0.0.1", 0))
        return int(listener.getsockname()[1])


class InstanceStartTests(unittest.TestCase):
    def setUp(self) -> None:
        self.temporary = tempfile.TemporaryDirectory(prefix="nexterm-instance-start-")
        self.addCleanup(self.temporary.cleanup)
        self.work = pathlib.Path(self.temporary.name)
        self.binary = self.work / "fake-server"
        self.binary.write_text(FAKE_SERVER, encoding="utf-8")
        self.binary.chmod(0o755)

    def new_instance(self, kind: str):
        module = MODULES[kind]
        work = self.work / kind
        if kind == "sync":
            return module.Instance(self.binary, work, "server", "test-master-key")
        return module.Instance(self.binary, work, "server", "test-master-key", "on")

    def test_selected_port_taken_before_start_retries_on_new_port(self) -> None:
        for kind, module in MODULES.items():
            with self.subTest(kind=kind):
                first = unused_port()
                second = unused_port()
                while second == first:
                    second = unused_port()
                with mock.patch.object(module, "free_port", side_effect=[first, second]) as choose_port:
                    instance = self.new_instance(kind)
                    blocker = socket.socket()
                    handles = []
                    original_stop = instance.stop

                    def record_stop() -> None:
                        if instance.log_handle is not None:
                            handles.append(instance.log_handle)
                        original_stop()

                    instance.stop = record_stop
                    try:
                        blocker.bind(("127.0.0.1", first))
                        instance.start()
                        self.assertEqual(instance.port, second)
                        self.assertEqual(choose_port.call_count, 2)
                        self.assertIsNotNone(instance.process)
                        self.assertIsNone(instance.process.poll())
                        self.assertEqual(len(handles), 1)
                        self.assertTrue(handles[0].closed)
                        self.assertIn("address already in use", instance.log_path.read_text(encoding="utf-8").lower())
                    finally:
                        blocker.close()
                        instance.stop()

    def test_other_process_failures_do_not_retry(self) -> None:
        for kind, module in MODULES.items():
            with self.subTest(kind=kind):
                with mock.patch.dict(os.environ, {STUB_EXIT: "permission denied"}), mock.patch.object(
                    module, "free_port", return_value=unused_port()
                ) as choose_port:
                    instance = self.new_instance(kind)
                    try:
                        with self.assertRaises(AssertionError):
                            instance.start()
                        self.assertEqual(choose_port.call_count, 1)
                        self.assertIsNone(instance.process)
                        self.assertIsNone(instance.log_handle)
                    finally:
                        instance.stop()

    def test_address_in_use_retries_are_bounded(self) -> None:
        for kind, module in MODULES.items():
            with self.subTest(kind=kind):
                ports = [unused_port() for _ in range(6)]
                with mock.patch.dict(os.environ, {STUB_EXIT: "EADDRINUSE"}), mock.patch.object(
                    module, "free_port", side_effect=ports
                ) as choose_port:
                    instance = self.new_instance(kind)
                    try:
                        with self.assertRaises(AssertionError):
                            instance.start()
                        self.assertEqual(choose_port.call_count, 6)
                        self.assertIsNone(instance.process)
                        self.assertIsNone(instance.log_handle)
                        self.assertEqual(instance.log_path.read_text(encoding="utf-8").count("EADDRINUSE"), 6)
                    finally:
                        instance.stop()


if __name__ == "__main__":
    unittest.main()
