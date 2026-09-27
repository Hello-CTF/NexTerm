/**
 * 侧栏宽度拖拽手柄。
 *
 * 用 Pointer Events + `setPointerCapture`，而不是在 window 上挂 mousemove：
 * 指针拖出窗口时仍然收得到 move，松手也不会"卡在拖拽态"—— 而在 window 上挂
 * 监听就必须在 unmount / 松手两条路径上都记得摘，漏一条就留下一个幽灵拖拽。
 *
 * ⚠️ 拖拽标志用 **ref** 而不是 useState：`pointerdown` 里 setState 之后，
 * 同一帧内紧跟着到达的 `pointermove` 读到的还是旧值（React 批量更新是异步的），
 * 于是第一个 move 会被自己判成"没在拖"而丢掉。真实鼠标下这一帧通常看不出来，
 * 自动化测试里必定翻车。ref 是同步的，没有这个问题。
 */
import { useRef, useState } from "react";

export interface ResizeHandleProps {
  /** `left` = 手柄贴左栏右侧（往右拖变宽）；`right` = 手柄贴右栏左侧（往左拖变宽）。 */
  side: "left" | "right";
  width: number;
  min: number;
  max: number;
  /** 双击复位到该宽度。 */
  defaultWidth: number;
  onChange: (w: number) => void;
}

export function ResizeHandle({
  side,
  width,
  min,
  max,
  defaultWidth,
  onChange,
}: ResizeHandleProps) {
  /** 同步的拖拽标志（见文件头注释）。 */
  const dragging = useRef(false);
  /** 纯样式用：决定要不要亮起那条高亮。 */
  const [active, setActive] = useState(false);
  /** 拖拽起点的指针位置与当时宽度。 */
  const start = useRef({ x: 0, w: 0 });

  const stop = (e: React.PointerEvent<HTMLDivElement>) => {
    dragging.current = false;
    setActive(false);
    if (e.currentTarget.hasPointerCapture(e.pointerId)) {
      e.currentTarget.releasePointerCapture(e.pointerId);
    }
  };

  return (
    <div
      role="separator"
      aria-orientation="vertical"
      aria-label={side === "left" ? "调整左栏宽度" : "调整 AI 侧栏宽度"}
      title="拖动调整宽度（双击复位）"
      className={`nx-resize-handle ${active ? "is-dragging" : ""}`}
      onPointerDown={(e) => {
        e.preventDefault();
        // 光标离开这条 5px 的窄条后仍要收到 move —— 必须捕获指针
        e.currentTarget.setPointerCapture(e.pointerId);
        start.current = { x: e.clientX, w: width };
        dragging.current = true;
        setActive(true);
      }}
      onPointerMove={(e) => {
        if (!dragging.current) return;
        const dx = e.clientX - start.current.x;
        // 左栏挂在它自己右边 → 往右拖变宽；右栏挂在左边 → 往左拖变宽
        const next = side === "left" ? start.current.w + dx : start.current.w - dx;
        onChange(Math.min(max, Math.max(min, next)));
      }}
      onPointerUp={stop}
      onPointerCancel={stop}
      onDoubleClick={() => onChange(defaultWidth)}
    />
  );
}
