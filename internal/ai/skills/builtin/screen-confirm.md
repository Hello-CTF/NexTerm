---
name: screen-confirm
description: Read the terminal screen before typing into it, and confirm the prompt state afterwards. Use when sending keys or commands to an interactive terminal.
---
# Read the screen before and after you type

Use this whenever you drive an interactive terminal tab with `send_keys` (editors, installers, REPLs, password or yes/no prompts, anything that is not a plain shell command).

## Before sending

1. Call `read_screen` for the target tab and read the header: tab title, kind, shell-reported working directory and command state.
2. Decide from the screen, not from memory of earlier output. If the screen shows something unexpected (a different host, a pager, a confirmation prompt you did not anticipate, a running foreground program), stop and tell the user instead of typing.
3. Never type a password, token or secret that the user has not given you for this exact prompt.

## Sending

- Send the smallest input that moves the task forward. Do not chain a destructive answer (`y`, `yes`, Enter on a default) together with the keys that lead to the prompt.
- For a prompt whose default is destructive or irreversible (overwrite, delete, reboot, force push, drop), ask the user for confirmation first, even if the task seems to imply it.

## After sending

1. Use `wait_for` with a pattern that matches the expected next prompt or message. Pass `since_seq` from your earlier `read_screen` so you only match new output. Add `quiet_ms` when the program prints in bursts.
2. Call `read_screen` again and verify the outcome. Report what the screen actually shows, including errors, rather than assuming success.
3. If `wait_for` times out, read the screen and report the real state; do not resend the same keys blindly.

## Leaving the terminal tidy

If you started a foreground program (editor, pager, REPL), exit it before finishing, and say so. If you cannot, tell the user which tab is left in which state.
