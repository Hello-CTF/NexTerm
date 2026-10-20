#!/usr/bin/env python3
"""CIR2 acceptance tests for deploy/systemd/install.sh (stubs only, no root, no real systemd, no real secrets).

The installer is always run with --root into a temp directory; systemctl, openssl and
useradd are stubs in PATH that record their argv, so no production service, user or key
material is touched. Coverage:

  1. First install writes safe defaults (loopback listen, auth on, data dir, web root,
     master key file), generates master.key 0600, installs units, enables the server
  2. Upgrade preserves /etc/nexterm/*.env and master.key byte for byte, replaces the
     payload, backs it up, and restarts only the previously active unit
  3. Upgrade migrates legacy units (inline ExecStart flags, EnvironmentFile=-, stray
     copies under /usr/lib/systemd/system or /lib/systemd/system), including explicit
     non-default flag values, which move into the env files instead of being discarded
  4. Upgrade without env files installs the safe defaults (no fallback to the
     built-in 0.0.0.0:8080 listener)
  5. Upgrade backfills missing env keys from the examples without touching
     existing values, inserting a separator newline when the file lacks a trailing one
  6. The effective master key is preserved: an explicit NEXTERM_MASTER_KEY_FILE, an
     existing <data-dir>/master.key, or /etc/nexterm/master.key; a new key is generated
     only when none exists
  7. Rollback restores the previous payload and units, keeps env and master key,
     consumes the backup; a retry after a failed upgrade preserves the original
     rollback backup and active-unit state, recovers legacy unit settings from the
     backup, and keeps env files created by the failed attempt from being treated
     as explicit configuration
  8. NEXTERM_INSTALL_FAIL_AT aborts at the named step and leaves the previous
     payload, env and master key intact; a failed first install resumes and
     enables/starts the unit on retry
  9. Env parsing follows systemd EnvironmentFile semantics: CRLF line endings,
     last duplicate assignment wins, surrounding whitespace is trimmed before
     matching quotes are stripped, unsupported quoting fails closed before any
     key generation; unit selection follows systemd priority so a
     lower-priority legacy copy never overrides the effective /etc unit;
     legacy --master-key-file migrates as an explicit NEXTERM_MASTER_KEY_FILE
     with precedence over the data-dir fallback; legacy ExecStart values are
     tokenized with systemd quote/escape rules, and unparseable units fail
     closed before any replacement
  10. Upgrade with no active unit never restarts or enables anything; a pending
     retry and rollback reuse the saved original active-unit state
  11. --no-enable skips enable --now on first install; missing payload fails closed
  12. Missing/empty/relative NEXTERM_DATA_DIR resolve against the effective unit
     working directory: the server fallback <wd>/data is pinned on upgrades when
     it holds data, relative paths resolve against the previous working
     directory, and the effective key at those locations is preserved. Logical
     paths are lexically cleaned (.. and . resolved like the server's
     filepath.Join) before the staged-root prefix is applied, with containment
     verified; legacy ExecStart values honor systemd C-unescape (\\s, hex,
     octal, \\u/\\U) with relaxed-literal preservation of invalid escapes,
     distinct \\u/\\U noncharacter rules, empty-argument preservation (an
     empty --master-key-file override clears a stale env key and follows the
     data-dir fallback), and duplicate flags keep the last value like the
     CLI, while values containing newlines/carriage returns, unexpandable
     systemd specifiers, or bytes that fail EnvironmentFile UTF-8
     validation fail closed before any replacement; env values round-trip
     through systemd's env-file parser (quotes, escapes, concatenation,
     continuations that never swallow assignments after comments)
  13. A combination matrix (first install/upgrade x failure point x env origin x
     key location x active state) asserts the key-preservation, data-dir,
     enable, and restart invariants across all of the above
  14. Platform detection uses explicit evidence: non-Linux kernels (Darwin,
     Windows) and non-systemd init systems fail visibly before any mutation;
     a systemd /sbin/init symlink is accepted as evidence; supported distros
     (ID or ID_LIKE) are identified from /etc/os-release while unknown or
     missing os-release warns and proceeds; rollback is gated the same way;
     a missing systemctl fails visibly
  15. First install verifies the enabled unit reports active and warns with a
     journalctl hint when it does not (the install itself still succeeds)
  16. Installer console output never contains master-key material (generated or
     preexisting)
  17. Real packaged-archive smoke test (runs only when a server archive built
     by scripts/pack-linux-server.sh is available; live systemd and live
     distribution runs remain outside this suite). When the host can execute
     the archived Linux binary, the smoke also drives `init-admin` against a
     scratch data dir: random first install prints the password, re-run is
     silent, and a failed supplied-password create retries cleanly.
  18. Super-admin bootstrap (DEP01): on first install the server binary is
     invoked as `init-admin` with the effective environment before the unit
     is enabled, its stdout (the initial password) reaches the installer
     console only after a successful create, upgrades and re-runs never
     invoke it, an already-initialized store (exit 3) stays silent, a failed
     create (exit 1) aborts the install so a re-run resumes and retries, an
     already-issued init code the bootstrap cannot prove usable (exit 4)
     keeps the install failed with the incomplete marker instead of starting
     an uninitialized service, the child environment always matches the
     service's effective database configuration (explicit env-file values,
     empty ones included, win over ambient pollution), the supplied password
     is only passed through the environment and never echoed by the installer
     itself, auth off/platform and staged roots skip the bootstrap, and
     --no-enable still bootstraps.
  19. Real-root bootstrap leg (runs only as root on Linux with
     NEXTERM_TEST_REAL_ROOT_INSTALL=1; mutates the system, so disposable
     containers only): the bootstrap executes as the nexterm service user via
     runuser, files it creates are born service-owned, and pre-existing
     top-level and nested files in a custom data directory keep their
     original owners (no recursive ownership changes). A missing runuser
     fails closed, and an unprivileged (nobody) reader scanning
     /proc/<pid>/cmdline for the whole blocking bootstrap never finds the
     admin password or its variable name: secrets travel via the process
     environment only, never via argv. Live systemd, live distribution and
     LazyCat injection runs remain outside this suite.

    python3 scripts/test-systemd-install.py
"""

from __future__ import annotations

import itertools
import os
import pathlib
import platform
import shutil
import stat
import subprocess
import sys
import tempfile
import time

ROOT = pathlib.Path(__file__).resolve().parent.parent
SCRIPT = ROOT / "deploy" / "systemd" / "install.sh"
SYSTEMD_DIR = ROOT / "deploy" / "systemd"
UNITS = ("nexterm-server.service", "nexterm-onlyserver.service")
ENVS = ("nexterm.env", "onlyserver.env")
FAKE_KEY_HEX = "0123456789abcdef" * 4

PASSED: list[str] = []
FAILED: list[str] = []

LEGACY_UNIT = """[Unit]
Description=NexTerm server (legacy inline-flag layout)
After=network-online.target

[Service]
Type=simple
User=nexterm
Group=nexterm
EnvironmentFile=-/etc/nexterm/nexterm.env
ExecStart=/opt/nexterm/nexterm-server \\
  --listen 127.0.0.1:8080 \\
  --auth loopback \\
  --data-dir /var/lib/nexterm \\
  --web-root /opt/nexterm/web
Restart=on-failure

[Install]
WantedBy=multi-user.target
"""

LEGACY_UNIT_NON_DEFAULT = """[Unit]
Description=NexTerm server (legacy non-default inline flags)
After=network-online.target

[Service]
Type=simple
User=nexterm
Group=nexterm
EnvironmentFile=-/etc/nexterm/nexterm.env
ExecStart=/opt/nexterm/nexterm-server \\
  --listen 0.0.0.0:9000 \\
  --auth off \\
  --data-dir /srv/nexterm-data \\
  --web-root /srv/nexterm-web
Restart=on-failure

[Install]
WantedBy=multi-user.target
"""

LEGACY_UNIT_KEY_FILE = """[Unit]
Description=NexTerm server (legacy inline master-key-file)
After=network-online.target

[Service]
Type=simple
User=nexterm
Group=nexterm
EnvironmentFile=-/etc/nexterm/nexterm.env
ExecStart=/opt/nexterm/nexterm-server \\
  --listen 127.0.0.1:8080 \\
  --auth on \\
  --data-dir /srv/nexterm-data \\
  --web-root /srv/nexterm-web \\
  --master-key-file /srv/keys/legacy.key
Restart=on-failure

[Install]
WantedBy=multi-user.target
"""

SYSTEMCTL_STUB = """#!/bin/sh
printf '%s\\n' "$*" >> "$SYSTEMCTL_LOG"
cmd="${1:-}"
unit=""
for a in "$@"; do
  case "$a" in -*) ;; *) unit="$a" ;; esac
done
if [ "$cmd" = "is-active" ]; then
  if [ "${FAKE_FAIL_START:-}" = "1" ] && [ "$unit" = "nexterm-server.service" ]; then
    exit 3
  fi
  for u in $FAKE_ACTIVE_UNITS; do
    if [ "$unit" = "$u" ]; then exit 0; fi
  done
  if [ -n "${SYSTEMCTL_STATE:-}" ] && [ -f "$SYSTEMCTL_STATE" ] && grep -qx "$unit" "$SYSTEMCTL_STATE"; then
    exit 0
  fi
  exit 3
fi
if [ "$cmd" = "enable" ] || [ "$cmd" = "restart" ]; then
  if [ -n "${SYSTEMCTL_STATE:-}" ] && [ -n "$unit" ]; then
    printf '%s\\n' "$unit" >> "$SYSTEMCTL_STATE"
  fi
fi
exit 0
"""

UNAME_STUB = """#!/bin/sh
printf '%s\\n' "${FAKE_UNAME_NAME:-Linux}"
"""

OPENSSL_STUB = """#!/bin/sh
printf '%s\\n' "$*" >> "$OPENSSL_LOG"
if [ "${1:-}" = "rand" ] && [ "${2:-}" = "-hex" ]; then
  printf '%s\\n' "FAKEKEYHEXPLACEHOLDER"
  exit 0
fi
exit 1
"""

USERADD_STUB = """#!/bin/sh
printf '%s\\n' "$*" >> "$USERADD_LOG"
exit 0
"""

INIT_ADMIN_STUB = """#!/bin/sh
printf '%s\\n' "$*" >> "$INIT_ADMIN_LOG"
printf 'init-admin\\n' >> "$SYSTEMCTL_LOG"
{
  printf 'RUNUSER_USER=%s\\n' "$(id -un)"
  printf 'NEXTERM_AUTH=%s\\n' "${NEXTERM_AUTH:-}"
  printf 'NEXTERM_DATA_DIR=%s\\n' "${NEXTERM_DATA_DIR:-}"
  printf 'NEXTERM_DB=%s\\n' "${NEXTERM_DB:-}"
  printf 'NEXTERM_DB_DSN=%s\\n' "${NEXTERM_DB_DSN:-}"
  printf 'NEXTERM_DB_PASSWORD_FILE=%s\\n' "${NEXTERM_DB_PASSWORD_FILE:-}"
  printf 'NEXTERM_ADMIN_USERNAME=%s\\n' "${NEXTERM_ADMIN_USERNAME:-}"
  printf 'NEXTERM_ADMIN_PASSWORD=%s\\n' "${NEXTERM_ADMIN_PASSWORD:-}"
} >> "$INIT_ADMIN_ENV_LOG"
if [ "${INIT_ADMIN_TOUCH_DATA:-0}" = "1" ]; then
  : > "$NEXTERM_DATA_DIR/data.db"
fi
if [ -n "${INIT_ADMIN_STDOUT:-}" ]; then
  printf '%s\\n' "$INIT_ADMIN_STDOUT"
fi
if [ -n "${INIT_ADMIN_BLOCK_SECONDS:-}" ]; then
  sleep "$INIT_ADMIN_BLOCK_SECONDS"
fi
exit "${INIT_ADMIN_EXIT:-0}"
"""


def make_payload(base: pathlib.Path, version: str) -> pathlib.Path:
    payload = base / f"payload-{version}"
    (payload / "web").mkdir(parents=True)
    for name in ("nexterm-server", "nexterm-helper-linux-amd64", "nexterm-helper-linux-arm64"):
        binary = payload / name
        binary.write_text(f"#!/bin/sh\n# fake {name} {version}\n")
        binary.chmod(0o755)
    (payload / "web" / "index.html").write_text(f"<html>nexterm web {version}</html>\n")
    for unit in UNITS:
        shutil.copyfile(SYSTEMD_DIR / unit, payload / unit)
    for env in ENVS:
        shutil.copyfile(SYSTEMD_DIR / f"{env}.example", payload / f"{env}.example")
    return payload


def make_stubs(base: pathlib.Path, with_systemctl: bool = True, with_openssl: bool = True) -> pathlib.Path:
    stubs = base / "stubs"
    stubs.mkdir()
    if with_systemctl:
        (stubs / "systemctl").write_text(SYSTEMCTL_STUB)
    (stubs / "uname").write_text(UNAME_STUB)
    if with_openssl:
        (stubs / "openssl").write_text(OPENSSL_STUB.replace("FAKEKEYHEXPLACEHOLDER", FAKE_KEY_HEX))
    (stubs / "useradd").write_text(USERADD_STUB)
    (stubs / "init-admin").write_text(INIT_ADMIN_STUB)
    for stub in stubs.iterdir():
        stub.chmod(0o755)
    return stubs


OS_RELEASE_UBUNTU = (
    'ID=ubuntu\nID_LIKE=debian\nPRETTY_NAME="Ubuntu 24.04.1 LTS"\nVERSION_ID="24.04"\n'
)

_RUN_SEQ = itertools.count(1)


def seed_platform(root: pathlib.Path, systemd: str, os_release: str | None) -> None:
    run_marker = root / "run/systemd/system"
    if run_marker.is_dir():
        shutil.rmtree(run_marker)
    init_link = root / "sbin/init"
    if init_link.is_symlink():
        init_link.unlink()
    if systemd == "run":
        (root / "run/systemd/system").mkdir(parents=True, exist_ok=True)
    elif systemd == "systemd-link":
        sbin = root / "sbin"
        sbin.mkdir(parents=True, exist_ok=True)
        (sbin / "init").symlink_to("/lib/systemd/systemd")
    elif systemd == "sysv":
        sbin = root / "sbin"
        sbin.mkdir(parents=True, exist_ok=True)
        (sbin / "init").symlink_to("/lib/sysvinit/init")
    if os_release == "default":
        os_release = OS_RELEASE_UBUNTU
    os_release_path = root / "etc/os-release"
    if os_release is None:
        if os_release_path.exists():
            os_release_path.unlink()
    else:
        etc = root / "etc"
        etc.mkdir(parents=True, exist_ok=True)
        os_release_path.write_text(os_release)


def make_install_env(base: pathlib.Path, stubs: pathlib.Path, *, fail_at: str = "", active: str = "",
                     uname: str = "", fail_start: bool = False, init_admin: bool = False,
                     init_admin_exit: int = 0, init_admin_stdout: str = "",
                     admin_password: str | None = None, ambient: dict[str, str] | None = None,
                     init_admin_block: float = 0) -> dict[str, str]:
    env = dict(os.environ)
    env["PATH"] = f"{stubs}:{env['PATH']}"
    if ambient:
        env.update(ambient)
    env["SYSTEMCTL_LOG"] = str(base / "systemctl.log")
    env["OPENSSL_LOG"] = str(base / "openssl.log")
    env["USERADD_LOG"] = str(base / "useradd.log")
    env["FAKE_ACTIVE_UNITS"] = active
    env["NEXTERM_INSTALL_SYSTEMCTL"] = str(stubs / "systemctl")
    state = base / f"systemctl-state-{next(_RUN_SEQ)}"
    state.write_text("")
    env["SYSTEMCTL_STATE"] = str(state)
    if uname:
        env["FAKE_UNAME_NAME"] = uname
    else:
        env.pop("FAKE_UNAME_NAME", None)
    if fail_start:
        env["FAKE_FAIL_START"] = "1"
    else:
        env.pop("FAKE_FAIL_START", None)
    if fail_at:
        env["NEXTERM_INSTALL_FAIL_AT"] = fail_at
    else:
        env.pop("NEXTERM_INSTALL_FAIL_AT", None)
    if init_admin:
        env["NEXTERM_INSTALL_INIT_ADMIN"] = str(stubs / "init-admin")
        env["INIT_ADMIN_LOG"] = str(base / "init-admin.log")
        env["INIT_ADMIN_ENV_LOG"] = str(base / "init-admin-env.log")
        env["INIT_ADMIN_EXIT"] = str(init_admin_exit)
        if init_admin_block:
            env["INIT_ADMIN_BLOCK_SECONDS"] = str(init_admin_block)
        else:
            env.pop("INIT_ADMIN_BLOCK_SECONDS", None)
        if init_admin_stdout:
            env["INIT_ADMIN_STDOUT"] = init_admin_stdout
        else:
            env.pop("INIT_ADMIN_STDOUT", None)
    else:
        env.pop("NEXTERM_INSTALL_INIT_ADMIN", None)
        env.pop("INIT_ADMIN_STDOUT", None)
        env.pop("INIT_ADMIN_BLOCK_SECONDS", None)
    if admin_password is not None:
        env["NEXTERM_ADMIN_PASSWORD"] = admin_password
    else:
        env.pop("NEXTERM_ADMIN_PASSWORD", None)
    return env


def run_install(payload: pathlib.Path, root: pathlib.Path, stubs: pathlib.Path, base: pathlib.Path,
                args: tuple[str, ...] = (), fail_at: str = "", active: str = "",
                uname: str = "", systemd: str = "run", os_release: str | None = "default",
                fail_start: bool = False, init_admin: bool = False, init_admin_exit: int = 0,
                init_admin_stdout: str = "", admin_password: str | None = None,
                ambient: dict[str, str] | None = None, init_admin_block: float = 0) -> subprocess.CompletedProcess:
    seed_platform(root, systemd, os_release)
    env = make_install_env(base, stubs, fail_at=fail_at, active=active, uname=uname,
                           fail_start=fail_start, init_admin=init_admin, init_admin_exit=init_admin_exit,
                           init_admin_stdout=init_admin_stdout, admin_password=admin_password,
                           ambient=ambient, init_admin_block=init_admin_block)
    return subprocess.run(
        ["/bin/bash", str(SCRIPT), "--root", str(root), "--source", str(payload), *args],
        capture_output=True, text=True, env=env, timeout=60,
    )


def read(path: pathlib.Path) -> str:
    return path.read_text()


def mode(path: pathlib.Path) -> int:
    return stat.S_IMODE(os.stat(path).st_mode)


def env_values(path: pathlib.Path) -> dict[str, str]:
    values: dict[str, str] = {}
    for line in read(path).splitlines():
        line = line.strip()
        if not line or line.startswith("#") or "=" not in line:
            continue
        key, _, value = line.partition("=")
        value = value.strip()
        if len(value) >= 2 and value[0] == '"' and value[-1] == '"':
            inner = value[1:-1]
            out = ""
            i = 0
            while i < len(inner):
                if inner[i] == "\\" and i + 1 < len(inner) and inner[i + 1] in ('\\', '"'):
                    out += inner[i + 1]
                    i += 2
                else:
                    out += inner[i]
                    i += 1
            value = out
        values[key] = value
    return values


