#!/usr/bin/env python3
"""FLEET157 acceptance tests for public/install-device.sh (all stubs, no network, no service installation, no real secrets).

All external effects of the script under test go through stubs in PATH: fake curl/wget
reads files from the local fixture directory and records URLs; fake id/uname is controlled
by environment variables; nexterm-agent in the downloaded package is a fake binary that
records argv (enroll/install does not touch the network or invoke systemctl/launchctl).
The enrollment code is a dummy string and is asserted not to appear in script output. Coverage:

  1. Platform/architecture selection (linux/darwin x amd64/arm64 asset names and release base)
  2. SHA256SUMS verification success/failure/missing entry/non-hex (no install or enroll on failure),
     fallback to shasum -a 256 when sha256sum is missing
  3. Command order: download -> verify -> install binary -> enroll -> install; the tar root must
     directly contain nexterm-agent, and the old nested-directory layout is rejected clearly
  4. Data directory precedence NEXTERM_DATA_DIR > XDG_DATA_HOME > HOME and single-argv quoting
     for values containing spaces
  5. Clear errors for root, non-Linux/macOS, unsupported architectures, and missing curl and wget
  6. enroll failure must not continue to install; enrollment codes and device secrets are not echoed

    python3 scripts/test-install-device.py
"""

from __future__ import annotations

import hashlib
import io
import os
import pathlib
import shutil
import subprocess
import sys
import tarfile
import tempfile

ROOT = pathlib.Path(__file__).resolve().parent.parent
SCRIPT = ROOT / "public" / "install-device.sh"
VERSION = "9.9.9-test"
RELEASE_BASE = "https://github.com/Hello-CTF/NexTerm/releases/download"
SERVER_URL = "https://nexterm.example.com"
ENROLL_CODE = "TEST-CODE-SECRET-123"

PASSED: list[str] = []
FAILED: list[str] = []

AGENT_BINARY = """#!/bin/sh
{
  printf '==\\n'
  for arg in "$@"; do printf '%s\\n' "$arg"; done
} >> "$FAKE_AGENT_LOG"
if [ "${1:-}" = "enroll" ]; then
  if [ "${FAKE_ENROLL_FAIL:-}" = "1" ]; then
    printf 'agent enroll: stub 注册失败\\n' >&2
    exit 1
  fi
  printf '注册成功: device_id=dev-stub name=stub base_urls=1 metrics_interval=30s desired_autostart=true terminal=开启\\n'
  exit 0
fi
if [ "${1:-}" = "install" ]; then
  if [ "${FAKE_INSTALL_FAIL:-}" = "1" ]; then
    printf 'agent install: stub 安装失败\\n' >&2
    exit 1
  fi
  printf 'install 完成: installed=true enabled=true active=true\\n'
  exit 0
fi
exit 0
"""

FAKE_CURL = """#!/bin/sh
dest=""
url=""
while [ $# -gt 0 ]; do
  case "$1" in
    -o) dest="$2"; shift 2 ;;
    *) url="$1"; shift ;;
  esac
done
printf '%s\\n' "$url" >> "$FAKE_CURL_LOG"
base="${url##*/}"
if [ -f "$FAKE_RELEASE_DIR/$base" ]; then
  cp "$FAKE_RELEASE_DIR/$base" "$dest"
  exit 0
fi
printf 'fake curl: HTTP 404 %s\\n' "$url" >&2
exit 22
"""

FAKE_WGET = """#!/bin/sh
dest=""
url=""
while [ $# -gt 0 ]; do
  case "$1" in
    -O) dest="$2"; shift 2 ;;
    -*) shift ;;
    *) url="$1"; shift ;;
  esac
done
printf '%s\\n' "$url" >> "$FAKE_CURL_LOG"
base="${url##*/}"
if [ -f "$FAKE_RELEASE_DIR/$base" ]; then
  cp "$FAKE_RELEASE_DIR/$base" "$dest"
  exit 0
fi
printf 'fake wget: 404 %s\\n' "$url" >&2
exit 8
"""

