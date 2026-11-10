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

// commandShortcutActive reports whether a terminal-local command shortcut acts now. When it does not, its chord is
// not withheld and reaches the program in the terminal as usual. Every other action is unaffected.
export function commandShortcutActive(action: KeybindingActionId, state: CommandShortcutState): boolean {
  if (BLOCK_ACTIONS.has(action)) return state.normalBuffer && state.blocks;
  if (action === "generateCommand") return state.normalBuffer;
  return true;
}
