# NexTerm Linux server installation

Use these steps from the extracted Linux server archive. The full server and
sync-only peer share their listen address and data directory; enable only one
of their units.

## Supported platforms

`install.sh` requires a Linux distribution with systemd and fails with a
visible error before changing anything on any other system. Detection uses
explicit evidence only: the kernel must report `Linux`, and systemd must be
the init system (`/run/systemd/system` present, or `/sbin/init` linked to
systemd). macOS, Windows and non-systemd init systems (SysV init, OpenRC,
runit) are not supported.

The distribution is identified from `/etc/os-release` (falling back to
`/usr/lib/os-release`). The supported IDs are `debian`, `ubuntu`, `raspbian`,
`rhel`, `fedora`, `centos`, `rocky`, `almalinux`, `ol`, `amzn`, `arch`,
`manjaro`, `sles`, `opensuse-leap` and `opensuse-tumbleweed`, matched against
both `ID` and `ID_LIKE`. An unrecognized or missing os-release prints a
warning and continues: the installed layout is identical across systemd
distributions.

The automated suite (`scripts/test-systemd-install.py`) exercises detection
against staged roots with stubbed `uname` and `systemctl`; live systemd and
live distribution runs are outside it.

## Install or upgrade

`install.sh` separates first install from upgrade and is safe to re-run:

```sh
sudo ./install.sh
```

First install creates the `nexterm` system user, `/opt/nexterm`,
`/etc/nexterm` and `/var/lib/nexterm`, installs the binaries, web assets,
units and environment files, generates `/etc/nexterm/master.key`, then runs
`systemctl enable --now nexterm-server` and verifies the unit reports active
(a unit that does not start produces a warning with a `journalctl` hint, not
a failed install). Use `--no-enable` to install without starting the service.
On a completed first install the script prints where to finish super-admin
setup; it never prints anything account-related on upgrades or re-runs.

An upgrade (detected from an existing `/opt/nexterm/nexterm-server`,
`/etc/nexterm/*.env` or unit) replaces binaries, web assets and units, and:

- preserves `/etc/nexterm/nexterm.env` and
  `/etc/nexterm/onlyserver.env`; existing values are never rewritten.
  Values are read with systemd `EnvironmentFile` semantics: CRLF line
  endings are accepted, surrounding whitespace is trimmed, the last
  duplicate assignment wins, and matching quotes are stripped;
  unsupported quoting aborts the run before any key is generated;
- preserves the effective master key: an explicit `NEXTERM_MASTER_KEY_FILE`
  setting wins, then an existing `<data-dir>/master.key`, then
  `/etc/nexterm/master.key`. A new key is generated only when none exists,
  so credentials encrypted under an existing key stay decryptable. A
  missing or empty `NEXTERM_DATA_DIR` resolves the way the server
  resolves it — `<unit WorkingDirectory>/data` — and relative paths
  resolve against the effective unit's working directory. Paths are
  lexically cleaned (dot segments resolved the way the server's
  `filepath.Join` resolves them) before any check, so a key reached
  through a dotted path is still found, and a root-escaping path can
  never leave the target root;
- backfills any missing `NEXTERM_LISTEN`, `NEXTERM_AUTH` or
  `NEXTERM_WEB_ROOT` key with the safe default from the shipped example,
  so an install that predates the env files never falls back to the
  built-in `0.0.0.0:8080` listener. `NEXTERM_DATA_DIR` is pinned to the
  previous effective location on upgrades when the fallback directory
  holds data, so an existing database is never moved to the example
  location;
- migrates legacy units (older releases kept `--listen`, `--auth`,
  `--data-dir`, `--web-root` and `--master-key-file` flags in
  `ExecStart`): explicit flag values move into the corresponding env
  files (they were the effective settings; a migrated
  `--master-key-file` becomes an explicit `NEXTERM_MASTER_KEY_FILE` that
  wins over the data-dir fallback). ExecStart values are tokenized with
  systemd's unquote/C-unescape rules, so quoted paths and escapes such
  as `\s` or `\x20` migrate intact, invalid escapes stay literal, the
  distinct `\u`/`\U` noncharacter rules are honored, empty arguments are
  preserved, and duplicate flags keep the last value like the server
  CLI. Values that cannot be represented safely are rejected before
  anything is replaced: newlines and carriage returns (no line-based env
  representation), systemd specifiers other than `%%` (which becomes a
  literal `%`), and bytes that fail EnvironmentFile UTF-8 validation
  (decoded `\uFFFF`/`\uD800`/`\xFF` and friends). An empty
  `--master-key-file` override clears a stale env key and follows the
  server's data-dir fallback. Only the effective unit per systemd
  priority (`/etc/systemd/system/`, then `/usr/lib/systemd/system/`,
  then `/lib/systemd/system/`) is consulted; lower-priority copies are
  removed without applying their values;