FAKE_ID = """#!/bin/sh
if [ "${FAKE_UID:-}" != "" ] && [ "${1:-}" = "-u" ]; then
  printf '%s\\n' "$FAKE_UID"
  exit 0
fi
exec @REAL@ "$@"
"""

FAKE_UNAME = """#!/bin/sh
case "${1:-}" in
  -s) printf '%s\\n' "${FAKE_UNAME_S:-Linux}" ;;
  -m) printf '%s\\n' "${FAKE_UNAME_M:-x86_64}" ;;
  *) exec @REAL@ "$@" ;;
esac
"""


def check(name: str, ok: bool, detail: object = "") -> None:
    if ok:
        PASSED.append(name)
        print(f"  PASS {name}", flush=True)
    else:
        FAILED.append(name)
        print(f"  FAIL {name}: {detail}", flush=True)


def asset_name(version: str, os_name: str, arch: str) -> str:
    public_os = "macos" if os_name == "darwin" else os_name
    return f"nexterm-agent_{public_os}_{arch}_{version}.tar.gz"


ALL_ASSETS = [(os_name, arch) for os_name in ("linux", "darwin") for arch in ("amd64", "arm64")]


class Harness:
    """A hermetic sandbox: stub PATH + fake HOME + local fixture release."""

    def __init__(self, with_curl: bool = True, with_wget: bool = False, sums_builder=None, with_sha256sum: bool = True, bad_layout: bool = False):
        # Sandbox lives on /var/tmp (disk): /tmp is often a size-limited tmpfs
        # that cannot hold the download drill.
        self._tmp = tempfile.TemporaryDirectory(prefix="install-device-test-", dir=os.environ.get("TMPDIR") or "/var/tmp")
        base = pathlib.Path(self._tmp.name)
        self.stub_dir = base / "stub"
        self.home = base / "home"
        self.release = base / "release"
        self.work = base / "work"
        self.tmpdir = base / "tmp"
        for directory in (self.stub_dir, self.home, self.release, self.work, self.tmpdir):
            directory.mkdir()
        self.curl_log = base / "curl.log"
        self.agent_log = base / "agent.log"
        self._write_stubs(with_curl, with_wget, with_sha256sum)
        self._write_fixtures(sums_builder, bad_layout)

    def cleanup(self) -> None:
        self._tmp.cleanup()

    def _stub(self, name: str, content: str) -> None:
        path = self.stub_dir / name
        path.write_text(content)
        path.chmod(0o755)

    def _write_stubs(self, with_curl: bool, with_wget: bool, with_sha256sum: bool) -> None:
        tools = ["mktemp", "awk", "tar", "gzip", "install", "mkdir", "rm", "cp"]
        if with_sha256sum:
            tools.append("sha256sum")
        for name in tools:
            real = shutil.which(name)
            if real is None:
                raise RuntimeError(f"acceptance requires system tool: {name}")
            self._stub(name, f'#!/bin/sh\nexec {real} "$@"\n')
        if not with_sha256sum:
            real_sha256sum = shutil.which("sha256sum")
            if real_sha256sum is None:
                raise RuntimeError("acceptance requires system tool: sha256sum (to simulate shasum)")
            self._stub("shasum", f'#!/bin/sh\n[ "${{1:-}}" = "-a" ] && shift 2\nexec {real_sha256sum} "$@"\n')
        self._stub("id", FAKE_ID.replace("@REAL@", shutil.which("id")))
        self._stub("uname", FAKE_UNAME.replace("@REAL@", shutil.which("uname")))
        if with_curl:
            self._stub("curl", FAKE_CURL)
        if with_wget:
            self._stub("wget", FAKE_WGET)

    def _write_fixtures(self, sums_builder, bad_layout: bool) -> None:
        binary = AGENT_BINARY.encode()
        hashes = {}
        for os_name, arch in ALL_ASSETS:
            name = asset_name(VERSION, os_name, arch)
            # Contract: tar root contains only nexterm-agent; bad_layout
            # simulates the old nested-directory layout and must be refused.
            member = f"NexTerm-agent_{VERSION}_{os_name}_{arch}/nexterm-agent" if bad_layout and (os_name, arch) == ("linux", "amd64") else "nexterm-agent"
            info = tarfile.TarInfo(member)
            info.size = len(binary)
            info.mode = 0o755
            info.mtime = 0
            with tarfile.open(self.release / name, "w:gz") as tar:
                tar.addfile(info, io.BytesIO(binary))
            hashes[name] = hashlib.sha256((self.release / name).read_bytes()).hexdigest()
        if sums_builder is None:
            lines = [f"{hashes[asset_name(VERSION, os_name, arch)]}  {asset_name(VERSION, os_name, arch)}" for os_name, arch in ALL_ASSETS]
            lines.append(f"{'0' * 64}  nexterm-desktop_linux_amd64_{VERSION}.deb")
            sums_text = "\n".join(lines) + "\n"
        else:
            sums_text = sums_builder(hashes)
        (self.release / "SHA256SUMS").write_text(sums_text)

    def run(self, *args: str, env_extra: dict | None = None) -> subprocess.CompletedProcess:
        env = {
            "PATH": str(self.stub_dir),
            "HOME": str(self.home),
            "TMPDIR": str(self.tmpdir),
            "FAKE_UID": "1000",
            "FAKE_RELEASE_DIR": str(self.release),
            "FAKE_CURL_LOG": str(self.curl_log),
            "FAKE_AGENT_LOG": str(self.agent_log),
        }
        if env_extra:
            env.update(env_extra)
        return subprocess.run(
            ["/bin/sh", str(SCRIPT), *args],
            env=env,
            cwd=self.work,
            capture_output=True,
            text=True,
            encoding="utf-8",
            errors="replace",
            timeout=60,
        )

    def base_args(self, version: str = VERSION) -> list[str]:
        return ["--server", SERVER_URL, "--code", ENROLL_CODE, "--version", version]

    def urls(self) -> list[str]:
        if not self.curl_log.exists():
            return []
        return self.curl_log.read_text().splitlines()

    def calls(self) -> list[list[str]]:
        if not self.agent_log.exists():
            return []
        records: list[list[str]] = []
        current: list[str] | None = None
        for line in self.agent_log.read_text().splitlines():
            if line == "==":
                current = []
                records.append(current)
            elif current is not None:
                current.append(line)
        return records

    def installed_binary(self) -> pathlib.Path:
        return self.home / ".local" / "bin" / "nexterm-agent"


