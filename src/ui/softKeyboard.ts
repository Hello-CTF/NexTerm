import { isEditableTarget } from "./DialogHost";

/** Whether the soft keyboard is open, as tracked by useKeyboardInset on touch screens. */
export function isSoftKeyboardOpen(): boolean {
  return document.documentElement.dataset.nxKeyboard === "open";
}

// The soft keyboard follows the focused editable element (a terminal's hidden textarea, the buffered input, the
// remote desktop's hidden input), so blurring it closes the keyboard; a later tap on that surface focuses it again.
export function dismissSoftKeyboard(): void {
  const active = document.activeElement;
  if (active instanceof HTMLElement && isEditableTarget(active)) active.blur();
  try {
    (navigator as Navigator & { virtualKeyboard?: { hide?: () => void } }).virtualKeyboard?.hide?.();
  } catch {
    // The VirtualKeyboard API is optional; the blur above is the portable path.
  }
}
