import { useId, type KeyboardEvent } from "react";

export type HelperCompatibility = "compatible" | "incompatible" | "unknown";

export interface HelperVersionPresentation {
  known: boolean;
  release?: string | null;
  commit?: string | null;
  protocol?: string | number | null;
  sha256?: string | null;
}

export interface HelperOfflineTrustPresentation {
  fileName?: string | null;
  sha256?: string | null;
  protocol?: string | number | null;
  compatibility?: HelperCompatibility;
}

export interface HelperUpdatePresentation {
  status: "available" | "deferred" | "interrupted" | "updated" | "rolled_back" | "unknown";
  detail?: string | null;
  updated?: boolean;
  interrupted?: boolean;
  rolledBack?: boolean;
}

export interface HelperOperationViewProps {
  stage: string;
  reason?: string | null;
  attempt?: number | null;
  maxAttempts?: number | null;
  deadline?: string | null;
  operationId?: string | null;
  path?: string | null;
  transport?: string | null;
  localVersion?: HelperVersionPresentation | null;
  runningVersion?: HelperVersionPresentation | null;
  compatibility?: HelperCompatibility;
  restoreFailure?: boolean;
  quickConnect?: boolean;
  rememberRegular?: boolean;
  controlsDisabled?: boolean;
  offlineTrust?: HelperOfflineTrustPresentation | null;
  update?: HelperUpdatePresentation | null;
  onRememberRegularChange?: (remember: boolean) => void;
  onRetryPersistent?: () => void;
  onContinueOrdinary?: () => void;
  onCancel?: () => void;
  onConfirmOfflineTrust?: () => void;
  onUpdate?: () => void;
  onDeferUpdate?: () => void;
  onInterruptAndUpdate?: () => void;
}

const STAGE_LABELS = new Map<string, string>([
  ["discover", "检测远端环境"],
  ["probe", "查找已有持久终端助手"],
  ["cache", "检查助手缓存"],
  ["upload", "上传持久终端助手"],
  ["verify", "校验持久终端助手"],
  ["start", "启动持久终端助手"],
  ["ready", "持久终端助手已就绪"],
  ["failed", "持久终端助手操作失败"],
  ["update", "更新持久终端助手"],
  ["rollback", "回滚持久终端助手"],
]);

function stageText(stage: string): string {
  const value = stage.trim();
  return STAGE_LABELS.get(value) ?? (value || "未知");
}

function textOrUnknown(value: string | null | undefined): string {
  return value?.trim() || "未知";
}

function metadataText(value: string | number | null | undefined): string {
  if (typeof value === "number") return Number.isFinite(value) ? String(value) : "未知";
  return textOrUnknown(value);
}

function attemptText(attempt: number | null | undefined, maxAttempts: number | null | undefined): string {
  if (attempt === null || attempt === undefined || !Number.isInteger(attempt) || attempt <= 0) {
    return "未知";
  }
  if (maxAttempts !== null && maxAttempts !== undefined && Number.isInteger(maxAttempts) && maxAttempts > 0) {
    return `${attempt}/${maxAttempts}`;
  }
  return String(attempt);
}

function compatibilityText(compatibility: HelperCompatibility): string {
  switch (compatibility) {
    case "compatible":
      return "协议兼容；发布版本不同不妨碍继续。";
    case "incompatible":
      return "协议不兼容；不会替换正在运行的助手。";
    case "unknown":
      return "兼容性未知，不能推断可安全继续。";
  }
}

function activateFromKeyboard(event: KeyboardEvent<HTMLButtonElement | HTMLInputElement>): void {
  const isButton = event.currentTarget.tagName === "BUTTON";
  const isActivation = event.key === " " || (isButton && event.key === "Enter");
  if (
    !isActivation ||
    event.repeat ||
    event.nativeEvent.isComposing ||
    event.altKey ||
    event.ctrlKey ||
    event.metaKey ||
    event.currentTarget.disabled
  ) {
    return;
  }
  event.preventDefault();
  event.currentTarget.click();
}

