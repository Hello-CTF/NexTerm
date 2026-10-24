import { useEffect, useMemo, useRef, useState, type KeyboardEvent as ReactKeyboardEvent } from "react";
import { createPortal } from "react-dom";
import type { ExecBatchConfirmationTarget } from "../../ipc/execBatchApi";
import {
  MANAGED_DEVICE_ENVIRONMENT,
  MANAGED_DEVICE_ENVIRONMENT_HINT,
  environmentDisplayLabel,
  evaluateEnvironmentConfirmation,
  isManagedDeviceEnvironment,
  targetKey,
  type EnvironmentConfirmationChallenge,
} from "../production/environmentConfirmation";
import { isImeKeyEvent, trapOverlayTab, useOverlayFocus } from "../../ui/DialogHost";
import { IconAlert, IconClose } from "../../ui/icons";

export { MANAGED_DEVICE_ENVIRONMENT };

export interface ProductionConfirmationDialogChallenge {
  targetSetHash: string;
  requiredEnvironmentNames?: string[];
  productionTargets: (ExecBatchConfirmationTarget & { tabId?: string })[];
}

export function ProductionConfirmationDialog({
  challenge,
  contentHash,
  command,
  onConfirm,
  onCancel,
  variant = "execute",
}: {
  challenge: ProductionConfirmationDialogChallenge;
  contentHash: string;
  command: string;
  onConfirm: (environmentNames: string[]) => void;
  onCancel: () => void;
  variant?: "execute" | "broadcast";
}) {
  return (
    <TypedEnvironmentConfirmationDialog
      challenge={challenge}
      contentHash={contentHash}
      command={command}
      onConfirm={onConfirm}
      onCancel={onCancel}
      variant={variant}
    />
  );
}

function TypedEnvironmentConfirmationDialog({
  challenge,
  contentHash,
  command,
  onConfirm,
  onCancel,
  variant,
}: {
  challenge: ProductionConfirmationDialogChallenge;
  contentHash: string;
  command: string;
  onConfirm: (environmentNames: string[]) => void;
  onCancel: () => void;
  variant: "execute" | "broadcast";
}) {
  const [typedByTarget, setTypedByTarget] = useState<Record<string, string>>({});
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

  const missingEnvironmentNames = !challenge.requiredEnvironmentNames?.length;
  const hasManagedDevice = challenge.productionTargets.some((target) => isManagedDeviceEnvironment(target.environment));
  const environmentChallenge: EnvironmentConfirmationChallenge = useMemo(
    () => ({
      targetSetHash: challenge.targetSetHash,
      requiredEnvironmentNames: challenge.requiredEnvironmentNames ?? [],
      productionTargets: challenge.productionTargets,
    }),
    [challenge],
  );
  // The dialog is keyed by the challenge hashes at every call site, so the
  // open challenge is always the latest one; re-evaluating with its own hash
  // keeps the fail-closed stale guard in the view-model authoritative.
  const state = evaluateEnvironmentConfirmation(environmentChallenge, typedByTarget, challenge.targetSetHash);

  const setTyped = (key: string, value: string) => {
    setTypedByTarget((current) => ({ ...current, [key]: value }));
  };

  const onModalKeyDown = (event: ReactKeyboardEvent<HTMLDivElement>) => {
    event.stopPropagation();
    if (layer.isTopmost()) trapOverlayTab(event, modalRef.current);
  };

  return createPortal(
    <div className="nx-overlay" onClick={onCancel}>
      <div
        ref={modalRef}
        className="nx-modal nx-modal-md"
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
              ? "服务端资产元数据将以下目标标记为生产环境。请逐目标输入实际环境名完成确认；取消不会发送待确认输入。"
              : "服务端资产元数据将以下目标标记为生产环境。请逐目标输入实际环境名完成确认；取消不会执行任何命令。"}
          </p>
          {hasManagedDevice && (
            <p role="note" className="mb-3 text-[11px] leading-relaxed text-neutral-300">
              {MANAGED_DEVICE_ENVIRONMENT_HINT}
            </p>
          )}
          {missingEnvironmentNames && (
            <div role="alert" className="mb-3 rounded border border-red-900/60 bg-red-950/20 px-2.5 py-2 text-[11px] text-red-300">
              生产确认响应缺少必需的环境名，无法确认；请取消操作。
            </div>
          )}
          <ul className="mb-3 flex max-h-44 flex-col gap-1.5 overflow-y-auto" aria-label="生产目标列表">
            {state.targets.map(({ target, typed, missing, matches }) => (
              <li
                key={targetKey(target)}
                className="rounded border border-amber-900/50 bg-amber-950/10 px-2.5 py-2"
                data-session-id={target.sessionId}
              >
                <div className="flex min-w-0 items-center gap-2">
                  <span className="min-w-0 flex-1 truncate font-mono text-[12px] text-neutral-100" title={target.hostname || target.sessionName}>
                    {target.hostname || target.sessionName}
                  </span>
                  <span className="nx-badge nx-badge-amber shrink-0">{environmentDisplayLabel(target.environment)}</span>
                </div>
                {target.hostname && target.sessionName !== target.hostname && (
                  <div className="mt-1 truncate text-[11px] text-neutral-400">会话：{target.sessionName}</div>
                )}
                <label className="mt-1.5 flex items-center gap-2 text-[11px] text-neutral-400">
                  <span className="shrink-0">输入环境名</span>
                  <input
                    className="nx-input font-mono text-xs"
                    aria-label={`输入环境名 ${target.hostname || target.sessionName}`}
                    autoComplete="off"
                    value={typed}
                    onChange={(event) => setTyped(targetKey(target), event.target.value)}
                  />
                  {!missing && !matches && <span className="shrink-0 text-red-300">环境名不匹配</span>}
                </label>
              </li>
            ))}
          </ul>
          {state.extraInputs.length > 0 && (
            <div role="alert" className="mb-3 rounded border border-red-900/60 bg-red-950/20 px-2.5 py-2 text-[11px] text-red-300">
              存在不属于当前目标集合的输入，确认已锁定：
              {state.extraInputs.map((extra) => `「${extra.key}」`).join("、")}
              <button
                type="button"
                className="nx-btn nx-btn-ghost nx-btn-xs ml-2"
                aria-label="清除过期输入"
                onClick={() => setTypedByTarget({})}
              >
                清除过期输入
              </button>
            </div>
          )}
          <div className="mb-1 text-[11px] text-neutral-500">{variant === "broadcast" ? "待广播输入" : "待执行命令"}</div>
          <pre className="nx-pre mb-3 max-h-28 overflow-auto whitespace-pre-wrap break-all text-[11px]">{command}</pre>
          <div className="mb-3 text-[10px] text-neutral-500" title={`目标集合 ${challenge.targetSetHash}；${contentLabel} ${contentHash}`}>
            确认将绑定当前目标集合与{contentLabel}哈希；任一变化都需要重新确认。本会话不会记住授权。
          </div>
        </div>
        <div className="nx-modal-footer">
          <button ref={cancelButtonRef} type="button" className="nx-btn nx-btn-ghost" onClick={onCancel}>
            取消
          </button>
          <button
            type="button"
            className="nx-btn nx-btn-danger"
            disabled={missingEnvironmentNames || !state.canConfirm}
            onClick={() => onConfirm(state.submittedNames)}
          >
            确认{action}（{challenge.productionTargets.length}）
          </button>
        </div>
      </div>
    </div>,
    document.body,
  );
}