def systemctl_calls(base: pathlib.Path) -> list[str]:
    log = base / "systemctl.log"
    if not log.exists():
        return []
    return [line for line in read(log).splitlines() if line]


def openssl_calls(base: pathlib.Path) -> list[str]:
    log = base / "openssl.log"
    if not log.exists():
        return []
    return [line for line in read(log).splitlines() if line]


def init_admin_calls(base: pathlib.Path) -> list[str]:
    log = base / "init-admin.log"
    if not log.exists():
        return []
    return [line for line in read(log).splitlines() if line]


def init_admin_env(base: pathlib.Path) -> dict[str, str]:
    log = base / "init-admin-env.log"
    if not log.exists():
        return {}
    values: dict[str, str] = {}
    for line in read(log).splitlines():
        key, _, value = line.partition("=")
        values[key] = value
    return values


def seed_first_install(base: pathlib.Path, stubs: pathlib.Path, root: pathlib.Path) -> pathlib.Path:
    payload = make_payload(base, "v1")
    result = run_install(payload, root, stubs, base)
    assert result.returncode == 0, f"seed first install failed: {result.stderr}"
    return payload


def test_first_install_safe_defaults() -> None:
    with tempfile.TemporaryDirectory() as tmp:
        base = pathlib.Path(tmp)
        stubs = make_stubs(base)
        root = base / "root"
        payload = make_payload(base, "v1")
        result = run_install(payload, root, stubs, base)
        assert result.returncode == 0, result.stderr
        assert "mode: first install" in result.stdout, result.stdout
        assert read(root / "opt/nexterm/nexterm-server") == read(payload / "nexterm-server")
        assert read(root / "opt/nexterm/web/index.html") == "<html>nexterm web v1</html>\n"
        for unit in UNITS:
            assert read(root / "etc/systemd/system" / unit) == read(SYSTEMD_DIR / unit), unit
        for env in ENVS:
            env_file = root / "etc/nexterm" / env
            assert env_file.exists(), env
            assert mode(env_file) == 0o600, f"{env} mode {oct(mode(env_file))}"
        values = env_values(root / "etc/nexterm/nexterm.env")
        assert values["NEXTERM_LISTEN"] == "127.0.0.1:8080", values
        assert values["NEXTERM_AUTH"] == "on", values
        assert values["NEXTERM_DATA_DIR"] == "/var/lib/nexterm", values
        assert values["NEXTERM_WEB_ROOT"] == "/opt/nexterm/web", values
        assert values["NEXTERM_MASTER_KEY_FILE"] == "/etc/nexterm/master.key", values
        key = root / "etc/nexterm/master.key"
        assert read(key) == FAKE_KEY_HEX + "\n", "master key content"
        assert mode(key) == 0o600, oct(mode(key))
        assert mode(root / "var/lib/nexterm") == 0o700, oct(mode(root / "var/lib/nexterm"))
        calls = systemctl_calls(base)
        assert "daemon-reload" in calls, calls
        assert "enable --now nexterm-server.service" in calls, calls
        assert not [c for c in calls if c.startswith("restart")], calls
        assert not (base / "useradd.log").exists(), "useradd must not run for a staged root"


def test_upgrade_preserves_env_and_master_key() -> None:
    with tempfile.TemporaryDirectory() as tmp:
        base = pathlib.Path(tmp)
        stubs = make_stubs(base)
        root = base / "root"
        seed_first_install(base, stubs, root)
        env_file = root / "etc/nexterm/nexterm.env"
        custom_env = (
            "NEXTERM_LISTEN=0.0.0.0:9000\n"
            "NEXTERM_AUTH=on\n"
            "NEXTERM_DATA_DIR=/var/lib/nexterm\n"
            "NEXTERM_WEB_ROOT=/opt/nexterm/web\n"
            "NEXTERM_MASTER_KEY_FILE=/etc/nexterm/master.key\n"
            "NEXTERM_DB=sqlite\n"
        )
        env_file.write_text(custom_env)
        key_file = root / "etc/nexterm/master.key"
        key_file.write_text("preexisting-key-material\n")
        (base / "systemctl.log").unlink()
        (base / "openssl.log").unlink(missing_ok=True)
        payload_v2 = make_payload(base, "v2")
        result = run_install(payload_v2, root, stubs, base, active="nexterm-server.service")
        assert result.returncode == 0, result.stderr
        assert "mode: upgrade install" in result.stdout, result.stdout
        assert read(root / "opt/nexterm/nexterm-server") == read(payload_v2 / "nexterm-server")
        assert read(root / "opt/nexterm/web/index.html") == "<html>nexterm web v2</html>\n"
        assert read(env_file) == custom_env, "env was rewritten"
        assert read(key_file) == "preexisting-key-material\n", "master key was rewritten"
        assert openssl_calls(base) == [], "openssl must not run when master.key exists"
        calls = systemctl_calls(base)
        assert "daemon-reload" in calls, calls
        assert "restart nexterm-server.service" in calls, calls
        assert not [c for c in calls if c.startswith("enable")], calls
        backup = root / "opt/nexterm/.rollback"
        assert "v1" in read(backup / "nexterm-server"), "backup must hold the v1 binary"


def test_upgrade_migrates_legacy_units() -> None:
    with tempfile.TemporaryDirectory() as tmp:
        base = pathlib.Path(tmp)
        stubs = make_stubs(base)
        root = base / "root"
        seed_first_install(base, stubs, root)
        unit_path = root / "etc/systemd/system/nexterm-server.service"
        unit_path.write_text(LEGACY_UNIT)
        lib_dir = root / "usr/lib/systemd/system"
        lib_dir.mkdir(parents=True)
        (lib_dir / "nexterm-server.service").write_text(LEGACY_UNIT)
        payload_v2 = make_payload(base, "v2")
        result = run_install(payload_v2, root, stubs, base)
        assert result.returncode == 0, result.stderr
        assert "migrating legacy unit nexterm-server.service" in result.stdout, result.stdout
        assert read(unit_path) == read(SYSTEMD_DIR / "nexterm-server.service"), "unit not replaced"
        assert not (lib_dir / "nexterm-server.service").exists(), "legacy /usr/lib copy not removed"
        backup_units = root / "opt/nexterm/.rollback/units"
        assert read(backup_units / "etc/systemd/system/nexterm-server.service") == LEGACY_UNIT
        assert read(backup_units / "usr/lib/systemd/system/nexterm-server.service") == LEGACY_UNIT


def test_upgrade_without_env_writes_safe_defaults() -> None:
    with tempfile.TemporaryDirectory() as tmp:
        base = pathlib.Path(tmp)
        stubs = make_stubs(base)
        root = base / "root"
        legacy_bin = root / "opt/nexterm/nexterm-server"
        legacy_bin.parent.mkdir(parents=True)
        legacy_bin.write_text("# fake nexterm-server v0\n")
        payload_v2 = make_payload(base, "v2")
        result = run_install(payload_v2, root, stubs, base)
        assert result.returncode == 0, result.stderr
        assert "mode: upgrade install" in result.stdout, result.stdout
        values = env_values(root / "etc/nexterm/nexterm.env")
        assert values["NEXTERM_LISTEN"] == "127.0.0.1:8080", values
        assert values["NEXTERM_AUTH"] == "on", values
        assert values["NEXTERM_DATA_DIR"] == "/var/lib/nexterm", values
        assert values["NEXTERM_WEB_ROOT"] == "/opt/nexterm/web", values
        assert values["NEXTERM_MASTER_KEY_FILE"] == "/etc/nexterm/master.key", values
        assert read(root / "etc/nexterm/master.key") == FAKE_KEY_HEX + "\n"
        assert "was missing and has been regenerated" in result.stdout, result.stdout
        assert read(root / "etc/systemd/system/nexterm-server.service") == read(SYSTEMD_DIR / "nexterm-server.service")


def test_upgrade_backfills_missing_env_keys() -> None:
    with tempfile.TemporaryDirectory() as tmp:
        base = pathlib.Path(tmp)
        stubs = make_stubs(base)
        root = base / "root"
        legacy_bin = root / "opt/nexterm/nexterm-server"
        legacy_bin.parent.mkdir(parents=True)
        legacy_bin.write_text("# fake nexterm-server v0\n")
        etc = root / "etc/nexterm"
        etc.mkdir(parents=True)
        (etc / "nexterm.env").write_text("NEXTERM_DB=postgres\nNEXTERM_LISTEN=0.0.0.0:9000\n")
        payload_v2 = make_payload(base, "v2")
        result = run_install(payload_v2, root, stubs, base)
        assert result.returncode == 0, result.stderr
        values = env_values(etc / "nexterm.env")
        assert values["NEXTERM_LISTEN"] == "0.0.0.0:9000", "existing value must be preserved"
        assert values["NEXTERM_DB"] == "postgres", "existing value must be preserved"
        assert values["NEXTERM_AUTH"] == "on", values
        assert values["NEXTERM_DATA_DIR"] == "/var/lib/nexterm/data", "the previous fallback location must be pinned, not the example default"
        assert values["NEXTERM_WEB_ROOT"] == "/opt/nexterm/web", values
        assert values["NEXTERM_MASTER_KEY_FILE"] == "/etc/nexterm/master.key", values
        only = env_values(etc / "onlyserver.env")
        assert only["NEXTERM_LISTEN"] == "127.0.0.1:8080", only
        assert only["NEXTERM_AUTH"] == "on", only


def test_upgrade_preserves_data_dir_master_key() -> None:
    with tempfile.TemporaryDirectory() as tmp:
        base = pathlib.Path(tmp)
        stubs = make_stubs(base)
        root = base / "root"
        seed_first_install(base, stubs, root)
        (root / "etc/nexterm/nexterm.env").write_text(
            "NEXTERM_LISTEN=127.0.0.1:8080\nNEXTERM_AUTH=on\n"
            "NEXTERM_DATA_DIR=/var/lib/nexterm\nNEXTERM_WEB_ROOT=/opt/nexterm/web\nNEXTERM_DB=sqlite\n"
        )
        (root / "etc/nexterm/onlyserver.env").write_text(
            "NEXTERM_LISTEN=127.0.0.1:8080\nNEXTERM_AUTH=on\n"
            "NEXTERM_DATA_DIR=/var/lib/nexterm\nNEXTERM_DB=sqlite\n"
        )
        (root / "etc/nexterm/master.key").unlink()
        data_key = root / "var/lib/nexterm/master.key"
        data_key.write_text("data-dir-key-material\n")
        (base / "openssl.log").unlink(missing_ok=True)
        payload_v2 = make_payload(base, "v2")
        result = run_install(payload_v2, root, stubs, base)
        assert result.returncode == 0, result.stderr
        assert read(data_key) == "data-dir-key-material\n", "data-dir key must be untouched"
        assert not (root / "etc/nexterm/master.key").exists(), "no new /etc key may be generated"
        assert openssl_calls(base) == [], "no key generation while a data-dir key exists"
        for env in ENVS:
            values = env_values(root / "etc/nexterm" / env)
            assert values["NEXTERM_MASTER_KEY_FILE"] == "/var/lib/nexterm/master.key", (env, values)

    with tempfile.TemporaryDirectory() as tmp:
        base = pathlib.Path(tmp)
        stubs = make_stubs(base)
        root = base / "root"
        legacy_bin = root / "opt/nexterm/nexterm-server"
        legacy_bin.parent.mkdir(parents=True)
        legacy_bin.write_text("# fake nexterm-server v0\n")
        data_key = root / "var/lib/nexterm/master.key"
        data_key.parent.mkdir(parents=True)
        data_key.write_text("data-dir-key-material\n")
        (base / "openssl.log").unlink(missing_ok=True)
        payload_v2 = make_payload(base, "v2")
        result = run_install(payload_v2, root, stubs, base)
        assert result.returncode == 0, result.stderr
        assert read(data_key) == "data-dir-key-material\n", "data-dir key must be untouched"
        assert not (root / "etc/nexterm/master.key").exists(), "fresh env files must not trigger a new /etc key"
        assert openssl_calls(base) == [], "no key generation while a data-dir key exists"
        for env in ENVS:
            values = env_values(root / "etc/nexterm" / env)
            assert values["NEXTERM_MASTER_KEY_FILE"] == "/var/lib/nexterm/master.key", (env, values)


def test_upgrade_preserves_legacy_key_file_setting() -> None:
    with tempfile.TemporaryDirectory() as tmp:
        base = pathlib.Path(tmp)
        stubs = make_stubs(base)
        root = base / "root"
        seed_first_install(base, stubs, root)
        env_file = root / "etc/nexterm/nexterm.env"
        env_file.write_text(
            "NEXTERM_LISTEN=127.0.0.1:8080\nNEXTERM_AUTH=on\n"
            "NEXTERM_DATA_DIR=/var/lib/nexterm\nNEXTERM_WEB_ROOT=/opt/nexterm/web\n"
            "NEXTERM_MASTER_KEY_FILE=/srv/keys/nexterm.key\nNEXTERM_DB=sqlite\n"
        )
        legacy_key = root / "srv/keys/nexterm.key"
        legacy_key.parent.mkdir(parents=True)
        legacy_key.write_text("legacy-key-material\n")
        (base / "openssl.log").unlink(missing_ok=True)
        payload_v2 = make_payload(base, "v2")
        result = run_install(payload_v2, root, stubs, base)
        assert result.returncode == 0, result.stderr
        assert read(legacy_key) == "legacy-key-material\n", "legacy key file must be untouched"
        assert openssl_calls(base) == [], "no key generation while the configured key exists"
        values = env_values(env_file)
        assert values["NEXTERM_MASTER_KEY_FILE"] == "/srv/keys/nexterm.key", values


def test_upgrade_migrates_legacy_non_default_flags() -> None:
    with tempfile.TemporaryDirectory() as tmp:
        base = pathlib.Path(tmp)
        stubs = make_stubs(base)
        root = base / "root"
        seed_first_install(base, stubs, root)
        (root / "etc/systemd/system/nexterm-server.service").write_text(LEGACY_UNIT_NON_DEFAULT)
        (root / "etc/nexterm/nexterm.env").write_text("NEXTERM_LISTEN=127.0.0.1:8080\n")
        data_key = root / "srv/nexterm-data/master.key"
        data_key.parent.mkdir(parents=True)
        data_key.write_text("srv-data-key\n")
        (base / "openssl.log").unlink(missing_ok=True)
        payload_v2 = make_payload(base, "v2")
        result = run_install(payload_v2, root, stubs, base)
        assert result.returncode == 0, result.stderr
        values = env_values(root / "etc/nexterm/nexterm.env")
        assert values["NEXTERM_LISTEN"] == "0.0.0.0:9000", "legacy flag must win over the env default"
        assert values["NEXTERM_AUTH"] == "off", values
        assert values["NEXTERM_DATA_DIR"] == "/srv/nexterm-data", values
        assert values["NEXTERM_WEB_ROOT"] == "/srv/nexterm-web", values
        assert values["NEXTERM_MASTER_KEY_FILE"] == "/srv/nexterm-data/master.key", values
        assert read(data_key) == "srv-data-key\n", "migrated data-dir key must be adopted, not regenerated"
        assert openssl_calls(base) == [], "no key generation while the migrated data-dir key exists"
        assert read(root / "etc/systemd/system/nexterm-server.service") == read(SYSTEMD_DIR / "nexterm-server.service")


def test_backfill_without_trailing_newline() -> None:
    with tempfile.TemporaryDirectory() as tmp:
        base = pathlib.Path(tmp)
        stubs = make_stubs(base)
        root = base / "root"
        seed_first_install(base, stubs, root)
        env_file = root / "etc/nexterm/nexterm.env"
        env_file.write_text("NEXTERM_DB=sqlite\n# local settings")
        payload_v2 = make_payload(base, "v2")
        result = run_install(payload_v2, root, stubs, base)
        assert result.returncode == 0, result.stderr
        values = env_values(env_file)
        assert values["NEXTERM_LISTEN"] == "127.0.0.1:8080", "listener must not be swallowed by the final comment"
        assert values["NEXTERM_DB"] == "sqlite", values
        lines = read(env_file).splitlines()
        assert "# local settings" in lines, lines
        assert "NEXTERM_LISTEN=127.0.0.1:8080" in lines, lines

    with tempfile.TemporaryDirectory() as tmp:
        base = pathlib.Path(tmp)
        stubs = make_stubs(base)
        root = base / "root"
        seed_first_install(base, stubs, root)
        env_file = root / "etc/nexterm/nexterm.env"
        env_file.write_text("NEXTERM_DB=sqlite")
        payload_v2 = make_payload(base, "v2")
        result = run_install(payload_v2, root, stubs, base)
        assert result.returncode == 0, result.stderr
        values = env_values(env_file)
        assert values["NEXTERM_DB"] == "sqlite", "final value must not be corrupted by the append"
        assert values["NEXTERM_LISTEN"] == "127.0.0.1:8080", values


def test_rollback_restores_previous_payload() -> None:
    with tempfile.TemporaryDirectory() as tmp:
        base = pathlib.Path(tmp)
        stubs = make_stubs(base)
        root = base / "root"
        payload_v1 = seed_first_install(base, stubs, root)
        unit_path = root / "etc/systemd/system/nexterm-server.service"
        unit_path.write_text(LEGACY_UNIT)
        env_file = root / "etc/nexterm/nexterm.env"
        env_file.write_text("NEXTERM_LISTEN=0.0.0.0:9000\n")
        payload_v2 = make_payload(base, "v2")
        result = run_install(payload_v2, root, stubs, base, active="nexterm-server.service")
        assert result.returncode == 0, result.stderr
        assert read(root / "opt/nexterm/nexterm-server") != read(payload_v1 / "nexterm-server")
        env_after_upgrade = read(env_file)
        assert "NEXTERM_LISTEN=127.0.0.1:8080" in env_after_upgrade.splitlines(), "legacy unit flags migrate into the env during the upgrade"
        (base / "systemctl.log").unlink()
        rollback = run_install(payload_v2, root, stubs, base, args=("--rollback",), active="nexterm-server.service")
        assert rollback.returncode == 0, rollback.stderr
        assert read(root / "opt/nexterm/nexterm-server") == read(payload_v1 / "nexterm-server"), "binary not restored"
        assert read(unit_path) == LEGACY_UNIT, "unit not restored"
        assert read(env_file) == env_after_upgrade, "env must not be touched by rollback"
        assert read(root / "etc/nexterm/master.key") == FAKE_KEY_HEX + "\n", "master key must not be touched"
        calls = systemctl_calls(base)
        assert "daemon-reload" in calls, calls
        assert "restart nexterm-server.service" in calls, calls
        assert (root / "opt/nexterm/.rollback.failed/nexterm-server").exists(), "replaced payload must be kept"
        assert not (root / "opt/nexterm/.rollback").exists(), "backup must be consumed"
        again = run_install(payload_v2, root, stubs, base, args=("--rollback",))
        assert again.returncode != 0, "second rollback must fail"
        assert "no rollback backup" in again.stderr, again.stderr


