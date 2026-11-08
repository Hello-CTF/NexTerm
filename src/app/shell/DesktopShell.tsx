import { useEffect, useState, type MouseEvent } from "react";
import { TRANSPORT } from "../../ipc/env";
import { closeWindow, isWindowFullscreen, minimiseWindow, toggleMaximiseWindow } from "../../ipc/wails";
import { isMac, isWailsDragRegionTarget, wailsNoDragRegionStyle } from "../platform";
import { IconClose, IconMaximize, IconMinus } from "../../ui/icons";
import { ShellFrame, type ShellParts } from "./ShellFrame";
import { useStatusHome } from "./useStatusHome";
import { t } from "../../i18n";

function onDragRegionDoubleClick(event: MouseEvent<HTMLElement>): void {
  if (TRANSPORT !== "desktop") return;
  if (isWailsDragRegionTarget(event.target)) {
    void toggleMaximiseWindow();
  }
}

// The window state the native traffic lights follow: grey while another app is in front, and gone in fullscreen,
// where macOS shows no window buttons either. Entering or leaving fullscreen always resizes the window.
function useMacWindowState(): { focused: boolean; fullscreen: boolean } {
  const [focused, setFocused] = useState(() => typeof document === "undefined" || document.hasFocus());
  const [fullscreen, setFullscreen] = useState(false);
  useEffect(() => {
    let active = true;
    const onFocus = () => setFocused(true);
    const onBlur = () => setFocused(false);
    const onResize = () => {
      void Promise.resolve()
        .then(() => isWindowFullscreen())
        .then((value) => {
          if (active) setFullscreen(value);
        })
        .catch(() => undefined);
    };
    window.addEventListener("focus", onFocus);
    window.addEventListener("blur", onBlur);
    window.addEventListener("resize", onResize);
    if (TRANSPORT === "desktop") onResize();
    return () => {
      active = false;
      window.removeEventListener("focus", onFocus);
      window.removeEventListener("blur", onBlur);
      window.removeEventListener("resize", onResize);
    };
  }, []);
  return { focused, fullscreen };
}

function MacWindowControls() {
  const { focused, fullscreen } = useMacWindowState();
  if (fullscreen) return null;
  return (
    <div className="nx-mac-controls" style={wailsNoDragRegionStyle} data-inactive={focused ? undefined : "true"}>
      <button
        type="button"
        className="nx-mac-control is-close"
        style={wailsNoDragRegionStyle}
        title={t("关闭")}
        aria-label={t("关闭窗口")}
        onClick={() => void closeWindow()}
      >
        <IconClose size={8} />
      </button>
      <button
        type="button"
        className="nx-mac-control is-minimise"
        style={wailsNoDragRegionStyle}
        title={t("最小化")}
        aria-label={t("最小化窗口")}
        onClick={() => void minimiseWindow()}
      >
        <IconMinus size={8} />
      </button>
      <button
        type="button"
        className="nx-mac-control is-zoom"
        style={wailsNoDragRegionStyle}
        title={t("最大化 / 还原（双击空白处亦可）")}
        aria-label={t("最大化或还原窗口")}
        onClick={() => void toggleMaximiseWindow()}
      >
        <IconMaximize size={8} />
      </button>
    </div>
  );
}

function WindowControls() {
  return (
    <div className="nx-window-controls" style={wailsNoDragRegionStyle}>
      <button
        type="button"
        className="nx-window-control"
        style={wailsNoDragRegionStyle}
        title={t("最小化")}
        aria-label={t("最小化窗口")}
        onClick={() => void minimiseWindow()}
      >
        <IconMinus size={13} />
      </button>
      <button
        type="button"
        className="nx-window-control"
        style={wailsNoDragRegionStyle}
        title={t("最大化 / 还原（双击空白处亦可）")}
        aria-label={t("最大化或还原窗口")}
        onClick={() => void toggleMaximiseWindow()}
      >
        <IconMaximize size={12} />
      </button>
      <button
        type="button"
        className="nx-window-control is-close"
        style={wailsNoDragRegionStyle}
        title={t("关闭")}
        aria-label={t("关闭窗口")}
        onClick={() => void closeWindow()}
      >
        <IconClose size={14} />
      </button>
    </div>
  );
}

// DesktopShell serves the "desktop" and "compact" modes: a window with its own title bar. Compact windows get
// overlay sidebars from the shared layout; the window chrome (traffic lights to the left of the title on macOS,
// caption buttons on the right on Windows/Linux, and double-click to maximise on the drag region) stays. The status
// wall opens from the rail's overview button on demand, never at launch.
export function DesktopShell(parts: ShellParts) {
  const desktop = TRANSPORT === "desktop";
  const mac = desktop && isMac();
  const leadingWindowControls = mac ? <MacWindowControls /> : null;
  const windowControls = desktop && !mac ? <WindowControls /> : null;
  const home = useStatusHome(parts.body.docks);
  return (
    <ShellFrame
      {...parts}
      header={{ ...parts.header, leadingWindowControls, windowControls }}
      body={{ ...parts.body, home }}
      onDoubleClick={onDragRegionDoubleClick}
    />
  );
}
