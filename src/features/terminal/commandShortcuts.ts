import type { KeybindingActionId } from "../../app/keybindings";

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

// commandShortcutActive reports whether a withheld shortcut acts now. When it does not, the terminal releases the
// chord: a chord the terminal encodes reaches the program, and one it sends nothing for still reaches the app.
// An alternate-screen program gets every chord but navigation; command block shortcuts also need command blocks.
export function commandShortcutActive(action: KeybindingActionId, state: CommandShortcutState): boolean {
  if (!state.normalBuffer) return ALTERNATE_SCREEN_KEPT_ACTIONS.has(action);
  if (BLOCK_ACTIONS.has(action)) return state.blocks;
  return true;
}
