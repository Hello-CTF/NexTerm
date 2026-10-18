import { useEffect, useState } from "react";
import { DESKTOP } from "../ipc/env";
import { desktopTrayApi } from "../ipc/commands";
import { EVENTS, listenEvent } from "../ipc/events";
import { closeWindow } from "../ipc/wails";
import { isWindows } from "./platform";
import { useUi } from "./store";
import { describeError } from "../ui/errorText";

// CloseConfirmDialog asks once how a window close should behave (tray vs
// quit) when the preference was never saved; the choice is persisted and the
// close is replayed so the tray hook applies it uniformly.
export function CloseConfirmDialog() {
  const showCloseConfirm = DESKTOP && isWindows();
  const { pushToast } = useUi();
  const [open, setOpen] = useState(false);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (!showCloseConfirm) return;
    let cancelled = false;
    let off: (() => void) | undefined;
    void listenEvent(EVENTS.closeConfirm, () => setOpen(true)).then((unlisten) => {
      if (cancelled) {
        unlisten();
      } else {
        off = unlisten;
      }
    });
    return () => {
      cancelled = true;
      off?.();
    };
  }, [showCloseConfirm]);

  if (!showCloseConfirm || !open) return null;

  const choose = (keepInTray: boolean) => {
    if (busy) return;
    setBusy(true);
    desktopTrayApi
      .set(keepInTray)
      .then(() => {
        setOpen(false);
        void closeWindow();
      })
      .catch((e) => {
        pushToast("error", describeError(e));
      })
      .finally(() => setBusy(false));
  };

  return (
    <div className="nx-overlay" onClick={() => setOpen(false)}>
      <div
        className="nx-modal max-w-[440px]"
        role="dialog"
        aria-modal="true"
        aria-labelledby="close-confirm-title"
        onClick={(event) => event.stopPropagation()}
        onKeyDown={(event) => {
          if (event.key === "Escape" && !event.repeat) {
            event.preventDefault();
            setOpen(false);
          }
        }}
      >
        <div className="nx-modal-body">
          <div id="close-confirm-title" className="mb-2 text-[13px] font-medium text-neutral-100">
            关闭窗口时如何处理?
          </div>
          <p className="nx-hint">
            可以保留在系统托盘后台运行,终端会话保持连接;也可以直接退出。之后可在「设置 → 终端」中更改。
          </p>
        </div>
        <div className="nx-modal-footer">
          <button type="button" className="nx-btn nx-btn-ghost" disabled={busy} onClick={() => choose(false)}>
            直接退出
          </button>
          <button type="button" className="nx-btn nx-btn-primary" disabled={busy} autoFocus onClick={() => choose(true)}>
            保留后台运行
          </button>
        </div>
      </div>
    </div>
  );
}
