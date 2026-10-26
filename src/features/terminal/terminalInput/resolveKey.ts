import type { AppKeybindingHit, KeyEventLike } from "../../../app/keybindings";
import { isImeKeyEvent } from "../../../ui/imeKeyEvent";
import { terminalKeyOverride } from "./keyEncoding";

export type TerminalKeyEvent = KeyEventLike & Pick<KeyboardEvent, "type" | "repeat" | "isComposing" | "keyCode">;

export interface ResolveKeyContext {
  /** macOS key conventions (Option word moves, Cmd line moves). */
  mac: boolean;
  /** The app-level shortcut this chord triggers under the current (possibly rebound, web or desktop) keymap. */
  matchAppShortcut: (event: KeyEventLike) => AppKeybindingHit | null;
  /** Milliseconds since the last compositionend, or null when none is recent. */
  sinceCompositionEnd: number | null;
  /** The running program enabled the kitty keyboard protocol, so xterm encodes every key itself. */
  kittyKeyboard: boolean;
}

export type KeyResolution =
  /**
   * The key belongs to the input method. `toXterm` keeps xterm's composition helper in the loop for keyCode 229
   * keydowns (it diffs the helper textarea to pick up soft-keyboard input); otherwise the key is withheld so it
   * only commits the composition and is never encoded as a terminal key (Enter must not also submit).
   */
  | { kind: "ime"; toXterm: boolean }
  /** An app-level shortcut: the terminal never sees it. Auto-repeats are dropped rather than re-run. */
  | { kind: "app-shortcut"; hit: AppKeybindingHit; repeat: boolean }
  /** Bytes the pipeline sends in place of xterm's default encoding. */
  | { kind: "terminal-bytes"; bytes: string }
  /** xterm's default handling. */
  | { kind: "pass" };

// WebKit commits a composition on Enter and then delivers that Enter's keydown after compositionend, with
// isComposing false and, in some webviews, a real keyCode 13. An Enter that close behind compositionend is
// the committing keystroke (its keydown and keypress), not a new Enter.
export const COMPOSITION_COMMIT_WINDOW_MS = 50;

/**
 * The single precedence order for a key event aimed at a terminal:
 *   1. IME composition (isComposing, keyCode 229, the Enter that WebKit delivers after committing)
 *   2. app shortcut (current keymap, web or desktop, including user rebinding)
 *   3. terminal override bytes (terminalKeyOverride; skipped while kitty keyboard reporting is active)
 *   4. xterm's default encoding
 */
export function resolveKey(event: TerminalKeyEvent, ctx: ResolveKeyContext): KeyResolution {
  if (isImeKeyEvent(event)) return { kind: "ime", toXterm: event.keyCode === 229 };
  if (
    event.key === "Enter" &&
    ctx.sinceCompositionEnd !== null &&
    ctx.sinceCompositionEnd <= COMPOSITION_COMMIT_WINDOW_MS
  ) {
    return { kind: "ime", toXterm: false };
  }
  const hit = ctx.matchAppShortcut(event);
  if (hit) return { kind: "app-shortcut", hit, repeat: event.repeat };
  if (!ctx.kittyKeyboard) {
    const bytes = terminalKeyOverride(event, ctx.mac);
    if (bytes !== null) return { kind: "terminal-bytes", bytes };
  }
  return { kind: "pass" };
}
