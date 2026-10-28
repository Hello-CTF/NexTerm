import type { KeyboardEvent as ReactKeyboardEvent } from "react";

type NativeImeFields = Pick<KeyboardEvent, "isComposing" | "keyCode">;

/**
 * Whether a key event belongs to an input method rather than to the page: the key arrived mid-composition,
 * or the browser reports keyCode 229 for a key the IME consumed (including Safari's composition-committing
 * Enter, which arrives after compositionend with isComposing already false).
 */
export function isImeKeyEvent(event: NativeImeFields | ReactKeyboardEvent): boolean {
  const native = "nativeEvent" in event ? event.nativeEvent : event;
  return native.isComposing || event.keyCode === 229;
}
