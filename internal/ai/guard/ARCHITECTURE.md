# AI guard classification and decision contracts

The guard package classifies shell commands, SQL and tool calls before the AI agent executes them, and maps rulings to execution decisions per permission mode. These contracts are versioned and self-consistent within the Go implementation.

## Classification model

`ClassifyCommand` parses a command line into shell segments (pipes, lists, redirections, command substitutions) and rates the worst segment. Recursion into wrapped or nested commands (wrappers such as `nice`/`timeout`/`xargs`, `su`/`sudo`, interpreters, `docker exec`, `kubectl exec`, ssh remote commands, `find -exec`) preserves argv token boundaries: nested commands are classified from their token slice, never from a re-joined string, so quoted scripts and clustered options keep their meaning. `xargs -I` replacement and `echo`/`printf` pipe producers are modeled so consumers classify the emitted payload, not the format directives.

Conservative defaults are part of the contract:

- Any wrapper, nested or attached form the parser cannot fully resolve rules Danger, never NeedsConfirm.
- Interpreter inline execution (`-c`/`-e`/`--eval` in attached, separate or clustered forms) rules Danger; shells keep recursive classification of the script.
- `docker rm`/`rmi -f` fed by a `$(docker|podman ... ps/images ...)` substitution that produces IDs (`-q`, `--format`) rules Danger because the deletion consumes exactly the enumerated set; a plain table listing is not an ID source and stays NeedsConfirm.
- Execution-capable options (`ssh -o ProxyCommand/LocalCommand/RemoteCommand`, `nc -e`, `ncat --exec/--sh-exec`, `socat EXEC:`/`SYSTEM:`, `git -c` executable configs) rule Danger or worse.
- Critical system paths are protected with one canonicalized target model (`filepath.Clean`, exact roots such as `/Users`, duplicate separators, `..` segments) across redirections, `sudo -e`, SQL `INTO OUTFILE` and file-producing commands (`cp`, `mv`, `dd of=`, `install`, `ln`, `tee`, `truncate`, `shred`, `rm`, `unlink`).
- Database client escapes that run files or shells (`mysql source`/`\.`, `psql \i`/`\!`) rule Danger.

## Decision matrix

`Decide(config, ruling, memory)` maps a ruling to an action:

| Ruling \ Mode | ReadOnly | ReadWrite | Silent |
| --- | --- | --- | --- |
| Safe | allow | allow | allow |
| NeedsConfirm | deny | ask (allow once the kind is remembered) | allow |
| Danger | deny | ask (never remembered) | ask |
| Forbidden | deny | deny | deny |

Because Silent auto-allows NeedsConfirm, anything that can destroy data or execute arbitrary code must rule Danger or Forbidden; classification regressions that fall to NeedsConfirm are security bugs, not friction.

## Documented design boundaries

These boundaries are deliberate and must not change without a mission decision:

- `git push origin main` (with or without `--force`) stays NeedsConfirm: a network write the user reviews at the remote, not local destruction. Silent mode therefore auto-allows it.
- Plain read-only HTTP(S) GET (`curl https://example.test`, clustered `-sG` read forms) stays Safe. SSRF risk against link-local/internal addresses is accepted at this layer; network egress policy is enforced outside the guard.
- Read-only enumerations stay usable: `at -l`, `git clean -n`/`--dry-run`, `systemctl status/--failed`, `docker system df`/`top`/`history`, `crontab -l`, `wget -O -`, `psql -c 'SELECT 1'` (NeedsConfirm as an interactive client).

## Regression conventions

Table-driven classify + Decide regressions pin every contract row (see `shell_m64_test.go` for the corpus contract and `shell_r*.go` per review round). Variant sweeps must cover attached, separate, clustered, case, alias and global-option forms, and both decision layers (Silent ask/deny floors, ReadOnly deny) for every new Danger ruling.
