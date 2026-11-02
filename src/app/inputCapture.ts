// Keyboard capture is a small shared flag. While a remote desktop canvas owns the keyboard, every key belongs to the
// remote machine and the app-level shortcuts (useAppKeybindings) stand down; the only chord that still reaches the app
// is the release chord, which the remote pane itself handles.

let owner: string | null = null;
const listeners = new Set<() => void>();

function publish(): void {
  for (const listener of [...listeners]) listener();
}

export function captureKeyboard(id: string): void {
  if (owner === id) return;
  owner = id;
  publish();
}

// A pane releases only what it captured, so a late blur from a closed pane cannot free another pane's capture.
export function releaseKeyboard(id: string): void {
  if (owner !== id) return;
  owner = null;
  publish();
}

export function isKeyboardCaptured(): boolean {
  return owner !== null;
}

export function keyboardCaptureOwner(): string | null {
  return owner;
}

export function subscribeKeyboardCapture(listener: () => void): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

export const RELEASE_CHORD_LABEL = "Ctrl+Alt+Shift+Esc";

export interface ReleaseChordEvent {
  key: string;
  code?: string;
  ctrlKey: boolean;
  altKey: boolean;
  shiftKey: boolean;
}

export function isReleaseChord(event: ReleaseChordEvent): boolean {
  return (event.key === "Escape" || event.code === "Escape") && event.ctrlKey && event.altKey && event.shiftKey;
}

export function resetKeyboardCaptureForTest(): void {
  owner = null;
  listeners.clear();
}
