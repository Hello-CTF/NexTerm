#!/usr/bin/env python3
"""FLEET157 public/install-device.sh 验收 (全 stub, 无网络/无服务安装/无真实密钥)。

被测脚本的全部外部效应都经过 PATH 里的 stub: fake curl/wget 只从本地
fixture 目录取文件并记录 URL, fake id/uname 由环境变量控制, 下载包内的
nexterm-server 是记录 argv 的假二进制 (enroll/install 不触网、不调
systemctl), 接入码是哑字符串且断言不出现在脚本输出里。覆盖:

  1. URL/版本/架构选择 (amd64/arm64 资产名与 release 基址)
  2. SHA256SUMS 校验成功/失败/缺条目/非 hex (失败时不得安装或 enroll)
  3. 命令顺序: 下载 -> 校验 -> 装二进制 -> --version -> enroll -> install
  4. 数据目录 NEXTERM_DATA_DIR > XDG_DATA_HOME > HOME 展开与带空格值的单 argv 引用
  5. 拒绝 root / 非 Linux / 不支持架构 / 缺 curl 与 wget 的明确报错
  6. enroll 失败不得继续 install; 接入码与 device secret 不回显

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
RELEASE_BASE = "https://github.com/ProbiusOfficial/NexTerm/releases/download"
SERVER_URL = "https://nexterm.example.com"
ENROLL_CODE = "TEST-CODE-SECRET-123"

PASSED: list[str] = []
FAILED: list[str] = []

AGENT_BINARY = """#!/bin/sh
{
  printf '==\\n'
  for arg in "$@"; do printf '%s\\n' "$arg"; done
} >> "$FAKE_AGENT_LOG"
if [ "${1:-}" = "--version" ]; then
  printf '%s\\n' "${FAKE_AGENT_VERSION:-}"
  exit 0
fi
if [ "${1:-}" = "agent" ] && [ "${2:-}" = "enroll" ]; then
  if [ "${FAKE_ENROLL_FAIL:-}" = "1" ]; then
    printf 'agent enroll: stub 注册失败\\n' >&2
    exit 1
  fi
  printf '注册成功: device_id=dev-stub name=stub base_urls=1 metrics_interval=30s desired_autostart=true terminal=开启\\n'
  exit 0
fi
if [ "${1:-}" = "agent" ] && [ "${2:-}" = "install" ]; then
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


def asset_name(version: str, arch: str) -> str:
    return f"NexTerm-server_{version}_linux_{arch}.tar.gz"


class Harness:
    """一个 hermetic 沙箱: stub PATH + 假 HOME + 本地 fixture release。"""

    def __init__(self, with_curl: bool = True, with_wget: bool = False, sums_builder=None):
        # 沙箱放 /var/tmp (磁盘): /tmp 常是容量受限的 tmpfs, 装不下下载演练。
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
        self._write_stubs(with_curl, with_wget)
        self._write_fixtures(sums_builder)

    def cleanup(self) -> None:
        self._tmp.cleanup()

    def _stub(self, name: str, content: str) -> None:
        path = self.stub_dir / name
        path.write_text(content)
        path.chmod(0o755)

    def _write_stubs(self, with_curl: bool, with_wget: bool) -> None:
        for name in ("mktemp", "awk", "sha256sum", "tar", "gzip", "install", "mkdir", "rm", "cp"):
            real = shutil.which(name)
            if real is None:
                raise RuntimeError(f"验收需要系统工具: {name}")
            self._stub(name, f'#!/bin/sh\nexec {real} "$@"\n')
        self._stub("id", FAKE_ID.replace("@REAL@", shutil.which("id")))
        self._stub("uname", FAKE_UNAME.replace("@REAL@", shutil.which("uname")))
        if with_curl:
            self._stub("curl", FAKE_CURL)
        if with_wget:
            self._stub("wget", FAKE_WGET)

    def _write_fixtures(self, sums_builder) -> None:
        binary = AGENT_BINARY.encode()
        hashes = {}
        for arch in ("amd64", "arm64"):
            name = asset_name(VERSION, arch)
            info = tarfile.TarInfo(f"NexTerm-server_{VERSION}_linux_{arch}/nexterm-server")
            info.size = len(binary)
            info.mode = 0o755
            info.mtime = 0
            with tarfile.open(self.release / name, "w:gz") as tar:
                tar.addfile(info, io.BytesIO(binary))
            hashes[name] = hashlib.sha256((self.release / name).read_bytes()).hexdigest()
        if sums_builder is None:
            lines = [f"{hashes[asset_name(VERSION, arch)]}  {asset_name(VERSION, arch)}" for arch in ("amd64", "arm64")]
            lines.append(f"{'0' * 64}  NexTerm-desktop_{VERSION}_linux_amd64.tar.gz")
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
            "FAKE_AGENT_VERSION": VERSION,
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
        return self.home / ".local" / "bin" / "nexterm-server"