def expected_calls(data_dir: str, insecure: bool = False) -> list[list[str]]:
    enroll = ["enroll", "--server", SERVER_URL, "--code", ENROLL_CODE]
    if insecure:
        enroll.append("--insecure")
    enroll += ["--data-dir", data_dir]
    return [enroll, ["install", "--data-dir", data_dir]]


def test_help() -> None:
    harness = Harness()
    try:
        result = harness.run("--help")
        check("help exits 0 and prints usage", result.returncode == 0 and "用法" in result.stdout, result)
    finally:
        harness.cleanup()


def test_missing_required_args() -> None:
    harness = Harness()
    try:
        cases = [
            (["--code", ENROLL_CODE, "--version", VERSION], "--server"),
            (["--server", SERVER_URL, "--version", VERSION], "--code"),
            (["--server", SERVER_URL, "--code", ENROLL_CODE], "--version"),
        ]
        for args, flag in cases:
            result = harness.run(*args)
            check(f"missing {flag} prints a usage error", result.returncode == 2 and flag in result.stderr, result.stderr)
        result = harness.run(*harness.base_args(), "--bogus")
        check("unknown argument prints a usage error", result.returncode == 2 and "未知参数" in result.stderr, result.stderr)
        check("argument errors do not download", harness.urls() == [], harness.urls())
    finally:
        harness.cleanup()


