// One-finger vertical swipes on the terminal screen scroll it the way a mouse wheel does: the scrollback in the
// normal buffer, wheel reports when the application tracks the mouse, and cursor keys in the alternate screen.
//
// xterm.js has its own touch scrolling (a document-level gesture recognizer that dispatches private
// "-xterm-gesturechange" events to the screen). It has no slop, so a tap or long press that jitters scrolls,
// it reacts to every finger of a pinch, ignores prefers-reduced-motion, and its fling loses the touch point that
// wheel reports need. Those change events are stopped at the host here so only this recognizer scrolls; xterm's
// recognizer still cancels the touch events on its screen, which keeps the page and the webview from scrolling.

export interface TouchScrollTerminal {
  readonly element: HTMLElement | undefined;
  readonly modes: { readonly mouseTrackingMode: string };
  readonly buffer: { readonly active: { readonly type: string } };
  scrollLines(amount: number): void;
}

export interface TouchScrollOptions {
  /** CSS pixel height of one terminal row; scrolling waits while it is unknown (0). */
  cellHeight?: () => number;
  reducedMotion?: () => boolean;
  requestFrame?: (callback: (time: number) => void) => number;
  cancelFrame?: (handle: number) => void;
  now?: () => number;
}

export interface TouchScrollHandle {
  /** True while the current touch (or the one that just ended) began by stopping a fling; it is not a tap. */
  stoppedFling(): boolean;
  dispose(): void;
}

// Matches the tap slop in XtermView and the link long-press slop: a touch that moves farther is no longer a tap.
export const TOUCH_SCROLL_SLOP_PX = 10;
const XTERM_GESTURE_CHANGE = "-xterm-gesturechange";
const VELOCITY_WINDOW_MS = 100;
const MIN_FLING_VELOCITY = 0.3; // px/ms
const STOP_FLING_VELOCITY = 0.02; // px/ms
const FLING_DECAY_PER_MS = 0.998;

interface CoreLike {
  _core?: { _renderService?: { dimensions?: { css?: { cell?: { height?: number } } } } };
}

function rendererCellHeight(term: TouchScrollTerminal): number {
  return (term as unknown as CoreLike)._core?._renderService?.dimensions?.css?.cell?.height ?? 0;
}

function prefersReducedMotion(): boolean {
  return typeof window.matchMedia === "function" && window.matchMedia("(prefers-reduced-motion: reduce)").matches;
}

type Phase = "idle" | "pending" | "scrolling" | "ignored";