def enroll_call(data_dir: str, insecure: bool = False) -> list[str]:
    call = ["agent", "enroll", "--server", SERVER_URL, "--code", ENROLL_CODE]
    if insecure:
        call.append("--insecure")
    call += ["--data-dir", data_dir]
    return call


def test_help() -> None:
    harness = Harness()
    try:
        result = harness.run("--help")
        check("help 退出码为 0 且打印用法", result.returncode == 0 and "用法" in result.stdout, result)
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
            check(f"缺少 {flag} 时报用法错误", result.returncode == 2 and flag in result.stderr, result.stderr)
        result = harness.run(*harness.base_args(), "--bogus")
        check("未知参数报用法错误", result.returncode == 2 and "未知参数" in result.stderr, result.stderr)
        check("参数错误时不发生下载", harness.urls() == [], harness.urls())
    finally:
        harness.cleanup()


def test_root_refused() -> None:
    harness = Harness()
    try:
        result = harness.run(*harness.base_args(), env_extra={"FAKE_UID": "0"})
        check("拒绝 root 运行", result.returncode == 1 and "root" in result.stderr, result.stderr)
        check("root 拒绝时不发生下载", harness.urls() == [], harness.urls())
    finally:
        harness.cleanup()


def test_unsupported_os() -> None:
    harness = Harness()
    try:
        result = harness.run(*harness.base_args(), env_extra={"FAKE_UNAME_S": "Darwin"})
        check("非 Linux 明确报错", result.returncode == 1 and "仅支持 Linux" in result.stderr, result.stderr)
        check("非 Linux 时不发生下载", harness.urls() == [], harness.urls())
    finally:
        harness.cleanup()


def test_unsupported_arch() -> None:
    harness = Harness()
    try:
        result = harness.run(*harness.base_args(), env_extra={"FAKE_UNAME_M": "mips64"})
        check("不支持的架构明确报错", result.returncode == 1 and "不支持的 CPU 架构" in result.stderr, result.stderr)
        check("不支持架构时不发生下载", harness.urls() == [], harness.urls())
    finally:
        harness.cleanup()


def test_no_downloader() -> None:
    harness = Harness(with_curl=False, with_wget=False)
    try:
        result = harness.run(*harness.base_args())
        check("缺 curl 与 wget 时明确报错", result.returncode == 1 and "curl 或 wget" in result.stderr, result.stderr)
    finally:
        harness.cleanup()


def test_wget_fallback() -> None:
    harness = Harness(with_curl=False, with_wget=True)
    try:
        result = harness.run(*harness.base_args())
        expected = [
            f"{RELEASE_BASE}/v{VERSION}/{asset_name(VERSION, 'amd64')}",
            f"{RELEASE_BASE}/v{VERSION}/SHA256SUMS",
        ]
        check("wget 回退可完成安装", result.returncode == 0, result.stderr)
        check("wget 下载 URL 正确", harness.urls() == expected, harness.urls())
    finally:
        harness.cleanup()


