// A framed NexTerm could be clickjacked or driven by the embedding page, so it never mounts.
export function isFramed(win: Window): boolean {
  try {
    return win.self !== win.top;
  } catch {
    return true;
  }
}

export function startUnlessFramed(win: Window, body: HTMLElement, start: () => void): void {
  if (isFramed(win)) {
    body.textContent = "NexTerm 不能在框架中打开。";
    return;
  }
  start();
}
