# NexTerm Linux server installation

Use these steps from the extracted Linux server archive. The full server and
sync-only peer share their listen address and data directory; enable only one
of their units.

## Install

Create the service account once, then install the binaries, web assets, units
and environment files:

```sh
sudo useradd --system --user-group --home-dir /var/lib/nexterm --shell /usr/sbin/nologin nexterm
sudo install -d -m 0755 /opt/nexterm /etc/nexterm
sudo install -d -m 0700 -o nexterm -g nexterm /var/lib/nexterm
sudo install -m 0755 nexterm-server nexterm-helper-linux-amd64 nexterm-helper-linux-arm64 /opt/nexterm/
sudo cp -R web /opt/nexterm/
sudo install -m 0644 nexterm-server.service nexterm-onlyserver.service /etc/systemd/system/
sudo install -m 0600 nexterm.env.example /etc/nexterm/nexterm.env
sudo install -m 0600 onlyserver.env.example /etc/nexterm/onlyserver.env
```

Keep both helpers beside `nexterm-server`. On the server's own architecture,
verify the matching helper before enabling the service; it must print JSON
with a protocol version:

```sh
/opt/nexterm/nexterm-helper-linux-amd64 --metadata  # x86_64 host
/opt/nexterm/nexterm-helper-linux-arm64 --metadata  # aarch64 host
```

Create a private vault root key, back it up with `/var/lib/nexterm`, and do not
replace it after credentials have been encrypted:

```sh
sudo install -m 0600 -o nexterm -g nexterm /dev/null /etc/nexterm/master.key
sudo sh -c 'openssl rand -hex 32 > /etc/nexterm/master.key'
```

The units read their settings from `/etc/nexterm/nexterm.env` and
`/etc/nexterm/onlyserver.env`; there are no listen, auth, data-dir or web-root
flags in `ExecStart` to override those files. The defaults listen on
`127.0.0.1:8080`, require account authentication and store local state in
`/var/lib/nexterm`. After editing an environment file, restart the selected
unit.

Reload systemd and start the full server:

```sh
sudo systemctl daemon-reload
sudo systemctl enable --now nexterm-server
journalctl -u nexterm-server
```

On first start with no account, the log contains a one-time initialization
code. Open the UI and finish super-admin setup. To run the sync-only peer
instead, disable the full server and enable `nexterm-onlyserver`; its unit adds
only the `--sync-only` mode flag and reads `onlyserver.env`.

## Database settings

`NEXTERM_DB` selects `sqlite` (the default), `postgres`, `mysql` or `mariadb`.
SQLite stores its data under `NEXTERM_DATA_DIR`; preserve that directory and
`/etc/nexterm/master.key` when backing up. Run only one server instance against
the same database.

For a remote database, set all applicable values in the selected env file:

- `NEXTERM_DB_DSN` is required. PostgreSQL example:
  `postgres://nexterm@db.example:5432/nexterm?sslmode=verify-full`.
  MySQL/MariaDB example:
  `nexterm@tcp(db.example:3306)/nexterm?parseTime=true&tls=true`.
- `NEXTERM_DB_PASSWORD_FILE` optionally names a 0600 file readable by the
  `nexterm` user. Use it instead of embedding a password in the DSN; setting a
  password in both places is rejected.
- `NEXTERM_DB_MAX_OPEN_CONNS` sets the remote connection pool limit and
  defaults to 16.

Back up the external database as well as the local data directory and master
key. Public deployments belong behind a TLS reverse proxy. Keep the listener
on loopback, make the proxy overwrite `X-Forwarded-Proto`, and set
`NEXTERM_TRUSTED_PROXY_CIDRS` to its source CIDRs so HTTPS session cookies are
marked `Secure`.
