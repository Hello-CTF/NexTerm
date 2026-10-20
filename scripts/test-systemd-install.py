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
     rollback backup and active-unit state
  8. NEXTERM_INSTALL_FAIL_AT aborts at the named step and leaves the previous
     payload, env and master key intact; a failed first install resumes and
     enables/starts the unit on retry
  9. Upgrade with no active unit never restarts or enables anything
  10. --no-enable skips enable --now on first install; missing payload fails closed

    python3 scripts/test-systemd-install.py
"""

from __future__ import annotations

import os
import pathlib
import shutil
import stat
import subprocess
import sys
import tempfile

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

SYSTEMCTL_STUB = """#!/bin/sh
printf '%s\\n' "$*" >> "$SYSTEMCTL_LOG"
if [ "${1:-}" = "is-active" ]; then
  for unit in $FAKE_ACTIVE_UNITS; do
    if [ "${3:-}" = "$unit" ]; then exit 0; fi
  done
  exit 3
fi
exit 0
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


def make_stubs(base: pathlib.Path) -> pathlib.Path:
    stubs = base / "stubs"
    stubs.mkdir()
    (stubs / "systemctl").write_text(SYSTEMCTL_STUB)
    (stubs / "openssl").write_text(OPENSSL_STUB.replace("FAKEKEYHEXPLACEHOLDER", FAKE_KEY_HEX))
    (stubs / "useradd").write_text(USERADD_STUB)
    for stub in stubs.iterdir():
        stub.chmod(0o755)
    return stubs


def run_install(payload: pathlib.Path, root: pathlib.Path, stubs: pathlib.Path, base: pathlib.Path,
                args: tuple[str, ...] = (), fail_at: str = "", active: str = "") -> subprocess.CompletedProcess:
    env = dict(os.environ)
    env["PATH"] = f"{stubs}:{env['PATH']}"
    env["SYSTEMCTL_LOG"] = str(base / "systemctl.log")
    env["OPENSSL_LOG"] = str(base / "openssl.log")
    env["USERADD_LOG"] = str(base / "useradd.log")
    env["FAKE_ACTIVE_UNITS"] = active
    if fail_at:
        env["NEXTERM_INSTALL_FAIL_AT"] = fail_at
    else:
        env.pop("NEXTERM_INSTALL_FAIL_AT", None)
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
        assert values["NEXTERM_DATA_DIR"] == "/var/lib/nexterm", values
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
        test_failed_upgrade_retry_preserves_rollback,
        test_first_install_failure_at_env_is_recoverable,
        test_upgrade_no_active_unit_never_restarts,
        test_no_enable_flag_and_missing_payload,
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