def test_download_failure() -> None:
    harness = Harness()
    try:
        result = harness.run(*harness.base_args(version="8.8.8-missing"))
        check("下载失败明确报错", result.returncode == 1 and "下载失败" in result.stderr, result.stderr)
        check("下载失败时不安装不注册", harness.calls() == [] and not harness.installed_binary().exists(), harness.calls())
    finally:
        harness.cleanup()


def test_checksum_mismatch() -> None:
    harness = Harness(sums_builder=lambda hashes: f"{'a' * 64}  {asset_name(VERSION, 'amd64')}\n")
    try:
        result = harness.run(*harness.base_args())
        check("校验失败明确报错", result.returncode == 1 and "校验失败" in result.stderr, result.stderr)
        check("校验失败时不安装不注册", harness.calls() == [] and not harness.installed_binary().exists(), harness.calls())
    finally:
        harness.cleanup()


def test_checksum_missing_entry() -> None:
    harness = Harness(sums_builder=lambda hashes: f"{'a' * 64}  NexTerm-desktop_{VERSION}_linux_amd64.tar.gz\n")
    try:
        result = harness.run(*harness.base_args())
        check("SHA256SUMS 缺条目明确报错", result.returncode == 1 and "SHA256SUMS" in result.stderr, result.stderr)
        check("缺条目时不安装不注册", harness.calls() == [] and not harness.installed_binary().exists(), harness.calls())
    finally:
        harness.cleanup()


def test_checksum_not_hex() -> None:
    harness = Harness(sums_builder=lambda hashes: f"{'z' * 64}  {asset_name(VERSION, 'amd64')}\n")
    try:
        result = harness.run(*harness.base_args())
        check("非 hex 校验和明确报错", result.returncode == 1 and "不是合法 sha256" in result.stderr, result.stderr)
    finally:
        harness.cleanup()


def assert_success(harness: Harness, result: subprocess.CompletedProcess, arch: str, data_dir: str, insecure: bool = False) -> None:
    check(f"{arch} 安装成功", result.returncode == 0, result.stderr)
    expected_urls = [
        f"{RELEASE_BASE}/v{VERSION}/{asset_name(VERSION, arch)}",
        f"{RELEASE_BASE}/v{VERSION}/SHA256SUMS",
    ]
    check(f"{arch} 下载 URL 与顺序正确", harness.urls() == expected_urls, harness.urls())
    binary = harness.installed_binary()
    check("二进制安装到用户 bin 目录", binary.exists() and binary.read_text() == AGENT_BINARY, binary)
    if binary.exists():
        check("二进制权限 0755", binary.stat().st_mode & 0o777 == 0o755, oct(binary.stat().st_mode))
    expected_calls = [["--version"], enroll_call(data_dir, insecure), ["agent", "install", "--data-dir", data_dir]]
    check("命令顺序: --version -> enroll -> install 且参数精确", harness.calls() == expected_calls, harness.calls())
    output = result.stdout + result.stderr
    check("接入码不回显", ENROLL_CODE not in output, output)
    check("日志以 ******** 遮蔽接入码", "--code ********" in result.stdout, result.stdout)


def test_success_amd64() -> None:
    harness = Harness()
    try:
        result = harness.run(*harness.base_args())
        assert_success(harness, result, "amd64", f"{harness.home}/.local/share/NexTerm")
    finally:
        harness.cleanup()


def test_success_arm64_selects_exact_sum() -> None:
    def sums(hashes: dict) -> str:
        arm64 = asset_name(VERSION, "arm64")
        return f"{hashes[arm64]}  {arm64}\n" + f"{'b' * 64}  {asset_name(VERSION, 'amd64')}\n"

    harness = Harness(sums_builder=sums)
    try:
        result = harness.run(*harness.base_args(), env_extra={"FAKE_UNAME_M": "aarch64"})
        assert_success(harness, result, "arm64", f"{harness.home}/.local/share/NexTerm")
    finally:
        harness.cleanup()