export function attachTouchScroll(
  term: TouchScrollTerminal,
  host: HTMLElement,
  options: TouchScrollOptions = {},
): TouchScrollHandle {
  const cellHeight = options.cellHeight ?? (() => rendererCellHeight(term));
  const reducedMotion = options.reducedMotion ?? prefersReducedMotion;
  const requestFrame = options.requestFrame ?? ((callback) => requestAnimationFrame(callback));
  const cancelFrame = options.cancelFrame ?? ((handle) => cancelAnimationFrame(handle));
  const now = options.now ?? (() => performance.now());

  let phase: Phase = "idle";
  let touchId = -1;
  let startX = 0;
  let startY = 0;
  let lastX = 0;
  let lastY = 0;
  let samples: Array<{ t: number; y: number }> = [];
  let pixels = 0;
  let fling: number | null = null;
  let flingStopped = false;

  const screen = () => term.element?.querySelector<HTMLElement>(".xterm-screen") ?? null;

  const sendWheel = (target: HTMLElement, rows: number) => {
    // Report at the finger's cell, kept inside the screen so a finger that slid past the edge still reports.
    const rect = target.getBoundingClientRect();
    const clientX = Math.min(Math.max(lastX, rect.left), Math.max(rect.left, rect.right - 1));
    const clientY = Math.min(Math.max(lastY, rect.top), Math.max(rect.top, rect.bottom - 1));
    const deltaY = rows > 0 ? 1 : -1;
    // A line-mode wheel step is exactly one report (or one cursor key) in xterm's own wheel handling, so the
    // application sees what a mouse wheel would send, in the protocol and encoding it asked for.
    for (let i = 0; i < Math.abs(rows); i++) {
      target.dispatchEvent(
        new WheelEvent("wheel", { deltaY, deltaMode: WheelEvent.DOM_DELTA_LINE, clientX, clientY, bubbles: true, cancelable: true }),
      );
    }
  };

  // Positive pixels move the content up (towards the bottom of the output), like a positive wheel deltaY.
  const scrollBy = (deltaPx: number) => {
    const height = cellHeight();
    if (!(height > 0)) return;
    pixels += deltaPx;
    const rows = Math.trunc(pixels / height);
    if (rows === 0) return;
    pixels -= rows * height;
    const target = screen();
    if (target && (term.modes.mouseTrackingMode !== "none" || term.buffer.active.type === "alternate")) {
      sendWheel(target, rows);
    } else {
      term.scrollLines(rows);
    }
  };

  const stopFling = () => {
    if (fling === null) return false;
    cancelFrame(fling);
    fling = null;
    return true;
  };

  const startFling = () => {
    const end = samples[samples.length - 1];
    const first = samples.find((sample) => end.t - sample.t <= VELOCITY_WINDOW_MS) ?? end;
    const elapsed = end.t - first.t;
    if (elapsed <= 0 || reducedMotion()) return;
    // Finger velocity in px/ms; content follows the finger, so scrolling runs opposite to it.
    let velocity = -(end.y - first.y) / elapsed;
    if (Math.abs(velocity) < MIN_FLING_VELOCITY) return;
    let last = now();
    const step = () => {
      const time = now();
      const dt = Math.max(0, time - last);
      last = time;
      scrollBy(velocity * dt);
      velocity *= Math.pow(FLING_DECAY_PER_MS, dt);
      fling = Math.abs(velocity) < STOP_FLING_VELOCITY ? null : requestFrame(step);
    };
    fling = requestFrame(step);
  };

  const reset = () => {
    phase = "idle";
    touchId = -1;
    samples = [];
  };

  const tracked = (list: TouchList) => {
    for (let i = 0; i < list.length; i++) {
      const touch = list[i];
      if (touch.identifier === touchId) return touch;
    }
    return null;
  };

  const onStart = (event: TouchEvent) => {
    if (event.touches.length > 1) {
      // A second finger (pinch, two-finger gestures) is not a scroll.
      phase = "ignored";
      return;
    }
    flingStopped = stopFling();
    const touch = event.touches[0];
    const target = screen();
    if (!touch || !target || !(event.target instanceof Node) || !target.contains(event.target)) {
      phase = "ignored";
      return;
    }
    phase = "pending";
    touchId = touch.identifier;
    startX = lastX = touch.clientX;
    startY = lastY = touch.clientY;
    pixels = 0;
    samples = [{ t: now(), y: touch.clientY }];
  };

  const onMove = (event: TouchEvent) => {
    if (phase !== "pending" && phase !== "scrolling") return;
    const touch = tracked(event.touches);
    if (!touch) return;
    if (phase === "pending") {
      const dx = touch.clientX - startX;
      const dy = touch.clientY - startY;
      if (Math.hypot(dx, dy) <= TOUCH_SCROLL_SLOP_PX) return;
      if (Math.abs(dx) >= Math.abs(dy)) {
        phase = "ignored";
        return;
      }
      // Start from here so crossing the slop does not jump the content.
      phase = "scrolling";
      lastY = touch.clientY;
    }
    event.preventDefault();
    const deltaY = touch.clientY - lastY;
    lastX = touch.clientX;
    lastY = touch.clientY;
    const t = now();
    samples.push({ t, y: touch.clientY });
    while (samples.length > 2 && t - samples[0].t > VELOCITY_WINDOW_MS) samples.shift();
    scrollBy(-deltaY);
  };

  const onEnd = (event: TouchEvent) => {
    if (event.touches.length > 0) {
      // Lifting one finger of several leaves the rest ignored until all are up.
      if (phase === "scrolling" || phase === "pending") phase = "ignored";
      return;
    }
    if (phase === "scrolling" && event.type === "touchend" && tracked(event.changedTouches)) startFling();
    reset();
  };

  // xterm dispatches its gesture to the screen element without bubbling; the capture phase still reaches the host.
  const suppressXtermScroll = (event: Event) => event.stopPropagation();

  host.addEventListener("touchstart", onStart, { capture: true, passive: true });
  // Not passive: a scroll gesture cancels the touch move so WKWebView and Android WebView do not scroll the shell.
  host.addEventListener("touchmove", onMove, { capture: true, passive: false });
  host.addEventListener("touchend", onEnd, { capture: true, passive: true });
  host.addEventListener("touchcancel", onEnd, { capture: true, passive: true });
  host.addEventListener(XTERM_GESTURE_CHANGE, suppressXtermScroll, true);

  return {
    stoppedFling: () => flingStopped,
    dispose: () => {
      stopFling();
      host.removeEventListener("touchstart", onStart, true);
      host.removeEventListener("touchmove", onMove, true);
      host.removeEventListener("touchend", onEnd, true);
      host.removeEventListener("touchcancel", onEnd, true);
      host.removeEventListener(XTERM_GESTURE_CHANGE, suppressXtermScroll, true);
    },
  };
}
