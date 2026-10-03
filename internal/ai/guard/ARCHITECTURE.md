# AI guard classification and decision contracts

The guard package classifies shell commands, SQL and tool calls before the AI agent executes them, and maps rulings to execution decisions per permission mode. These contracts are versioned and self-consistent within the Go implementation.

## Classification model

`ClassifyCommand` parses a command line with a real Bash AST parser (`mvdan.cc/sh/v3/syntax`, pure Go, no cgo) and rates the worst construct. The handwritten shell lexer and its per-case token special-casing are gone; every construct below is handled by walking the AST uniformly.

### Provable literals

A simple command is only classified by the command tables when every word is provably literal: plain or quoted literal parts only. Any parameter expansion (`$x`, `${x}`), command substitution (`$(...)`, backquotes), arithmetic (`$((...))`, `$[...]`), process substitution (`<(...)`, `>(...)`), extended glob (`@(...)` and friends), `$'...'` escape quoting, array/index assignments, and any non-literal here-document body rate **Unknowable**. Plain glob characters (`*.log`) in argument position stay literal text and are classified as written; in command position and redirect targets they rate Unknowable, as do dynamic redirect targets (`> $f`). `~` is literal text and keeps the established critical-path treatment (`rm -rf ~` stays Forbidden; `rm -rf $HOME` is Unknowable).

Command and process substitutions are still classified recursively for their own effects: `echo $(rm -rf /)` is Forbidden because the substitution's content is Forbidden, while `echo $(id)` floors at Unknowable because the outer word is dynamic. One uniform word walker applies everywhere words appear — simple commands, assignments (including array elements and subscripts), `[[ ... ]]` operands, `case` words and patterns, loop items, here-document bodies and redirect targets — and it descends into parameter expansions (`${x:-$(...)}`, `${a[...]}`, `${x/pat/rep}`, slices) and arithmetic (`$(( $(...) ))`, `((...))`, `let`): nested substitutions execute and are Worst-aggregated, so `echo ${x:-$(rm -rf /)}` is Forbidden while `echo ${x:-default}` stays Unknowable.

### Capability tiers

Classification produces one of five tiers, ordered Safe < NeedsConfirm < Unknowable < Danger < Forbidden:

- **Safe (T0, read-only)**: provably read-only commands (`ls`, `cat`, `grep`, read-only `git`/`docker`/`kubectl`/`systemctl` subcommands, plain HTTP GET) and provably read-only structure (literal `[[ ... ]]`, quoted here-documents into filters).
- **NeedsConfirm (T1, bounded change)**: bounded, user-reviewed changes: file writes with literal targets (`touch`, `cp`, redirects), `git push`, package installs, service changes, interactive clients with bounded payloads, `sudo`/`su -c` around bounded commands, script-file interpreters, shell `-c` with a literal script (classified recursively with a T1 floor).
- **Unknowable**: anything the guard cannot prove bounded — dynamic words, unknown or ambiguous options during unwrapping (sudo/su/env/wrapper tables), unresolvable global options (git/systemctl/kubectl exec/docker/redis-cli), parse failures, function definitions, arithmetic commands, C-style loops, unknown pipe consumers, and depth/input/node budget overruns.
- **Danger**: known-dangerous capabilities — destructive operations (`kill`, `crontab` installs, `git clean` without dry-run, `systemctl stop/mask`, bulk `docker` deletion semantics), execution of unverifiable content (interpreter inline code, bare interpreter or `su`/`sudo` sessions, `at -f`, piped or file-fed execution consumers with non-determinable input, `sort --compress-program`, `tar --checkpoint-action=exec`), network-device redirects (`/dev/tcp/`, `/dev/udp/`), environment-based code injection (`LD_PRELOAD` and friends), alias definition or removal.
- **Forbidden**: always denied — critical-path writes and recursive deletes, `DROP DATABASE`, block-device writes, `mkfs`.

### Unwrapping