def test_insecure_flag() -> None:
    harness = Harness()
    try:
        result = harness.run(*harness.base_args(), "--insecure")
        data_dir = f"{harness.home}/.local/share/NexTerm"
        check("--insecure 透传到 enroll", harness.calls() == [["--version"], enroll_call(data_dir, True), ["agent", "install", "--data-dir", data_dir]], harness.calls())
        check("--insecure 运行成功", result.returncode == 0, result.stderr)
    finally:
        harness.cleanup()


def test_data_dir_env_precedence() -> None:
    harness = Harness()
    try:
        spaced = "/tmp/fake data dir"
        result = harness.run(*harness.base_args(), env_extra={"NEXTERM_DATA_DIR": spaced})
        expected = [["--version"], enroll_call(spaced), ["agent", "install", "--data-dir", spaced]]
        check("NEXTERM_DATA_DIR 最优先且原样使用, 带空格仍是单 argv", result.returncode == 0 and harness.calls() == expected, (result.stderr, harness.calls()))
    finally:
        harness.cleanup()
    harness = Harness()
    try:
        xdg = "/tmp/fake-xdg"
        result = harness.run(*harness.base_args(), env_extra={"XDG_DATA_HOME": xdg})
        expected = [["--version"], enroll_call(f"{xdg}/NexTerm"), ["agent", "install", "--data-dir", f"{xdg}/NexTerm"]]
        check("XDG_DATA_HOME 次之且拼 /NexTerm", result.returncode == 0 and harness.calls() == expected, (result.stderr, harness.calls()))
    finally:
        harness.cleanup()
    harness = Harness()
    try:
        custom = "/tmp/custom-agent-data"
        result = harness.run(*harness.base_args(), "--data-dir", custom, env_extra={"NEXTERM_DATA_DIR": "/tmp/ignored"})
        expected = [["--version"], enroll_call(custom), ["agent", "install", "--data-dir", custom]]
        check("--data-dir flag 覆盖 env 且原样使用", result.returncode == 0 and harness.calls() == expected, (result.stderr, harness.calls()))
    finally:
        harness.cleanup()


def test_enroll_failure_blocks_install() -> None:
    harness = Harness()
    try:
        result = harness.run(*harness.base_args(), env_extra={"FAKE_ENROLL_FAIL": "1"})
        calls = harness.calls()
        check("enroll 失败时脚本非零退出", result.returncode == 1, result.stdout + result.stderr)
        check("enroll 失败时不执行 install", len(calls) == 2 and calls[1][:2] == ["agent", "enroll"], calls)
    finally:
        harness.cleanup()


def test_install_failure_reported() -> None:
    harness = Harness()
    try:
        result = harness.run(*harness.base_args(), env_extra={"FAKE_INSTALL_FAIL": "1"})
        calls = harness.calls()
        check("install 失败时脚本非零退出", result.returncode == 1, result.stdout + result.stderr)
        check("install 失败前 enroll 已执行", len(calls) == 3 and calls[1][:2] == ["agent", "enroll"] and calls[2][:2] == ["agent", "install"], calls)
    finally:
        harness.cleanup()


def test_version_mismatch() -> None:
    harness = Harness()
    try:
        result = harness.run(*harness.base_args(), env_extra={"FAKE_AGENT_VERSION": "0.0.0-wrong"})
        calls = harness.calls()
        check("二进制版本不一致明确报错", result.returncode == 1 and "不一致" in result.stderr, result.stderr)
        check("版本不一致时不 enroll 不 install", calls == [["--version"]], calls)
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
        test_download_failure,
        test_checksum_mismatch,
        test_checksum_missing_entry,
        test_checksum_not_hex,
        test_success_amd64,
        test_success_arm64_selects_exact_sum,
        test_insecure_flag,
        test_data_dir_env_precedence,
        test_enroll_failure_blocks_install,
        test_install_failure_reported,
        test_version_mismatch,
    ]
    for test in tests:
        print(f"== {test.__name__}", flush=True)
        test()
    print(f"\ninstall-device 验收: {len(PASSED)} passed, {len(FAILED)} failed", flush=True)
    if FAILED:
        for name in FAILED:
            print(f"  FAILED: {name}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