def test_failure_injection_leaves_previous_payload_intact() -> None:
    with tempfile.TemporaryDirectory() as tmp:
        base = pathlib.Path(tmp)
        stubs = make_stubs(base)
        root = base / "root"
        payload_v1 = seed_first_install(base, stubs, root)
        binary = root / "opt/nexterm/nexterm-server"
        payload_v2 = make_payload(base, "v2")

        failed = run_install(payload_v2, root, stubs, base, fail_at="binaries")
        assert failed.returncode != 0, "injected failure must abort"
        assert "injected failure at step: binaries" in failed.stderr, failed.stderr
        assert read(binary) == read(payload_v1 / "nexterm-server"), "binary must be untouched"
        assert (root / "opt/nexterm/.rollback/nexterm-server").exists(), "backup must exist before mutation"

        failed = run_install(payload_v2, root, stubs, base, fail_at="units")
        assert failed.returncode != 0, "injected failure must abort"
        assert read(binary) == read(payload_v2 / "nexterm-server"), "binaries are replaced before units"
        assert read(root / "etc/systemd/system/nexterm-server.service") == read(SYSTEMD_DIR / "nexterm-server.service")
        assert read(root / "etc/nexterm/master.key") == FAKE_KEY_HEX + "\n", "master key must be intact"

        rollback = run_install(payload_v2, root, stubs, base, args=("--rollback",))
        assert rollback.returncode == 0, rollback.stderr
        assert read(binary) == read(payload_v1 / "nexterm-server"), "rollback must restore v1"


def test_failed_masterkey_retry_preserves_data_dir_key() -> None:
    with tempfile.TemporaryDirectory() as tmp:
        base = pathlib.Path(tmp)
        stubs = make_stubs(base)
        root = base / "root"
        legacy_bin = root / "opt/nexterm/nexterm-server"
        legacy_bin.parent.mkdir(parents=True)
        legacy_bin.write_text("# fake nexterm-server v0\n")
        data_key = root / "var/lib/nexterm/master.key"
        data_key.parent.mkdir(parents=True)
        data_key.write_text("data-dir-key-material\n")
        payload_v2 = make_payload(base, "v2")

        failed = run_install(payload_v2, root, stubs, base, fail_at="masterkey")
        assert failed.returncode != 0, "injected failure must abort"
        assert (root / "etc/nexterm/nexterm.env").exists(), "the failed attempt already installed the example env"
        assert not (root / "etc/nexterm/master.key").exists(), "the failed attempt must not generate a key"

        retry = run_install(payload_v2, root, stubs, base)
        assert retry.returncode == 0, retry.stderr
        assert "preserving rollback backup" in retry.stdout, retry.stdout
        assert read(data_key) == "data-dir-key-material\n", "data-dir key must be untouched"
        assert not (root / "etc/nexterm/master.key").exists(), "retry must not generate a new /etc key"
        for env in ENVS:
            values = env_values(root / "etc/nexterm" / env)
            assert values["NEXTERM_MASTER_KEY_FILE"] == "/var/lib/nexterm/master.key", (env, values)


def test_failed_env_retry_recovers_legacy_values() -> None:
    with tempfile.TemporaryDirectory() as tmp:
        base = pathlib.Path(tmp)
        stubs = make_stubs(base)
        root = base / "root"
        legacy_bin = root / "opt/nexterm/nexterm-server"
        legacy_bin.parent.mkdir(parents=True)
        legacy_bin.write_text("# fake nexterm-server v0\n")
        (root / "etc/systemd/system").mkdir(parents=True)
        (root / "etc/systemd/system/nexterm-server.service").write_text(LEGACY_UNIT_NON_DEFAULT)
        data_key = root / "srv/nexterm-data/master.key"
        data_key.parent.mkdir(parents=True)
        data_key.write_text("srv-data-key\n")
        payload_v2 = make_payload(base, "v2")

        failed = run_install(payload_v2, root, stubs, base, fail_at="env")
        assert failed.returncode != 0, "injected failure must abort"
        assert read(root / "etc/systemd/system/nexterm-server.service") == read(SYSTEMD_DIR / "nexterm-server.service"), "the failed attempt already replaced the unit"

        retry = run_install(payload_v2, root, stubs, base)
        assert retry.returncode == 0, retry.stderr
        assert "recovering legacy unit settings" in retry.stdout, retry.stdout
        values = env_values(root / "etc/nexterm/nexterm.env")
        assert values["NEXTERM_LISTEN"] == "0.0.0.0:9000", values
        assert values["NEXTERM_AUTH"] == "off", values
        assert values["NEXTERM_DATA_DIR"] == "/srv/nexterm-data", values
        assert values["NEXTERM_WEB_ROOT"] == "/srv/nexterm-web", values
        assert values["NEXTERM_MASTER_KEY_FILE"] == "/srv/nexterm-data/master.key", values
        assert read(data_key) == "srv-data-key\n", "recovered data-dir key must be adopted, not regenerated"


def test_quoted_data_dir_and_duplicate_assignments() -> None:
    with tempfile.TemporaryDirectory() as tmp:
        base = pathlib.Path(tmp)
        stubs = make_stubs(base)
        root = base / "root"
        seed_first_install(base, stubs, root)
        env_file = root / "etc/nexterm/nexterm.env"
        env_file.write_text(
            "NEXTERM_LISTEN=127.0.0.1:8080\nNEXTERM_AUTH=on\n"
            "NEXTERM_DATA_DIR=/bogus\nNEXTERM_DATA_DIR=\"/srv/nexterm-data\"\n"
            "NEXTERM_WEB_ROOT=/opt/nexterm/web\nNEXTERM_DB=sqlite\n"
        )
        data_key = root / "srv/nexterm-data/master.key"
        data_key.parent.mkdir(parents=True)
        data_key.write_text("quoted-key\n")
        payload_v2 = make_payload(base, "v2")
        result = run_install(payload_v2, root, stubs, base)
        assert result.returncode == 0, result.stderr
        values = env_values(env_file)
        assert values["NEXTERM_MASTER_KEY_FILE"] == "/srv/nexterm-data/master.key", "last assignment wins and quotes are stripped"
        assert read(data_key) == "quoted-key\n", "quoted data-dir key must be adopted"

    with tempfile.TemporaryDirectory() as tmp:
        base = pathlib.Path(tmp)
        stubs = make_stubs(base)
        root = base / "root"
        seed_first_install(base, stubs, root)
        env_file = root / "etc/nexterm/nexterm.env"
        env_file.write_text(
            "NEXTERM_LISTEN=127.0.0.1:8080\nNEXTERM_AUTH=on\n"
            "NEXTERM_DATA_DIR=\"/srv/nexterm-data\n"
            "NEXTERM_WEB_ROOT=/opt/nexterm/web\nNEXTERM_DB=sqlite\n"
        )
        payload_v2 = make_payload(base, "v2")
        result = run_install(payload_v2, root, stubs, base)
        assert result.returncode == 0, result.stderr
        values = env_values(env_file)
        assert values["NEXTERM_DATA_DIR"] == "\"/srv/nexterm-data", values
        assert values["NEXTERM_MASTER_KEY_FILE"] == "/etc/nexterm/master.key", "an unclosed quote resolves per systemd semantics (content after the opening quote)"

    for raw_line, label in (
        ('NEXTERM_DATA_DIR="/srv/nexterm-data" trailing', "text after a closing quote concatenates per systemd semantics"),
        ("NEXTERM_DATA_DIR=/srv/nexterm\\-data", "unquoted escapes drop the backslash per systemd semantics"),
        ('NEXTERM_DATA_DIR="/srv/a" "/srv/b"', "quote concatenation joins into one value per systemd semantics"),
    ):
        with tempfile.TemporaryDirectory() as tmp:
            base = pathlib.Path(tmp)
            stubs = make_stubs(base)
            root = base / "root"
            seed_first_install(base, stubs, root)
            etc_key_before = read(root / "etc/nexterm/master.key")
            env_file = root / "etc/nexterm/nexterm.env"
            env_file.write_text(
                "NEXTERM_LISTEN=127.0.0.1:8080\nNEXTERM_AUTH=on\n"
                + raw_line + "\n"
                "NEXTERM_WEB_ROOT=/opt/nexterm/web\nNEXTERM_DB=sqlite\n"
            )
            payload_v2 = make_payload(base, "v2")
            result = run_install(payload_v2, root, stubs, base)
            assert result.returncode == 0, (label, result.stderr)
            assert read(root / "etc/nexterm/master.key") == etc_key_before, (label, "the seed key must be preserved")
            assert raw_line.split("=", 1)[1] in read(env_file), (label, "the raw value must be preserved byte for byte")
            values = env_values(env_file)
            assert values["NEXTERM_MASTER_KEY_FILE"] == "/etc/nexterm/master.key", (label, values)


def test_lower_priority_legacy_unit_does_not_override_etc() -> None:
    with tempfile.TemporaryDirectory() as tmp:
        base = pathlib.Path(tmp)
        stubs = make_stubs(base)
        root = base / "root"
        seed_first_install(base, stubs, root)
        (root / "etc/nexterm/nexterm.env").write_text(
            "NEXTERM_LISTEN=127.0.0.1:9000\nNEXTERM_AUTH=on\n"
            "NEXTERM_DATA_DIR=/var/lib/nexterm\nNEXTERM_WEB_ROOT=/opt/nexterm/web\n"
            "NEXTERM_MASTER_KEY_FILE=/etc/nexterm/master.key\nNEXTERM_DB=sqlite\n"
        )
        lib_dir = root / "usr/lib/systemd/system"
        lib_dir.mkdir(parents=True)
        (lib_dir / "nexterm-server.service").write_text(LEGACY_UNIT_NON_DEFAULT)
        payload_v2 = make_payload(base, "v2")
        result = run_install(payload_v2, root, stubs, base)
        assert result.returncode == 0, result.stderr
        values = env_values(root / "etc/nexterm/nexterm.env")
        assert values["NEXTERM_LISTEN"] == "127.0.0.1:9000", "the effective /etc unit wins over the lower-priority copy"
        assert values["NEXTERM_AUTH"] == "on", values
        assert not (lib_dir / "nexterm-server.service").exists(), "lower-priority copy must still be removed"
        assert "migrating legacy unit" not in result.stdout, "lower-priority legacy values must not be applied"
        backup_units = root / "opt/nexterm/.rollback/units"
        assert read(backup_units / "usr/lib/systemd/system/nexterm-server.service") == LEGACY_UNIT_NON_DEFAULT

    with tempfile.TemporaryDirectory() as tmp:
        base = pathlib.Path(tmp)
        stubs = make_stubs(base)
        root = base / "root"
        seed_first_install(base, stubs, root)
        (root / "etc/systemd/system/nexterm-server.service").write_text(LEGACY_UNIT_NON_DEFAULT)
        other_legacy = LEGACY_UNIT_NON_DEFAULT.replace("0.0.0.0:9000", "0.0.0.0:9001").replace("/srv/nexterm-data", "/srv/other-data")
        lib_dir = root / "usr/lib/systemd/system"
        lib_dir.mkdir(parents=True)
        (lib_dir / "nexterm-server.service").write_text(other_legacy)
        payload_v2 = make_payload(base, "v2")
        result = run_install(payload_v2, root, stubs, base)
        assert result.returncode == 0, result.stderr
        values = env_values(root / "etc/nexterm/nexterm.env")
        assert values["NEXTERM_LISTEN"] == "0.0.0.0:9000", "only the effective /etc unit's values migrate"
        assert values["NEXTERM_DATA_DIR"] == "/srv/nexterm-data", values
        assert not (lib_dir / "nexterm-server.service").exists(), "lower-priority copy must still be removed"


def test_first_install_masterkey_retry_preserves_data_dir_key() -> None:
    with tempfile.TemporaryDirectory() as tmp:
        base = pathlib.Path(tmp)
        stubs = make_stubs(base)
        root = base / "root"
        data_key = root / "var/lib/nexterm/master.key"
        data_key.parent.mkdir(parents=True)
        data_key.write_text("data-dir-key-material\n")
        payload = make_payload(base, "v1")

        failed = run_install(payload, root, stubs, base, fail_at="masterkey")
        assert failed.returncode != 0, "injected failure must abort"
        assert (root / "etc/nexterm/nexterm.env").exists(), "the failed attempt already installed the example env"
        assert not (root / "etc/nexterm/master.key").exists(), "the failed attempt must not generate a key"

        (base / "systemctl.log").unlink(missing_ok=True)
        retry = run_install(payload, root, stubs, base)
        assert retry.returncode == 0, retry.stderr
        assert "resuming incomplete first install" in retry.stdout, retry.stdout
        assert read(data_key) == "data-dir-key-material\n", "data-dir key must be untouched"
        assert not (root / "etc/nexterm/master.key").exists(), "retry must adopt the data-dir key, not generate a new /etc key"
        for env in ENVS:
            values = env_values(root / "etc/nexterm" / env)
            assert values["NEXTERM_MASTER_KEY_FILE"] == "/var/lib/nexterm/master.key", (env, values)
        calls = systemctl_calls(base)
        assert "enable --now nexterm-server.service" in calls, "the resumed first install must enable and start the unit"


def test_legacy_master_key_file_flag_migrates_with_precedence() -> None:
    def seed(base: pathlib.Path, root: pathlib.Path) -> None:
        legacy_bin = root / "opt/nexterm/nexterm-server"
        legacy_bin.parent.mkdir(parents=True)
        legacy_bin.write_text("# fake nexterm-server v0\n")
        (root / "etc/systemd/system").mkdir(parents=True)
        (root / "etc/systemd/system/nexterm-server.service").write_text(LEGACY_UNIT_KEY_FILE)
        data_key = root / "srv/nexterm-data/master.key"
        data_key.parent.mkdir(parents=True)
        data_key.write_text("data-dir-key\n")
        explicit_key = root / "srv/keys/legacy.key"
        explicit_key.parent.mkdir(parents=True)
        explicit_key.write_text("explicit-legacy-key\n")

    def assert_migrated(root: pathlib.Path) -> None:
        values = env_values(root / "etc/nexterm/nexterm.env")
        assert values["NEXTERM_MASTER_KEY_FILE"] == "/srv/keys/legacy.key", "explicit legacy --master-key-file must win over the data-dir fallback"
        assert read(root / "srv/keys/legacy.key") == "explicit-legacy-key\n"
        assert read(root / "srv/nexterm-data/master.key") == "data-dir-key\n"

    with tempfile.TemporaryDirectory() as tmp:
        base = pathlib.Path(tmp)
        stubs = make_stubs(base)
        root = base / "root"
        seed(base, root)
        payload_v2 = make_payload(base, "v2")
        result = run_install(payload_v2, root, stubs, base)
        assert result.returncode == 0, result.stderr
        assert "migrated legacy unit setting NEXTERM_MASTER_KEY_FILE=/srv/keys/legacy.key" in result.stdout, result.stdout
        assert_migrated(root)

    with tempfile.TemporaryDirectory() as tmp:
        base = pathlib.Path(tmp)
        stubs = make_stubs(base)
        root = base / "root"
        seed(base, root)
        payload_v2 = make_payload(base, "v2")
        failed = run_install(payload_v2, root, stubs, base, fail_at="env")
        assert failed.returncode != 0, "injected failure must abort"
        retry = run_install(payload_v2, root, stubs, base)
        assert retry.returncode == 0, retry.stderr
        assert "recovering legacy unit settings" in retry.stdout, retry.stdout
        assert_migrated(root)


def test_env_value_whitespace_and_quote_variants() -> None:
    adopt_variants = [
        'NEXTERM_DATA_DIR=/srv/nexterm-data   ',
        '\tNEXTERM_DATA_DIR=/srv/nexterm-data\t',
        '  NEXTERM_DATA_DIR=  "/srv/nexterm-data"  ',
        "NEXTERM_DATA_DIR='/srv/nexterm-data'",
        'NEXTERM_DATA_DIR  =/srv/nexterm-data',
        'NEXTERM_DATA_DIR\t=\t/srv/nexterm-data',
    ]
    for variant in adopt_variants:
        with tempfile.TemporaryDirectory() as tmp:
            base = pathlib.Path(tmp)
            stubs = make_stubs(base)
            root = base / "root"
            seed_first_install(base, stubs, root)
            data_key = root / "srv/nexterm-data/master.key"
            data_key.parent.mkdir(parents=True)
            data_key.write_text("ws-key\n")
            (root / "etc/nexterm/nexterm.env").write_text(
                "NEXTERM_LISTEN=127.0.0.1:8080\nNEXTERM_AUTH=on\n" + variant + "\n"
                "NEXTERM_WEB_ROOT=/opt/nexterm/web\nNEXTERM_DB=sqlite\n"
            )
            payload_v2 = make_payload(base, "v2")
            result = run_install(payload_v2, root, stubs, base)
            assert result.returncode == 0, (variant, result.stderr)
            values = env_values(root / "etc/nexterm/nexterm.env")
            assert values["NEXTERM_MASTER_KEY_FILE"] == "/srv/nexterm-data/master.key", (variant, values)
            assert read(data_key) == "ws-key\n", variant

    with tempfile.TemporaryDirectory() as tmp:
        base = pathlib.Path(tmp)
        stubs = make_stubs(base)
        root = base / "root"
        seed_first_install(base, stubs, root)
        etc_key_before = read(root / "etc/nexterm/master.key")
        (root / "etc/nexterm/nexterm.env").write_text(
            "NEXTERM_LISTEN=127.0.0.1:8080\nNEXTERM_AUTH=on\n"
            'NEXTERM_DATA_DIR="/srv/nexterm-data" trailing\n'
            "NEXTERM_WEB_ROOT=/opt/nexterm/web\nNEXTERM_DB=sqlite\n"
        )
        payload_v2 = make_payload(base, "v2")
        result = run_install(payload_v2, root, stubs, base)
        assert result.returncode == 0, result.stderr
        assert read(root / "etc/nexterm/master.key") == etc_key_before, "the seed key must be preserved"
        values = env_values(root / "etc/nexterm/nexterm.env")
        assert values["NEXTERM_MASTER_KEY_FILE"] == "/etc/nexterm/master.key", values


def test_pending_retry_and_rollback_use_saved_active_units() -> None:
    with tempfile.TemporaryDirectory() as tmp:
        base = pathlib.Path(tmp)
        stubs = make_stubs(base)
        root = base / "root"
        seed_first_install(base, stubs, root)
        payload_v2 = make_payload(base, "v2")
        failed = run_install(payload_v2, root, stubs, base, fail_at="units", active="nexterm-server.service")
        assert failed.returncode != 0, "injected failure must abort"
        assert "nexterm-server.service" in read(root / "opt/nexterm/.rollback/active-units")

        (base / "systemctl.log").unlink(missing_ok=True)
        retry = run_install(payload_v2, root, stubs, base, active="")
        assert retry.returncode == 0, retry.stderr
        assert "reusing the active-unit state" in retry.stdout, retry.stdout
        calls = systemctl_calls(base)
        assert "restart nexterm-server.service" in calls, "retry must restart the originally active unit even when it is currently inactive"

    with tempfile.TemporaryDirectory() as tmp:
        base = pathlib.Path(tmp)
        stubs = make_stubs(base)
        root = base / "root"
        seed_first_install(base, stubs, root)
        payload_v2 = make_payload(base, "v2")
        failed = run_install(payload_v2, root, stubs, base, fail_at="units", active="nexterm-server.service")
        assert failed.returncode != 0, "injected failure must abort"

        (base / "systemctl.log").unlink(missing_ok=True)
        rollback = run_install(payload_v2, root, stubs, base, args=("--rollback",), active="")
        assert rollback.returncode == 0, rollback.stderr
        calls = systemctl_calls(base)
        assert "restart nexterm-server.service" in calls, "rollback must restart the originally active unit even when it is currently inactive"


