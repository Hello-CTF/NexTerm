# AI guard classification and decision contracts

The guard package classifies shell commands, SQL and tool calls before the AI agent executes them, and maps rulings to execution decisions per permission mode. These contracts are versioned and self-consistent within the Go implementation.

## Classification model

`ClassifyCommand` parses a command line into shell segments (pipes, lists, redirections, command substitutions) and rates the worst segment.

- Execution wrappers (`nice`, `timeout`, `systemd-run`, `xargs`, …), `kubectl exec` and `docker exec` classify the nested command from its argv token slice, so quoted scripts and clustered options keep their meaning. `ssh` remote commands and `find -exec` are deliberately classified from a re-joined string: both tools flatten their arguments into one shell command, so re-joining matches what the remote shell parses.
- A shell parse failure rules Danger: the command's structure cannot be determined.
- Command substitutions are classified recursively; the outer command keeps its own ruling (for example `docker rm -f $(docker ps)` stays NeedsConfirm because a plain table listing is not an ID source, while quiet/`--format` enumerations that feed `rm -f`/`rmi -f` are bulk Danger).

### Conservative defaults in execution contexts

The following rule Danger rather than NeedsConfirm; each is implemented at the corresponding recursion boundary and pinned by the R8 cross-branch floor tests:

- Wrapper options or structure the parser cannot resolve (unknown option, missing command), and uncontrolled or non-determinable `xargs` input.
- `at`/`batch` reading standard input whose bytes are not exactly determinable, and `at -f`/`--file` (unverifiable file content). Installing a scheduled job confirms at minimum even for read-only payloads.
- Piped input into execution consumers (shells, interpreters, `at`/`batch`/`xargs`, including through `sudo`/`su`/`env`/wrappers) whose producer is not exactly determinable — the single `executionFloor` applied after all segments are classified.
- Non-shell interpreter inline execution (`-c`/`-e`/`--eval` in attached, separate or clustered forms), and piped stdin into any interpreter. Shell `-c` clusters with attached letters classify both the getopt-attached and next-argument interpretations and keep the worse ruling, because dash and bash/zsh disagree.
- Execution-capable options and escapes: `ssh -o ProxyCommand/LocalCommand/RemoteCommand`, `nc`/`ncat` attached or separate `-e`/`-c` and `--exec`/`--sh-exec`/`--lua-exec`, case-insensitive `socat EXEC:`/`SYSTEM:`, `git -c`/`--config-env` executable configs (pager/editor/sshCommand/proxy/hooksPath/external/fsmonitor/filter.*/alias.*/include*), `mysql source`/`\.`/`system` (per statement), `psql \!`/`\i`/`\ir`, `sudo -e` on any target (critical targets are Forbidden).
- Unknown global options of `systemctl`, `git` and `kubectl exec` (GNU unique-abbreviation resolution is applied first), and unknown `kubectl exec` options.

### Exactly determinable producers

Downstream consumers (`at`, `xargs`, interpreters) only trust producer output they can compute exactly:

- `echo` with no backslash in any argument (dash interprets escapes by default, `-e`/`-E` make bash do the same, so backslashes are shell-dependent).
- `printf` formats using only literal text, the modeled escapes (`\n \t \r \a \b \f \v \\ \c`) and the modeled verbs (`%s %c %d %i %o %u %x %X %f %e %E %g %G %%`); `%b`, hex/octal escapes and unknown verbs fail closed.

Anything else (`cat`, `curl`, unknown commands, escape-carrying echo/printf) is non-determinable input and rates Danger at the consumer. `xargs` with no command is the narrow read-only default (its built-in echo); `at -l` and `git clean -n`/`--dry-run` stay read-only, with clean's option parsing aware that `-e`/`--exclude` consumes a value.

### Critical paths

Critical system paths are protected with one canonicalized target model (`filepath.Clean`, exact roots such as `/Users`, `..` segments, duplicate separators) across redirections, `sudo -e`, SQL `INTO OUTFILE` and file-producing commands (`cp`, `mv`, `dd of=`, `install`, `ln`, `tee`, `truncate`, `shred`, `rm`, `rmdir`, `unlink`).

## Decision matrix

`Decide(config, ruling, memory)` maps a ruling to an action:

| Ruling \ Mode | ReadOnly | ReadWrite | Silent |
| --- | --- | --- | --- |
| Safe | allow | allow | allow |
| NeedsConfirm | deny | ask (allow once the kind is remembered) | allow |
| Danger | deny | ask (never remembered) | ask |
| Forbidden | deny | deny | deny |

Because Silent auto-allows NeedsConfirm, anything that can destroy data or execute arbitrary code must rule Danger or Forbidden. A single unknown top-level command still rules NeedsConfirm (the user confirms the exact text, and Silent pre-approves that); inside execution contexts the conservative defaults above apply instead.

## Documented design boundaries

These boundaries are deliberate and must not change without a mission decision:

- `git push origin main` (with or without `--force`) stays NeedsConfirm: a network write the user reviews at the remote, not local destruction. Silent mode therefore auto-allows it.
- Plain read-only HTTP(S) GET (`curl https://example.test`, clustered `-sG` read forms) stays Safe. SSRF risk against link-local/internal addresses is accepted at this layer; network egress policy is enforced outside the guard.
- Read-only enumerations stay usable: `at -l`, `git clean -n`/`--dry-run`, `systemctl status/--failed`, `docker system df`/`top`/`history`, `crontab -l`, `wget -O -`, `echo x | xargs` (default echo), `psql -c 'SELECT 1'` (NeedsConfirm as an interactive client).
- Plain Docker table substitutions (`docker rm -f $(docker ps)`) stay NeedsConfirm; only quiet/`--format` ID enumerations are bulk Danger.

## Regression conventions

Table-driven classify + Decide regressions pin every contract row (see `shell_m64_test.go` for the corpus contract and `shell_r*.go` per review round). `TestR8RiskFloorCrossBranch` and `TestR8FloorVariants` pin the conservative floor across every execution-context branch with attached, separate, clustered, case, alias and global-option variants, and `TestR8NarrowReadOnlyStaysUsable` pins the false-positive floor. Every new Danger ruling needs both decision layers (Silent ask/deny, ReadOnly deny) covered.
