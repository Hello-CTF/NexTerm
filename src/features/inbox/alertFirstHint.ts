import { useEffect, useState } from "react";

// Whether this device already showed the first-alert hint. It is a per-device note, never synced.
const STORAGE_KEY = "nexterm.device-alert-hint.v1";

let seen: boolean | null = null;

function read(): boolean {
  try {
    return localStorage.getItem(STORAGE_KEY) === "seen";
  } catch {
    return false;
  }
}

function markSeen(): void {
  seen = true;
  try {
    localStorage.setItem(STORAGE_KEY, "seen");
  } catch {
    // Storage may be unavailable; the hint stays hidden for this session only.
  }
}

// useAlertFirstHint: true while the hint is shown. Showing it once counts as seen, but it stays on screen for
// the lifetime of the component that first displayed it.
export function useAlertFirstHint(): { visible: boolean; dismiss: () => void } {
  const [visible, setVisible] = useState(() => {
    seen ??= read();
    return !seen;
  });
  useEffect(() => {
    if (visible) markSeen();
  }, [visible]);
  return { visible, dismiss: () => setVisible(false) };
}

export function resetAlertHintForTest(): void {
  seen = null;
}
