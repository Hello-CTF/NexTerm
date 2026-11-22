import type { KeyEventLike, KeybindingActionId } from "../../app/keybindings";

export interface CommandShortcutState {
  // The normal buffer is in front; an alternate-screen program (vim, less, tmux, nano) owns every key otherwise.
  normalBuffer: boolean;
  // The terminal tracks command blocks (not WinRM).
  blocks: boolean;
}

const BLOCK_ACTIONS: ReadonlySet<KeybindingActionId> = new Set<KeybindingActionId>([
  "previousPrompt",
  "nextPrompt",
  "copyLastOutput",
]);

// Moving between panes, tabs and workspaces stays with the app in an alternate-screen program too, so a full-screen
// editor never traps the keyboard in its pane.
export const ALTERNATE_SCREEN_KEPT_ACTIONS: ReadonlySet<KeybindingActionId> = new Set<KeybindingActionId>([
  "focusPaneLeft",
  "focusPaneRight",
  "focusPaneUp",
  "focusPaneDown",
  "nextTab",
  "previousTab",
  "switchTab",
  "nextWorkspace",
  "previousWorkspace",
]);

// The physical keys xterm turns into a single C0 control byte under Ctrl alone: letters, Space, 2-8, [ \ ] and /.
const BARE_CTRL_CODES = /^(Key[A-Z]|Digit[2-8]|Space|BracketLeft|Backslash|BracketRight|Slash)$/;
// Under Ctrl+Shift xterm sends ^_, ^@ and ^^ for the keys that type _, @ and ^ on a US layout.
const SHIFTED_CTRL_CODES = /^(Minus|Digit2|Digit6)$/;

/**
 * Whether xterm sends this chord as one bare C0 control byte (Ctrl+W is ^W): a key a full-screen program reads as
 * its own. Chords xterm sends as an ESC sequence (Ctrl+Alt+W is ESC ^W, read as Meta) or does not encode at all
 * (Ctrl+Shift+T) are not program keys.
 */
export function sendsBareControlByte(event: KeyEventLike): boolean {
  if (!event.ctrlKey || event.altKey || event.metaKey) return false;
  const code = event.code ?? "";
  return event.shiftKey ? SHIFTED_CTRL_CODES.test(code) : BARE_CTRL_CODES.test(code);
}

// commandShortcutActive reports whether a withheld shortcut acts now. When it does not, the terminal releases the
// chord to the program. Command block and AI command shortcuts act only at the shell (block shortcuts also need
// command blocks). In an alternate-screen program the app keeps navigation and every chord the program would not
// read as a plain control key; a chord xterm sends as a bare C0 byte (a binding on Ctrl+W, say) goes to the program.
// The terminal pane's own overlays (search, links, copy mode, quick select, session sidebar) take their chords with
// capture listeners before this check, so they act in an alternate-screen program too.
export function commandShortcutActive(action: KeybindingActionId, state: CommandShortcutState, event?: KeyEventLike): boolean {
  if (BLOCK_ACTIONS.has(action)) return state.normalBuffer && state.blocks;
  if (action === "generateCommand") return state.normalBuffer;
  if (state.normalBuffer || ALTERNATE_SCREEN_KEPT_ACTIONS.has(action)) return true;
  return event === undefined || !sendsBareControlByte(event);
}