The wrapper registry (`nice`, `nohup`, `timeout`, `stdbuf`, `command`, `builtin`, `exec`, `time`, `watch`, `chrt`, `setsid`, `ionice`, `unshare`, `nsenter`, `systemd-run`, `at`, `batch`, `xargs`) only unwraps to the real command; it never guesses unknown options. `sudo`/`doas`/`sudoedit`, `su` and `env` unwrap the same way through their option tables (GNU unique-abbreviation resolution included). Unresolvable options or structure rate Unknowable. Nested commands keep their argv token boundaries, so quoted scripts and clustered options keep their meaning; `ssh` remote commands, `find -exec`, `git -c` executable configs, `mysql system`, `env -S` payloads and here-document bodies are re-parsed as shell text one level deeper.

Remote clients unwrap their leading options with complete value/flag tables before any dispatch: `kubectl` consumes its inherited globals (`--context`, `--kubeconfig`, `--namespace`/`-n`, `--server`/`-s`, `--v` and the rest of the documented set, exact pflag spellings — no abbreviations), and `ssh` consumes every documented short option (`-R`, `-D`, `-p`, `-J`, `-o`, ...) so a value can never shift into the remote command; `ssh -o` accepts both `Key=Value` and `Key Value` config syntax for the executing keywords (`ProxyCommand`, `LocalCommand`, `RemoteCommand`). `scp`/`sftp` unwrap `-o` the same way, `scp -S`, `sftp -D` and `rsync -e`/`--rsh`/`--rsync-path` classify the replacement program as a command, and `sftp -b` rates Danger. `tar` aligns short-option clusters (`-f` owns its value) so `-I` and `-F` always reach the external-program classification.

### Pipes and execution consumers

For `producer | consumer`, the consumer's stdin is exactly modeled when the producer is provably computable: `echo` without backslashes, a fully modeled `printf` (option handling incl. `--` and `-v`, escape sequences, and format reuse until the arguments run out — `printf '%s' a b` emits `ab`; `%b`, hex/octal escapes, `*` width or unknown verbs fail closed), or `cat` with no file operands whose last stdin-setting redirect is a literal here-document/here-string (`cat /tmp/x <<'EOF' | sh` is not determinable). Execution consumers — shells and interpreters, `at`/`batch`, `xargs`, `su`, database clients (`mysql`/`psql` via SQL classification, `redis-cli` per line) — classify the determinable payload with the full model (`echo 'rm -rf /home/user' | sh` is Forbidden; `echo hi | at now` stays T1 with the scheduled-job floor). Non-determinable input into an execution consumer rates Danger; a pipe into an unknown command rates Unknowable. Pipes between provable read-only commands (`curl ... | jq .`) stay Safe.

The stdin source propagates through compound statements instead of being dropped: `echo 'DROP DATABASE prod' | (mysql)` is Forbidden, `{ redis-cli; } <<< 'FLUSHALL'` and `echo 'FLUSHALL' | if true; then redis-cli; fi` are Danger, and a `coproc` body always sees an undetermined pipe. When several nested statements could consume the same stdin the guard attributes it to each of them — a deliberate over-approximation that never drops the source.

`xargs` parses the whole input stream with its real rules before building argv: default mode honors blanks, single/double quotes and backslash escapes (`echo '"-rf" "/"' | xargs rm` is Forbidden); `-0` and `-d` take items literally; words batch into one invocation by default, `-n N` batches by count, and `-L`/`-s` or unparseable input (unterminated quote, trailing backslash) fail closed to Danger. `-I`/`-J`/`--replace` (and GNU `-i`) insert one quote-processed line per invocation; `-r`/`--no-run-if-empty` with empty input rates Safe. `xargs` with no command is the narrow read-only default (its built-in echo); `xargs -a` rates Danger.

### Budgets

Classification is bounded: recursion depth (8 nested classification levels), input size (64 KiB) and AST node count (4096). Exceeding any budget rates Unknowable; a parser or walker panic is recovered into Unknowable.

### Tool calls

