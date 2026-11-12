import { keybindingRepeats, type AppKeybindingHit, type KeyEventLike } from "../../../app/keybindings";
import { isImeKeyEvent } from "../../../ui/imeKeyEvent";
import { altMetaBytes, terminalKeyOverride } from "./keyEncoding";

export type TerminalKeyEvent = KeyEventLike & Pick<KeyboardEvent, "type" | "repeat" | "isComposing" | "keyCode">;

export interface ResolveKeyContext {
  /** macOS key conventions (Option word moves, Cmd line moves). */
  mac: boolean;
  /** The app-level shortcut this chord triggers under the current (possibly rebound, web or desktop) keymap. */
  matchAppShortcut: (event: KeyEventLike) => AppKeybindingHit | null;
  /**
   * WebKit only: a composition just ended and the Enter that committed it has not been seen yet. The adapter sets it
   * for at most one Enter per compositionend (see COMPOSITION_COMMIT_WINDOW_MS).
   */
  commitEnterPending: boolean;
  /** The running program enabled the kitty keyboard protocol, so xterm encodes every key itself. */
  kittyKeyboard: boolean;
  /** The platform is Windows (Ctrl+V pastes there, as in Windows Terminal). */
  windows: boolean;
  /** The local "Ctrl+V pastes" setting (Windows only). */
  ctrlVPaste: boolean;
  /**
   * The held Alt key acts as Meta: Alt+character sends ESC and the character. Always on outside macOS; on macOS it
   * follows the Option-as-Meta setting and which Option key is held.
   */
  altMeta: boolean;
}

export type KeyResolution =
  /**
   * The key belongs to the input method. `toXterm` keeps xterm's composition helper in the loop for keyCode 229
   * keydowns (it diffs the helper textarea to pick up soft-keyboard input); otherwise the key is withheld so it
   * only commits the composition and is never encoded as a terminal key (Enter must not also submit).
   */
  | { kind: "ime"; toXterm: boolean }
  /**
   * An app-level shortcut: the terminal never sees it. `dropRepeat` marks an auto-repeat of an action that runs once
   * per press (anything but tab, workspace and pane navigation): it neither reaches the terminal nor re-runs.
   */
  | { kind: "app-shortcut"; hit: AppKeybindingHit; dropRepeat: boolean }
  /**
   * Ctrl+V on Windows: the browser's default action must run so the paste event reaches the terminal. xterm would
   * encode it as ^V and preventDefault, which suppresses the paste. Applies to keydown, keypress and keyup alike.
   */
  | { kind: "native-paste" }
  /** Bytes the pipeline sends in place of xterm's default encoding. */
  | { kind: "terminal-bytes"; bytes: string }
  /** xterm's default handling. */
  | { kind: "pass" };

// WebKit (Safari, WKWebView, WebKitGTK) commits a composition on Enter and then delivers that Enter's keydown after
// compositionend, with isComposing false and sometimes a real keyCode 13. Chromium-based engines report the
// committing Enter as isComposing / keyCode 229 and need no rule; there a real Enter after compositionend is a new
// Enter (Android keyboards commit the word and send Enter in the same frame).
export const COMPOSITION_COMMIT_WINDOW_MS = 50;

/**
 * The single precedence order for a key event aimed at a terminal:
 *   1. IME composition (isComposing, keyCode 229, the one Enter WebKit delivers after committing); a Meta chord
 *      outside a composition is not IME input even when an input method reports it as keyCode 229
 *   2. app shortcut (current keymap, web or desktop, including user rebinding)
 *   3. Ctrl+V paste on Windows (also while kitty keyboard reporting is active)
 *   4. terminal override bytes (terminalKeyOverride, then Meta chords; skipped while kitty keyboard reporting is active)
 *   5. xterm's default encoding
 */
export function resolveKey(event: TerminalKeyEvent, ctx: ResolveKeyContext): KeyResolution {
  const metaBytes = ctx.altMeta && !event.isComposing ? altMetaBytes(event) : null;
  if (isImeKeyEvent(event) && metaBytes === null) return { kind: "ime", toXterm: event.keyCode === 229 };
  if (event.key === "Enter" && ctx.commitEnterPending) return { kind: "ime", toXterm: false };
  const hit = ctx.matchAppShortcut(event);
  if (hit) return { kind: "app-shortcut", hit, dropRepeat: event.repeat && !keybindingRepeats(hit.action) };
  if (ctx.windows && ctx.ctrlVPaste && isPlainCtrlV(event)) return { kind: "native-paste" };
  if (!ctx.kittyKeyboard) {
    const bytes = terminalKeyOverride(event, ctx.mac) ?? metaBytes;
    if (bytes !== null) return { kind: "terminal-bytes", bytes };
  }
  return { kind: "pass" };
}

function isPlainCtrlV(event: TerminalKeyEvent): boolean {
  if (!event.ctrlKey || event.shiftKey || event.altKey || event.metaKey) return false;
  if (event.key === "v" || event.key === "V") return true;
  // A non-Latin layout reports another character for the physical V key, so fall back to the key code.
  return !/^[A-Za-z]$/.test(event.key) && (event.code === "KeyV" || event.keyCode === 86);
}
