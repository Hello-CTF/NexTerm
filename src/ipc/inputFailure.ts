// Terminal input that fails must not vanish: the first failure in a window is reported through a handler the app
// installs (a toast), and a failure that means the link or tab is gone also asks the app to probe and reconnect.

export interface InputFailure {
  tabId: string;
  error: unknown;
  // The error says the connection or the terminal tab is gone, so a reconnect probe is worth running.
  connectionLost: boolean;
}

export const INPUT_FAILURE_THROTTLE_MS = 5000;

let handler: ((failure: InputFailure) => void) | null = null;
let lastReported = Number.NEGATIVE_INFINITY;
let lastLostProbe = Number.NEGATIVE_INFINITY;

export function setInputFailureHandler(next: ((failure: InputFailure) => void) | null): void {
  handler = next;
  lastReported = Number.NEGATIVE_INFINITY;
  lastLostProbe = Number.NEGATIVE_INFINITY;
}

export function isConnectionLostError(error: unknown): boolean {
  if (!error || typeof error !== "object") return false;
  const { code, message } = error as { code?: unknown; message?: unknown };
  if (code === "disconnected" || code === "tab_closed" || code === "not_found") return true;
  return typeof message === "string" && /标签页已关闭|已断开/.test(message);
}

// reportInputFailure is throttled: a burst of failed keystrokes yields one report, and a connection-lost error
// is delivered at most once per window even when ordinary failures were reported just before.
export function reportInputFailure(tabId: string, error: unknown): void {
  if (!handler) return;
  const now = Date.now();
  const connectionLost = isConnectionLostError(error);
  if (connectionLost) {
    if (now - lastLostProbe < INPUT_FAILURE_THROTTLE_MS) return;
    lastLostProbe = now;
    lastReported = now;
  } else {
    if (now - lastReported < INPUT_FAILURE_THROTTLE_MS) return;
    lastReported = now;
  }
  try {
    handler({ tabId, error, connectionLost });
  } catch {
    // A failing reporter must not break the input path.
  }
}