`ClassifyTool` fails closed the same way: tool arguments that are not a valid JSON object, an `exec_commands` payload that is not a command list, and unknown tool names rate Unknowable — never Silent-pre-approved. An empty command list is Safe (nothing executes).

### Critical paths

Critical system paths are protected with one canonicalized target model (`filepath.Clean`, exact roots such as `/Users`, `..` segments, duplicate separators) across redirections, `sudo -e`, SQL `INTO OUTFILE` and file-producing commands (`cp`, `mv`, `dd of=`, `install`, `ln`, `tee`, `truncate`, `shred`, `rm`, `rmdir`, `unlink`).

## Decision matrix

`Decide(config, ruling, memory)` maps a ruling to an action:

| Ruling \ Mode | ReadOnly | ReadWrite | Silent |
| --- | --- | --- | --- |
| Safe (T0) | allow | allow | allow |
| NeedsConfirm (T1) | deny | ask (allow once every kind is remembered) | allow |
| Unknowable | deny | ask | ask |
| Danger | deny | ask (never remembered) | ask |
| Forbidden | deny | deny | deny |

Silent auto-allows only T0 and T1. Unknowable is **not** pre-approved: dynamic input, unknown options, unresolvable wrappers and parse failures always ask, in every mode except ReadOnly (which denies everything above T0). This is a behavior change from the previous contract, where a single unknown top-level command ruled NeedsConfirm and Silent pre-approved it; under the new model anything dynamic rates Unknowable and asks.

The memory escape hatch is unchanged in shape: approving a T1 ruling in ReadWrite remembers its kinds, and later T1 rulings whose kinds are all remembered run without asking. Unknowable and Danger rulings carry no rememberable kinds (`KindUnknown` and `KindDanger` are rejected by `Memory`), so they always ask.

## Documented design boundaries

These boundaries are deliberate and must not change without a mission decision:

- `git push origin main` (with or without `--force`) stays T1: a network write the user reviews at the remote, not local destruction. Silent mode therefore auto-allows it.
- Plain read-only HTTP(S) GET (`curl https://example.test`, clustered `-sG` read forms) stays Safe. SSRF risk against link-local/internal addresses is accepted at this layer; network egress policy is enforced outside the guard. Attached stdin forms are not the stdout exemption: `curl -T-` (upload) and `curl -d-` (POST body from stdin) are NeedsConfirm, while `-o-`/`-D-`/`-c-` stay Safe.
- Read-only enumerations stay usable: `at -l`, `git clean -n`/`--dry-run`, `systemctl status/--failed`, `docker system df`/`top`/`history`, `crontab -l`, `wget -O -`, `echo x | xargs` (default echo), `psql -c 'SELECT 1'` (T1 as an interactive client).
- A bare interpreter (`bash`, `python3`), a bare `sudo`/`su` session, and alias definition/removal rate Danger: they open an execution environment the guard cannot review, where the previous model let them through as T1.
- `docker rm -f $(docker ps)` and every other command-substitution form rate Unknowable and ask in Silent; the previous model pre-approved plain table-listing substitutions. Deleting by dynamic ID set is never auto-allowed.

## Regression conventions

Table-driven classify + Decide regressions pin every contract row (see `shell_m64_test.go` for the corpus contract and `shell_r*.go` per review round). `shell_invariant_test.go` pins the cross-cutting invariants: Silent only auto-allows T0/T1, dynamic constructs rate Unknowable, stdin execution consumers fail closed, unwrapping never guesses, budgets fail closed, and a fuzz target asserts no panics and decision/tier consistency on arbitrary input. `shell_r9_test.go` pins the external-review round: test/case expansion walking, compound-statement stdin propagation, the exact printf/cat producer model, xargs stream semantics, tar/ssh/kubectl option alignment, nested expansion substitutions, curl attached stdin forms and tool-call fail-closed parsing. Every new Danger or Unknowable ruling needs both decision layers (Silent ask/deny, ReadOnly deny) covered.