def test_root_refused() -> None:
    harness = Harness()
    try:
        result = harness.run(*harness.base_args(), env_extra={"FAKE_UID": "0"})
        check("root execution is refused", result.returncode == 1 and "root" in result.stderr, result.stderr)
        check("root refusal does not download", harness.urls() == [], harness.urls())
    finally:
        harness.cleanup()


def test_unsupported_os() -> None:
    harness = Harness()
    try:
        result = harness.run(*harness.base_args(), env_extra={"FAKE_UNAME_S": "FreeBSD"})
        check("non-Linux/macOS reports a clear error", result.returncode == 1 and "仅支持 Linux/macOS" in result.stderr, result.stderr)
        check("non-Linux/macOS does not download", harness.urls() == [], harness.urls())
    finally:
        harness.cleanup()


def test_unsupported_arch() -> None:
    harness = Harness()
    try:
        result = harness.run(*harness.base_args(), env_extra={"FAKE_UNAME_M": "mips64"})
        check("unsupported architecture reports a clear error", result.returncode == 1 and "不支持的 CPU 架构" in result.stderr, result.stderr)
        check("unsupported architecture does not download", harness.urls() == [], harness.urls())
    finally:
        harness.cleanup()


def test_no_downloader() -> None:
    harness = Harness(with_curl=False, with_wget=False)
    try:
        result = harness.run(*harness.base_args())
        check("missing curl and wget reports a clear error", result.returncode == 1 and "curl 或 wget" in result.stderr, result.stderr)
    finally:
        harness.cleanup()


def test_wget_fallback() -> None:
    harness = Harness(with_curl=False, with_wget=True)
    try:
        result = harness.run(*harness.base_args())
        expected = [
            f"{RELEASE_BASE}/v{VERSION}/{asset_name(VERSION, 'linux', 'amd64')}",
            f"{RELEASE_BASE}/v{VERSION}/SHA256SUMS",
        ]
        check("wget fallback completes installation", result.returncode == 0, result.stderr)
        check("wget downloads the correct URLs", harness.urls() == expected, harness.urls())
    finally:
        harness.cleanup()


def test_shasum_fallback() -> None:
    harness = Harness(with_sha256sum=False)
    try:
        result = harness.run(*harness.base_args())
        data_dir = f"{harness.home}/.local/share/NexTerm"
        check("missing sha256sum falls back to shasum and completes installation", result.returncode == 0, result.stderr)
        check("shasum fallback preserves the command sequence", harness.calls() == expected_calls(data_dir), harness.calls())
    finally:
        harness.cleanup()


def test_download_failure() -> None:
    harness = Harness()
    try:
        result = harness.run(*harness.base_args(version="8.8.8-missing"))
        check("download failure reports a clear error", result.returncode == 1 and "下载失败" in result.stderr, result.stderr)
        check("download failure does not install or enroll", harness.calls() == [] and not harness.installed_binary().exists(), harness.calls())
    finally:
        harness.cleanup()


def test_checksum_mismatch() -> None:
    harness = Harness(sums_builder=lambda hashes: f"{'a' * 64}  {asset_name(VERSION, 'linux', 'amd64')}\n")
    try:
        result = harness.run(*harness.base_args())
        check("checksum failure reports a clear error", result.returncode == 1 and "校验失败" in result.stderr, result.stderr)
        check("checksum failure does not install or enroll", harness.calls() == [] and not harness.installed_binary().exists(), harness.calls())
    finally:
        harness.cleanup()


def test_checksum_missing_entry() -> None:
    harness = Harness(sums_builder=lambda hashes: f"{'a' * 64}  nexterm-desktop_linux_amd64_{VERSION}.deb\n")
    try:
        result = harness.run(*harness.base_args())
        check("missing SHA256SUMS entry reports a clear error", result.returncode == 1 and "SHA256SUMS" in result.stderr, result.stderr)
        check("missing entry does not install or enroll", harness.calls() == [] and not harness.installed_binary().exists(), harness.calls())
    finally:
        harness.cleanup()


