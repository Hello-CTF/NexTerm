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

// MacWindowControls draws the three window buttons at the top left. The frameless window runs on Wails' default
// macOS options, which hide the native traffic lights, so the shell paints its own and reserves room for them (the
// rail's top padding and the header's left padding).
function MacWindowControls() {
  return (
    <div className="nx-mac-controls" style={wailsNoDragRegionStyle}>
      <button
        type="button"
        className="nx-mac-control is-close"
        style={wailsNoDragRegionStyle}
        title="关闭"
        aria-label="关闭窗口"
        onClick={() => void closeWindow()}
      >
        <IconClose size={8} />
      </button>
      <button
        type="button"
        className="nx-mac-control is-minimise"
        style={wailsNoDragRegionStyle}
        title="最小化"
        aria-label="最小化窗口"
        onClick={() => void minimiseWindow()}
      >
        <IconMinus size={8} />
      </button>
      <button
        type="button"
        className="nx-mac-control is-zoom"
        style={wailsNoDragRegionStyle}
        title="最大化 / 还原（双击空白处亦可）"
        aria-label="最大化或还原窗口"
        onClick={() => void toggleMaximiseWindow()}
      >
        <IconMaximize size={8} />
      </button>
    </div>
  );
}

// DesktopShell serves the "desktop" and "compact" modes: a window with its own title bar. Compact windows get
// overlay sidebars from the shared layout; the window chrome (custom controls, double-click to maximise on the
// drag region) stays. Windows and Linux put the controls at the right of the header, macOS at the top left.
export function DesktopShell(parts: ShellParts) {
  const desktop = TRANSPORT === "desktop";
  const windowControls = desktop && !isMac() ? <WindowControls /> : null;
  return (
    <>
      {desktop && isMac() ? <MacWindowControls /> : null}
      <ShellFrame
        {...parts}
        header={{ ...parts.header, windowControls }}
        onDoubleClick={onDragRegionDoubleClick}
      />
    </>
  );
}
