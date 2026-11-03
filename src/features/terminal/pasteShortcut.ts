import { isMac, isWindows } from "../../app/platform";

/** The chord users press to paste into the terminal on this platform, as shown in hints. */
export function pasteShortcutLabel(ctrlVPaste: boolean): string {
  if (isMac()) return "Cmd+V";
  if (isWindows() && ctrlVPaste) return "Ctrl+V";
  return "Ctrl+Shift+V";
}