def test_install_upgrade_combination_matrix() -> None:
    key_paths = {
        "none": "/etc/nexterm/master.key",
        "fallback": "/var/lib/nexterm/data/master.key",
        "data_dir": "/var/lib/nexterm/master.key",
        "etc": "/etc/nexterm/master.key",
        "explicit": "/custom/nexterm.key",
    }

    def expected_data_dir(mode: str, env_origin: str, key: str) -> str:
        if env_origin == "missing-datadir":
            return "/var/lib/nexterm/data"
        if env_origin == "empty-datadir":
            return ""
        if env_origin == "relative-datadir":
            return "data"
        if env_origin == "absent" and key == "fallback":
            return "/var/lib/nexterm/data"
        return "/var/lib/nexterm"

    def seed_env(root: pathlib.Path, env_origin: str) -> None:
        env_file = root / "etc/nexterm/nexterm.env"
        if env_origin == "absent":
            return
        env_file.parent.mkdir(parents=True, exist_ok=True)
        lines = ["NEXTERM_LISTEN=127.0.0.1:8080", "NEXTERM_AUTH=on"]
        if env_origin in ("plain", "plain-crlf", "explicit"):
            lines.append("NEXTERM_DATA_DIR=/var/lib/nexterm")
        elif env_origin == "empty-datadir":
            lines.append("NEXTERM_DATA_DIR=")
        elif env_origin == "relative-datadir":
            lines.append("NEXTERM_DATA_DIR=data")
        lines.append("NEXTERM_DB=sqlite")
        if env_origin == "explicit":
            lines.append("NEXTERM_MASTER_KEY_FILE=/custom/nexterm.key")
        ending = "\r\n" if env_origin == "plain-crlf" else "\n"
        env_file.write_bytes((ending.join(lines) + ending).encode())

    def seed_key(root: pathlib.Path, key: str) -> pathlib.Path:
        paths = {
            "fallback": "var/lib/nexterm/data/master.key",
            "data_dir": "var/lib/nexterm/master.key",
            "etc": "etc/nexterm/master.key",
            "explicit": "custom/nexterm.key",
        }
        seeded = root / paths[key]
        seeded.parent.mkdir(parents=True, exist_ok=True)
        seeded.write_text(f"seeded-{key}-key-material\n")
        return seeded

    pairs = [
        ("absent", "none"), ("absent", "fallback"), ("absent", "etc"),
        ("plain", "none"), ("plain", "data_dir"), ("plain", "etc"),
        ("plain-crlf", "data_dir"),
        ("missing-datadir", "fallback"),
        ("empty-datadir", "fallback"),
        ("relative-datadir", "fallback"),
        ("explicit", "explicit"),
    ]
    combos = []
    for failure in (None, "env", "masterkey", "units"):
        for env_origin, key in pairs[:3]:
            combos.append(("first", failure, env_origin, key, False))
        for env_origin, key in pairs:
            for active in (False, True):
                combos.append(("upgrade", failure, env_origin, key, active))

    for mode, failure, env_origin, key, active in combos:
        label = f"{mode}/fail={failure}/env={env_origin}/key={key}/active={active}"
        with tempfile.TemporaryDirectory() as tmp:
            base = pathlib.Path(tmp)
            stubs = make_stubs(base)
            root = base / "root"
            if mode == "upgrade":
                legacy_bin = root / "opt/nexterm/nexterm-server"
                legacy_bin.parent.mkdir(parents=True)
                legacy_bin.write_text("# fake nexterm-server v0\n")
            seed_env(root, env_origin)
            seeded_key = None
            if key != "none":
                seeded_key = seed_key(root, key)

            payload_v2 = make_payload(base, "v2")
            active_unit = "nexterm-server.service" if active else ""
            if failure is None:
                result = run_install(payload_v2, root, stubs, base, active=active_unit)
                assert result.returncode == 0, (label, result.stderr)
            else:
                failed = run_install(payload_v2, root, stubs, base, fail_at=failure, active=active_unit)
                assert failed.returncode != 0, (label, "injected failure must abort")
                retry = run_install(payload_v2, root, stubs, base, active="")
                assert retry.returncode == 0, (label, retry.stderr)

            values = env_values(root / "etc/nexterm/nexterm.env")
            assert values["NEXTERM_MASTER_KEY_FILE"] == key_paths[key], (label, values)
            assert values.get("NEXTERM_DATA_DIR", "") == expected_data_dir(mode, env_origin, key), (label, values)
            if seeded_key is not None:
                assert read(seeded_key) == f"seeded-{key}-key-material\n", (label, "seeded key must be untouched")
            else:
                assert read(root / "etc/nexterm/master.key") == FAKE_KEY_HEX + "\n", (label, "a fresh key must be generated")
            calls = systemctl_calls(base)
            enabled = "enable --now nexterm-server.service" in calls
            restarted = "restart nexterm-server.service" in calls
            assert enabled == (mode == "first"), (label, calls)
            assert restarted == (mode == "upgrade" and active), (label, calls)
            assert not (root / "opt/nexterm/.install-incomplete").exists(), label
            assert not (root / "opt/nexterm/.rollback/pending").exists(), label


def test_crlf_env_file_preserves_key() -> None:
    with tempfile.TemporaryDirectory() as tmp:
        base = pathlib.Path(tmp)
        stubs = make_stubs(base)
        root = base / "root"
        seed_first_install(base, stubs, root)
        (root / "etc/nexterm/master.key").unlink()
        data_key = root / "srv/nexterm-data/master.key"
        data_key.parent.mkdir(parents=True)
        data_key.write_text("crlf-key\n")
        (root / "etc/nexterm/nexterm.env").write_bytes(
            b"NEXTERM_LISTEN=127.0.0.1:8080\r\nNEXTERM_AUTH=on\r\n"
            b"NEXTERM_DATA_DIR=/srv/nexterm-data\r\nNEXTERM_WEB_ROOT=/opt/nexterm/web\r\n"
            b"NEXTERM_DB=sqlite\r\n"
        )
        (root / "etc/nexterm/onlyserver.env").write_text(
            "NEXTERM_LISTEN=127.0.0.1:8080\nNEXTERM_AUTH=on\n"
            "NEXTERM_DATA_DIR=/srv/nexterm-data\nNEXTERM_DB=sqlite\n"
        )
        (base / "openssl.log").unlink(missing_ok=True)
        payload_v2 = make_payload(base, "v2")
        result = run_install(payload_v2, root, stubs, base)
        assert result.returncode == 0, result.stderr
        values = env_values(root / "etc/nexterm/nexterm.env")
        assert values["NEXTERM_MASTER_KEY_FILE"] == "/srv/nexterm-data/master.key", values
        assert read(data_key) == "crlf-key\n", "CRLF env must resolve to the same key path systemd will use"
        assert not (root / "etc/nexterm/master.key").exists(), "no new /etc key may be generated"
        assert openssl_calls(base) == [], "no key generation while the data-dir key exists"


def test_legacy_quoted_paths_tokenization() -> None:
    legacy_quoted = """[Unit]
Description=NexTerm server (legacy quoted paths)
After=network-online.target

[Service]
Type=simple
User=nexterm
Group=nexterm
EnvironmentFile=-/etc/nexterm/nexterm.env
WorkingDirectory=/var/lib/nexterm
ExecStart=/opt/nexterm/nexterm-server \\
  --listen 0.0.0.0:9000 \\
  --auth off \\
  --data-dir "/srv/nexterm data" \\
  --web-root "/srv/nexterm web" \\
  --master-key-file "/srv/keys/legacy key"
Restart=on-failure

[Install]
WantedBy=multi-user.target
"""

    def seed(root: pathlib.Path) -> None:
        legacy_bin = root / "opt/nexterm/nexterm-server"
        legacy_bin.parent.mkdir(parents=True)
        legacy_bin.write_text("# fake nexterm-server v0\n")
        (root / "etc/systemd/system").mkdir(parents=True)
        (root / "etc/systemd/system/nexterm-server.service").write_text(legacy_quoted)
        data_key = root / "srv/nexterm data/master.key"
        data_key.parent.mkdir(parents=True)
        data_key.write_text("spaced-data-key\n")
        explicit_key = root / "srv/keys/legacy key"
        explicit_key.parent.mkdir(parents=True)
        explicit_key.write_text("spaced-explicit-key\n")

    def assert_migrated(root: pathlib.Path) -> None:
        values = env_values(root / "etc/nexterm/nexterm.env")
        assert values["NEXTERM_LISTEN"] == "0.0.0.0:9000", values
        assert values["NEXTERM_AUTH"] == "off", values
        assert values["NEXTERM_DATA_DIR"] == "/srv/nexterm data", values
        assert values["NEXTERM_WEB_ROOT"] == "/srv/nexterm web", values
        assert values["NEXTERM_MASTER_KEY_FILE"] == "/srv/keys/legacy key", values
        assert read(root / "srv/keys/legacy key") == "spaced-explicit-key\n"
        assert read(root / "srv/nexterm data/master.key") == "spaced-data-key\n"

    with tempfile.TemporaryDirectory() as tmp:
        base = pathlib.Path(tmp)
        stubs = make_stubs(base)
        root = base / "root"
        seed(root)
        payload_v2 = make_payload(base, "v2")
        result = run_install(payload_v2, root, stubs, base)
        assert result.returncode == 0, result.stderr
        assert_migrated(root)

    with tempfile.TemporaryDirectory() as tmp:
        base = pathlib.Path(tmp)
        stubs = make_stubs(base)
        root = base / "root"
        seed(root)
        payload_v2 = make_payload(base, "v2")
        failed = run_install(payload_v2, root, stubs, base, fail_at="env")
        assert failed.returncode != 0, "injected failure must abort"
        retry = run_install(payload_v2, root, stubs, base)
        assert retry.returncode == 0, retry.stderr
        assert "recovering legacy unit settings" in retry.stdout, retry.stdout
        assert_migrated(root)

    with tempfile.TemporaryDirectory() as tmp:
        base = pathlib.Path(tmp)
        stubs = make_stubs(base)
        root = base / "root"
        seed(root)
        unit_path = root / "etc/systemd/system/nexterm-server.service"
        unit_path.write_text(unit_path.read_text().replace('"/srv/keys/legacy key"', '"/srv/keys/legacy key'))
        payload_v2 = make_payload(base, "v2")
        failed = run_install(payload_v2, root, stubs, base)
        assert failed.returncode != 0, "unterminated quoting must fail closed"
        assert "unsupported quoting" in failed.stderr, failed.stderr
        assert "legacy quoted paths" in read(unit_path), "the unit must not be replaced when parsing fails"
        assert not (root / "etc/nexterm/master.key").exists(), "no key may be generated after a parse failure"


def test_missing_and_empty_data_dir_preserve_fallback() -> None:
    with tempfile.TemporaryDirectory() as tmp:
        base = pathlib.Path(tmp)
        stubs = make_stubs(base)
        root = base / "root"
        legacy_bin = root / "opt/nexterm/nexterm-server"
        legacy_bin.parent.mkdir(parents=True)
        legacy_bin.write_text("# fake nexterm-server v0\n")
        fallback = root / "var/lib/nexterm/data"
        fallback.mkdir(parents=True)
        (fallback / "data.db").write_text("db-bytes\n")
        (fallback / "master.key").write_text("fallback-key\n")
        (base / "openssl.log").unlink(missing_ok=True)
        payload_v2 = make_payload(base, "v2")
        result = run_install(payload_v2, root, stubs, base)
        assert result.returncode == 0, result.stderr
        values = env_values(root / "etc/nexterm/nexterm.env")
        assert values["NEXTERM_DATA_DIR"] == "/var/lib/nexterm/data", "the fallback location must be pinned"
        assert values["NEXTERM_MASTER_KEY_FILE"] == "/var/lib/nexterm/data/master.key", values
        assert read(fallback / "master.key") == "fallback-key\n"
        assert read(fallback / "data.db") == "db-bytes\n"
        assert not (root / "etc/nexterm/master.key").exists(), "no new /etc key may be generated"
        assert openssl_calls(base) == []

    with tempfile.TemporaryDirectory() as tmp:
        base = pathlib.Path(tmp)
        stubs = make_stubs(base)
        root = base / "root"
        legacy_bin = root / "opt/nexterm/nexterm-server"
        legacy_bin.parent.mkdir(parents=True)
        legacy_bin.write_text("# fake nexterm-server v0\n")
        fallback = root / "var/lib/nexterm/data"
        fallback.mkdir(parents=True)
        (fallback / "data.db").write_text("db-bytes\n")
        (fallback / "master.key").write_text("fallback-key\n")
        etc = root / "etc/nexterm"
        etc.mkdir(parents=True)
        (etc / "nexterm.env").write_text(
            "NEXTERM_LISTEN=127.0.0.1:8080\nNEXTERM_AUTH=on\nNEXTERM_DATA_DIR=\n"
            "NEXTERM_WEB_ROOT=/opt/nexterm/web\nNEXTERM_DB=sqlite\n"
        )
        payload_v2 = make_payload(base, "v2")
        result = run_install(payload_v2, root, stubs, base)
        assert result.returncode == 0, result.stderr
        values = env_values(etc / "nexterm.env")
        assert values["NEXTERM_DATA_DIR"] == "", "an explicit empty value must be preserved"
        assert values["NEXTERM_MASTER_KEY_FILE"] == "/var/lib/nexterm/data/master.key", values
        assert read(fallback / "master.key") == "fallback-key\n"
        assert not (root / "etc/nexterm/master.key").exists()

    with tempfile.TemporaryDirectory() as tmp:
        base = pathlib.Path(tmp)
        stubs = make_stubs(base)
        root = base / "root"
        legacy_bin = root / "opt/nexterm/nexterm-server"
        legacy_bin.parent.mkdir(parents=True)
        legacy_bin.write_text("# fake nexterm-server v0\n")
        (root / "etc/systemd/system").mkdir(parents=True)
        (root / "etc/systemd/system/nexterm-server.service").write_text(
            LEGACY_UNIT.replace("  --data-dir /var/lib/nexterm \\\n", "").replace(
                "EnvironmentFile=-/etc/nexterm/nexterm.env",
                "EnvironmentFile=-/etc/nexterm/nexterm.env\nWorkingDirectory=/srv/wd",
            ),
        )
        fallback = root / "srv/wd/data"
        fallback.mkdir(parents=True)
        (fallback / "master.key").write_text("custom-wd-key\n")
        payload_v2 = make_payload(base, "v2")
        result = run_install(payload_v2, root, stubs, base)
        assert result.returncode == 0, result.stderr
        values = env_values(root / "etc/nexterm/nexterm.env")
        assert values["NEXTERM_DATA_DIR"] == "/srv/wd/data", "the legacy unit's working-directory fallback must be pinned"
        assert values["NEXTERM_MASTER_KEY_FILE"] == "/srv/wd/data/master.key", values
        assert read(fallback / "master.key") == "custom-wd-key\n"


def test_relative_data_dir_resolved_against_working_dir() -> None:
    with tempfile.TemporaryDirectory() as tmp:
        base = pathlib.Path(tmp)
        stubs = make_stubs(base)
        root = base / "root"
        seed_first_install(base, stubs, root)
        (root / "etc/nexterm/master.key").unlink()
        fallback = root / "var/lib/nexterm/data"
        fallback.mkdir(parents=True)
        (fallback / "master.key").write_text("relative-key\n")
        (root / "etc/nexterm/nexterm.env").write_text(
            "NEXTERM_LISTEN=127.0.0.1:8080\nNEXTERM_AUTH=on\nNEXTERM_DATA_DIR=data\n"
            "NEXTERM_WEB_ROOT=/opt/nexterm/web\nNEXTERM_DB=sqlite\n"
        )
        (root / "etc/nexterm/onlyserver.env").write_text(
            "NEXTERM_LISTEN=127.0.0.1:8080\nNEXTERM_AUTH=on\nNEXTERM_DATA_DIR=data\nNEXTERM_DB=sqlite\n"
        )
        payload_v2 = make_payload(base, "v2")
        result = run_install(payload_v2, root, stubs, base)
        assert result.returncode == 0, result.stderr
        values = env_values(root / "etc/nexterm/nexterm.env")
        assert values["NEXTERM_DATA_DIR"] == "data", "the relative value must be preserved when the working directory is unchanged"
        assert values["NEXTERM_MASTER_KEY_FILE"] == "/var/lib/nexterm/data/master.key", values
        assert read(fallback / "master.key") == "relative-key\n"
        assert not (root / "etc/nexterm/master.key").exists()

    with tempfile.TemporaryDirectory() as tmp:
        base = pathlib.Path(tmp)
        stubs = make_stubs(base)
        root = base / "root"
        legacy_bin = root / "opt/nexterm/nexterm-server"
        legacy_bin.parent.mkdir(parents=True)
        legacy_bin.write_text("# fake nexterm-server v0\n")
        (root / "etc/systemd/system").mkdir(parents=True)
        (root / "etc/systemd/system/nexterm-server.service").write_text(
            LEGACY_UNIT.replace("  --data-dir /var/lib/nexterm", "  --data-dir rel-data").replace(
                "EnvironmentFile=-/etc/nexterm/nexterm.env",
                "EnvironmentFile=-/etc/nexterm/nexterm.env\nWorkingDirectory=/srv/wd",
            ),
        )
        data_key = root / "srv/wd/rel-data/master.key"
        data_key.parent.mkdir(parents=True)
        data_key.write_text("relative-custom-wd-key\n")
        payload_v2 = make_payload(base, "v2")
        result = run_install(payload_v2, root, stubs, base)
        assert result.returncode == 0, result.stderr
        values = env_values(root / "etc/nexterm/nexterm.env")
        assert values["NEXTERM_DATA_DIR"] == "/srv/wd/rel-data", "a relative data dir must be absolutized against the previous working directory"
        assert values["NEXTERM_MASTER_KEY_FILE"] == "/srv/wd/rel-data/master.key", values
        assert read(data_key) == "relative-custom-wd-key\n"


