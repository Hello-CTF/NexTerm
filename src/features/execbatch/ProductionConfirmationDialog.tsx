import { useEffect, useRef, useState, type KeyboardEvent as ReactKeyboardEvent } from "react";
import { createPortal } from "react-dom";
import type { ExecBatchConfirmationTarget } from "../../ipc/execBatchApi";
import { isImeKeyEvent, trapOverlayTab, useOverlayFocus } from "../../ui/DialogHost";
import { IconAlert, IconClose } from "../../ui/icons";

export interface ProductionConfirmationDialogChallenge {
  targetSetHash: string;
  productionTargets: (ExecBatchConfirmationTarget & { tabId?: string })[];
}

export function ProductionConfirmationDialog({
  challenge,
  contentHash,
  command,
  onConfirm,
  onCancel,
  variant = "execute",
  typedPhrase,
}: {
  challenge: ProductionConfirmationDialogChallenge;
  contentHash: string;
  command: string;
  onConfirm: () => void;
  onCancel: () => void;
  variant?: "execute" | "broadcast";
  typedPhrase?: string;
}) {
  const [acknowledged, setAcknowledged] = useState(false);
  const [typed, setTyped] = useState("");
  const confirmed = typedPhrase ? typed === typedPhrase : acknowledged;
  const action = variant === "broadcast" ? "广播" : "执行";
  const contentLabel = variant === "broadcast" ? "输入" : "命令";
  const modalRef = useRef<HTMLDivElement>(null);
  const cancelButtonRef = useRef<HTMLButtonElement>(null);
  const layer = useOverlayFocus(true, modalRef, { initialFocus: () => cancelButtonRef.current });
  const onCancelRef = useRef(onCancel);
  onCancelRef.current = onCancel;

  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if (event.key !== "Escape" || event.repeat || isImeKeyEvent(event) || !layer.isTopmost()) return;
      event.preventDefault();
      event.stopImmediatePropagation();
      onCancelRef.current();
    };
    window.addEventListener("keydown", onKey, true);
    return () => window.removeEventListener("keydown", onKey, true);
  }, [layer]);

  const onModalKeyDown = (event: ReactKeyboardEvent<HTMLDivElement>) => {
    event.stopPropagation();
    if (layer.isTopmost()) trapOverlayTab(event, modalRef.current);
  };

  return createPortal(
    <div className="nx-overlay" onClick={onCancel}>
      <div
        ref={modalRef}
        className="nx-modal max-w-[560px]"
        role="dialog"
        aria-modal="true"
        aria-label={`确认生产环境${action}`}
        tabIndex={-1}
        onClick={(event) => event.stopPropagation()}
        onKeyDown={onModalKeyDown}
      >
        <div className="nx-modal-header">
          <span className="flex items-center gap-1.5 truncate">
            <IconAlert size={14} className="text-amber-300" />
            确认生产环境{action}
          </span>
          <div className="nx-spacer" />
          <button type="button" className="nx-icon-btn nx-icon-btn-sm" aria-label="关闭生产确认" onClick={onCancel}>
            <IconClose size={12} />
          </button>
        </div>
        <div className="nx-modal-body">
          <p className="mb-3 text-[12px] leading-relaxed text-amber-300">
            {variant === "broadcast"
              ? "服务端资产元数据将以下目标标记为生产环境。请核对主机和输入；取消不会发送待确认输入。"
              : "服务端资产元数据将以下目标标记为生产环境。请核对主机和命令；取消不会执行任何命令。"}
          </p>
          <ul className="mb-3 flex max-h-44 flex-col gap-1.5 overflow-y-auto" aria-label="生产目标列表">
            {challenge.productionTargets.map((target) => (
              <li
                key={target.tabId ?? target.sessionId}
                className="rounded border border-amber-900/50 bg-amber-950/10 px-2.5 py-2"
                data-session-id={target.sessionId}
              >
                <div className="flex min-w-0 items-center gap-2">
                  <span className="min-w-0 flex-1 truncate font-mono text-[12px] text-neutral-100" title={target.hostname || target.sessionName}>
                    {target.hostname || target.sessionName}
                  </span>
                  <span className="nx-badge nx-badge-amber shrink-0">{target.environment}</span>
                </div>
                {target.hostname && target.sessionName !== target.hostname && (
                  <div className="mt-1 truncate text-[11px] text-neutral-400">会话：{target.sessionName}</div>
                )}
              </li>
            ))}
          </ul>
          <div className="mb-1 text-[11px] text-neutral-500">{variant === "broadcast" ? "待广播输入" : "待执行命令"}</div>
          <pre className="nx-pre mb-3 max-h-28 overflow-auto whitespace-pre-wrap break-all text-[11px]">{command}</pre>
          <div className="mb-3 text-[10px] text-neutral-500" title={`目标集合 ${challenge.targetSetHash}；${contentLabel} ${contentHash}`}>
            确认将绑定当前目标集合与{contentLabel}哈希；任一变化都需要重新确认。
          </div>
          {typedPhrase ? (
            <label className="flex flex-col gap-1.5 text-[12px] text-neutral-200">
              <span>请输入 <code className="text-amber-300">{typedPhrase}</code> 确认{action}生产环境操作。</span>
              <input
                className="nx-input font-mono"
                aria-label="输入生产环境确认文字"
                autoComplete="off"
                value={typed}
                onChange={(event) => setTyped(event.target.value)}
              />
            </label>
          ) : (
            <label className="flex cursor-pointer items-start gap-2 text-[12px] text-neutral-200">
              <input
                type="checkbox"
                className="mt-0.5 shrink-0 accent-amber-500"
                aria-label="确认生产环境风险"
                checked={acknowledged}
                onChange={(event) => setAcknowledged(event.target.checked)}
              />
              <span>我已核对上述 {challenge.productionTargets.length} 个生产目标和{contentLabel}，确认{action}。</span>
            </label>
          )}
        </div>
        <div className="nx-modal-footer">
          <button ref={cancelButtonRef} type="button" className="nx-btn nx-btn-ghost" onClick={onCancel}>
            取消
          </button>
          <button type="button" className="nx-btn nx-btn-danger" disabled={!confirmed} onClick={onConfirm}>
            确认{action}（{challenge.productionTargets.length}）
          </button>
        </div>
      </div>
    </div>,
    document.body,
  );
}
