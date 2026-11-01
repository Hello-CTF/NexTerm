// Touch input for the remote screen. The recognizer is plain code over touch points so it can be tested without a
// browser; RemoteTouchLayer connects it to the DOM.

export type TouchMode = "direct" | "trackpad";

export const TAP_SLOP_PX = 10;
export const LONG_PRESS_MS = 500;
export const PINCH_THRESHOLD = 0.06;

export interface Point {
  x: number;
  y: number;
}

export type GestureAction =
  | { type: "move"; at: Point }
  | { type: "button"; button: 0 | 2; down: boolean; at: Point }
  | { type: "wheel"; deltaX: number; deltaY: number; at: Point }
  | { type: "pinch"; factor: number; center: Point }
  | { type: "pan"; dx: number; dy: number };

function distance(a: Point, b: Point): number {
  return Math.hypot(a.x - b.x, a.y - b.y);
}

function midpoint(a: Point, b: Point): Point {
  return { x: (a.x + b.x) / 2, y: (a.y + b.y) / 2 };
}

// GestureRecognizer turns touches into remote pointer actions.
//   one finger, direct mode: tap clicks where touched; moving drags with the left button held
//   one finger, trackpad mode: moving moves the cursor by the finger's distance; tap clicks at the cursor
//   long press without moving: right click
//   two fingers: moving together scrolls; spreading or pinching zooms and pans
export class GestureRecognizer {
  private mode: TouchMode;
  private readonly emit: (action: GestureAction) => void;
  private cursor: Point = { x: 0, y: 0 };
  private start: Point | null = null;
  private last: Point | null = null;
  private dragging = false;
  private longPressed = false;
  private multi = false;
  private pinching = false;
  private lastDistance = 0;
  private lastCenter: Point | null = null;
  private timer: ReturnType<typeof setTimeout> | null = null;

  constructor(mode: TouchMode, emit: (action: GestureAction) => void, cursor?: Point) {
    this.mode = mode;
    this.emit = emit;
    if (cursor) this.cursor = cursor;
  }

  setMode(mode: TouchMode): void {
    this.mode = mode;
  }

  setCursor(point: Point): void {
    this.cursor = point;
  }

  getCursor(): Point {
    return this.cursor;
  }

  private clearTimer(): void {
    if (this.timer !== null) clearTimeout(this.timer);
    this.timer = null;
  }

  private target(point: Point): Point {
    return this.mode === "trackpad" ? this.cursor : point;
  }

  touchStart(points: readonly Point[]): void {
    if (points.length >= 2) {
      this.clearTimer();
      // A second finger ends a one-finger gesture that was in progress.
      if (this.dragging) this.emit({ type: "button", button: 0, down: false, at: this.target(this.last ?? points[0]) });
      this.dragging = false;
      this.multi = true;
      this.pinching = false;
      this.lastDistance = distance(points[0], points[1]);
      this.lastCenter = midpoint(points[0], points[1]);
      return;
    }
    if (this.multi) return;
    this.start = points[0];
    this.last = points[0];
    this.dragging = false;
    this.longPressed = false;
    this.clearTimer();
    this.timer = setTimeout(() => {
      this.timer = null;
      if (this.dragging || !this.start) return;
      this.longPressed = true;
      const at = this.target(this.start);
      this.emit({ type: "move", at });
      this.emit({ type: "button", button: 2, down: true, at });
      this.emit({ type: "button", button: 2, down: false, at });
    }, LONG_PRESS_MS);
  }

  touchMove(points: readonly Point[]): void {
    if (points.length >= 2 && this.multi) {
      const center = midpoint(points[0], points[1]);
      const spread = distance(points[0], points[1]);
      if (this.lastDistance > 0) {
        const factor = spread / this.lastDistance;
        if (this.pinching || Math.abs(factor - 1) > PINCH_THRESHOLD) {
          this.pinching = true;
          this.emit({ type: "pinch", factor, center });
          if (this.lastCenter) this.emit({ type: "pan", dx: center.x - this.lastCenter.x, dy: center.y - this.lastCenter.y });
        } else if (this.lastCenter) {
          this.emit({ type: "wheel", deltaX: -(center.x - this.lastCenter.x), deltaY: -(center.y - this.lastCenter.y), at: this.target(center) });
        }
      }
      this.lastDistance = spread;
      this.lastCenter = center;
      return;
    }
    if (this.multi || !this.start || !this.last) return;
    const point = points[0];
    if (!this.dragging && distance(point, this.start) <= TAP_SLOP_PX) return;
    this.clearTimer();
    if (this.longPressed) return;
    if (this.mode === "direct") {
      if (!this.dragging) {
        this.dragging = true;
        this.emit({ type: "move", at: this.start });
        this.emit({ type: "button", button: 0, down: true, at: this.start });
      }
      this.emit({ type: "move", at: point });
    } else {
      this.dragging = true;
      this.cursor = { x: this.cursor.x + (point.x - this.last.x), y: this.cursor.y + (point.y - this.last.y) };
      this.emit({ type: "move", at: this.cursor });
    }
    this.last = point;
  }

  touchEnd(remaining: number): void {
    this.clearTimer();
    if (this.multi) {
      if (remaining === 0) {
        this.multi = false;
        this.pinching = false;
        this.lastCenter = null;
      }
      return;
    }
    if (remaining > 0 || !this.start) return;
    const at = this.target(this.last ?? this.start);
    if (this.dragging) {
      if (this.mode === "direct") this.emit({ type: "button", button: 0, down: false, at });
    } else if (!this.longPressed) {
      this.emit({ type: "move", at });
      this.emit({ type: "button", button: 0, down: true, at });
      this.emit({ type: "button", button: 0, down: false, at });
    }
    this.start = null;
    this.last = null;
    this.dragging = false;
  }

  cancel(): void {
    this.clearTimer();
    if (this.dragging && this.mode === "direct") this.emit({ type: "button", button: 0, down: false, at: this.target(this.last ?? { x: 0, y: 0 }) });
    this.start = null;
    this.last = null;
    this.dragging = false;
    this.multi = false;
    this.pinching = false;
  }
}
