import type { MouseEvent as ReactMouseEvent } from "react";
import { IconCheck, IconCopy, IconInsert } from "../../ui/icons";
import "./selectionActionBar.css";

const COARSE_POINTER_QUERY = "(pointer: coarse)";
const BAR_HALF_WIDTH_PX = 112;
const VIEWPORT_MARGIN_PX = 8;

// 平台判定不引用 platform.ts: 多个终端测试对 platform 的局部 mock 未导出 isCoarsePointer。
export function isCoarsePointerDevice(): boolean {
  return (
    typeof window !== "undefined" &&
    typeof window.matchMedia === "function" &&
    window.matchMedia(COARSE_POINTER_QUERY).matches
  );
}

export interface SelectionActionBarProps {
  x: number;
  y: number;
  onCopy: () => void;
  onPaste: () => void;
  onSelectAll: () => void;
}

export function SelectionActionBar({ x, y, onCopy, onPaste, onSelectAll }: SelectionActionBarProps) {
  const minLeft = BAR_HALF_WIDTH_PX + VIEWPORT_MARGIN_PX;
  const maxLeft = Math.max(minLeft, window.innerWidth - BAR_HALF_WIDTH_PX - VIEWPORT_MARGIN_PX);
  const left = Math.min(Math.max(x, minLeft), maxLeft);
  const placeBelow = y < 72;
  const stopContextMenu = (event: ReactMouseEvent) => event.stopPropagation();
  return (
    <div
      role="toolbar"
      aria-label="选区操作"
      className="nx-selection-bar"
      style={{
        left,
        top: placeBelow ? y + 22 : y - 10,
        transform: placeBelow ? "translateX(-50%)" : "translate(-50%, -100%)",
      }}
      onContextMenu={stopContextMenu}
    >
      <button type="button" onClick={onCopy}>
        <IconCopy size={13} />
        复制
      </button>
      <button type="button" onClick={onPaste}>
        <IconInsert size={13} />
        粘贴
      </button>
      <button type="button" onClick={onSelectAll}>
        <IconCheck size={13} />
        全选
      </button>
    </div>
  );
}
