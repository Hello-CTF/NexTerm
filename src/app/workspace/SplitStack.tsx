import { useEffect, useRef, useState, type ReactNode } from "react";
import { clampSplitRatio, splitRatioAt, splitRatioForKey } from "../../features/terminal/workspaceLayout";
import { createFrameCoalescer, RESIZE_END_EVENT, type FrameCoalescer } from "../../ui/ResizeHandle";

export function SplitStack({
  ratio,
  onRatio,
  top,
  bottom,
}: {
  ratio: number;
  onRatio: (r: number) => void;
  top: ReactNode;
  bottom?: ReactNode;
}) {
  const boxRef = useRef<HTMLDivElement>(null);
  const dragging = useRef(false);
  const [active, setActive] = useState(false);
  const currentRatio = clampSplitRatio(ratio);
  const ratioRef = useRef(currentRatio);
  ratioRef.current = currentRatio;
  const onRatioRef = useRef(onRatio);
  onRatioRef.current = onRatio;
  const framesRef = useRef<FrameCoalescer<number> | null>(null);
  if (!framesRef.current) {
    framesRef.current = createFrameCoalescer<number>((next) => onRatioRef.current(next));
  }

  useEffect(() => () => framesRef.current?.cancel(), []);

  const measure = (clientY: number) => {
    const rect = boxRef.current?.getBoundingClientRect();
    return rect
      ? splitRatioAt(clientY, rect, ratioRef.current)
      : ratioRef.current;
  };
  const finish = (clientY?: number) => {
    if (!dragging.current) return;
    dragging.current = false;
    setActive(false);
    if (clientY === undefined) framesRef.current?.flush();
    else framesRef.current?.flush(measure(clientY));
    window.dispatchEvent(new Event(RESIZE_END_EVENT));
  };
  const split = bottom != null;

  return (
    <div ref={boxRef} className="flex h-full min-h-0 flex-col">
      <div
        className="flex min-h-0 flex-col"
        style={split ? { flex: `0 0 calc(${currentRatio * 100}% - 3px)` } : { flex: "1 1 auto" }}
      >
        {top}
      </div>
      {split && (
        <>
          <div
            role="separator"
            tabIndex={0}
            aria-orientation="horizontal"
            aria-label="调整上下分屏比例"
            aria-valuemin={15}
            aria-valuemax={85}
            aria-valuenow={Math.round(currentRatio * 100)}
            aria-valuetext={`上栏 ${Math.round(currentRatio * 100)}%`}
            className={`nx-split-handle ${active ? "is-dragging" : ""}`}
            title="拖动调整上下比例（双击或回车复位，方向键微调）"
            onPointerDown={(event) => {
              if (event.button !== 0) return;
              event.preventDefault();
              event.currentTarget.setPointerCapture(event.pointerId);
              dragging.current = true;
              setActive(true);
            }}
            onPointerMove={(event) => {
              if (dragging.current) framesRef.current?.schedule(measure(event.clientY));
            }}
            onPointerUp={(event) => {
              finish(event.clientY);
              if (event.currentTarget.hasPointerCapture(event.pointerId)) {
                event.currentTarget.releasePointerCapture(event.pointerId);
              }
            }}
            onPointerCancel={() => finish()}
            onLostPointerCapture={() => finish()}
            onDoubleClick={() => {
              framesRef.current?.cancel();
              onRatioRef.current(0.5);
            }}
            onKeyDown={(event) => {
              const next = splitRatioForKey(ratioRef.current, event.key, event.shiftKey);
              if (next === null) return;
              event.preventDefault();
              onRatioRef.current(next);
            }}
          >
            <span className="nx-split-grip" />
          </div>
          <div className="flex min-h-0 flex-1 flex-col">{bottom}</div>
        </>
      )}
    </div>
  );
}