def test_checksum_not_hex() -> None:
    harness = Harness(sums_builder=lambda hashes: f"{'z' * 64}  {asset_name(VERSION, 'linux', 'amd64')}\n")
    try:
        result = harness.run(*harness.base_args())
        check("non-hex checksum reports a clear error", result.returncode == 1 and "不是合法 sha256" in result.stderr, result.stderr)
    finally:
        harness.cleanup()


def test_bad_tar_layout_refused() -> None:
    harness = Harness(bad_layout=True)
    try:
        result = harness.run(*harness.base_args())
        check("missing nexterm-agent at tar root reports a clear error", result.returncode == 1 and "缺少 nexterm-agent" in result.stderr, result.stderr)
        check("invalid layout does not install or enroll", harness.calls() == [] and not harness.installed_binary().exists(), harness.calls())
    finally:
        harness.cleanup()


def assert_success(harness: Harness, result: subprocess.CompletedProcess, os_name: str, arch: str, data_dir: str, insecure: bool = False) -> None:
    check(f"{os_name}/{arch} installs successfully", result.returncode == 0, result.stderr)
    expected_urls = [
        f"{RELEASE_BASE}/v{VERSION}/{asset_name(VERSION, os_name, arch)}",
        f"{RELEASE_BASE}/v{VERSION}/SHA256SUMS",
    ]
    check(f"{os_name}/{arch} downloads the correct URLs in order", harness.urls() == expected_urls, harness.urls())
    binary = harness.installed_binary()
    check("binary is installed in the user bin directory", binary.exists() and binary.read_text() == AGENT_BINARY, binary)
    if binary.exists():
        check("binary permissions are 0755", binary.stat().st_mode & 0o777 == 0o755, oct(binary.stat().st_mode))
    check("command order: enroll -> install with exact arguments", harness.calls() == expected_calls(data_dir, insecure), harness.calls())
    output = result.stdout + result.stderr
    check("enrollment code is not echoed", ENROLL_CODE not in output, output)
    check("log masks the enrollment code with ********", "--code ********" in result.stdout, result.stdout)


def test_success_amd64() -> None:
    harness = Harness()
    try:
        result = harness.run(*harness.base_args())
        assert_success(harness, result, "linux", "amd64", f"{harness.home}/.local/share/NexTerm")
        check("linux reports systemd --user", "systemd --user" in result.stdout, result.stdout)
    finally:
        harness.cleanup()


def test_success_arm64_selects_exact_sum() -> None:
    def sums(hashes: dict) -> str:
        arm64 = asset_name(VERSION, "linux", "arm64")
        return f"{hashes[arm64]}  {arm64}\n" + f"{'b' * 64}  {asset_name(VERSION, 'linux', 'amd64')}\n"

    harness = Harness(sums_builder=sums)
    try:
        result = harness.run(*harness.base_args(), env_extra={"FAKE_UNAME_M": "aarch64"})
        assert_success(harness, result, "linux", "arm64", f"{harness.home}/.local/share/NexTerm")
    finally:
        harness.cleanup()


def test_success_darwin() -> None:
    harness = Harness()
    try:
        result = harness.run(*harness.base_args(), env_extra={"FAKE_UNAME_S": "Darwin", "FAKE_UNAME_M": "arm64"})
        assert_success(harness, result, "darwin", "arm64", f"{harness.home}/.local/share/NexTerm")
        check("darwin reports launchd", "launchd" in result.stdout, result.stdout)
    finally:
        harness.cleanup()


def test_success_darwin_amd64() -> None:
    harness = Harness()
    try:
        result = harness.run(*harness.base_args(), env_extra={"FAKE_UNAME_S": "Darwin", "FAKE_UNAME_M": "x86_64"})
        assert_success(harness, result, "darwin", "amd64", f"{harness.home}/.local/share/NexTerm")
    finally:
        harness.cleanup()


