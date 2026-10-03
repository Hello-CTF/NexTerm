import { useEffect, useRef, useState } from "react";

export const RESIZE_END_EVENT = "nexterm:resize-end";

interface FrameDriver {
  request: (callback: () => void) => number;
  cancel: (handle: number) => void;
}

const browserFrames: FrameDriver = {
  request: (callback) => requestAnimationFrame(callback),
  cancel: (handle) => cancelAnimationFrame(handle),
};

export interface FrameCoalescer<T> {
  schedule: (value: T) => void;
  flush: (value?: T) => void;
  cancel: () => void;
}

export function createFrameCoalescer<T>(
  publish: (value: T) => void,
  driver: FrameDriver = browserFrames,
): FrameCoalescer<T> {
  let frame: number | null = null;
  let latest: T | undefined;
  let hasValue = false;

  const cancel = () => {
    if (frame !== null) driver.cancel(frame);
    frame = null;
    latest = undefined;
    hasValue = false;
  };

  return {
    schedule(value) {
      latest = value;
      hasValue = true;
      if (frame !== null) return;
      frame = driver.request(() => {
        frame = null;
        if (!hasValue) return;
        const next = latest as T;
        latest = undefined;
        hasValue = false;
        publish(next);
      });
    },
    flush(value) {
      if (arguments.length > 0) {
        latest = value;
        hasValue = true;
      }
      if (frame !== null) driver.cancel(frame);
      frame = null;
      if (!hasValue) return;
      const next = latest as T;
      latest = undefined;
      hasValue = false;
      publish(next);
    },
    cancel,
  };
}

export interface ResizeHandleProps {
  side: "left" | "right";
  width: number;
  min: number;
  max: number;
  defaultWidth: number;
  onChange: (width: number) => void;
}

export function widthForPointer(
  side: "left" | "right",
  startWidth: number,
  startX: number,
  clientX: number,
  min: number,
  max: number,
): number {
  const delta = clientX - startX;
  const next = side === "left" ? startWidth + delta : startWidth - delta;
  return Math.min(max, Math.max(min, next));
}

export function widthForKey(
  side: "left" | "right",
  width: number,
  key: string,
  min: number,
  max: number,
  shiftKey = false,
): number | null {
  const step = shiftKey ? 50 : 10;
  const direction = side === "left" ? 1 : -1;
  switch (key) {
    case "ArrowLeft":
      return Math.min(max, Math.max(min, width - step * direction));
    case "ArrowRight":
      return Math.min(max, Math.max(min, width + step * direction));
    case "Home":
      return min;
    case "End":
      return max;
    case "Enter":
      return null;
    default:
      return null;
  }
}

export function ResizeHandle({
  side,
  width,
  min,
  max,
  defaultWidth,
  onChange,
}: ResizeHandleProps) {
  const dragging = useRef(false);
  const [active, setActive] = useState(false);
  const start = useRef({ x: 0, width: 0 });
  const onChangeRef = useRef(onChange);
  onChangeRef.current = onChange;
  const framesRef = useRef<FrameCoalescer<number> | null>(null);
  if (!framesRef.current) {
    framesRef.current = createFrameCoalescer<number>((next) => onChangeRef.current(next));
  }

  useEffect(() => () => framesRef.current?.cancel(), []);

  const finish = (clientX?: number) => {
    if (!dragging.current) return;
    dragging.current = false;
    setActive(false);
    if (clientX === undefined) framesRef.current?.flush();
    else {
      framesRef.current?.flush(
        widthForPointer(side, start.current.width, start.current.x, clientX, min, max),
      );
    }
    window.dispatchEvent(new Event(RESIZE_END_EVENT));
  };

  return (
    <div
      role="separator"
      tabIndex={0}
      aria-orientation="vertical"
      aria-label={side === "left" ? "调整左栏宽度" : "调整 AI 侧栏宽度"}
      aria-valuemin={min}
      aria-valuemax={max}
      aria-valuenow={Math.round(width)}
      aria-valuetext={`${Math.round(width)} 像素`}
      title="拖动调整宽度（双击或回车复位，方向键微调）"
      className={`nx-resize-handle ${active ? "is-dragging" : ""}`}
      onPointerDown={(event) => {
        if (event.button !== 0) return;
        event.preventDefault();
        event.currentTarget.setPointerCapture(event.pointerId);
        start.current = { x: event.clientX, width };
        dragging.current = true;
        setActive(true);
      }}
      onPointerMove={(event) => {
        if (!dragging.current) return;
        framesRef.current?.schedule(
          widthForPointer(side, start.current.width, start.current.x, event.clientX, min, max),
        );
      }}
      onPointerUp={(event) => {
        finish(event.clientX);
        if (event.currentTarget.hasPointerCapture(event.pointerId)) {
          event.currentTarget.releasePointerCapture(event.pointerId);
        }
      }}
      onPointerCancel={() => finish()}
      onLostPointerCapture={() => finish()}
      onDoubleClick={() => {
        framesRef.current?.cancel();
        onChangeRef.current(defaultWidth);
      }}
      onKeyDown={(event) => {
        if (event.key === "Enter") {
          event.preventDefault();
          onChangeRef.current(defaultWidth);
          return;
        }
        const next = widthForKey(side, width, event.key, min, max, event.shiftKey);
        if (next === null) return;
        event.preventDefault();
        onChangeRef.current(next);
      }}
    />
  );
}
