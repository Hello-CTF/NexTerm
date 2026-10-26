import type { Terminal } from "@xterm/xterm";
import { matchTerminalWithheldKeybinding } from "../../../app/keybindings";
import { isMac } from "../../../app/platform";
import { attachImeTextInput } from "../imeTextInput";
import { kittyKeyboardActive } from "./kittyKeyboard";
import { resolveKey, type KeyResolution } from "./resolveKey";

export interface TerminalInputOptions {
  /** The kitty keyboard switch the terminal was created with (vtExtensions.kittyKeyboard). */
  kittyKeyboard: boolean;
  now?: () => number;
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
  let compositionEndAt: number | null = null;
  const onCompositionEnd = () => {
    compositionEndAt = now();
  };
  host.addEventListener("compositionend", onCompositionEnd, true);
  const detachImeTextInput = attachImeTextInput(term, host);

  const apply = (event: KeyboardEvent, resolution: KeyResolution): boolean => {
    switch (resolution.kind) {
      case "ime":
        return resolution.toXterm;
      case "app-shortcut":
        // Holding a shortcut must neither type into the terminal nor re-run the action on every repeat.
        if (resolution.repeat) {
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
      case "pass":
        return true;
    }
  };

  term.attachCustomKeyEventHandler((event) =>
    apply(
      event,
      resolveKey(event, {
        mac: isMac(),
        matchAppShortcut: matchTerminalWithheldKeybinding,
        sinceCompositionEnd: compositionEndAt === null ? null : now() - compositionEndAt,
        kittyKeyboard: kittyKeyboardActive(term, options.kittyKeyboard),
      }),
    ),
  );

  return () => {
    host.removeEventListener("compositionend", onCompositionEnd, true);
    detachImeTextInput();
  };
}