- saves the previous payload to `/opt/nexterm/.rollback` and restarts only
  the units that were active. An interrupted upgrade keeps that original
  backup across retries, records whether each env file predates the
  transaction and which units were active, and recovers legacy unit
  settings from the backup, so a retry never overwrites the rollback
  point, adopts an installer-written example env as explicit
  configuration, loses pending migrations, or skips restarting a unit
  that was active before the interruption.

Options: `--source DIR` (payload directory, default the script's own),
`--root DIR` (staged install prefix, for testing), `--rollback`,
`--no-enable`. An interrupted first install is resumed by re-running the
script — including whether each env file predates the attempt, so an
example env written by the interrupted run is never mistaken for explicit
configuration — and finishes with `enable --now`.

Keep both helpers beside `nexterm-server`. On the server's own
architecture, verify the matching helper before enabling the service; it
must print JSON with a protocol version:

```sh
/opt/nexterm/nexterm-helper-linux-amd64 --metadata  # x86_64 host
/opt/nexterm/nexterm-helper-linux-arm64 --metadata  # aarch64 host
```

The units read their settings from `/etc/nexterm/nexterm.env` and
`/etc/nexterm/onlyserver.env`; there are no listen, auth, data-dir or
web-root flags in `ExecStart` to override those files. The defaults listen
on `127.0.0.1:8080`, require account authentication and store local state
in `/var/lib/nexterm`. After editing an environment file, restart the
selected unit.

On first start with no account, the log contains a one-time initialization
code. Open the UI and finish super-admin setup. To run the sync-only peer
instead, disable the full server and enable `nexterm-onlyserver`; its unit
adds only the `--sync-only` mode flag and reads `onlyserver.env`.

## Rollback

```sh
sudo ./install.sh --rollback
```

restores the binaries, web assets and units saved by the previous upgrade,
reloads systemd and restarts the previously active unit. Environment files
and the master key are not touched. The replaced payload is kept in
`/opt/nexterm/.rollback.failed` for inspection, and the backup is consumed,
so a second consecutive rollback fails.

## Master key

`install.sh` preserves the effective master key across installs and
upgrades: an explicit `NEXTERM_MASTER_KEY_FILE` setting wins, then an
existing `<data-dir>/master.key` (the server's own default location), then
`/etc/nexterm/master.key`. A new key is generated only when none exists,
and an existing key is never replaced. Back the key up together with
`/var/lib/nexterm`; it cannot be recreated after credentials have been
encrypted.

## Database settings

`NEXTERM_DB` selects `sqlite` (the default), `postgres`, `mysql` or
`mariadb`. SQLite stores its data under `NEXTERM_DATA_DIR`; preserve that
directory and `/etc/nexterm/master.key` when backing up. Run only one
server instance against the same database.

For a remote database, set all applicable values in the selected env file:

- `NEXTERM_DB_DSN` is required. PostgreSQL example:
  `postgres://nexterm@db.example:5432/nexterm?sslmode=verify-full`.
  MySQL/MariaDB example:
  `nexterm@tcp(db.example:3306)/nexterm?parseTime=true&tls=true`.
- `NEXTERM_DB_PASSWORD_FILE` optionally names a 0600 file readable by the
  `nexterm` user. Use it instead of embedding a password in the DSN;
  setting a password in both places is rejected.
- `NEXTERM_DB_MAX_OPEN_CONNS` sets the remote connection pool limit and
  defaults to 16.

Back up the external database as well as the local data directory and
master key. Public deployments belong behind a TLS reverse proxy. Keep the
listener on loopback, make the proxy overwrite `X-Forwarded-Proto`, and set
`NEXTERM_TRUSTED_PROXY_CIDRS` to its source CIDRs so HTTPS session cookies
are marked `Secure`.
