# NexTerm Linux server installation

Use these steps from the extracted Linux server archive. The full server and
sync-only peer share their listen address and data directory; enable only one
of their units.

## Install or upgrade

`install.sh` separates first install from upgrade and is safe to re-run:

```sh
sudo ./install.sh
```

First install creates the `nexterm` system user, `/opt/nexterm`,
`/etc/nexterm` and `/var/lib/nexterm`, installs the binaries, web assets,
units and environment files, generates `/etc/nexterm/master.key`, then runs
`systemctl enable --now nexterm-server`. Use `--no-enable` to install
without starting the service.

An upgrade (detected from an existing `/opt/nexterm/nexterm-server`,
`/etc/nexterm/*.env` or unit) replaces binaries, web assets and units, and:

- preserves `/etc/nexterm/nexterm.env`, `/etc/nexterm/onlyserver.env` and
  `/etc/nexterm/master.key` byte for byte;
- backfills any missing `NEXTERM_LISTEN`, `NEXTERM_AUTH`,
  `NEXTERM_DATA_DIR`, `NEXTERM_WEB_ROOT` or `NEXTERM_MASTER_KEY_FILE` key
  with the safe default from the shipped example, so an install that
  predates the env files never falls back to the built-in `0.0.0.0:8080`
  listener;
- migrates legacy units (older releases kept `--listen`, `--auth`,
  `--data-dir` and `--web-root` flags in `ExecStart`) to the env-driven
  units, and removes stray copies under `/usr/lib/systemd/system/` or
  `/lib/systemd/system/`;
- saves the previous payload to `/opt/nexterm/.rollback` and restarts only
  the units that were active.

Options: `--source DIR` (payload directory, default the script's own),
`--root DIR` (staged install prefix, for testing), `--rollback`,
`--no-enable`.

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

`install.sh` generates `/etc/nexterm/master.key` when it is absent and never
replaces an existing one. Back it up with `/var/lib/nexterm`; it cannot be
recreated after credentials have been encrypted.

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
