import { type MouseEvent } from "react";
import { TRANSPORT } from "../../ipc/env";
import { closeWindow, minimiseWindow, toggleMaximiseWindow } from "../../ipc/wails";
import { isMac, isWailsDragRegionTarget, wailsNoDragRegionStyle } from "../platform";
import { IconClose, IconMaximize, IconMinus } from "../../ui/icons";
import { ShellFrame, type ShellParts } from "./ShellFrame";

function onDragRegionDoubleClick(event: MouseEvent<HTMLElement>): void {
  if (TRANSPORT !== "desktop") return;
  if (isWailsDragRegionTarget(event.target)) {
    void toggleMaximiseWindow();
  }
}

function WindowControls() {
  return (
    <div className="flex shrink-0 items-center gap-0.5 border-l border-neutral-800/60 pl-1.5">
      <button
        className="nx-icon-btn" style={wailsNoDragRegionStyle}
        title="最小化"
        aria-label="最小化窗口"
        onClick={() => void minimiseWindow()}
      >
        <IconMinus size={13} />
      </button>
      <button
        className="nx-icon-btn" style={wailsNoDragRegionStyle}
        title="最大化 / 还原（双击空白处亦可）"
        aria-label="最大化或还原窗口"
        onClick={() => void toggleMaximiseWindow()}
      >
        <IconMaximize size={12} />
      </button>
      <button
        className="nx-icon-btn is-danger" style={wailsNoDragRegionStyle}
        title="关闭"
        aria-label="关闭窗口"
        onClick={() => void closeWindow()}
      >
        <IconClose size={14} />
      </button>
    </div>
  );
}

// DesktopShell serves the "desktop" and "compact" modes: a window with its own title bar. Compact windows get
// overlay sidebars from the shared layout; the window chrome (custom controls off macOS, double-click to maximise on
// the drag region) stays. macOS shows no window buttons: Wails hides the native ones on a frameless window.
export function DesktopShell(parts: ShellParts) {
  const windowControls = TRANSPORT === "desktop" && !isMac() ? <WindowControls /> : null;
  return (
    <ShellFrame
      {...parts}
      header={{ ...parts.header, windowControls }}
      onDoubleClick={onDragRegionDoubleClick}
    />
  );
}
