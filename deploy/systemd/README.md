# NexTerm Linux server installation

Use these steps from the extracted Linux server archive. The full server and
sync-only peer share their listen address and data directory; enable only one
of their units.

## Quick install

Without downloading the archive first, run as root:

```sh
curl -fsSL https://nexterm.work/install.sh | sudo bash
```

The script downloads the latest server archive, verifies it against
`SHA256SUMS`, and runs the bundled `install.sh` described below. Running it
again upgrades in place; add `-s -- --rollback` to undo the last upgrade or
`-s -- --version x.y.z` to pin a version. Set `NEXTERM_ADMIN_PASSWORD` to
preset the initial super-admin password; otherwise the installer prints a
random one once.

The service listens on `127.0.0.1:8080` and requires sign-in. Reach it
through an SSH tunnel, `ssh -L 8080:127.0.0.1:8080 user@server`, then open
`http://localhost:8080`, or put a TLS reverse proxy in front (see
[Database settings](#database-settings) for the proxy trust settings).
Settings live in `/etc/nexterm/nexterm.env`; run
`systemctl restart nexterm-server` after changing them. Take database
backups with `nexterm-server backup` and keep the master key and TOTP key
separately (see [Backup and restore](#backup-and-restore)); copying
`/var/lib/nexterm` while the server runs can capture an inconsistent
database.
`nexterm-server --help` lists every option.

`--auth off` disables accounts, administration and device routes: anyone who
can reach the listen address can operate the server, so never use it on a
public network.

The LazyCat MicroServer package installs from its App Store (amd64 devices
only). It runs in platform mode, has the [multi-user limits](#multi-user-limits)
below, and its built-in port forwarding is unavailable, so use the
platform's forwarding.

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

The server binary is built with `CGO_ENABLED=0` and loads ONNX Runtime through purego, so it is dynamically linked against glibc only (no minimum glibc version) and runs on any glibc-based Linux; musl distributions such as Alpine are not supported. The optional ONNX Runtime library (`ai-runtime/`) additionally needs glibc 2.27 or newer. Before changing anything, `install.sh` runs
the payload's `nexterm-server --version` and stops if it cannot execute on
this host. ONNX Runtime also needs `libstdc++.so.6`; when it is missing the
installer warns and only optional local AI redaction is unavailable.

The automated suite (`go test ./scripts/installer`) exercises detection
against staged roots with stubbed `uname` and `systemctl`; live systemd and
live distribution runs are outside it.

## Install or upgrade

`install.sh` separates first install from upgrade and is safe to re-run:

```sh
sudo ./install.sh
```

First install creates the `nexterm` system user, `/opt/nexterm`,
`/etc/nexterm` and `/var/lib/nexterm`, installs the binaries, web assets,
units and environment files, generates `/etc/nexterm/master.key`, creates
the super-admin account, then runs `systemctl enable --now nexterm-server`
and verifies the unit reports active (a unit that does not start produces
a warning with a `journalctl` hint, not a failed install). Use
`--no-enable` to install without starting the service.

The super-admin bootstrap runs before the first service start, through
the server's own account-initialization contract (no separate account
path, no direct database edits). Only after the account was actually
created does the installer print the initial password to the installing
console; nothing account-related is ever printed on upgrades, existing
accounts or re-runs, and the password is never written to env files,
disk or logs. Two provisioning modes are supported:

- default: a cryptographically random initial password, marked
  must-change, so the first login has to set a new password;
- injected: with `NEXTERM_ADMIN_PASSWORD` set in the installer's
  environment, that exact password becomes the initial password with no
  forced rotation — the channel install systems such as LazyCat use to
  inject a known credential. `NEXTERM_ADMIN_USERNAME` overrides the
  default name `admin`. The password travels via the process environment
  only — never via argv, which is world-readable through
  `/proc/<pid>/cmdline` — and is never written to env files, disk or
  logs.

The bootstrap runs as the `nexterm` service user: everything it creates
is born with the right ownership, a data directory the service cannot
write fails closed during the install, and pre-existing files in a
custom data directory are never touched (the installer performs no
recursive ownership changes). Its environment is scrubbed to a whitelist
mirroring the service's effective configuration — the unit's `HOME` and
every key from the env file, including `PG*` values the file sets
explicitly — so ambient variables such as `PGDATABASE` or `PGPASSWORD`
can never point the bootstrap at a different database or credential than
the service uses. The bootstrap executes in the unit's
`WorkingDirectory`, so relative paths such as
`NEXTERM_DB_PASSWORD_FILE` resolve exactly like the service resolves
them. When the bootstrap does not run (staged `--root` installs,
`auth=off` or `auth=platform` deployments), finish setup with the
one-time code from the server log instead. If an init code was
already issued and cannot be proven usable (for example a previous
attempt failed to clean up its own code), the first install keeps
failing with recovery guidance instead of starting an uninitialized
service and reporting success. A failed bootstrap aborts the first
install before the service starts, so fixing the problem and re-running
resumes and retries it.

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
  the units that were active. With SQLite the active units are stopped
  first and `data.db` with its `-wal`/`-shm` files is copied into
  `/opt/nexterm/.rollback/db`, because the new version migrates the
  database on start; the copy needs free space for one more database.
  PostgreSQL, MySQL and MariaDB are not snapshotted: dump them before
  upgrading. An interrupted upgrade keeps that original
  backup across retries, records whether each env file predates the
  transaction and which units were active, and recovers legacy unit
  settings from the backup, so a retry never overwrites the rollback
  point, adopts an installer-written example env as explicit
  configuration, loses pending migrations, or skips restarting a unit
  that was active before the interruption.

Options: `--source DIR` (payload directory, default the script's own),
`--root DIR` (staged install prefix, for testing), `--rollback`,
`--keep-database` (with `--rollback`), `--no-enable`, `-y`/`--yes`. An interrupted first install is resumed by re-running the
script — including whether each env file predates the attempt, so an
example env written by the interrupted run is never mistaken for explicit
configuration — and finishes with `enable --now`.

Keep all nine target helpers beside `nexterm-server`: Linux
amd64/arm64/armv7/riscv64/loong64, macOS amd64/arm64, and Windows
amd64/arm64 (`.exe`). Build metadata acceptance uses three representatives:
Linux amd64, Windows amd64, and macOS arm64. The other six targets are
marked as untested cross-compiled helpers with metadata not-run; do not
present them as target-accepted. This archive does not record real SSH
deployment acceptance. Execute `--metadata` only on the actual target host;
its JSON includes the protocol and the executing file's SHA-256:

```sh
/opt/nexterm/nexterm-helper-linux-amd64 --metadata  # x86_64 host
/opt/nexterm/nexterm-helper-linux-arm64 --metadata  # aarch64 host
/opt/nexterm/nexterm-helper-linux-armv7 --metadata  # ARMv7 host
```

The units read their settings from `/etc/nexterm/nexterm.env` and
`/etc/nexterm/onlyserver.env`; there are no listen, auth, data-dir or
web-root flags in `ExecStart` to override those files. The defaults listen
on `127.0.0.1:8080`, require account authentication and store local state
in `/var/lib/nexterm`. After editing an environment file, restart the
selected unit.

When no super-admin exists at first start (the bootstrap was skipped, or
it was never run), the log contains a one-time initialization code: open
the UI and finish super-admin setup with it. To run the sync-only peer
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

When the upgrade saved a SQLite snapshot, the rollback also stops the
running units and puts that snapshot back, because the older version may
not run against the schema the newer one migrated. **Everything written
since the upgrade is lost**; the replaced `data.db` files are kept in
`/opt/nexterm/.rollback.failed/db`. The installer asks before doing this
and, without a terminal, refuses unless `--yes` is given. Pass
`--keep-database` to restore only the program files and keep the current
database (the previous behavior). A remote database is never rolled back:
restore it from your own dump if needed.

## Backup and restore

SQLite runs in WAL mode, so copying `data.db` while the server runs can
produce a copy that does not match its `-wal` file. Use the backup command
instead; it is safe while the service is running:

```sh
sudo install -d -m 0700 -o nexterm -g nexterm /var/backups/nexterm
sudo -u nexterm /opt/nexterm/nexterm-server backup \
  --data-dir /var/lib/nexterm --out /var/backups/nexterm/data.db
```

It writes a verified snapshot (`VACUUM INTO`, then `integrity_check`)
atomically with mode 0600, plus `data.db.manifest.json` with the NexTerm
version, time, size, SHA-256 and schema fingerprint. An existing file is
replaced only with `--force`. `nexterm-server backup --help` lists the
options and exit codes.

The snapshot does **not** contain the keys that decrypt it. Back these up
separately and keep them away from the database copy:

- the vault master key: `NEXTERM_MASTER_KEY_FILE`, by default
  `/etc/nexterm/master.key`;
- the TOTP key `/var/lib/nexterm/totp.key`, which protects two-factor
  secrets.

Other files under the data directory (uploaded files in `files/`,
`known_hosts.json`, ...) are not in the snapshot either; copy them while
the service is stopped if you need them.

With PostgreSQL, MySQL or MariaDB the command does not connect to the
database: it prints a `pg_dump`, `mysqldump` or `mariadb-dump` command for
the configured host and database (never the password) and exits 3.

To restore a SQLite snapshot:

```sh
sudo systemctl stop nexterm-server
sudo mkdir -m 0700 /var/lib/nexterm/replaced
sudo sh -c 'mv /var/lib/nexterm/data.db* /var/lib/nexterm/replaced/'
sudo install -m 0600 -o nexterm -g nexterm /var/backups/nexterm/data.db /var/lib/nexterm/data.db
sudo systemctl start nexterm-server
```

Restore the master key and TOTP key that were in use when the snapshot was
taken, and run a NexTerm version at least as new as the one in the
manifest.

## Master key

`install.sh` preserves the effective master key across installs and
upgrades: an explicit `NEXTERM_MASTER_KEY_FILE` setting wins, then an
existing `<data-dir>/master.key` (the server's own default location), then
`/etc/nexterm/master.key`. A new key is generated only when none exists,
and an existing key is never replaced. Back the key up separately from
the database backup; it cannot be recreated after credentials have been
encrypted.

## Database settings

`NEXTERM_DB` selects `sqlite` (the default), `postgres`, `mysql` or
`mariadb`. SQLite stores its data under `NEXTERM_DATA_DIR`; back it up as
described in [Backup and restore](#backup-and-restore). Run only one
server instance against the same database: login rate limits and
second-factor lockouts are held in process memory, are not shared between
instances, and reset on restart.

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
`NEXTERM_TRUSTED_PROXY_CIDRS` to the proxy's own addresses only (connections
through an `ssh -L` tunnel also look like 127.0.0.1) so HTTPS session cookies
are marked `Secure`. That setting only trusts `X-Forwarded-Proto`. To rate
limit logins and record session origins by the real client address, also set
`NEXTERM_TRUST_FORWARDED_FOR=1`, provided the proxy overwrites or appends to
`X-Forwarded-For` (nginx `$remote_addr` and `$proxy_add_x_forwarded_for` both
work) and never passes the client-supplied value through unchanged. With
chained proxies, list every hop in `NEXTERM_TRUSTED_PROXY_CIDRS`; the server
takes the rightmost address that is not in that list. Without it only the
direct peer address is used.

The server rejects browser requests and WebSockets whose `Origin` does not
match the request `Host`. Make the proxy forward the public host name
(nginx: `proxy_set_header Host $host;`, plus `proxy_http_version 1.1;`,
`proxy_set_header Upgrade $http_upgrade;` and
`proxy_set_header Connection "upgrade";` for WebSockets). If the proxy must
rewrite `Host` to the upstream address, list the public origins in
`NEXTERM_ALLOWED_ORIGINS` instead, comma separated: `host[:port]` patterns
such as `nexterm.example.com` match any scheme, patterns with `://` such as
`https://nexterm.example.com` match the scheme too, and `*` wildcards are
allowed. Setting it replaces the built-in `localhost:*` and `127.0.0.1:*`
entries. `NEXTERM_TRUST_FORWARDED_FOR` accepts `1`/`0` or `true`/`false`;
any other value stops startup. Listed origins receive credentialed CORS
responses, so list only origins you control.

A reverse proxy on the same host must send `X-Forwarded-For` (or `Forwarded`
/ `X-Real-IP`) on every request. Without one, a proxied remote caller looks
like a direct loopback caller and receives the detailed `/healthz` payload
(see Health checks below).

## Health checks

- `GET /livez` answers 200 while the process serves HTTP and touches neither
  the database nor the vault. Use it for liveness (restart on failure).
- `GET /healthz` is the readiness probe. It returns 503 with `"ok":false`
  when the database ping fails, or when `--require-vault` is set and the
  vault is locked; otherwise 200.
- Both are public, so callers that are not on loopback and hold no
  super-admin session get only `ok`, `service` and `version`. The detailed
  payload (vault state, database pool, channel counts, web root, retention)
  is returned to direct loopback callers, to callers that trusted proxies
  report as loopback through `X-Forwarded-For` (see the reverse proxy notes
  above), and to signed-in super-admins. A request from a loopback peer
  that carries `X-Forwarded-For`, `Forwarded` or `X-Real-IP` without being a
  trusted proxy counts as remote, so a local reverse proxy must send one of
  those headers.
- `nexterm-server healthcheck [--url URL | --listen ADDRESS] [--timeout 5s]`
  probes `/healthz` without curl and exits 0 when ready, 1 when unreachable
  or unhealthy, and 2 on usage errors. The target is `--url`, else
  `http://<listen>/healthz` built from `--listen`, then `NEXTERM_LISTEN`, then
  `0.0.0.0:8080`, with a wildcard host replaced by loopback. Pass the same
  `--listen` value the server uses when it is not set through the
  environment. It suits a container health check such as
  `HEALTHCHECK CMD ["nexterm-server", "healthcheck"]`.

## Logging

The units send the server's stderr to the journal: read it with
`journalctl -u nexterm-server` (or `nexterm-onlyserver`). Because records
already reach the journal, the default `NEXTERM_LOG_FILE=auto` does not also
write `/var/lib/nexterm/logs/nexterm.log` when stderr is the systemd journal.
Set `NEXTERM_LOG_FILE=on` to keep the file as well, or `off` to never write
it. Outside systemd (containers, manual runs) `auto` writes the file. The file
rotates at 10 MB and keeps five backups (`nexterm.log.1` to `nexterm.log.5`).

- `NEXTERM_LOG_LEVEL`: `debug`, `info` (default), `warn` or `error`.
- `NEXTERM_LOG_FORMAT`: `text` (default) or `json`.
- `NEXTERM_ACCESS_LOG=1`: one record per HTTP request with method, path,
  status, bytes, duration and client address. The client address follows
  `NEXTERM_TRUSTED_PROXY_CIDRS` and `NEXTERM_TRUST_FORWARDED_FOR` as above.
  Query strings, request bodies and share-link tokens in paths are never
  logged. WebSocket connections are logged when they close, with their
  lifetime as the duration.

An invalid value for any of these stops the server at startup with exit
code 2 and names the variable.

## File staging limits

In the browser, uploads, downloads, terminal recordings and log exports pass
through temporary files under `NEXTERM_DATA_DIR/blobs`. A temporary file is
removed two hours after it was last written, but not while a download,
recording or export is still writing it. A write in progress counts against
the account's quota with its whole allowance until it ends, so parallel
transfers cannot add up past the quota. These settings bound them (sizes in
bytes):

- `NEXTERM_BLOB_MAX_BYTES`: one browser upload, default 256 MiB;
- `NEXTERM_BLOB_STAGE_MAX_BYTES`: all temporary files of one account
  together, default 2 GiB; each account may also hold at most 1000 staged
  entries at a time;
- `NEXTERM_BLOB_DOWNLOAD_MAX_BYTES`: one file the server writes for the
  browser (file and folder downloads, terminal recordings, log and recording
  exports), default 2 GiB. A larger download is refused before anything is
  written, and a recording stops with an error once it reaches the limit;
- `NEXTERM_BLOB_PERSIST_MAX_BYTES`: all persisted uploads together,
  default 1 GiB;
- `NEXTERM_BLOB_DISK_MAX_PERCENT`: new staged or persisted files are refused
  once the data disk is this full, default 90.

The desktop app does not use these limits.

## Multi-user limits

With accounts enabled (the default `NEXTERM_AUTH=on`, and the platform mode
used by the LazyCat package) the server does not run commands on the server
machine or read its files on an account's behalf:

- local terminal assets and disk mounts are unavailable;
- SSH settings that run or read on the server are refused: ProxyCommand,
  SSH agent use (including agent forwarding), and private key or certificate
  file paths. Upload key contents or use the vault, and use a jump host or a
  SOCKS/HTTP proxy instead;
- imports from the server's `~/.ssh/config`, kubeconfig, or Docker contexts,
  and Kubernetes target exec, are unavailable.

Only open access lifts these limits: `--auth off`, or `--auth loopback`
listening on loopback when no account is signed in.

## Vault requirement

Set `NEXTERM_REQUIRE_VAULT=1` (or pass `--require-vault`) to stop startup
when the credential vault cannot be unlocked with the master key, instead of
starting with stored credentials unavailable. The shipped `nexterm.env`
enables it for new installs (upgrades keep their env file); set it to `0`
only while recovering a lost or mismatched key.

## Service sandboxing

`nexterm-onlyserver.service` runs fully sandboxed: `ProtectSystem=strict`
with only `/var/lib/nexterm` writable, `ProtectHome=yes`, no capabilities,
`NoNewPrivileges=yes`, private `/tmp` and `/dev`, IPv4/IPv6/Unix sockets
only, a `@system-service` system call filter and
`MemoryDenyWriteExecute=yes`. When `onlyserver.env` sets a data directory
elsewhere, or a data directory, master key or database password file under
`/home`, `/root` or `/run/user`, the installer writes
`/etc/systemd/system/nexterm-onlyserver.service.d/50-nexterm-paths.conf`
with the matching `ReadWritePaths=` and `ProtectHome=read-only`, and removes
it again when the defaults suffice. Do not edit that file; put your own
settings in a separate drop-in. To listen on a port below 1024, add a
drop-in with `AmbientCapabilities=CAP_NET_BIND_SERVICE` and
`CapabilityBoundingSet=CAP_NET_BIND_SERVICE`.

`nexterm-server.service` keeps `ProtectSystem=full` and a smaller set,
because programs started from local terminals (open access with
`NEXTERM_AUTH=off`, or `loopback` without a signed-in account) inherit the
sandbox (so setgid directories, `git init --shared=group` and realtime
audio tools keep working there) and sshfs mounts need `/dev/fuse` and the
setuid `fusermount3`.
Unix sockets such as `/var/run/docker.sock` stay reachable under either
unit. With accounts on, which disables local terminals and mounts, you can
tighten it with
`systemctl edit nexterm-server.service`:

```ini
[Service]
NoNewPrivileges=yes
PrivateDevices=yes
ProtectHome=yes
ProtectClock=yes
ProtectProc=invisible
RestrictNamespaces=yes
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX
SystemCallArchitectures=native
RestrictSUIDSGID=yes
RestrictRealtime=yes
CapabilityBoundingSet=
```

`MemoryDenyWriteExecute=yes` is left out of that list because the optional
local AI redaction loads the ONNX Runtime library into the server process.
Both units allow 60 seconds for a stop: on shutdown the server sends open
WebSockets a going-away close (code 1001) and drops connections that have
not closed after a short grace period (10 seconds at most).

## Tuning and diagnostics

These optional variables go in the env file of either unit:

- `NEXTERM_WS_KEEPALIVE` (default `25s`), `NEXTERM_WS_PING_TIMEOUT`
  (`10s`) and `NEXTERM_WS_WRITE_TIMEOUT` (`10s`) are Go durations for the
  WebSocket ping interval, the wait for its pong and the write deadline. A
  negative keepalive disables pings; raise it when an idle-timeout proxy
  allows longer gaps, lower it when the proxy closes idle connections
  sooner than 25 seconds.
- `NEXTERM_WS_COMPRESSION` overrides WebSocket compression: `takeover`,
  `nocontext` (less memory per connection) or `off`.
- `NEXTERM_EVENT_QUEUE_SIZE` (default 64) is the per-connection event queue
  length and `NEXTERM_EVENT_BUFFER_SIZE` (default 256) the number of recent
  events kept for clients that reconnect.
- `NEXTERM_PPROF_ADDR` serves Go pprof endpoints, loopback only unless an
  explicit wildcard is given. Block and mutex profiles stay off unless
  `NEXTERM_PPROF_CONTENTION=N` is also set: it samples about one blocking
  event per N nanoseconds spent blocked and one in N mutex contention events
  (`1` records everything and slows a busy server).

## Prometheus metrics

`nexterm-server` can expose Prometheus text metrics at `/metrics` on a
separate listener. It is disabled by default. Enable it with
`--metrics-addr 127.0.0.1:9464` or `NEXTERM_METRICS_ADDR=127.0.0.1:9464` (for
example in the unit's environment file). A bare `:9464` binds `127.0.0.1`.
The endpoint has no authentication, so any non-loopback address, wildcards
included, is refused at startup unless `NEXTERM_METRICS_ALLOW_REMOTE=1` is
set; in that case restrict access with a firewall or a private network.

Exposed series: process (goroutines, heap, GC, uptime, build info), database
connection pool, WebSocket connections per kind, live terminal tabs by kind
and persistence, connected device agents, active port forwards, HTTP request
counts and latency per route template, and login success and failure counters.
Labels come from small fixed sets: no user names, session IDs, device IDs, or
request paths appear in any label.

```yaml
scrape_configs:
  - job_name: nexterm-server
    static_configs:
      - targets: ["127.0.0.1:9464"]
```
