// Shared "summary line + manage button -> secondary dialog" pattern for settings groups that would otherwise
// flatten many switches into the main panel. Anything that signals a problem belongs in `summary`, not in the dialog.
import { useId, useRef, useState, type ReactNode } from "react";
import { createPortal } from "react-dom";
import { isImeKeyEvent, trapOverlayTab, useOverlayFocus } from "../../ui/DialogHost";

export function SummaryDialog({
  summary,
  buttonLabel = "管理",
  manageable = true,
  title,
  description,
  children,
}: {
  summary: ReactNode;
  buttonLabel?: string;
  /** False renders the summary alone, for users who may not manage the group. */
  manageable?: boolean;
  title: string;
  description?: ReactNode;
  children: ReactNode;
}) {
  const [open, setOpen] = useState(false);
  const titleId = useId();
  const modalRef = useRef<HTMLDivElement>(null);
  const layer = useOverlayFocus(open, modalRef);
  const close = () => setOpen(false);

  return (
    <>
      <div className="flex flex-wrap items-center gap-2">
        <div className="nx-hint min-w-0 flex-1 text-[12px]">{summary}</div>
        {manageable && (
          <button type="button" className="nx-btn nx-btn-outline nx-btn-sm shrink-0" aria-haspopup="dialog" onClick={() => setOpen(true)}>
            {buttonLabel}
          </button>
        )}
      </div>
      {open &&
        createPortal(
          <div className="nx-overlay" onClick={close}>
            <div
              ref={modalRef}
              className="nx-modal max-w-[560px]"
              role="dialog"
              aria-modal="true"
              aria-labelledby={titleId}
              tabIndex={-1}
              onClick={(event) => event.stopPropagation()}
              onKeyDown={(event) => {
                event.stopPropagation();
                if (!layer.isTopmost()) return;
                if (event.key === "Escape" && !event.repeat && !isImeKeyEvent(event)) {
                  event.preventDefault();
                  close();
                  return;
                }
                trapOverlayTab(event, modalRef.current);
              }}
            >
              <div className="nx-modal-body flex max-h-[75vh] flex-col gap-3 overflow-y-auto">
                <div>
                  <h2 id={titleId} className="text-[14px] font-semibold text-neutral-100">{title}</h2>
                  {description && <p className="nx-hint mt-1">{description}</p>}
                </div>
                {children}
                <div className="flex justify-end">
                  <button type="button" className="nx-btn nx-btn-primary nx-btn-sm" onClick={close}>完成</button>
                </div>
              </div>
            </div>
          </div>,
          document.body,
        )}
    </>
  );
}
