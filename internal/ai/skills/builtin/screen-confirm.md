---
name: screen-confirm
description: Read the terminal screen before typing into it, and confirm the prompt state afterwards. Use when sending keys or commands to an interactive terminal.
---
# Read the screen before and after you type

Use this whenever you drive an interactive terminal tab with `send_keys` (editors, installers, REPLs, password or yes/no prompts, anything that is not a plain shell command).

Do not use it for plain, non-interactive commands that `exec_commands` can run and return output for, or for reading a file or listing a directory (use the file tools). If the command is a single shell command at an idle prompt, one `read_screen` before and the exit code from `wait_ms` after is enough.

## Before sending

1. Pick the tab. With more than one terminal open, call `terminal_list` and use the handle of the exact tab as `tab_id`; do not assume the current one is the right one.
2. Call `read_screen` for the target tab and read the header: tab title, kind, shell-reported working directory and command state.
3. Decide from the screen, not from memory of earlier output. If the screen shows something unexpected (a different host, a pager, a confirmation prompt you did not anticipate, a running foreground program), stop and tell the user instead of typing.
4. Never type a password, token or secret that the user has not given you for this exact prompt.

## Sending

- Send the smallest input that moves the task forward. Do not chain a destructive answer (`y`, `yes`, Enter on a default) together with the keys that lead to the prompt.
- For a prompt whose default is destructive or irreversible (overwrite, delete, reboot, force push, drop), ask the user for confirmation first, even if the task seems to imply it.
- Use `paste=true` for multi-line text or text with special characters (it is sent literally, so `<enter>` and similar sequences are not interpreted). Use plain keys for single keystrokes and control keys such as `<ctrl+c>`.
- If `send_keys` is refused because the user typed into the terminal after your last read, that is a safeguard. Call `read_screen` again, tell the user what changed, and only continue once the screen matches your plan. Do not add `force=true` to get past it.

## After sending

1. For a plain shell command in a terminal that reports command state, pass `wait_ms` to `send_keys`: it waits for the command to finish and returns the exit code and screen tail, so no separate `wait_for` is needed. If the result says it is unverified, continue with step 2.
2. Otherwise use `wait_for` with a pattern that matches the expected next prompt or message. It checks only the last 30 lines of the terminal, so the pattern must match text that will be on screen at that point, not output that has scrolled away. Pass `since_seq` set to the `submitted_seq` returned by `send_keys` so you only match output produced after your input (not the typed text itself). Note that `submitted_seq` marks where your output starts and does not prove the program accepted the input. Add `quiet_ms` when the program prints in bursts.
3. Call `read_screen` again and verify the outcome. Report what the screen actually shows, including errors, rather than assuming success.
4. If `wait_for` times out, read the screen and report the real state; do not resend the same keys blindly.

## Leaving the terminal tidy

If you started a foreground program (editor, pager, REPL), exit it before finishing, and say so. If you cannot, tell the user which tab is left in which state.