def test_insecure_flag() -> None:
    harness = Harness()
    try:
        result = harness.run(*harness.base_args(), "--insecure")
        data_dir = f"{harness.home}/.local/share/NexTerm"
        check("--insecure is forwarded to enroll", harness.calls() == expected_calls(data_dir, True), harness.calls())
        check("--insecure runs successfully", result.returncode == 0, result.stderr)
    finally:
        harness.cleanup()


def test_data_dir_env_precedence() -> None:
    harness = Harness()
    try:
        spaced = "/tmp/fake data dir"
        result = harness.run(*harness.base_args(), env_extra={"NEXTERM_DATA_DIR": spaced})
        check("NEXTERM_DATA_DIR takes precedence and is preserved verbatim, including spaces as one argv", result.returncode == 0 and harness.calls() == expected_calls(spaced), (result.stderr, harness.calls()))
    finally:
        harness.cleanup()
    harness = Harness()
    try:
        xdg = "/tmp/fake-xdg"
        result = harness.run(*harness.base_args(), env_extra={"XDG_DATA_HOME": xdg})
        check("XDG_DATA_HOME is next and appends /NexTerm", result.returncode == 0 and harness.calls() == expected_calls(f"{xdg}/NexTerm"), (result.stderr, harness.calls()))
    finally:
        harness.cleanup()
    harness = Harness()
    try:
        custom = "/tmp/custom-agent-data"
        result = harness.run(*harness.base_args(), "--data-dir", custom, env_extra={"NEXTERM_DATA_DIR": "/tmp/ignored"})
        check("--data-dir flag overrides env and is preserved verbatim", result.returncode == 0 and harness.calls() == expected_calls(custom), (result.stderr, harness.calls()))
    finally:
        harness.cleanup()


def test_enroll_failure_blocks_install() -> None:
    harness = Harness()
    try:
        result = harness.run(*harness.base_args(), env_extra={"FAKE_ENROLL_FAIL": "1"})
        calls = harness.calls()
        check("script exits nonzero when enroll fails", result.returncode == 1, result.stdout + result.stderr)
        check("install is not executed when enroll fails", len(calls) == 1 and calls[0][0] == "enroll", calls)
    finally:
        harness.cleanup()


def test_install_failure_reported() -> None:
    harness = Harness()
    try:
        result = harness.run(*harness.base_args(), env_extra={"FAKE_INSTALL_FAIL": "1"})
        calls = harness.calls()
        check("script exits nonzero when install fails", result.returncode == 1, result.stdout + result.stderr)
        check("enroll has executed before install failure", len(calls) == 2 and calls[0][0] == "enroll" and calls[1][0] == "install", calls)
    finally:
        harness.cleanup()


def main() -> int:
    if not SCRIPT.exists():
        print(f"missing script: {SCRIPT}", file=sys.stderr)
        return 1
    tests = [
        test_help,
        test_missing_required_args,
        test_root_refused,
        test_unsupported_os,
        test_unsupported_arch,
        test_no_downloader,
        test_wget_fallback,
        test_shasum_fallback,
        test_download_failure,
        test_checksum_mismatch,
        test_checksum_missing_entry,
        test_checksum_not_hex,
        test_bad_tar_layout_refused,
        test_success_amd64,
        test_success_arm64_selects_exact_sum,
        test_success_darwin,
        test_success_darwin_amd64,
        test_insecure_flag,
        test_data_dir_env_precedence,
        test_enroll_failure_blocks_install,
        test_install_failure_reported,
    ]
    for test in tests:
        print(f"== {test.__name__}", flush=True)
        test()
    print(f"\ninstall-device acceptance: {len(PASSED)} passed, {len(FAILED)} failed", flush=True)
    if FAILED:
        for name in FAILED:
            print(f"  FAILED: {name}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