def test_legacy_c_style_escapes() -> None:
    legacy_escaped = """[Unit]
Description=NexTerm server (legacy C-style escapes)
After=network-online.target

[Service]
Type=simple
User=nexterm
Group=nexterm
EnvironmentFile=-/etc/nexterm/nexterm.env
WorkingDirectory=/var/lib/nexterm
ExecStart=/opt/nexterm/nexterm-server \\
  --listen 0.0.0.0:9000 \\
  --auth off \\
  --data-dir /srv/nexterm\\sdata \\
  --web-root /srv/nexterm\\x20web \\
  --master-key-file /srv/keys/legacy\\skey
Restart=on-failure

[Install]
WantedBy=multi-user.target
"""

    def seed(root: pathlib.Path) -> None:
        legacy_bin = root / "opt/nexterm/nexterm-server"
        legacy_bin.parent.mkdir(parents=True)
        legacy_bin.write_text("# fake nexterm-server v0\n")
        (root / "etc/systemd/system").mkdir(parents=True)
        (root / "etc/systemd/system/nexterm-server.service").write_text(legacy_escaped)
        data_key = root / "srv/nexterm data/master.key"
        data_key.parent.mkdir(parents=True)
        data_key.write_text("escaped-data-key\n")
        explicit_key = root / "srv/keys/legacy key"
        explicit_key.parent.mkdir(parents=True)
        explicit_key.write_text("escaped-explicit-key\n")

    def assert_migrated(root: pathlib.Path) -> None:
        values = env_values(root / "etc/nexterm/nexterm.env")
        assert values["NEXTERM_DATA_DIR"] == "/srv/nexterm data", values
        assert values["NEXTERM_WEB_ROOT"] == "/srv/nexterm web", values
        assert values["NEXTERM_MASTER_KEY_FILE"] == "/srv/keys/legacy key", values
        assert read(root / "srv/keys/legacy key") == "escaped-explicit-key\n"
        assert read(root / "srv/nexterm data/master.key") == "escaped-data-key\n"

    with tempfile.TemporaryDirectory() as tmp:
        base = pathlib.Path(tmp)
        stubs = make_stubs(base)
        root = base / "root"
        seed(root)
        payload_v2 = make_payload(base, "v2")
        result = run_install(payload_v2, root, stubs, base)
        assert result.returncode == 0, result.stderr
        assert_migrated(root)

    with tempfile.TemporaryDirectory() as tmp:
        base = pathlib.Path(tmp)
        stubs = make_stubs(base)
        root = base / "root"
        seed(root)
        payload_v2 = make_payload(base, "v2")
        failed = run_install(payload_v2, root, stubs, base, fail_at="env")
        assert failed.returncode != 0, "injected failure must abort"
        retry = run_install(payload_v2, root, stubs, base)
        assert retry.returncode == 0, retry.stderr
        assert "recovering legacy unit settings" in retry.stdout, retry.stdout
        assert_migrated(root)

    with tempfile.TemporaryDirectory() as tmp:
        base = pathlib.Path(tmp)
        stubs = make_stubs(base)
        root = base / "root"
        legacy_bin = root / "opt/nexterm/nexterm-server"
        legacy_bin.parent.mkdir(parents=True)
        legacy_bin.write_text("# fake nexterm-server v0\n")
        (root / "etc/systemd/system").mkdir(parents=True)
        (root / "etc/systemd/system/nexterm-server.service").write_text(
            legacy_escaped.replace("/srv/keys/legacy\\skey", '"/srv/keys/legacy key')
        )
        payload_v2 = make_payload(base, "v2")
        failed = run_install(payload_v2, root, stubs, base)
        assert failed.returncode != 0, "unterminated quoting must fail closed"
        assert "unsupported quoting" in failed.stderr, failed.stderr
        assert "legacy C-style escapes" in read(root / "etc/systemd/system/nexterm-server.service"), "the unit must not be replaced when parsing fails"


def test_legacy_duplicate_flags_last_value_wins() -> None:
    legacy_dupe = """[Unit]
Description=NexTerm server (legacy duplicate flags)
After=network-online.target

[Service]
Type=simple
User=nexterm
Group=nexterm
EnvironmentFile=-/etc/nexterm/nexterm.env
WorkingDirectory=/var/lib/nexterm
ExecStart=/opt/nexterm/nexterm-server \\
  --listen 127.0.0.1:8080 \\
  --data-dir /srv/first \\
  --data-dir=/srv/last \\
  --master-key-file /srv/first.key \\
  --master-key-file=/srv/last.key
Restart=on-failure

[Install]
WantedBy=multi-user.target
"""

    def seed(root: pathlib.Path) -> None:
        legacy_bin = root / "opt/nexterm/nexterm-server"
        legacy_bin.parent.mkdir(parents=True)
        legacy_bin.write_text("# fake nexterm-server v0\n")
        (root / "etc/systemd/system").mkdir(parents=True)
        (root / "etc/systemd/system/nexterm-server.service").write_text(legacy_dupe)
        last_key = root / "srv/last.key"
        last_key.parent.mkdir(parents=True)
        last_key.write_text("last-key\n")

    def assert_migrated(root: pathlib.Path) -> None:
        values = env_values(root / "etc/nexterm/nexterm.env")
        assert values["NEXTERM_DATA_DIR"] == "/srv/last", values
        assert values["NEXTERM_MASTER_KEY_FILE"] == "/srv/last.key", values
        assert read(root / "srv/last.key") == "last-key\n"
        assert not (root / "srv/first.key").exists(), "the superseded first key must not be generated"

    with tempfile.TemporaryDirectory() as tmp:
        base = pathlib.Path(tmp)
        stubs = make_stubs(base)
        root = base / "root"
        seed(root)
        payload_v2 = make_payload(base, "v2")
        result = run_install(payload_v2, root, stubs, base)
        assert result.returncode == 0, result.stderr
        assert_migrated(root)

    with tempfile.TemporaryDirectory() as tmp:
        base = pathlib.Path(tmp)
        stubs = make_stubs(base)
        root = base / "root"
        seed(root)
        payload_v2 = make_payload(base, "v2")
        failed = run_install(payload_v2, root, stubs, base, fail_at="env")
        assert failed.returncode != 0, "injected failure must abort"
        retry = run_install(payload_v2, root, stubs, base)
        assert retry.returncode == 0, retry.stderr
        assert "recovering legacy unit settings" in retry.stdout, retry.stdout
        assert_migrated(root)


def test_path_normalization_and_containment() -> None:
    with tempfile.TemporaryDirectory() as tmp:
        base = pathlib.Path(tmp)
        stubs = make_stubs(base)
        root = base / "root"
        seed_first_install(base, stubs, root)
        (root / "etc/nexterm/master.key").unlink()
        data_key = root / "srv/nexterm-data/master.key"
        data_key.parent.mkdir(parents=True)
        data_key.write_text("dotdot-key\n")
        (root / "etc/nexterm/nexterm.env").write_text(
            "NEXTERM_LISTEN=127.0.0.1:8080\nNEXTERM_AUTH=on\n"
            "NEXTERM_DATA_DIR=/srv/missing/../nexterm-data\nNEXTERM_WEB_ROOT=/opt/nexterm/web\n"
            "NEXTERM_DB=sqlite\n"
        )
        (root / "etc/nexterm/onlyserver.env").write_text(
            "NEXTERM_LISTEN=127.0.0.1:8080\nNEXTERM_AUTH=on\n"
            "NEXTERM_DATA_DIR=/srv/missing/../nexterm-data\nNEXTERM_DB=sqlite\n"
        )
        payload_v2 = make_payload(base, "v2")
        result = run_install(payload_v2, root, stubs, base)
        assert result.returncode == 0, result.stderr
        values = env_values(root / "etc/nexterm/nexterm.env")
        assert values["NEXTERM_MASTER_KEY_FILE"] == "/srv/nexterm-data/master.key", "the cleaned key path must be adopted"
        assert read(data_key) == "dotdot-key\n"
        assert not (root / "etc/nexterm/master.key").exists()

    with tempfile.TemporaryDirectory() as tmp:
        base = pathlib.Path(tmp)
        stubs = make_stubs(base)
        root = base / "root"
        seed_first_install(base, stubs, root)
        (root / "etc/nexterm/master.key").unlink()
        data_key = root / "srv/nexterm-data/master.key"
        data_key.parent.mkdir(parents=True)
        data_key.write_text("dotdot-key\n")
        (root / "etc/nexterm/nexterm.env").write_text(
            "NEXTERM_LISTEN=127.0.0.1:8080\nNEXTERM_AUTH=on\n"
            "NEXTERM_DATA_DIR=/srv/missing/../nexterm-data\nNEXTERM_WEB_ROOT=/opt/nexterm/web\n"
            "NEXTERM_DB=sqlite\n"
        )
        (root / "etc/nexterm/onlyserver.env").write_text(
            "NEXTERM_LISTEN=127.0.0.1:8080\nNEXTERM_AUTH=on\n"
            "NEXTERM_DATA_DIR=/srv/missing/../nexterm-data\nNEXTERM_DB=sqlite\n"
        )
        payload_v2 = make_payload(base, "v2")
        failed = run_install(payload_v2, root, stubs, base, fail_at="masterkey")
        assert failed.returncode != 0, "injected failure must abort"
        retry = run_install(payload_v2, root, stubs, base)
        assert retry.returncode == 0, retry.stderr
        values = env_values(root / "etc/nexterm/nexterm.env")
        assert values["NEXTERM_MASTER_KEY_FILE"] == "/srv/nexterm-data/master.key", "retry must adopt the cleaned key path"
        assert read(data_key) == "dotdot-key\n"

    with tempfile.TemporaryDirectory() as tmp:
        base = pathlib.Path(tmp)
        stubs = make_stubs(base)
        root = base / "root"
        seed_first_install(base, stubs, root)
        (root / "etc/nexterm/master.key").unlink()
        escaped_key = root / "review-escaped/master.key"
        escaped_key.parent.mkdir(parents=True)
        escaped_key.write_text("contained-key\n")
        (root / "etc/nexterm/nexterm.env").write_text(
            "NEXTERM_LISTEN=127.0.0.1:8080\nNEXTERM_AUTH=on\n"
            "NEXTERM_DATA_DIR=/var/lib/nexterm\nNEXTERM_WEB_ROOT=/opt/nexterm/web\n"
            "NEXTERM_MASTER_KEY_FILE=/../review-escaped/master.key\nNEXTERM_DB=sqlite\n"
        )
        (root / "etc/nexterm/onlyserver.env").write_text(
            "NEXTERM_LISTEN=127.0.0.1:8080\nNEXTERM_AUTH=on\n"
            "NEXTERM_DATA_DIR=/var/lib/nexterm\nNEXTERM_DB=sqlite\n"
        )
        payload_v2 = make_payload(base, "v2")
        result = run_install(payload_v2, root, stubs, base)
        assert result.returncode == 0, result.stderr
        assert read(escaped_key) == "contained-key\n", "the escaped path must resolve inside the staged root"
        assert not (base / "review-escaped").exists(), "the staged root's parent must never be touched"
        values = env_values(root / "etc/nexterm/nexterm.env")
        assert values["NEXTERM_MASTER_KEY_FILE"] == "/../review-escaped/master.key", "the admin's raw value is preserved; systemd cleans it identically"


def test_legacy_newline_values_fail_closed() -> None:
    for spelling in ("legacy\\nkey", "legacy\\u000Akey"):
        for failure in (None, "env"):
            with tempfile.TemporaryDirectory() as tmp:
                base = pathlib.Path(tmp)
                stubs = make_stubs(base)
                root = base / "root"
                legacy_bin = root / "opt/nexterm/nexterm-server"
                legacy_bin.parent.mkdir(parents=True)
                legacy_bin.write_text("# fake nexterm-server v0\n")
                (root / "etc/systemd/system").mkdir(parents=True)
                (root / "etc/systemd/system/nexterm-server.service").write_text(
                    LEGACY_UNIT.replace("  --web-root /opt/nexterm/web", f"  --master-key-file /srv/keys/{spelling}\n  --web-root /opt/nexterm/web")
                )
                payload_v2 = make_payload(base, "v2")
                if failure is not None:
                    failed = run_install(payload_v2, root, stubs, base, fail_at=failure)
                    assert failed.returncode != 0, "injected failure must abort"
                result = run_install(payload_v2, root, stubs, base)
                assert result.returncode != 0, "newline values must fail closed"
                assert "contain a newline" in result.stderr, result.stderr
                assert "legacy inline-flag layout" in read(root / "etc/systemd/system/nexterm-server.service"), "the unit must not be replaced"
                assert not (root / "etc/nexterm/master.key").exists(), "no key may be generated"


def test_legacy_noncharacter_escape_round_trip() -> None:
    def seed(root: pathlib.Path, unit_text: str, key_rel: str) -> pathlib.Path:
        legacy_bin = root / "opt/nexterm/nexterm-server"
        legacy_bin.parent.mkdir(parents=True)
        legacy_bin.write_text("# fake nexterm-server v0\n")
        (root / "etc/systemd/system").mkdir(parents=True)
        (root / "etc/systemd/system/nexterm-server.service").write_text(unit_text)
        key = root / key_rel
        key.parent.mkdir(parents=True)
        key.write_text("literal-path-key\n")
        (root / "etc/nexterm").mkdir(parents=True, exist_ok=True)
        (root / "etc/nexterm/onlyserver.env").write_text(
            "NEXTERM_LISTEN=127.0.0.1:8080\nNEXTERM_AUTH=on\n"
            "NEXTERM_DATA_DIR=/var/lib/nexterm\nNEXTERM_DB=sqlite\n"
            'NEXTERM_MASTER_KEY_FILE="/srv/keys/legacy\\\\U0000FFFFkey"\n'
        )
        return key

    def assert_migrated(root: pathlib.Path, key_rel: str, expected: str) -> None:
        values = env_values(root / "etc/nexterm/nexterm.env")
        assert values["NEXTERM_MASTER_KEY_FILE"] == expected, values
        assert read(root / key_rel) == "literal-path-key\n"
        assert not (root / "etc/nexterm/master.key").exists(), "no key may be generated at a decoded path"

    for failure in (None, "env"):
        with tempfile.TemporaryDirectory() as tmp:
            base = pathlib.Path(tmp)
            stubs = make_stubs(base)
            root = base / "root"
            key = seed(root, LEGACY_UNIT.replace("  --web-root /opt/nexterm/web", "  --master-key-file /srv/keys/legacy\\U0000FFFFkey\n  --web-root /opt/nexterm/web"), "srv/keys/legacy\\U0000FFFFkey")
            payload_v2 = make_payload(base, "v2")
            if failure is not None:
                failed = run_install(payload_v2, root, stubs, base, fail_at=failure)
                assert failed.returncode != 0, "injected failure must abort"
            result = run_install(payload_v2, root, stubs, base)
            assert result.returncode == 0, result.stderr
            assert_migrated(root, "srv/keys/legacy\\U0000FFFFkey", "/srv/keys/legacy\\U0000FFFFkey")

    for failure in (None, "env"):
        with tempfile.TemporaryDirectory() as tmp:
            base = pathlib.Path(tmp)
            stubs = make_stubs(base)
            root = base / "root"
            key = seed(root, LEGACY_UNIT.replace("  --web-root /opt/nexterm/web", "  --web-root /srv/web\\x4Groot"), "srv/web\\x4Groot/master.key")
            payload_v2 = make_payload(base, "v2")
            if failure is not None:
                failed = run_install(payload_v2, root, stubs, base, fail_at=failure)
                assert failed.returncode != 0, "injected failure must abort"
            result = run_install(payload_v2, root, stubs, base)
            assert result.returncode == 0, result.stderr
            values = env_values(root / "etc/nexterm/nexterm.env")
            assert values["NEXTERM_WEB_ROOT"] == "/srv/web\\x4Groot", values
            assert read(key) == "literal-path-key\n"


def test_legacy_specifier_values_fail_closed() -> None:
    for failure in (None, "env"):
        with tempfile.TemporaryDirectory() as tmp:
            base = pathlib.Path(tmp)
            stubs = make_stubs(base)
            root = base / "root"
            legacy_bin = root / "opt/nexterm/nexterm-server"
            legacy_bin.parent.mkdir(parents=True)
            legacy_bin.write_text("# fake nexterm-server v0\n")
            (root / "etc/systemd/system").mkdir(parents=True)
            (root / "etc/systemd/system/nexterm-server.service").write_text(
                LEGACY_UNIT.replace("  --web-root /opt/nexterm/web", "  --master-key-file /srv/keys/legacy%ikey\n  --web-root /opt/nexterm/web")
            )
            payload_v2 = make_payload(base, "v2")
            if failure is not None:
                failed = run_install(payload_v2, root, stubs, base, fail_at=failure)
                assert failed.returncode != 0, "injected failure must abort"
            result = run_install(payload_v2, root, stubs, base)
            assert result.returncode != 0, "specifier values must fail closed"
            assert "specifiers" in result.stderr, result.stderr
            assert "legacy inline-flag layout" in read(root / "etc/systemd/system/nexterm-server.service"), "the unit must not be replaced"
            assert not (root / "etc/nexterm/master.key").exists(), "no key may be generated"

    for failure in (None, "env"):
        with tempfile.TemporaryDirectory() as tmp:
            base = pathlib.Path(tmp)
            stubs = make_stubs(base)
            root = base / "root"
            legacy_bin = root / "opt/nexterm/nexterm-server"
            legacy_bin.parent.mkdir(parents=True)
            legacy_bin.write_text("# fake nexterm-server v0\n")
            (root / "etc/systemd/system").mkdir(parents=True)
            (root / "etc/systemd/system/nexterm-server.service").write_text(
                LEGACY_UNIT.replace("  --web-root /opt/nexterm/web", "  --master-key-file /srv/keys/legacy%%key\n  --web-root /opt/nexterm/web")
            )
            percent_key = root / "srv/keys/legacy%key"
            percent_key.parent.mkdir(parents=True)
            percent_key.write_text("percent-key\n")
            payload_v2 = make_payload(base, "v2")
            if failure is not None:
                failed = run_install(payload_v2, root, stubs, base, fail_at=failure)
                assert failed.returncode != 0, "injected failure must abort"
            result = run_install(payload_v2, root, stubs, base)
            assert result.returncode == 0, result.stderr
            values = env_values(root / "etc/nexterm/nexterm.env")
            assert values["NEXTERM_MASTER_KEY_FILE"] == "/srv/keys/legacy%key", values
            assert read(percent_key) == "percent-key\n"


def test_legacy_newline_and_cr_values_fail_closed() -> None:
    for spelling in ("legacy\\x0Akey", "legacy\\012key", "legacy\\rkey"):
        for failure in (None, "env"):
            with tempfile.TemporaryDirectory() as tmp:
                base = pathlib.Path(tmp)
                stubs = make_stubs(base)
                root = base / "root"
                legacy_bin = root / "opt/nexterm/nexterm-server"
                legacy_bin.parent.mkdir(parents=True)
                legacy_bin.write_text("# fake nexterm-server v0\n")
                (root / "etc/systemd/system").mkdir(parents=True)
                (root / "etc/systemd/system/nexterm-server.service").write_text(
                    LEGACY_UNIT.replace("  --web-root /opt/nexterm/web", f"  --master-key-file /srv/keys/{spelling} \\\n  --web-root /opt/nexterm/web")
                )
                payload_v2 = make_payload(base, "v2")
                if failure is not None:
                    failed = run_install(payload_v2, root, stubs, base, fail_at=failure)
                    assert failed.returncode != 0, "injected failure must abort"
                result = run_install(payload_v2, root, stubs, base)
                assert result.returncode != 0, "LF/CR values must fail closed for every escape form"
                assert "contain a newline" in result.stderr, result.stderr
                assert "legacy inline-flag layout" in read(root / "etc/systemd/system/nexterm-server.service"), "the unit must not be replaced"
                assert not (root / "etc/nexterm/master.key").exists(), "no key may be generated"


