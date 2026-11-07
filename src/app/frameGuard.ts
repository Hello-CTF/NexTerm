import { t } from "../i18n";
// A framed NexTerm could be clickjacked or driven by the embedding page, so it never mounts.
export function isFramed(win: Window): boolean {
  try {
    return win.self !== win.top;
  } catch {
    return true;
  }
}

// Browser and platform modes are protected by the server's framing headers, and platform mode is
// embedded on purpose, so only the desktop transport refuses to start inside a frame.
export function startUnlessFramed(win: Window, body: HTMLElement, start: () => void, opts: { desktop: boolean }): void {
  if (opts.desktop && isFramed(win)) {
    body.textContent = t("NexTerm 不能在框架中打开。");
    return;
  }
  start();
}
