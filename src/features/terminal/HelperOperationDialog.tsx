import { useEffect, useRef } from "react";
import { isImeKeyEvent, trapOverlayTab, useOverlayFocus } from "../../ui/DialogHost";
import { IconClose } from "../../ui/icons";
import { HelperOperationView, type HelperOperationViewProps } from "./HelperOperationView";

export function HelperOperationDialog({ onClose, ...props }: HelperOperationViewProps & { onClose: () => void }) {
  const modalRef = useRef<HTMLDivElement>(null);
  const closeRef = useRef<HTMLButtonElement>(null);
  const layer = useOverlayFocus(true, modalRef, { initialFocus: () => closeRef.current });
  const onCloseRef = useRef(onClose);
  onCloseRef.current = onClose;
  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if (event.key !== "Escape" || event.repeat || isImeKeyEvent(event) || !layer.isTopmost()) return;
      event.preventDefault();
      event.stopImmediatePropagation();
      onCloseRef.current();
    };
    window.addEventListener("keydown", onKey, true);
    return () => window.removeEventListener("keydown", onKey, true);
  }, [layer]);
  return (
    <div className="nx-overlay" onClick={onClose}>
      <div
        ref={modalRef}
        role="dialog"
        aria-modal="true"
        aria-label="持久终端组件"
        tabIndex={-1}
        className="nx-modal relative max-w-[480px]"
        onClick={(event) => event.stopPropagation()}
        onKeyDown={(event) => {
          event.stopPropagation();
          if (layer.isTopmost()) trapOverlayTab(event, modalRef.current);
        }}
      >
        <button ref={closeRef} type="button" aria-label="关闭持久终端组件窗口" className="nx-icon-btn absolute top-2 right-2" onClick={onClose}>
          <IconClose size={14} />
        </button>
        <HelperOperationView {...props} />
      </div>
    </div>
  );
}