def test_legacy_lowercase_u_decodes_noncharacters() -> None:
    def seed(root: pathlib.Path, flag_value: str, key_rel: str | None) -> pathlib.Path | None:
        legacy_bin = root / "opt/nexterm/nexterm-server"
        legacy_bin.parent.mkdir(parents=True)
        legacy_bin.write_text("# fake nexterm-server v0\n")
        (root / "etc/systemd/system").mkdir(parents=True)
        (root / "etc/systemd/system/nexterm-server.service").write_text(
            LEGACY_UNIT.replace("  --web-root /opt/nexterm/web", f"  --master-key-file {flag_value} \\\n  --web-root /opt/nexterm/web")
        )
        (root / "etc/nexterm").mkdir(parents=True, exist_ok=True)
        (root / "etc/nexterm/onlyserver.env").write_text(
            "NEXTERM_LISTEN=127.0.0.1:8080\nNEXTERM_AUTH=on\n"
            "NEXTERM_DATA_DIR=/var/lib/nexterm\nNEXTERM_DB=sqlite\n"
            'NEXTERM_MASTER_KEY_FILE="/srv/keys/legacy\\\\U0000FFFFkey"\n'
        )
        if key_rel is None:
            return None
        key = root / key_rel
        key.parent.mkdir(parents=True)
        key.write_text("decoded-path-key\n")
        return key

    for failure in (None, "env"):
        with tempfile.TemporaryDirectory() as tmp:
            base = pathlib.Path(tmp)
            stubs = make_stubs(base)
            root = base / "root"
            seed(root, "/srv/keys/legacy\\uFFFFkey", None)
            payload_v2 = make_payload(base, "v2")
            if failure is not None:
                failed = run_install(payload_v2, root, stubs, base, fail_at=failure)
                assert failed.returncode != 0, "injected failure must abort"
            result = run_install(payload_v2, root, stubs, base)
            assert result.returncode != 0, "a decoded noncharacter must fail closed, not be written to an unloadable env file"
            assert "cannot be represented in an environment file" in result.stderr, result.stderr
            assert "legacy inline-flag layout" in read(root / "etc/systemd/system/nexterm-server.service"), "the unit must not be replaced"
            assert not (root / "etc/nexterm/master.key").exists(), "no key may be generated"

    for failure in (None, "env"):
        with tempfile.TemporaryDirectory() as tmp:
            base = pathlib.Path(tmp)
            stubs = make_stubs(base)
            root = base / "root"
            key = seed(root, "/srv/keys/legacy\\U0000FFFFkey", "srv/keys/legacy\\U0000FFFFkey")
            payload_v2 = make_payload(base, "v2")
            if failure is not None:
                failed = run_install(payload_v2, root, stubs, base, fail_at=failure)
                assert failed.returncode != 0, "injected failure must abort"
            result = run_install(payload_v2, root, stubs, base)
            assert result.returncode == 0, result.stderr
            values = env_values(root / "etc/nexterm/nexterm.env")
            assert values["NEXTERM_MASTER_KEY_FILE"] == "/srv/keys/legacy\\U0000FFFFkey", "uppercase \\U keeps noncharacters literal like systemd"
            assert read(key) == "decoded-path-key\n"
            assert not (root / "etc/nexterm/master.key").exists()


def test_legacy_empty_master_key_arg_falls_back() -> None:
    for failure in (None, "env"):
        with tempfile.TemporaryDirectory() as tmp:
            base = pathlib.Path(tmp)
            stubs = make_stubs(base)
            root = base / "root"
            legacy_bin = root / "opt/nexterm/nexterm-server"
            legacy_bin.parent.mkdir(parents=True)
            legacy_bin.write_text("# fake nexterm-server v0\n")
            (root / "etc/systemd/system").mkdir(parents=True)
            (root / "etc/systemd/system/nexterm-server.service").write_text(
                LEGACY_UNIT.replace(
                    "  --web-root /opt/nexterm/web",
                    '  --data-dir /srv/empty-data \\\n  --master-key-file "" \\\n  --web-root /srv/web',
                )
            )
            data_key = root / "srv/empty-data/master.key"
            data_key.parent.mkdir(parents=True)
            data_key.write_text("empty-data-key\n")
            payload_v2 = make_payload(base, "v2")
            if failure is not None:
                failed = run_install(payload_v2, root, stubs, base, fail_at=failure)
                assert failed.returncode != 0, "injected failure must abort"
            result = run_install(payload_v2, root, stubs, base)
            assert result.returncode == 0, result.stderr
            values = env_values(root / "etc/nexterm/nexterm.env")
            assert values["NEXTERM_DATA_DIR"] == "/srv/empty-data", values
            assert values["NEXTERM_MASTER_KEY_FILE"] == "/srv/empty-data/master.key", "an empty master-key argument must fall back to the data-dir key, not the next flag"
            assert read(data_key) == "empty-data-key\n"
            assert not (root / "var/lib/nexterm/--web-root").exists(), "the next flag must never become the key path"


def test_env_comment_continuation_keeps_assignment() -> None:
    for failure in (None, "env"):
        with tempfile.TemporaryDirectory() as tmp:
            base = pathlib.Path(tmp)
            stubs = make_stubs(base)
            root = base / "root"
            seed_first_install(base, stubs, root)
            (root / "etc/nexterm/master.key").unlink()
            data_key = root / "srv/comment-data/master.key"
            data_key.parent.mkdir(parents=True)
            data_key.write_text("comment-key\n")
            (root / "etc/nexterm/nexterm.env").write_text(
                "# continuation comment \\\n"
                "NEXTERM_LISTEN=127.0.0.1:8080\nNEXTERM_AUTH=on\n"
                "NEXTERM_DATA_DIR=/srv/comment-data\nNEXTERM_WEB_ROOT=/opt/nexterm/web\n"
                "NEXTERM_DB=sqlite\n"
            )
            (root / "etc/nexterm/onlyserver.env").write_text(
                "NEXTERM_LISTEN=127.0.0.1:8080\nNEXTERM_AUTH=on\n"
                "NEXTERM_DATA_DIR=/srv/comment-data\nNEXTERM_DB=sqlite\n"
            )
            payload_v2 = make_payload(base, "v2")
            if failure is not None:
                failed = run_install(payload_v2, root, stubs, base, fail_at=failure)
                assert failed.returncode != 0, "injected failure must abort"
            result = run_install(payload_v2, root, stubs, base)
            assert result.returncode == 0, result.stderr
            values = env_values(root / "etc/nexterm/nexterm.env")
            assert values["NEXTERM_MASTER_KEY_FILE"] == "/srv/comment-data/master.key", "an assignment after a comment-ending backslash stays active"
            assert read(data_key) == "comment-key\n"
            assert not (root / "etc/nexterm/master.key").exists(), "no key may be generated"


def test_empty_data_dir_pinned_on_working_dir_change() -> None:
    for failure in (None, "env"):
        with tempfile.TemporaryDirectory() as tmp:
            base = pathlib.Path(tmp)
            stubs = make_stubs(base)
            root = base / "root"
            legacy_bin = root / "opt/nexterm/nexterm-server"
            legacy_bin.parent.mkdir(parents=True)
            legacy_bin.write_text("# fake nexterm-server v0\n")
            (root / "etc/systemd/system").mkdir(parents=True)
            (root / "etc/systemd/system/nexterm-server.service").write_text(
                LEGACY_UNIT.replace("  --data-dir /var/lib/nexterm \\\n", "").replace(
                    "EnvironmentFile=-/etc/nexterm/nexterm.env",
                    "EnvironmentFile=-/etc/nexterm/nexterm.env\nWorkingDirectory=/srv/old-wd",
                )
            )
            fallback = root / "srv/old-wd/data"
            fallback.mkdir(parents=True)
            (fallback / "data.db").write_text("db-bytes\n")
            (fallback / "master.key").write_text("old-wd-key\n")
            etc = root / "etc/nexterm"
            etc.mkdir(parents=True)
            (etc / "nexterm.env").write_text(
                "NEXTERM_LISTEN=127.0.0.1:8080\nNEXTERM_AUTH=on\nNEXTERM_DATA_DIR=\n"
                "NEXTERM_WEB_ROOT=/opt/nexterm/web\nNEXTERM_DB=sqlite\n"
            )
            (etc / "onlyserver.env").write_text(
                "NEXTERM_LISTEN=127.0.0.1:8080\nNEXTERM_AUTH=on\nNEXTERM_DATA_DIR=/srv/old-wd/data\n"
                "NEXTERM_DB=sqlite\n"
            )
            payload_v2 = make_payload(base, "v2")
            if failure is not None:
                failed = run_install(payload_v2, root, stubs, base, fail_at=failure)
                assert failed.returncode != 0, "injected failure must abort"
            result = run_install(payload_v2, root, stubs, base)
            assert result.returncode == 0, result.stderr
            values = env_values(etc / "nexterm.env")
            assert values["NEXTERM_DATA_DIR"] == "/srv/old-wd/data", "an explicit empty data-dir must be pinned to the previous fallback when the working directory changes"
            assert values["NEXTERM_MASTER_KEY_FILE"] == "/srv/old-wd/data/master.key", values
            assert read(fallback / "master.key") == "old-wd-key\n"
            assert read(fallback / "data.db") == "db-bytes\n"
            assert not (root / "etc/nexterm/master.key").exists(), "no key may be generated"


def test_legacy_empty_master_key_override_clears_stale_env_key() -> None:
    layouts = {
        "middle": "  --data-dir /srv/data \\\n  {empty_flag} \\\n  --web-root /srv/web",
        "trailing": "  --data-dir /srv/data \\\n  --web-root /srv/web \\\n  {empty_flag}",
    }
    for position, layout in layouts.items():
        for empty_flag in ('--master-key-file ""', "--master-key-file ''", "--master-key-file="):
            for failure in (None, "env"):
                with tempfile.TemporaryDirectory() as tmp:
                    base = pathlib.Path(tmp)
                    stubs = make_stubs(base)
                    root = base / "root"
                    legacy_bin = root / "opt/nexterm/nexterm-server"
                    legacy_bin.parent.mkdir(parents=True)
                    legacy_bin.write_text("# fake nexterm-server v0\n")
                    (root / "etc/systemd/system").mkdir(parents=True)
                    (root / "etc/systemd/system/nexterm-server.service").write_text(
                        LEGACY_UNIT.replace("  --web-root /opt/nexterm/web", layout.format(empty_flag=empty_flag))
                    )
                    env_key = root / "srv/keys/env.key"
                    env_key.parent.mkdir(parents=True)
                    env_key.write_text("stale-env-key\n")
                    data_key = root / "srv/data/master.key"
                    data_key.parent.mkdir(parents=True)
                    data_key.write_text("data-dir-key\n")
                    etc = root / "etc/nexterm"
                    etc.mkdir(parents=True)
                    (etc / "nexterm.env").write_text(
                        "NEXTERM_LISTEN=127.0.0.1:8080\nNEXTERM_AUTH=on\nNEXTERM_DATA_DIR=/srv/data\n"
                        "NEXTERM_WEB_ROOT=/srv/web\nNEXTERM_MASTER_KEY_FILE=/srv/keys/env.key\nNEXTERM_DB=sqlite\n"
                    )
                    payload_v2 = make_payload(base, "v2")
                    if failure is not None:
                        failed = run_install(payload_v2, root, stubs, base, fail_at=failure)
                        assert failed.returncode != 0, "injected failure must abort"
                    result = run_install(payload_v2, root, stubs, base)
                    label = f"{position} {empty_flag} failure={failure}"
                    assert result.returncode == 0, (label, result.stderr)
                    values = env_values(etc / "nexterm.env")
                    assert values["NEXTERM_MASTER_KEY_FILE"] == "/srv/data/master.key", f"an empty legacy override must clear the stale env key and follow the data-dir fallback ({label})"
                    assert read(data_key) == "data-dir-key\n", label
                    assert read(env_key) == "stale-env-key\n", f"the stale env key must not be adopted ({label})"


def test_legacy_unrepresentable_bytes_fail_closed() -> None:
    for flag_value, label in (
        ("/srv/keys/legacy\\uFFFFkey", "U+FFFF noncharacter"),
        ("/srv/keys/legacy\\uD800key", "U+D800 surrogate"),
        ("/srv/keys/legacy\\xFFkey", "raw 0xFF byte"),
    ):
        for failure in (None, "env"):
            with tempfile.TemporaryDirectory() as tmp:
                base = pathlib.Path(tmp)
                stubs = make_stubs(base)
                root = base / "root"
                legacy_bin = root / "opt/nexterm/nexterm-server"
                legacy_bin.parent.mkdir(parents=True)
                legacy_bin.write_text("# fake nexterm-server v0\n")
                (root / "etc/systemd/system").mkdir(parents=True)
                (root / "etc/systemd/system/nexterm-server.service").write_text(
                    LEGACY_UNIT.replace("  --web-root /opt/nexterm/web", f"  --master-key-file {flag_value} \\\n  --web-root /opt/nexterm/web")
                )
                payload_v2 = make_payload(base, "v2")
                if failure is not None:
                    failed = run_install(payload_v2, root, stubs, base, fail_at=failure)
                    assert failed.returncode != 0, "injected failure must abort"
                result = run_install(payload_v2, root, stubs, base)
                assert result.returncode != 0, label
                assert "cannot be represented in an environment file" in result.stderr, (label, result.stderr)
                assert "legacy inline-flag layout" in read(root / "etc/systemd/system/nexterm-server.service"), (label, "the unit must not be replaced")
                assert not (root / "etc/nexterm/master.key").exists(), (label, "no key may be generated")


def test_failed_upgrade_retry_preserves_rollback() -> None:
    with tempfile.TemporaryDirectory() as tmp:
        base = pathlib.Path(tmp)
        stubs = make_stubs(base)
        root = base / "root"
        payload_v1 = seed_first_install(base, stubs, root)
        binary = root / "opt/nexterm/nexterm-server"
        payload_v2 = make_payload(base, "v2")

        failed = run_install(payload_v2, root, stubs, base, fail_at="units")
        assert failed.returncode != 0, "injected failure must abort"
        backup_binary = root / "opt/nexterm/.rollback/nexterm-server"
        assert read(backup_binary) == read(payload_v1 / "nexterm-server"), "backup must hold v1 after the failed upgrade"

        retry = run_install(payload_v2, root, stubs, base)
        assert retry.returncode == 0, retry.stderr
        assert "preserving rollback backup" in retry.stdout, retry.stdout
        assert read(backup_binary) == read(payload_v1 / "nexterm-server"), "retry must not overwrite the original backup"
        assert not (root / "opt/nexterm/.rollback/pending").exists(), "successful retry clears the pending marker"

        rollback = run_install(payload_v2, root, stubs, base, args=("--rollback",))
        assert rollback.returncode == 0, rollback.stderr
        assert read(binary) == read(payload_v1 / "nexterm-server"), "rollback after a retried upgrade must restore v1"


def test_first_install_failure_at_env_is_recoverable() -> None:
    with tempfile.TemporaryDirectory() as tmp:
        base = pathlib.Path(tmp)
        stubs = make_stubs(base)
        root = base / "root"
        payload = make_payload(base, "v1")
        failed = run_install(payload, root, stubs, base, fail_at="env")
        assert failed.returncode != 0, "injected failure must abort"
        assert "injected failure at step: env" in failed.stderr, failed.stderr
        assert not (root / "etc/nexterm/nexterm.env").exists(), "env step must not have run"
        (base / "systemctl.log").unlink(missing_ok=True)
        retry = run_install(payload, root, stubs, base)
        assert retry.returncode == 0, retry.stderr
        assert "resuming incomplete first install" in retry.stdout, retry.stdout
        assert "mode: first install" in retry.stdout, retry.stdout
        assert (root / "etc/nexterm/nexterm.env").exists(), "clean retry must install env"
        calls = systemctl_calls(base)
        assert "daemon-reload" in calls, calls
        assert "enable --now nexterm-server.service" in calls, "retry of a failed first install must enable and start the unit"
        assert not (root / "opt/nexterm/.install-incomplete").exists(), "successful retry clears the incomplete marker"


def test_upgrade_no_active_unit_never_restarts() -> None:
    with tempfile.TemporaryDirectory() as tmp:
        base = pathlib.Path(tmp)
        stubs = make_stubs(base)
        root = base / "root"
        seed_first_install(base, stubs, root)
        (base / "systemctl.log").unlink()
        payload_v2 = make_payload(base, "v2")
        result = run_install(payload_v2, root, stubs, base, active="")
        assert result.returncode == 0, result.stderr
        calls = systemctl_calls(base)
        assert "daemon-reload" in calls, calls
        assert not [c for c in calls if c.startswith("restart")], calls
        assert not [c for c in calls if c.startswith("enable")], calls


def test_no_enable_flag_and_missing_payload() -> None:
    with tempfile.TemporaryDirectory() as tmp:
        base = pathlib.Path(tmp)
        stubs = make_stubs(base)
        root = base / "root"
        payload = make_payload(base, "v1")
        result = run_install(payload, root, stubs, base, args=("--no-enable",))
        assert result.returncode == 0, result.stderr
        calls = systemctl_calls(base)
        assert "daemon-reload" in calls, calls
        assert not [c for c in calls if c.startswith("enable")], calls

        empty = base / "empty-payload"
        empty.mkdir()
        missing = run_install(empty, base / "root2", stubs, base)
        assert missing.returncode != 0, "missing payload must fail"
        assert "payload is missing" in missing.stderr, missing.stderr
        assert not (base / "root2/opt/nexterm").exists(), "failed preflight must not create directories"


def test_non_linux_fails_visibly() -> None:
    for kernel in ("Darwin", "Windows_NT", "MINGW64_NT-10.0"):
        with tempfile.TemporaryDirectory() as tmp:
            base = pathlib.Path(tmp)
            stubs = make_stubs(base)
            root = base / "root"
            payload = make_payload(base, "v1")
            result = run_install(payload, root, stubs, base, uname=kernel)
            assert result.returncode != 0, kernel
            assert f"unsupported operating system: {kernel}" in result.stderr, (kernel, result.stderr)
            assert not (root / "opt/nexterm").exists(), kernel
            assert not (root / "etc/nexterm").exists(), kernel
            assert systemctl_calls(base) == [], kernel
            assert openssl_calls(base) == [], kernel
            assert not (base / "useradd.log").exists(), kernel


def test_non_systemd_init_fails_visibly() -> None:
    for variant in ("sysv", "none"):
        with tempfile.TemporaryDirectory() as tmp:
            base = pathlib.Path(tmp)
            stubs = make_stubs(base)
            root = base / "root"
            payload = make_payload(base, "v1")
            result = run_install(payload, root, stubs, base, systemd=variant)
            assert result.returncode != 0, variant
            assert "systemd is required" in result.stderr, (variant, result.stderr)
            assert "non-systemd" in result.stderr, (variant, result.stderr)
            assert not (root / "opt/nexterm").exists(), variant
            assert not (root / "etc/nexterm").exists(), variant
            assert systemctl_calls(base) == [], variant


