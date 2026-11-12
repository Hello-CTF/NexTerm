import type { Terminal } from "@xterm/xterm";
import { matchTerminalWithheldKeybinding, type AppKeybindingHit, type KeyEventLike, type KeybindingActionId } from "../../../app/keybindings";
import { getCtrlVPaste, getTerminalPrefs, type TerminalMacOptionMeta } from "../../../app/preferences";
import { isMac, isWindows } from "../../../app/platform";
import { attachImeTextInput } from "../imeTextInput";
import { kittyKeyboardActive } from "./kittyKeyboard";
import { COMPOSITION_COMMIT_WINDOW_MS, resolveKey, type KeyResolution } from "./resolveKey";

export interface TerminalInputOptions {
  /** The kitty keyboard switch the terminal was created with (vtExtensions.kittyKeyboard). */
  kittyKeyboard: boolean;
  /** Whether the engine delivers the composition-committing Enter after compositionend; detected when omitted. */
  webkitCommitEnter?: boolean;
  now?: () => number;
  /**
   * Whether a withheld action acts right now; when it returns false the chord goes to the program instead (for
   * example command block navigation while an alternate-screen editor runs). All withheld actions act when omitted.
   */
  withholds?: (action: KeybindingActionId) => boolean;
}

/**
 * WebKit engines: desktop Safari, WKWebView (macOS desktop, iOS and iPadOS, where every browser is WebKit) and
 * WebKitGTK. Chromium-based engines (Chrome, Edge, WebView2, Android WebView) also carry "AppleWebKit" in their user
 * agent but name Chrome or Chromium, and Android browsers name Android.
 */
export function isWebKitEngine(userAgent: string): boolean {
  return /AppleWebKit\//.test(userAgent) && !/Chrome|Chromium|Android/.test(userAgent);
}

/**
 * Whether the held Alt key acts as Meta. Outside macOS Alt is always Meta (AltGr is excluded by the encoder). On macOS
 * the setting chooses: both Option keys, only the left one (the right one keeps typing the layout's characters), or
 * neither. `side` is the Option key last pressed while the terminal was focused; unknown counts as left.
 */
export function altActsAsMeta(mac: boolean, setting: TerminalMacOptionMeta, side: "left" | "right" | null): boolean {
  if (!mac || setting === "both") return true;
  if (setting === "off") return false;
  return side !== "right";
}

/**
 * Wires a terminal's keyboard input through resolveKey. xterm always preventDefault + stopPropagation on keys it
 * handles, so window-level listeners receive nothing while the terminal is focused: app shortcuts are returned
 * unhandled here and bubble to the window, while override bytes are sent in place of xterm's default encoding.
 */
export function attachTerminalInput(
  term: Pick<Terminal, "attachCustomKeyEventHandler" | "input" | "textarea">,
  host: HTMLElement,
  options: TerminalInputOptions,
): () => void {
  const now = options.now ?? (() => performance.now());
  const withholds = options.withholds;
  const matchAppShortcut = (event: KeyEventLike): AppKeybindingHit | null => {
    const hit = matchTerminalWithheldKeybinding(event);
    return hit && withholds && !withholds(hit.action) ? null : hit;
  };
  const webkitCommitEnter = options.webkitCommitEnter ?? isWebKitEngine(navigator.userAgent);
  // WebKit's committing Enter: "pending" from compositionend until the next keydown (at most one Enter is taken),
  // "swallowing" from that Enter's keydown until its keyup so its keypress is withheld too.
  let commitEnter: "none" | "pending" | "swallowing" = "none";
  let compositionEndAt = 0;
  const onCompositionEnd = () => {
    if (!webkitCommitEnter) return;
    commitEnter = "pending";
    compositionEndAt = now();
  };
  host.addEventListener("compositionend", onCompositionEnd, true);
  const detachImeTextInput = attachImeTextInput(term, host);

  const commitEnterPending = (event: KeyboardEvent): boolean => {
    if (event.type === "keydown") {
      return commitEnter === "pending" && now() - compositionEndAt <= COMPOSITION_COMMIT_WINDOW_MS;
    }
    return event.type === "keypress" && commitEnter === "swallowing";
  };

  const apply = (event: KeyboardEvent, resolution: KeyResolution): boolean => {
    switch (resolution.kind) {
      case "ime":
        return resolution.toXterm;
      case "app-shortcut":
        // A held one-shot shortcut must neither type into the terminal nor re-run the action on every repeat.
        if (resolution.dropRepeat) {
          event.preventDefault();
          event.stopPropagation();
        }
        return false;
      case "terminal-bytes":
        // Overridden chords auto-repeat like any other key, so repeats send the bytes again.
        if (event.type === "keydown") {
          event.preventDefault();
          term.input(resolution.bytes, true);
        }
        return false;
      case "native-paste":
        // No preventDefault: the browser's default paste fires a paste event on xterm's textarea.
        return false;
      case "pass":
        return true;
    }
  };

  // The Option key held down, from its own keydown (location 2 is the right-hand key).
  let altSide: "left" | "right" | null = null;

  term.attachCustomKeyEventHandler((event) => {
    if (event.key === "Alt") {
      altSide = event.type === "keyup" ? null : event.location === 2 ? "right" : "left";
    } else if (!event.altKey) {
      altSide = null;
    }
    const pending = commitEnterPending(event);
    const resolution = resolveKey(event, {
      mac: isMac(),
      matchAppShortcut,
      commitEnterPending: pending,
      kittyKeyboard: kittyKeyboardActive(term, options.kittyKeyboard),
      windows: isWindows(),
      ctrlVPaste: getCtrlVPaste(),
      altMeta: altActsAsMeta(isMac(), getTerminalPrefs().macOptionMeta, altSide),
    });
    if (event.type === "keydown") {
      const swallowed = pending && event.key === "Enter" && resolution.kind === "ime" && !resolution.toXterm;
      commitEnter = swallowed ? "swallowing" : "none";
    } else if (event.type === "keyup" && commitEnter === "swallowing") {
      commitEnter = "none";
    }
    return apply(event, resolution);
  });

  return () => {
    host.removeEventListener("compositionend", onCompositionEnd, true);
    detachImeTextInput();
  };
}