function Definition({ term, value }: { term: string; value: string }) {
  return (
    <div className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-1">
      <dt className="text-neutral-500">{term}</dt>
      <dd className="min-w-0 break-words text-neutral-300">{value}</dd>
    </div>
  );
}

function VersionSection({
  label,
  version,
}: {
  label: string;
  version: HelperVersionPresentation | null | undefined;
}) {
  const known = version?.known === true;
  return (
    <section aria-label={label} className="min-w-0 rounded border border-neutral-800/70 p-2">
      <h3 className="font-medium text-neutral-200">{label}</h3>
      {!known && <p className="mt-1 text-neutral-500">元数据未知（未提供或无法验证）。</p>}
      <dl className="mt-2 flex flex-col gap-1">
        <Definition term="发布版本" value={known ? metadataText(version.release) : "未知"} />
        <Definition term="提交" value={known ? metadataText(version.commit) : "未知"} />
        <Definition term="协议" value={known ? metadataText(version.protocol) : "未知"} />
        <Definition term="SHA-256" value={known ? metadataText(version.sha256) : "未知"} />
      </dl>
    </section>
  );
}

export function HelperOperationView({
  stage,
  reason,
  attempt,
  maxAttempts,
  deadline,
  operationId,
  path,
  transport,
  localVersion,
  runningVersion,
  compatibility,
  restoreFailure = false,
  quickConnect = false,
  rememberRegular,
  controlsDisabled = false,
  offlineTrust,
  update,
  onRememberRegularChange,
  onRetryPersistent,
  onContinueOrdinary,
  onCancel,
  onConfirmOfflineTrust,
  onUpdate,
  onDeferUpdate,
  onInterruptAndUpdate,
}: HelperOperationViewProps) {
  const id = useId();
  const titleId = `${id}-title`;
  const retryNoteId = `${id}-retry-note`;
  const ordinaryWarningId = `${id}-ordinary-warning`;
  const cancelNoteId = `${id}-cancel-note`;
  const rememberNoteId = `${id}-remember-note`;
  const offlineTrustNoteId = `${id}-offline-trust-note`;
  const updateNoteId = `${id}-update-note`;
  const failed = stage === "failed" || restoreFailure;
  const showVersions =
    localVersion !== undefined || runningVersion !== undefined || compatibility !== undefined;
  const showRemember =
    !quickConnect && (rememberRegular !== undefined || onRememberRegularChange !== undefined);
  const showControls =
    showRemember || onRetryPersistent !== undefined || onContinueOrdinary !== undefined || onCancel !== undefined;
  const offlineCompatibility = offlineTrust?.compatibility ?? "unknown";
  const canConfirmOfflineTrust =
    offlineTrust !== null &&
    offlineTrust !== undefined &&
    /^[0-9a-f]{64}$/i.test(offlineTrust.sha256?.trim() ?? "") &&
    metadataText(offlineTrust.protocol) !== "未知" &&
    offlineCompatibility === "compatible";

  return (
    <section
      aria-labelledby={titleId}
      className="flex flex-col gap-3 border-b border-neutral-800/60 bg-neutral-950/20 px-3 py-2 text-xs text-neutral-300"
    >
      <h2 id={titleId} className="font-medium text-neutral-100">
        持久终端助手
      </h2>

      <div
        role={failed ? "alert" : "status"}
        aria-live={failed ? "assertive" : "polite"}
        aria-atomic="true"
        className={failed ? "text-red-300" : "text-neutral-300"}
      >
        <dl aria-label="后台操作状态" className="grid gap-1 sm:grid-cols-2">
          <Definition term="阶段" value={stageText(stage)} />
          <Definition term="原因" value={textOrUnknown(reason)} />
          <Definition term="尝试次数" value={attemptText(attempt, maxAttempts)} />
          <Definition term="截止时间" value={textOrUnknown(deadline)} />
          {operationId !== undefined && <Definition term="操作 ID" value={textOrUnknown(operationId)} />}
          {path !== undefined && <Definition term="路径" value={textOrUnknown(path)} />}
          {transport !== undefined && <Definition term="传输" value={textOrUnknown(transport)} />}
        </dl>
      </div>

      {showVersions && (
        <div className="flex flex-col gap-2">
          <div className="grid gap-2 md:grid-cols-2">
            <VersionSection label="本地携带版本" version={localVersion} />
            <VersionSection label="远端运行版本" version={runningVersion} />
          </div>
          <p className="text-neutral-400">
            <span className="font-medium">兼容性：</span>
            {compatibilityText(compatibility ?? "unknown")}
          </p>
        </div>
      )}

      {showControls && (
        <div className="flex flex-col gap-2 border-t border-neutral-800/60 pt-2">
          {onRetryPersistent && (
            <p id={retryNoteId} className="text-neutral-500">
              重试会使用新的部署时间预算；操作结果仍以上报状态为准。
            </p>
          )}
          {onContinueOrdinary && (
            <p id={ordinaryWarningId} className="text-[var(--nx-fg-warning)]">
              {restoreFailure
                ? "原持久会话恢复失败。此操作会新建普通终端和新的 shell，不会恢复原会话；原远端进程状态未知，不能视为已终止。"
                : "普通终端在断开后无法从后台会话接管。已有持久会话仍可单独查看和接管。"}
            </p>
          )}
          {onCancel && (
            <p id={cancelNoteId} className="text-neutral-500">
              取消仅请求停止本次后台操作，不代表远端进程已终止或远端会话已清理。
            </p>
          )}
          <div className="flex flex-wrap gap-2">
            {onRetryPersistent && (
              <button
                type="button"
                className="nx-btn nx-btn-ghost nx-btn-sm"
                disabled={controlsDisabled}
                aria-describedby={retryNoteId}
                onClick={onRetryPersistent}
                onKeyDown={activateFromKeyboard}
              >
                重试持久终端
              </button>
            )}
            {onContinueOrdinary && (
              <button
                type="button"
                className="nx-btn nx-btn-ghost nx-btn-sm"
                disabled={controlsDisabled}
                aria-describedby={ordinaryWarningId}
                onClick={onContinueOrdinary}
                onKeyDown={activateFromKeyboard}
              >
                {restoreFailure ? "新建普通终端" : "继续使用普通终端"}
              </button>
            )}
            {onCancel && (
              <button
                type="button"
                className="nx-btn nx-btn-ghost nx-btn-sm"
                disabled={controlsDisabled}
                aria-describedby={cancelNoteId}
                onClick={onCancel}
                onKeyDown={activateFromKeyboard}
              >
                取消后台操作
              </button>
            )}
          </div>
          {showRemember && (
            <div>
              <label className="flex items-center gap-2 text-neutral-300">
                <input
                  type="checkbox"
                  checked={rememberRegular ?? false}
                  disabled={controlsDisabled || !onRememberRegularChange}
                  aria-describedby={rememberNoteId}
                  onChange={(event) => onRememberRegularChange?.(event.currentTarget.checked)}
                  onKeyDown={activateFromKeyboard}
                />
                <span>记住此资产以后使用普通终端</span>
              </label>
              <p id={rememberNoteId} className="mt-1 text-neutral-500">
                仅影响此资产以后新开的终端；当前终端不变，已有持久会话仍可查看和接管。
              </p>
            </div>
          )}
        </div>
      )}

      {offlineTrust && (
        <section
          aria-label="离线 helper 信任"
          className="flex flex-col gap-2 rounded border border-neutral-800/70 p-2"
        >
          <h3 className="font-medium text-neutral-200">离线 helper 信任</h3>
          <p id={offlineTrustNoteId} className="text-[var(--nx-fg-warning)]">
            只有核对 SHA-256 且协议兼容后才能信任；发布版本不同本身不代表不兼容。
          </p>
          <dl className="flex flex-col gap-1">
            <Definition term="文件" value={textOrUnknown(offlineTrust.fileName)} />
            <Definition term="SHA-256" value={metadataText(offlineTrust.sha256)} />
            <Definition term="协议" value={metadataText(offlineTrust.protocol)} />
          </dl>
          <p className="text-neutral-400">{compatibilityText(offlineCompatibility)}</p>
          {onConfirmOfflineTrust && (
            <div>
              <button
                type="button"
                className="nx-btn nx-btn-ghost nx-btn-sm"
                disabled={controlsDisabled || !canConfirmOfflineTrust}
                aria-describedby={offlineTrustNoteId}
                onClick={onConfirmOfflineTrust}
                onKeyDown={activateFromKeyboard}
              >
                确认 SHA-256 并信任离线文件
              </button>
              {!canConfirmOfflineTrust && (
                <p className="mt-1 text-neutral-500">SHA-256 或协议信息不足，不能确认信任。</p>
              )}
            </div>
          )}
        </section>
      )}

      {update && (
        <section
          aria-label="助手更新"
          className="flex flex-col gap-2 rounded border border-neutral-800/70 p-2"
        >
          <h3 className="font-medium text-neutral-200">助手更新</h3>
          <div role="status" aria-live="polite" aria-atomic="true">
            {update.status === "available" && <p>可以检查并执行助手更新。</p>}
            {update.status === "deferred" && <p>更新已延后：不会主动中断活动 PTY 或其他 exec。</p>}
            {update.status === "interrupted" && <p>已请求中断并更新。原有进程状态以实际结果为准。</p>}
            {update.status === "updated" && !update.updated && <p>助手更新已完成；原有进程不会因更新或回滚而复活。</p>}
            {update.status === "rolled_back" && <p>更新失败，已返回回滚结果。</p>}
            {update.status === "unknown" && <p>更新结果未知，不能推断已更新、已延后或已中断。</p>}
            {update.interrupted && <p>已明确中断活动进程。</p>}
            {update.updated && <p>助手更新已完成；原有进程不会因更新或回滚而复活。</p>}
            {update.rolledBack && <p>已回滚助手二进制；原 shell 和其他进程不会复活。</p>}
            {update.detail && <p className="mt-1 text-neutral-400">{update.detail}</p>}
            <p id={updateNoteId} className="mt-1 text-[var(--nx-fg-warning)]">
              活动进程存在时默认稍后更新；只有明确选择后才能中断。更新或回滚都不会复活原 shell 或其他进程。
            </p>
          </div>
          <div className="flex flex-wrap gap-2">
            {update.status === "available" && onUpdate && (
              <button
                type="button"
                className="nx-btn nx-btn-ghost nx-btn-sm"
                disabled={controlsDisabled}
                onClick={onUpdate}
                onKeyDown={activateFromKeyboard}
              >
                更新助手
              </button>
            )}
            {update.status === "available" && onDeferUpdate && (
              <button
                type="button"
                className="nx-btn nx-btn-ghost nx-btn-sm"
                disabled={controlsDisabled}
                onClick={onDeferUpdate}
                onKeyDown={activateFromKeyboard}
              >
                稍后更新
              </button>
            )}
            {(update.status === "available" || update.status === "deferred") && onInterruptAndUpdate && (
              <button
                type="button"
                className="nx-btn nx-btn-ghost nx-btn-sm"
                disabled={controlsDisabled}
                aria-describedby={updateNoteId}
                onClick={onInterruptAndUpdate}
                onKeyDown={activateFromKeyboard}
              >
                中断活动进程并更新
              </button>
            )}
          </div>
        </section>
      )}
    </section>
  );
}