def test_systemd_init_symlink_evidence_accepted() -> None:
    with tempfile.TemporaryDirectory() as tmp:
        base = pathlib.Path(tmp)
        stubs = make_stubs(base)
        root = base / "root"
        payload = make_payload(base, "v1")
        result = run_install(payload, root, stubs, base, systemd="systemd-link")
        assert result.returncode == 0, result.stderr
        assert "init: systemd" in result.stdout, result.stdout
        assert "platform: Linux" in result.stdout, result.stdout


def test_distro_detection_variants() -> None:
    supported = [
        OS_RELEASE_UBUNTU,
        'ID=debian\nPRETTY_NAME="Debian GNU/Linux 12 (bookworm)"\n',
        'ID=rocky\nID_LIKE="rhel centos fedora"\nPRETTY_NAME="Rocky Linux 9.4"\n',
        'ID=linuxmint\nID_LIKE="ubuntu debian"\nPRETTY_NAME="Linux Mint 22"\n',
        'ID=arch\nPRETTY_NAME="Arch Linux"\n',
        'ID=opensuse-tumbleweed\nID_LIKE="opensuse suse"\nPRETTY_NAME="openSUSE Tumbleweed"\n',
    ]
    for os_release in supported:
        with tempfile.TemporaryDirectory() as tmp:
            base = pathlib.Path(tmp)
            stubs = make_stubs(base)
            root = base / "root"
            payload = make_payload(base, "v1")
            result = run_install(payload, root, stubs, base, os_release=os_release)
            assert result.returncode == 0, (os_release, result.stderr)
            assert "(supported)" in result.stdout, (os_release, result.stdout)
            assert "not in the supported" not in result.stdout, (os_release, result.stdout)
            assert "cannot identify the distribution" not in result.stdout, (os_release, result.stdout)

    with tempfile.TemporaryDirectory() as tmp:
        base = pathlib.Path(tmp)
        stubs = make_stubs(base)
        root = base / "root"
        payload = make_payload(base, "v1")
        result = run_install(payload, root, stubs, base, os_release='ID=weirdos\nPRETTY_NAME="WeirdOS 1.0"\n')
        assert result.returncode == 0, result.stderr
        assert "not in the supported" in result.stdout, result.stdout
        assert "WeirdOS 1.0" in result.stdout, result.stdout
        assert "WARNING" in result.stdout, result.stdout

    with tempfile.TemporaryDirectory() as tmp:
        base = pathlib.Path(tmp)
        stubs = make_stubs(base)
        root = base / "root"
        payload = make_payload(base, "v1")
        result = run_install(payload, root, stubs, base, os_release=None)
        assert result.returncode == 0, result.stderr
        assert "cannot identify the distribution" in result.stdout, result.stdout
        assert "WARNING" in result.stdout, result.stdout


def test_first_install_reports_active_service() -> None:
    with tempfile.TemporaryDirectory() as tmp:
        base = pathlib.Path(tmp)
        stubs = make_stubs(base)
        root = base / "root"
        payload = make_payload(base, "v1")
        result = run_install(payload, root, stubs, base)
        assert result.returncode == 0, result.stderr
        assert "nexterm-server.service is active" in result.stdout, result.stdout

    with tempfile.TemporaryDirectory() as tmp:
        base = pathlib.Path(tmp)
        stubs = make_stubs(base)
        root = base / "root"
        payload = make_payload(base, "v1")
        result = run_install(payload, root, stubs, base, fail_start=True)
        assert result.returncode == 0, result.stderr
        assert "did not report active" in result.stdout, result.stdout
        assert "journalctl -u nexterm-server.service" in result.stdout, result.stdout
        assert "WARNING" in result.stdout, result.stdout


def test_console_output_redacts_key_material() -> None:
    with tempfile.TemporaryDirectory() as tmp:
        base = pathlib.Path(tmp)
        stubs = make_stubs(base)
        root = base / "root"
        payload = make_payload(base, "v1")
        result = run_install(payload, root, stubs, base)
        assert result.returncode == 0, result.stderr
        assert read(root / "etc/nexterm/master.key") == FAKE_KEY_HEX + "\n"
        assert FAKE_KEY_HEX not in result.stdout + result.stderr, "generated key material must never reach the console"

        key_file = root / "etc/nexterm/master.key"
        key_file.write_text("preexisting-super-secret-key\n")
        (base / "openssl.log").unlink(missing_ok=True)
        payload_v2 = make_payload(base, "v2")
        upgrade = run_install(payload_v2, root, stubs, base, active="nexterm-server.service")
        assert upgrade.returncode == 0, upgrade.stderr
        combined = upgrade.stdout + upgrade.stderr
        assert "preexisting-super-secret-key" not in combined, "existing key material must never reach the console"
        assert FAKE_KEY_HEX not in combined, "no key regeneration on upgrade"
        assert openssl_calls(base) == [], "no key generation while the key exists"


def test_rollback_requires_supported_platform() -> None:
    with tempfile.TemporaryDirectory() as tmp:
        base = pathlib.Path(tmp)
        stubs = make_stubs(base)
        root = base / "root"
        payload_v1 = seed_first_install(base, stubs, root)
        payload_v2 = make_payload(base, "v2")
        failed = run_install(payload_v2, root, stubs, base, fail_at="units", active="nexterm-server.service")
        assert failed.returncode != 0, "injected failure must abort"

        rollback = run_install(payload_v2, root, stubs, base, args=("--rollback",), uname="Darwin")
        assert rollback.returncode != 0, "rollback on a non-Linux host must fail"
        assert "unsupported operating system: Darwin" in rollback.stderr, rollback.stderr
        assert read(root / "opt/nexterm/nexterm-server") == read(payload_v2 / "nexterm-server"), "payload must be untouched"

        rollback = run_install(payload_v2, root, stubs, base, args=("--rollback",), systemd="none")
        assert rollback.returncode != 0, "rollback without systemd evidence must fail"
        assert "systemd is required" in rollback.stderr, rollback.stderr
        assert read(root / "opt/nexterm/nexterm-server") == read(payload_v2 / "nexterm-server"), "payload must be untouched"

        rollback = run_install(payload_v2, root, stubs, base, args=("--rollback",), active="nexterm-server.service")
        assert rollback.returncode == 0, rollback.stderr
        assert read(root / "opt/nexterm/nexterm-server") == read(payload_v1 / "nexterm-server"), "supported rollback must restore v1"


def test_missing_systemctl_fails_visibly() -> None:
    with tempfile.TemporaryDirectory() as tmp:
        base = pathlib.Path(tmp)
        stubs = make_stubs(base, with_systemctl=False)
        root = base / "root"
        payload = make_payload(base, "v1")
        result = run_install(payload, root, stubs, base)
        assert result.returncode != 0, "a missing systemctl must fail"
        assert "systemctl not found" in result.stderr, result.stderr
        assert not (root / "opt/nexterm").exists(), "failed preflight must not create directories"


def test_first_install_bootstrap_prints_password_before_service_start() -> None:
    with tempfile.TemporaryDirectory() as tmp:
        base = pathlib.Path(tmp)
        stubs = make_stubs(base)
        root = base / "root"
        payload = make_payload(base, "v1")
        result = run_install(payload, root, stubs, base, init_admin=True,
                             init_admin_stdout="nexterm-server: 初始密码: bootstrap-random-password-123（仅显示一次，请立即妥善保存）")
        assert result.returncode == 0, result.stderr
        assert "bootstrap-random-password-123" in result.stdout, result.stdout
        assert "super-admin created" in result.stdout, result.stdout
        assert init_admin_calls(base) == ["init-admin"], init_admin_calls(base)
        envlog = init_admin_env(base)
        assert envlog["NEXTERM_AUTH"] == "on", envlog
        assert envlog["NEXTERM_DATA_DIR"] == "/var/lib/nexterm", envlog
        assert envlog["NEXTERM_DB"] == "sqlite", envlog
        assert envlog["NEXTERM_ADMIN_PASSWORD"] == "", envlog
        assert envlog["NEXTERM_ADMIN_USERNAME"] == "", envlog
        calls = systemctl_calls(base)
        assert "init-admin" in calls and "enable --now nexterm-server.service" in calls, calls
        assert calls.index("init-admin") < calls.index("enable --now nexterm-server.service"), calls


def test_bootstrap_supplied_password_passthrough() -> None:
    with tempfile.TemporaryDirectory() as tmp:
        base = pathlib.Path(tmp)
        stubs = make_stubs(base)
        root = base / "root"
        payload = make_payload(base, "v1")
        result = run_install(payload, root, stubs, base, init_admin=True,
                             admin_password="synthetic-password-1",
                             init_admin_stdout="nexterm-server: 初始密码: synthetic-password-1（仅显示一次，请立即妥善保存）")
        assert result.returncode == 0, result.stderr
        assert init_admin_env(base)["NEXTERM_ADMIN_PASSWORD"] == "synthetic-password-1", init_admin_env(base)
        assert "初始密码: synthetic-password-1" in result.stdout, result.stdout


def test_bootstrap_failed_create_aborts_and_redacts() -> None:
    with tempfile.TemporaryDirectory() as tmp:
        base = pathlib.Path(tmp)
        stubs = make_stubs(base)
        root = base / "root"
        payload = make_payload(base, "v1")
        result = run_install(payload, root, stubs, base, init_admin=True,
                             init_admin_exit=1, admin_password="synthetic-password-1")
        assert result.returncode != 0, "a failed bootstrap must abort the first install so a re-run retries it"
        combined = result.stdout + result.stderr
        assert "synthetic-password-1" not in combined, "the injected password must never reach the installer output"
        assert "初始密码" not in result.stdout, result.stdout
        assert "super-admin bootstrap failed" in result.stderr, result.stderr
        calls = systemctl_calls(base)
        assert "enable --now nexterm-server.service" not in calls, calls
        assert (root / "opt/nexterm/.install-incomplete").exists(), "the incomplete marker must remain for the retry"


def test_bootstrap_failed_create_retry() -> None:
    with tempfile.TemporaryDirectory() as tmp:
        base = pathlib.Path(tmp)
        stubs = make_stubs(base)
        root = base / "root"
        payload = make_payload(base, "v1")
        failed = run_install(payload, root, stubs, base, init_admin=True, init_admin_exit=1)
        assert failed.returncode != 0, failed.stdout
        assert "初始密码" not in failed.stdout, failed.stdout

        retry = run_install(payload, root, stubs, base, init_admin=True,
                            init_admin_stdout="nexterm-server: 初始密码: retry-password-12345（仅显示一次，请立即妥善保存）")
        assert retry.returncode == 0, retry.stderr
        assert "resuming incomplete first install" in retry.stdout, retry.stdout
        assert "retry-password-12345" in retry.stdout, retry.stdout
        assert not (root / "opt/nexterm/.install-incomplete").exists(), "the successful retry clears the marker"
        calls = systemctl_calls(base)
        assert calls.count("init-admin") == 2, calls
        assert calls.index("init-admin", calls.index("init-admin") + 1) < calls.index("enable --now nexterm-server.service"), calls


def test_bootstrap_existing_account_silence() -> None:
    with tempfile.TemporaryDirectory() as tmp:
        base = pathlib.Path(tmp)
        stubs = make_stubs(base)
        root = base / "root"
        payload = make_payload(base, "v1")
        result = run_install(payload, root, stubs, base, init_admin=True, init_admin_exit=3)
        assert result.returncode == 0, result.stderr
        assert "初始密码" not in result.stdout, result.stdout
        assert "already initialized" in result.stdout, result.stdout
        assert init_admin_calls(base) == ["init-admin"], init_admin_calls(base)


def test_bootstrap_not_invoked_on_upgrade() -> None:
    with tempfile.TemporaryDirectory() as tmp:
        base = pathlib.Path(tmp)
        stubs = make_stubs(base)
        root = base / "root"
        payload_v1 = make_payload(base, "v1")
        seed = run_install(payload_v1, root, stubs, base, init_admin=True,
                           init_admin_stdout="nexterm-server: 初始密码: first-install-password-123（仅显示一次）")
        assert seed.returncode == 0, seed.stderr
        (base / "init-admin.log").unlink()

        payload_v2 = make_payload(base, "v2")
        upgrade = run_install(payload_v2, root, stubs, base, active="nexterm-server.service", init_admin=True)
        assert upgrade.returncode == 0, upgrade.stderr
        assert "mode: upgrade install" in upgrade.stdout, upgrade.stdout
        assert not (base / "init-admin.log").exists(), "upgrades must never invoke the bootstrap"
        assert "初始密码" not in upgrade.stdout, upgrade.stdout


def test_bootstrap_skipped_when_auth_off() -> None:
    with tempfile.TemporaryDirectory() as tmp:
        base = pathlib.Path(tmp)
        stubs = make_stubs(base)
        root = base / "root"
        payload = make_payload(base, "v1")
        example = payload / "nexterm.env.example"
        example.write_text(example.read_text().replace("NEXTERM_AUTH=on", "NEXTERM_AUTH=off"))
        result = run_install(payload, root, stubs, base, init_admin=True)
        assert result.returncode == 0, result.stderr
        assert not (base / "init-admin.log").exists(), "auth=off manages accounts itself; the bootstrap must not run"
        assert "manages accounts itself" in result.stdout, result.stdout


def test_bootstrap_skipped_on_staged_root_without_hook() -> None:
    with tempfile.TemporaryDirectory() as tmp:
        base = pathlib.Path(tmp)
        stubs = make_stubs(base)
        root = base / "root"
        payload = make_payload(base, "v1")
        result = run_install(payload, root, stubs, base)
        assert result.returncode == 0, result.stderr
        assert "skipping super-admin bootstrap (staged root" in result.stdout, result.stdout


def test_no_enable_still_bootstraps() -> None:
    with tempfile.TemporaryDirectory() as tmp:
        base = pathlib.Path(tmp)
        stubs = make_stubs(base)
        root = base / "root"
        payload = make_payload(base, "v1")
        result = run_install(payload, root, stubs, base, args=("--no-enable",), init_admin=True,
                             init_admin_stdout="nexterm-server: 初始密码: no-enable-password-12345（仅显示一次）")
        assert result.returncode == 0, result.stderr
        assert "no-enable-password-12345" in result.stdout, result.stdout
        assert init_admin_calls(base) == ["init-admin"], init_admin_calls(base)
        calls = systemctl_calls(base)
        assert not [c for c in calls if c.startswith("enable")], calls


def test_bootstrap_db_env_unconditional_overrides_ambient() -> None:
    with tempfile.TemporaryDirectory() as tmp:
        base = pathlib.Path(tmp)
        stubs = make_stubs(base)
        root = base / "root"
        payload = make_payload(base, "v1")
        example = payload / "nexterm.env.example"
        example.write_text(example.read_text().replace(
            "NEXTERM_DB=sqlite", "NEXTERM_DB=\nNEXTERM_DB_DSN=\nNEXTERM_DB_PASSWORD_FILE="))
        result = run_install(payload, root, stubs, base, init_admin=True, ambient={
            "NEXTERM_DB": "postgres",
            "NEXTERM_DB_DSN": "postgres://db.example/nexterm",
            "NEXTERM_DB_PASSWORD_FILE": "/root/db-password",
        })
        assert result.returncode == 0, result.stderr
        envlog = init_admin_env(base)
        assert envlog["NEXTERM_DB"] == "", f"the service resolves an empty NEXTERM_DB to sqlite; the child must see the same: {envlog}"
        assert envlog["NEXTERM_DB_DSN"] == "", envlog
        assert envlog["NEXTERM_DB_PASSWORD_FILE"] == "", envlog
        combined = result.stdout + result.stderr
        assert "postgres://db.example/nexterm" not in combined, "ambient DSN must never reach the console"
        assert "/root/db-password" not in combined, "ambient password-file path must never reach the console"


def test_bootstrap_db_env_from_file_wins_over_ambient() -> None:
    with tempfile.TemporaryDirectory() as tmp:
        base = pathlib.Path(tmp)
        stubs = make_stubs(base)
        root = base / "root"
        payload = make_payload(base, "v1")
        example = payload / "nexterm.env.example"
        example.write_text(example.read_text().replace(
            "NEXTERM_DB=sqlite", "NEXTERM_DB=postgres\nNEXTERM_DB_DSN=postgres://db.example/nexterm"))
        result = run_install(payload, root, stubs, base, init_admin=True, ambient={
            "NEXTERM_DB": "sqlite",
            "NEXTERM_DB_DSN": "",
        })
        assert result.returncode == 0, result.stderr
        envlog = init_admin_env(base)
        assert envlog["NEXTERM_DB"] == "postgres", envlog
        assert envlog["NEXTERM_DB_DSN"] == "postgres://db.example/nexterm", envlog


def test_bootstrap_exit4_keeps_install_failed() -> None:
    with tempfile.TemporaryDirectory() as tmp:
        base = pathlib.Path(tmp)
        stubs = make_stubs(base)
        root = base / "root"
        payload = make_payload(base, "v1")
        failed = run_install(payload, root, stubs, base, init_admin=True, init_admin_exit=1)
        assert failed.returncode != 0, failed.stdout
        assert (root / "opt/nexterm/.install-incomplete").exists(), "the failed create keeps the incomplete marker"

        blocked = run_install(payload, root, stubs, base, init_admin=True, init_admin_exit=4)
        assert blocked.returncode != 0, "an unprovable init code must keep the first install failed"
        assert "super-admin bootstrap found an already-issued one-time init code" in blocked.stderr, blocked.stderr
        assert "初始密码" not in blocked.stdout, blocked.stdout
        assert (root / "opt/nexterm/.install-incomplete").exists(), "the incomplete marker must survive the exit-4 run"
        calls = systemctl_calls(base)
        assert "enable --now nexterm-server.service" not in calls, f"the service must not start on an exit-4 run: {calls}"

        retry = run_install(payload, root, stubs, base, init_admin=True,
                            init_admin_stdout="nexterm-server: 初始密码: recovered-password-12345（仅显示一次）")
        assert retry.returncode == 0, retry.stderr
        assert "recovered-password-12345" in retry.stdout, retry.stdout
        assert not (root / "opt/nexterm/.install-incomplete").exists()


def owner(path: pathlib.Path) -> tuple[int, int]:
    info = os.stat(path)
    return info.st_uid, info.st_gid


