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
     distinct \\u/\\U noncharacter rules, empty-argument preservation, and
     duplicate flags keep the last value like the CLI, while values
     containing newlines/carriage returns or unexpandable systemd
     specifiers fail closed before any replacement; env values round-trip
     through systemd's env-file parser (quotes, escapes, concatenation,
     continuations that never swallow assignments after comments)
  13. A combination matrix (first install/upgrade x failure point x env origin x
     key location x active state) asserts the key-preservation, data-dir,
     enable, and restart invariants across all of the above

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
            assert result.returncode == 0 or "Illegal byte sequence" in result.stderr, result.stderr
            values = env_values(root / "etc/nexterm/nexterm.env")
            assert values["NEXTERM_MASTER_KEY_FILE"] == "/srv/keys/legacy\uffffkey", "lowercase \\u must decode the noncharacter, not preserve the literal spelling"
            assert not (root / "srv/keys/legacy\\uFFFFkey").exists(), "the literal spelling must not be configured or generated"

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
        test_install_upgrade_combination_matrix,
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
