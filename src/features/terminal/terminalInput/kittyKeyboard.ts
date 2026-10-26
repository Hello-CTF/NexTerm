// Kitty keyboard protocol (CSI = / ? / > / < u). @xterm/xterm 6.1.0-beta ships an implementation behind the
// `vtExtensions.kittyKeyboard` option (default off). NexTerm keeps it off; this device-local switch exists so the
// protocol can be evaluated in real webviews without a build: localStorage "nexterm.terminal.kittyKeyboard" = "on".
// New terminals pick the value up when they are created.
export const KITTY_KEYBOARD_SWITCH_KEY = "nexterm.terminal.kittyKeyboard";

export function kittyKeyboardSwitch(): boolean {
  try {
    return localStorage.getItem(KITTY_KEYBOARD_SWITCH_KEY) === "on";
  } catch {
    return false;
  }
}

interface KittyAwareCore {
  _core?: { _keyboardService?: { useKitty?: boolean } };
}

/**
 * Whether the program in the terminal has pushed kitty keyboard flags, so xterm encodes every key itself and the
 * pipeline's legacy byte overrides must stay out of the way. xterm exposes no public mode for this; with the switch
 * off the protocol can never become active, so the private state is read only after the switch is on.
 */
export function kittyKeyboardActive(term: object, switchOn: boolean): boolean {
  if (!switchOn) return false;
  return (term as KittyAwareCore)._core?._keyboardService?.useKitty === true;
}