def test_real_root_bootstrap_preserves_custom_data_dir_ownership() -> None:
    if os.geteuid() != 0 or platform.system() != "Linux" or os.environ.get("NEXTERM_TEST_REAL_ROOT_INSTALL") != "1":
        print("skip   test_real_root_bootstrap_preserves_custom_data_dir_ownership (needs root on Linux plus NEXTERM_TEST_REAL_ROOT_INSTALL=1; mutates the system: disposable containers only)")
        return
    for path in ("/opt/nexterm", "/etc/nexterm", "/var/lib/nexterm"):
        if os.path.exists(path):
            raise AssertionError(f"refusing the real-root leg on a system with an existing install: {path}")
    import pwd

    nexterm = pwd.getpwnam("nexterm") if any(line.startswith("nexterm:") for line in open("/etc/passwd")) else None
    created_user = False
    created_run_dir = False
    try:
        if nexterm is None:
            subprocess.run(["useradd", "--system", "--user-group", "--home-dir", "/var/lib/nexterm",
                            "--shell", "/usr/sbin/nologin", "nexterm"], check=True)
            nexterm = pwd.getpwnam("nexterm")
            created_user = True
        if not os.path.isdir("/run/systemd/system"):
            os.makedirs("/run/systemd/system")
            created_run_dir = True
        with tempfile.TemporaryDirectory() as tmp:
            base = pathlib.Path(tmp)
            os.chmod(base, 0o777)
            stubs = make_stubs(base)
            custom = base / "custom-data"
            (custom / "nested").mkdir(parents=True)
            (custom / "preexisting-top.txt").write_text("top\n")
            (custom / "nested" / "nested.txt").write_text("nested\n")
            subprocess.run(["chown", "-R", "12345:12345", str(custom / "nested")], check=True)
            subprocess.run(["chown", "12345:12345", str(custom / "preexisting-top.txt")], check=True)
            subprocess.run(["chown", "nexterm:nexterm", str(custom)], check=True)
            os.chmod(custom, 0o755)
            payload = make_payload(base, "v1")
            example = payload / "nexterm.env.example"
            example.write_text(example.read_text().replace("NEXTERM_DATA_DIR=/var/lib/nexterm", f"NEXTERM_DATA_DIR={custom}"))

            def real_root_env(path_value: str) -> dict[str, str]:
                env = dict(os.environ)
                env["PATH"] = path_value
                env["SYSTEMCTL_LOG"] = str(base / "systemctl.log")
                env["OPENSSL_LOG"] = str(base / "openssl.log")
                env["USERADD_LOG"] = str(base / "useradd.log")
                env["INIT_ADMIN_LOG"] = str(base / "init-admin.log")
                env["INIT_ADMIN_ENV_LOG"] = str(base / "init-admin-env.log")
                env["NEXTERM_INSTALL_SYSTEMCTL"] = str(stubs / "systemctl")
                env["NEXTERM_INSTALL_UNAME"] = str(stubs / "uname")
                env["NEXTERM_INSTALL_INIT_ADMIN"] = str(stubs / "init-admin")
                env["FAKE_ACTIVE_UNITS"] = ""
                env["NEXTERM_ADMIN_PASSWORD"] = "synthetic-password-1"
                state = base / "systemctl-state-realroot"
                state.write_text("")
                env["SYSTEMCTL_STATE"] = str(state)
                return env

            # Phase 1: runuser unavailable (PATH without /usr/sbin) fails
            # closed before the service is ever enabled.
            blocked = subprocess.run(["/bin/bash", str(SCRIPT), "--source", str(payload)],
                                     capture_output=True, text=True,
                                     env=real_root_env(f"{stubs}:/bin:/usr/bin"), timeout=120)
            assert blocked.returncode != 0, "a missing runuser must fail the bootstrap closed"
            assert "runuser not found" in blocked.stderr, blocked.stderr
            assert os.path.exists("/opt/nexterm/.install-incomplete"), "the incomplete marker must survive"
            calls = systemctl_calls(base)
            assert "enable --now nexterm-server.service" not in calls, calls

            # Phase 2: full run with a blocking bootstrap stub; an
            # unprivileged reader scans /proc for argv leaks the whole time.
            env = real_root_env(f"{stubs}:{os.environ['PATH']}")
            env["INIT_ADMIN_TOUCH_DATA"] = "1"
            env["INIT_ADMIN_BLOCK_SECONDS"] = "8"
            process = subprocess.Popen(["/bin/bash", str(SCRIPT), "--source", str(payload)],
                                       stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, env=env)
            leaks: list[str] = []
            try:
                deadline = time.time() + 60
                while not (base / "init-admin-env.log").exists() and time.time() < deadline:
                    if process.poll() is not None:
                        break
                    time.sleep(0.05)
                assert (base / "init-admin-env.log").exists(), "the blocking bootstrap stub never started"
                needles = base / "proc-needles.txt"
                needles.write_text("synthetic-password-1\nNEXTERM_ADMIN_PASSWORD\n")
                os.chmod(needles, 0o644)
                for _ in range(10):
                    scan = subprocess.run(
                        ["runuser", "-u", "nobody", "--", "sh", "-c",
                         'grep -a -l -F -f "$1" /proc/[0-9]*/cmdline 2>/dev/null', "sh", str(needles)],
                        capture_output=True, text=True, timeout=30)
                    if scan.stdout.strip():
                        leaks.append(scan.stdout.strip())
                    time.sleep(0.3)
                stdout, stderr = process.communicate(timeout=120)
            finally:
                if process.poll() is None:
                    process.kill()
                    process.communicate()
            assert not leaks, f"bootstrap secrets leaked into /proc cmdline: {leaks}"
            assert process.returncode == 0, stderr
            assert "mode: first install" in stdout, stdout
            assert "resuming incomplete first install" in stdout, stdout
            envlog = init_admin_env(base)
            assert envlog.get("RUNUSER_USER") == "nexterm", f"the bootstrap must run as the service user: {envlog}"
            assert envlog["NEXTERM_ADMIN_PASSWORD"] == "synthetic-password-1", envlog
            assert envlog["NEXTERM_DATA_DIR"] == str(custom), envlog
            assert envlog["NEXTERM_DB"] == "sqlite", envlog
            assert (custom / "data.db").exists(), "the bootstrap must create the database"
            assert owner(custom / "data.db") == (nexterm.pw_uid, nexterm.pw_gid), "the database must be born service-owned"
            assert owner(custom / "preexisting-top.txt") == (12345, 12345), "pre-existing top-level file must keep its owner"
            assert owner(custom / "nested") == (12345, 12345), "pre-existing nested directory must keep its owner"
            assert owner(custom / "nested" / "nested.txt") == (12345, 12345), "pre-existing nested file must keep its owner"
            calls = systemctl_calls(base)
            assert "enable --now nexterm-server.service" in calls, calls
    finally:
        for path in ("/opt/nexterm", "/etc/nexterm", "/var/lib/nexterm"):
            shutil.rmtree(path, ignore_errors=True)
        if created_run_dir:
            shutil.rmtree("/run/systemd/system", ignore_errors=True)
        if created_user:
            subprocess.run(["userdel", "nexterm"], capture_output=True)


def test_bootstrap_never_uses_env_argv() -> None:
    with tempfile.TemporaryDirectory() as tmp:
        base = pathlib.Path(tmp)
        stubs = make_stubs(base)
        real_env = shutil.which("env")
        assert real_env, "env must exist on the host"
        (stubs / "env").write_text(f"""#!/bin/sh
printf '%s\\n' "$*" >> "$ENV_ARGV_LOG"
sleep 5
exec {real_env} "$@"
""")
        (stubs / "env").chmod(0o755)
        root = base / "root"
        payload = make_payload(base, "v1")
        result = run_install(payload, root, stubs, base, init_admin=True,
                             admin_password="synthetic-password-1",
                             init_admin_stdout="nexterm-server: 初始密码: synthetic-password-1（仅显示一次）",
                             ambient={"ENV_ARGV_LOG": str(base / "env-argv.log")})
        assert result.returncode == 0, result.stderr
        assert not (base / "env-argv.log").exists(), \
            "the bootstrap must not pass secrets through an env argv (world-readable via /proc)"
        assert "初始密码: synthetic-password-1" in result.stdout, result.stdout
        envlog = init_admin_env(base)
        assert envlog["NEXTERM_ADMIN_PASSWORD"] == "synthetic-password-1", \
            f"the child must receive the password via the environment: {envlog}"
        assert envlog["NEXTERM_DB"] == "sqlite", envlog


def proc_cmdline_leaks(*needles: str) -> list[str]:
    hits = []
    for entry in pathlib.Path("/proc").iterdir():
        if not entry.name.isdigit():
            continue
        try:
            data = (entry / "cmdline").read_bytes()
        except OSError:
            continue
        for needle in needles:
            if needle.encode() in data:
                hits.append(f"pid {entry.name}: {needle}")
    return hits


def test_bootstrap_secrets_never_in_proc_cmdline() -> None:
    if platform.system() != "Linux":
        print("skip   test_bootstrap_secrets_never_in_proc_cmdline (needs /proc; Linux only)")
        return
    with tempfile.TemporaryDirectory() as tmp:
        base = pathlib.Path(tmp)
        stubs = make_stubs(base)
        root = base / "root"
        payload = make_payload(base, "v1")
        seed_platform(root, "run", "default")
        env = make_install_env(base, stubs, init_admin=True, init_admin_block=8,
                               admin_password="synthetic-password-1",
                               init_admin_stdout="nexterm-server: 初始密码: synthetic-password-1（仅显示一次）")
        env["ENV_ARGV_LOG"] = str(base / "env-argv.log")
        process = subprocess.Popen(
            ["/bin/bash", str(SCRIPT), "--root", str(root), "--source", str(payload)],
            stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, env=env,
        )
        leaks: list[str] = []
        try:
            deadline = time.time() + 30
            while not (base / "init-admin-env.log").exists() and time.time() < deadline:
                if process.poll() is not None:
                    break
                time.sleep(0.05)
            assert (base / "init-admin-env.log").exists(), "the blocking bootstrap stub never started"
            for _ in range(10):
                leaks.extend(proc_cmdline_leaks("synthetic-password-1", "NEXTERM_ADMIN_PASSWORD"))
                time.sleep(0.2)
            stdout, stderr = process.communicate(timeout=60)
        finally:
            if process.poll() is None:
                process.kill()
                process.communicate()
        assert not leaks, f"bootstrap secrets leaked into /proc cmdline: {leaks}"
        assert process.returncode == 0, stderr
        assert "初始密码: synthetic-password-1" in stdout, stdout
        envlog = init_admin_env(base)
        assert envlog["NEXTERM_ADMIN_PASSWORD"] == "synthetic-password-1", envlog


def test_real_server_archive_smoke() -> None:
    archive_dir = ROOT / "target/release-assets"
    candidates = sorted(archive_dir.glob("NexTerm-server_*_linux_*.tar.gz")) if archive_dir.is_dir() else []
    env_archive = os.environ.get("NEXTERM_TEST_SERVER_ARCHIVE")
    archive = pathlib.Path(env_archive) if env_archive else (candidates[-1] if candidates else None)
    if archive is None:
        print("skip   test_real_server_archive_smoke (no packaged server archive; build one with scripts/pack-linux-server.sh and re-run, or set NEXTERM_TEST_SERVER_ARCHIVE)")
        return
    with tempfile.TemporaryDirectory() as tmp:
        base = pathlib.Path(tmp)
        extract = base / "extracted"
        extract.mkdir()
        subprocess.run(["tar", "-xzf", str(archive), "-C", str(extract)], check=True)
        dirs = [p for p in extract.iterdir() if p.is_dir()]
        assert len(dirs) == 1, f"archive must extract to a single directory: {dirs}"
        payload = dirs[0]
        for member in (
            "install.sh", "nexterm-server", "nexterm-helper-linux-amd64", "nexterm-helper-linux-arm64",
            "nexterm-server.service", "nexterm-onlyserver.service",
            "nexterm.env.example", "onlyserver.env.example", "web/index.html",
        ):
            assert (payload / member).exists(), f"archive payload is missing {member}"
        stubs = make_stubs(base, with_openssl=False)
        root = base / "root"
        seed_platform(root, "run", "default")
        env = dict(os.environ)
        env["PATH"] = f"{stubs}:{env['PATH']}"
        env["SYSTEMCTL_LOG"] = str(base / "systemctl.log")
        env["USERADD_LOG"] = str(base / "useradd.log")
        env["FAKE_ACTIVE_UNITS"] = ""
        env["NEXTERM_INSTALL_SYSTEMCTL"] = str(stubs / "systemctl")
        state = base / "systemctl-state-archive"
        state.write_text("")
        env["SYSTEMCTL_STATE"] = str(state)
        result = subprocess.run(
            ["/bin/bash", str(payload / "install.sh"), "--root", str(root)],
            capture_output=True, text=True, env=env, timeout=120,
        )
        assert result.returncode == 0, result.stderr
        assert "platform: Linux" in result.stdout, result.stdout
        assert "init: systemd" in result.stdout, result.stdout
        assert "(supported)" in result.stdout, result.stdout
        key = root / "etc/nexterm/master.key"
        content = read(key).strip()
        assert mode(key) == 0o600, oct(mode(key))
        assert len(content) == 64 and all(c in "0123456789abcdef" for c in content), "a real random hex key must be generated"
        assert content not in result.stdout + result.stderr, "key material must never reach the console"
        assert (root / "opt/nexterm/nexterm-server").read_bytes() == (payload / "nexterm-server").read_bytes()
        for unit in UNITS:
            assert read(root / "etc/systemd/system" / unit) == read(payload / unit), unit
        calls = systemctl_calls(base)
        assert "enable --now nexterm-server.service" in calls, calls
        assert "nexterm-server.service is active" in result.stdout, result.stdout

        server_binary = payload / "nexterm-server"
        probe_env = {k: v for k, v in os.environ.items() if not k.startswith("NEXTERM_")}
        try:
            probe = subprocess.run([str(server_binary), "--version"], capture_output=True, text=True, env=probe_env)
            executable = probe.returncode == 0
        except OSError:
            executable = False
        if not executable:
            print("skip   real-binary init-admin smoke (host cannot execute the archived Linux binary; live systemd, distribution and LazyCat acceptance remain outside this suite)")
            return
        data_dir = root / "var/lib/nexterm"
        first = subprocess.run([str(server_binary), "init-admin", "--data-dir", str(data_dir)], capture_output=True, text=True, env=probe_env)
        assert first.returncode == 0, first.stderr
        marker = "初始密码: "
        assert marker in first.stdout, first.stdout
        password = first.stdout.split(marker, 1)[1].split("（", 1)[0].strip()
        assert password and " " not in password, first.stdout
        assert "首次登录时必须修改" in first.stdout, "the default random policy must force a first-login change"
        assert password not in first.stderr, "the initial password must not reach stderr"
        again = subprocess.run([str(server_binary), "init-admin", "--data-dir", str(data_dir)], capture_output=True, text=True, env=probe_env)
        assert again.returncode == 3, (again.returncode, again.stderr)
        assert again.stdout == "", "existing-account re-run must stay silent"
        supplied_dir = base / "supplied-data"
        failed = subprocess.run([str(server_binary), "init-admin", "--data-dir", str(supplied_dir)], capture_output=True, text=True,
                                env={**probe_env, "NEXTERM_ADMIN_PASSWORD": "short"})
        assert failed.returncode == 1 and failed.stdout == "", (failed.returncode, failed.stdout)
        retried = subprocess.run([str(server_binary), "init-admin", "--data-dir", str(supplied_dir)], capture_output=True, text=True,
                                 env={**probe_env, "NEXTERM_ADMIN_PASSWORD": "synthetic-password-1"})
        assert retried.returncode == 0, retried.stderr
        assert "初始密码: synthetic-password-1" in retried.stdout, retried.stdout
        assert "synthetic-password-1" not in retried.stderr, "the supplied password must not reach stderr"


def main() -> int:
    tests = [
        test_first_install_safe_defaults,
        test_upgrade_preserves_env_and_master_key,
        test_upgrade_migrates_legacy_units,
        test_upgrade_without_env_writes_safe_defaults,
        test_upgrade_backfills_missing_env_keys,
        test_upgrade_preserves_data_dir_master_key,
        test_upgrade_preserves_legacy_key_file_setting,
        test_upgrade_migrates_legacy_non_default_flags,
        test_backfill_without_trailing_newline,
        test_rollback_restores_previous_payload,
        test_failure_injection_leaves_previous_payload_intact,
        test_failed_masterkey_retry_preserves_data_dir_key,
        test_failed_env_retry_recovers_legacy_values,
        test_quoted_data_dir_and_duplicate_assignments,
        test_lower_priority_legacy_unit_does_not_override_etc,
        test_first_install_masterkey_retry_preserves_data_dir_key,
        test_legacy_master_key_file_flag_migrates_with_precedence,
        test_env_value_whitespace_and_quote_variants,
        test_pending_retry_and_rollback_use_saved_active_units,
        test_crlf_env_file_preserves_key,
        test_legacy_quoted_paths_tokenization,
        test_missing_and_empty_data_dir_preserve_fallback,
        test_relative_data_dir_resolved_against_working_dir,
        test_legacy_c_style_escapes,
        test_legacy_duplicate_flags_last_value_wins,
        test_path_normalization_and_containment,
        test_legacy_newline_values_fail_closed,
        test_legacy_noncharacter_escape_round_trip,
        test_legacy_specifier_values_fail_closed,
        test_legacy_newline_and_cr_values_fail_closed,
        test_legacy_lowercase_u_decodes_noncharacters,
        test_legacy_empty_master_key_arg_falls_back,
        test_env_comment_continuation_keeps_assignment,
        test_empty_data_dir_pinned_on_working_dir_change,
        test_legacy_empty_master_key_override_clears_stale_env_key,
        test_legacy_unrepresentable_bytes_fail_closed,
        test_install_upgrade_combination_matrix,
        test_failed_upgrade_retry_preserves_rollback,
        test_first_install_failure_at_env_is_recoverable,
        test_upgrade_no_active_unit_never_restarts,
        test_no_enable_flag_and_missing_payload,
        test_non_linux_fails_visibly,
        test_non_systemd_init_fails_visibly,
        test_systemd_init_symlink_evidence_accepted,
        test_distro_detection_variants,
        test_first_install_reports_active_service,
        test_console_output_redacts_key_material,
        test_rollback_requires_supported_platform,
        test_missing_systemctl_fails_visibly,
        test_first_install_bootstrap_prints_password_before_service_start,
        test_bootstrap_supplied_password_passthrough,
        test_bootstrap_failed_create_aborts_and_redacts,
        test_bootstrap_failed_create_retry,
        test_bootstrap_existing_account_silence,
        test_bootstrap_not_invoked_on_upgrade,
        test_bootstrap_skipped_when_auth_off,
        test_bootstrap_skipped_on_staged_root_without_hook,
        test_no_enable_still_bootstraps,
        test_bootstrap_db_env_unconditional_overrides_ambient,
        test_bootstrap_db_env_from_file_wins_over_ambient,
        test_bootstrap_exit4_keeps_install_failed,
        test_bootstrap_never_uses_env_argv,
        test_bootstrap_secrets_never_in_proc_cmdline,
        test_real_root_bootstrap_preserves_custom_data_dir_ownership,
        test_real_server_archive_smoke,
    ]
    for test in tests:
        try:
            test()
            PASSED.append(test.__name__)
            print(f"ok   {test.__name__}")
        except AssertionError as exc:
            FAILED.append(f"{test.__name__}: {exc}")
            print(f"FAIL {test.__name__}: {exc}")
    print(f"\n{len(PASSED)} passed, {len(FAILED)} failed")
    return 1 if FAILED else 0


if __name__ == "__main__":
    sys.exit(main())
