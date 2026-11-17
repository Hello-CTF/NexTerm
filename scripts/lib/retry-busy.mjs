// macOS hosted runners intermittently fail `hdiutil create` with "Resource busy" while another process (Spotlight,
// XProtect) holds the disk image machinery. Such a failure is transient; any other failure is not retried.
export const BUSY_PATTERN = /Resource busy/i;

// runRetryingBusy calls attempt() until it succeeds, fails for a reason other than a busy resource, or `attempts` tries
// have been made. attempt returns { ok, output }; sleep(ms) waits between tries, growing by delayMs each time.
export function runRetryingBusy(attempt, { attempts = 5, delayMs = 5000, sleep, onRetry = () => {} } = {}) {
  for (let tryNumber = 1; ; tryNumber += 1) {
    const result = attempt(tryNumber);
    if (result.ok) return { ok: true, tries: tryNumber };
    const busy = BUSY_PATTERN.test(result.output || "");
    if (!busy || tryNumber >= attempts) return { ok: false, tries: tryNumber, busy, output: result.output || "" };
    const wait = delayMs * tryNumber;
    onRetry(tryNumber, wait);
    sleep(wait);
  }
}

export function sleepSync(ms) {
  Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, ms);
}
