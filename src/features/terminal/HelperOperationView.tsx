import { useEffect, useId, useRef, useState, type KeyboardEvent } from "react";
import { copyText } from "../../ui/clipboard";
import { IconLoader } from "../../ui/icons";

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
  status: "current" | "available" | "deferred" | "interrupted" | "updated" | "rolled_back" | "legacy" | "replaced" | "unknown";
  detail?: string | null;
  updated?: boolean;
  interrupted?: boolean;
  rolledBack?: boolean;
  endedSessions?: number;
  endedCount?: "exact" | "atLeast" | "unknown";
  forced?: boolean;
}

/** A consented replacement that failed: where, and what had already happened to the older helper. */
export interface HelperReplaceFailurePresentation {
  stage: string;
  stopped: boolean;
  forced: boolean;
  rolledBack: boolean;
  cause?: string;
}

const FORCED_STOP_NOTE = "旧版组件未响应退出请求，已被强制结束，部分后台进程可能仍在运行。";

/** "N 个会话", "至少 N 个终端会话" or "全部会话", as exact as the old helper reported. */
export function endedSessionsText(count: number | undefined, kind: "exact" | "atLeast" | "unknown" | undefined): string {
  if (kind === "atLeast") return `至少 ${count ?? 0} 个终端会话`;
  if (kind === "unknown" || count === undefined) return "全部会话";
  return `${count} 个会话`;
}

function replaceFailureText(failure: HelperReplaceFailurePresentation): string {
  switch (failure.stage) {
    case "verify":
      return "无法核实主机上的旧版组件，未做任何更改。";
    case "terminate":
      return "未能结束旧版组件，它和其中的会话仍在运行；未启动新版持久终端组件。";
    case "start":
      if (failure.stopped && failure.rolledBack) return "旧版组件已结束（其中的会话已终止），但新版持久终端组件未能启动；已重新启动旧版组件，原会话不会恢复。";
      if (failure.stopped) return "旧版组件已结束（其中的会话已终止），但新版持久终端组件未能启动。";
      return "新版持久终端组件未能启动。";
    default:
      return "升级持久终端组件失败。";
  }
}

/** An older-protocol helper that blocks durable terminals until the user agrees to end it. */
export interface LegacyHelperPresentation {
  protocol: number;
  pid?: number;
  ptySessions: number;
  execSessions: number;
  activityKnown: boolean;
  ptyListed: boolean;
  release?: string;
  consentToken?: string;
  stale?: boolean;
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
  controlsDisabled?: boolean;
  busyLabel?: string;
  offlineTrust?: HelperOfflineTrustPresentation | null;
  update?: HelperUpdatePresentation | null;
  legacy?: LegacyHelperPresentation | null;
  replaceFailure?: HelperReplaceFailurePresentation | null;
  onRetryPersistent?: () => void;
  onContinueOrdinary?: () => void;
  onCancel?: () => void;
  onConfirmOfflineTrust?: () => void;
  onUpdate?: () => void;
  onDeferUpdate?: () => void;
  onInterruptAndUpdate?: () => void;
  /** Ends the legacy helper the user approved; only offered with a consent token. */
  onReplaceLegacy?: () => void;
}

const STAGE_LABELS = new Map<string, string>([
  ["discover", "检测远端环境"],
  ["probe", "查找已有持久终端组件"],
  ["cache", "检查持久终端组件缓存"],
  ["upload", "上传持久终端组件"],
  ["verify", "校验持久终端组件"],
  ["start", "启动持久终端组件"],
  ["ready", "持久终端已就绪"],
  ["backoff", "连接暂时失败，正在重试"],
  ["install_update", "准备持久终端组件更新"],
  ["deferred", "当前有进程运行，更新已延后"],
  ["switch", "切换持久终端组件"],
  ["updated", "持久终端组件已更新"],
  ["fallback", "正在使用普通终端"],
  ["failed", "持久终端组件操作失败"],
  ["update", "更新持久终端组件"],
  ["rollback", "回滚持久终端组件"],
]);

export function helperStageText(stage: string): string {
  const value = stage.trim();
  return STAGE_LABELS.get(value) ?? (value || "未知");
}

// Stages where the helper is up and nothing is pending: attempt counters and deadlines no longer mean anything.
const HEALTHY_STAGES = new Set(["ready", "updated"]);

function textOrUnknown(value: string | null | undefined): string {
  return value?.trim() || "未知";
}

function metadataText(value: string | number | null | undefined): string {
  if (typeof value === "number") return Number.isFinite(value) ? String(value) : "未知";
  return textOrUnknown(value);
}

function presentText(value: string | number | null | undefined): string | null {
  if (typeof value === "number") return Number.isFinite(value) ? String(value) : null;
  return value?.trim() || null;
}

function attemptText(attempt: number | null | undefined, maxAttempts: number | null | undefined): string | null {
  if (attempt === null || attempt === undefined || !Number.isInteger(attempt) || attempt <= 0) return null;
  if (maxAttempts !== null && maxAttempts !== undefined && Number.isInteger(maxAttempts) && maxAttempts > 0) {
    return `${attempt}/${maxAttempts}`;
  }
  return String(attempt);
}

// The offline trust check spells out every value, missing ones included: trusting a file needs all of them.
function offlineCompatibilityText(compatibility: HelperCompatibility): string {
  switch (compatibility) {
    case "compatible":
      return "协议兼容；发布版本不同不妨碍继续。";
    case "incompatible":
      return "协议不兼容；正在运行的组件不会被自动替换。";
    case "unknown":
      return "兼容性未知，不能推断可安全继续。";
  }
}

type VersionField = "release" | "commit" | "protocol" | "sha256";

const VERSION_FIELDS: readonly { key: VersionField; label: string }[] = [
  { key: "release", label: "发布版本" },
  { key: "commit", label: "提交" },
  { key: "protocol", label: "协议" },
  { key: "sha256", label: "SHA-256" },
];

function versionValue(version: HelperVersionPresentation | null | undefined, key: VersionField): string | null {
  return version?.known === true ? presentText(version[key]) : null;
}

function comparableValue(key: VersionField, value: string | null): string | null {
  if (value === null) return null;
  if (key === "release") return value.replace(/^v/i, "");
  if (key === "commit" || key === "sha256") return value.toLowerCase();
  return value;
}

function versionFieldDiffers(
  local: HelperVersionPresentation | null | undefined,
  running: HelperVersionPresentation | null | undefined,
  key: VersionField,
): boolean {
  return comparableValue(key, versionValue(local, key)) !== comparableValue(key, versionValue(running, key));
}

function versionsIdentical(
  local: HelperVersionPresentation | null | undefined,
  running: HelperVersionPresentation | null | undefined,
): boolean {
  if (local?.known !== true || running?.known !== true) return false;
  if (VERSION_FIELDS.every(({ key }) => versionValue(local, key) === null)) return false;
  return VERSION_FIELDS.every(({ key }) => !versionFieldDiffers(local, running, key));
}

function shortCommit(commit: string): string {
  return /^[0-9a-f]{12,}$/i.test(commit) ? commit.slice(0, 7) : commit;
}

function versionSummary(version: HelperVersionPresentation): string {
  const parts: string[] = [];
  const release = versionValue(version, "release");
  const commit = versionValue(version, "commit");
  const protocol = versionValue(version, "protocol");
  if (release) parts.push(`版本 ${release.replace(/^v/i, "")}`);
  if (commit) parts.push(`提交 ${shortCommit(commit)}`);
  if (protocol) parts.push(`协议 ${protocol}`);
  return parts.join(" · ") || "组件版本";
}

// An older helper that was inspected is replaced in the app after explicit consent (the 旧版组件 section). Any other
// incompatible helper reports no process or session count, so the only way past it is to stop it on the host.
const INCOMPATIBLE_REMEDY =
  "可以在主机上手动结束旧的持久终端组件进程（其中运行的终端会话会随之结束），然后重新连接，NexTerm 会安装新组件。";
const LEGACY_REMEDY = "可在下方「旧版组件」中查看其中的会话并确认升级。";

function versionCompatibilityText(
  compatibility: HelperCompatibility,
  local: HelperVersionPresentation | null | undefined,
  running: HelperVersionPresentation | null | undefined,
  update: HelperUpdatePresentation | null | undefined,
  failed: boolean,
  legacy: boolean,
): string {
  switch (compatibility) {
    case "compatible": {
      // A failed operation cannot be "continued": the protocol matching is all that can be said.
      const base = failed ? "协议兼容" : "协议兼容，可继续使用";
      const updateAvailable = update?.status === "available" || update?.status === "deferred";
      return updateAvailable ? `${base}；有可用更新。` : `${base}。`;
    }
    case "incompatible": {
      const localProtocol = versionValue(local, "protocol");
      const runningProtocol = versionValue(running, "protocol");
      const protocols = localProtocol && runningProtocol ? `（本地 ${localProtocol}，主机 ${runningProtocol}）` : "";
      return `协议不兼容${protocols}：NexTerm 无法使用主机上正在运行的持久终端组件，也不会自动替换它，持久终端暂不可用。${legacy ? LEGACY_REMEDY : INCOMPATIBLE_REMEDY}`;
    }
    case "unknown":
      return "无法确认兼容性，不能推断可安全继续。";
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

function legacySessionText(legacy: LegacyHelperPresentation): string {
  if (legacy.activityKnown) {
    const total = legacy.ptySessions + legacy.execSessions;
    if (total === 0) return "其中没有正在运行的会话。";
    return `其中有 ${total} 个会话在运行（终端 ${legacy.ptySessions} 个，后台命令 ${legacy.execSessions} 个），包括其他设备或客户端创建的会话。`;
  }
  if (legacy.ptyListed) {
    return `其中至少有 ${legacy.ptySessions} 个终端会话在运行（包括其他设备或客户端创建的会话）；后台命令数未知。`;
  }
  return "无法确认其中有多少会话在运行。";
}

function legacyConsentLabel(legacy: LegacyHelperPresentation): string {
  if (legacy.activityKnown) {
    const total = legacy.ptySessions + legacy.execSessions;
    return total === 0 ? "我已了解：将终止旧版组件" : `我已了解：将终止这 ${total} 个会话，且无法恢复`;
  }
  if (legacy.ptyListed) return `我已了解：将终止旧版组件中的全部会话（至少 ${legacy.ptySessions} 个终端会话），且无法恢复`;
  return "我已了解：将终止旧版组件中的全部会话，且无法恢复";
}

function LegacySection({
  legacy,
  controlsDisabled,
  onReplaceLegacy,
}: {
  legacy: LegacyHelperPresentation;
  controlsDisabled: boolean;
  onReplaceLegacy?: () => void;
}) {
  const id = useId();
  const noteId = `${id}-legacy-note`;
  const [agreed, setAgreed] = useState(false);
  const identity = [
    `协议 v${legacy.protocol}`,
    legacy.release ? `版本 ${legacy.release}` : null,
    legacy.pid ? `进程 ${legacy.pid}` : null,
  ].filter(Boolean).join("，");
  const canReplace = Boolean(legacy.consentToken) && onReplaceLegacy !== undefined;
  return (
    <section aria-label="旧版组件" className="flex flex-col gap-2 rounded border border-[var(--nx-fg-warning)]/40 p-2">
      <h3 className="font-medium text-neutral-200">旧版组件</h3>
      <p>
        主机上仍在运行旧版持久终端组件（{identity}），当前版本无法接入其中的终端；不升级时它们继续在旧版组件中运行。
      </p>
      <p>{legacySessionText(legacy)}</p>
      {legacy.stale && <p role="alert" className="text-[var(--nx-fg-warning)]">旧版组件的状态已变化，请核对上面最新的会话数后重新确认。</p>}
      {legacy.consentToken ? (
        <p id={noteId} className="text-[var(--nx-fg-warning)]">
          升级会先请求旧版组件退出，超时后强制结束，再启动新版组件。上述会话会被终止且无法恢复；它们的输出记录保留为只读。
        </p>
      ) : (
        <p className="text-neutral-500">无法核实旧版组件的进程归属或会话数，不能在应用内升级。</p>
      )}
      {canReplace && (
        <div className="flex flex-col gap-2">
          <label className="flex items-center gap-2">
            <input
              type="checkbox"
              checked={agreed}
              disabled={controlsDisabled}
              aria-describedby={noteId}
              onChange={(event) => setAgreed(event.currentTarget.checked)}
              onKeyDown={activateFromKeyboard}
            />
            {legacyConsentLabel(legacy)}
          </label>
          <div>
            <button
              type="button"
              className="nx-btn nx-btn-danger nx-btn-sm"
              disabled={controlsDisabled || !agreed}
              aria-describedby={noteId}
              onClick={onReplaceLegacy}
              onKeyDown={activateFromKeyboard}
            >
              终止旧版组件并升级
            </button>
          </div>
        </div>
      )}
    </section>
  );
}

function Definition({ term, value }: { term: string; value: string }) {
  return (
    <div className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-1">
      <dt className="text-neutral-500">{term}</dt>
      <dd className="min-w-0 break-words text-neutral-300">{value}</dd>
    </div>
  );
}

// CopyButton copies one identifier; the outcome is shown on the button and announced through a live region.
// The copy outcome is cleared again after this long, and at the start of every copy, so a repeated copy of the same
// value changes the live region again and is announced again.
const COPY_STATUS_RESET_MS = 2_000;

function CopyButton({ name, value }: { name: string; value: string }) {
  const [state, setState] = useState<"idle" | "copied" | "failed">("idle");
  const resetTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  useEffect(() => () => {
    if (resetTimer.current !== null) clearTimeout(resetTimer.current);
  }, []);
  const show = (next: "copied" | "failed") => {
    setState(next);
    if (resetTimer.current !== null) clearTimeout(resetTimer.current);
    resetTimer.current = setTimeout(() => {
      resetTimer.current = null;
      setState("idle");
    }, COPY_STATUS_RESET_MS);
  };
  return (
    <>
      <button
        type="button"
        className="nx-btn nx-btn-ghost nx-btn-xs shrink-0"
        aria-label={`复制${name}`}
        onClick={() => {
          setState("idle");
          copyText(value).then(() => show("copied"), () => show("failed"));
        }}
      >
        {state === "copied" ? "已复制" : state === "failed" ? "复制失败" : "复制"}
      </button>
      <span role="status" aria-live="polite" className="nx-sr-only">
        {state === "copied" ? `已复制${name}` : state === "failed" ? `复制${name}失败` : ""}
      </span>
    </>
  );
}

function CopyValue({ term, value }: { term: string; value: string }) {
  return (
    <div className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-1">
      <dt className="text-neutral-500">{term}</dt>
      <dd className="flex min-w-0 items-start gap-2 text-neutral-300">
        <span data-value className="min-w-0 break-all font-mono">{value}</span>
        <CopyButton name={term} value={value} />
      </dd>
    </div>
  );
}

// IdenticalVersion states a local bundle and a running helper that match once; the full commit and digest
// stay one click away for anyone comparing them by hand.
function IdenticalVersion({ version }: { version: HelperVersionPresentation }) {
  const commit = versionValue(version, "commit");
  const sha256 = versionValue(version, "sha256");
  return (
    <section aria-label="组件版本" className="flex flex-col gap-1">
      <p className="text-neutral-300">{versionSummary(version)}（本地与主机一致）</p>
      {(commit || sha256) && (
        <details>
          <summary tabIndex={0} className="cursor-pointer text-[11px] text-neutral-500">完整提交与 SHA-256</summary>
          <dl className="mt-1 flex flex-col gap-1">
            {commit && <CopyValue term="提交" value={commit} />}
            {sha256 && <CopyValue term="SHA-256" value={sha256} />}
          </dl>
        </details>
      )}
    </section>
  );
}

function VersionSection({
  label,
  version,
  other,
}: {
  label: string;
  version: HelperVersionPresentation | null | undefined;
  other: HelperVersionPresentation | null | undefined;
}) {
  if (version?.known !== true) {
    return (
      <section aria-label={label} className="min-w-0 rounded border border-neutral-800/70 p-2">
        <h3 className="font-medium text-neutral-200">{label}</h3>
        <p className="mt-1 text-neutral-500">无法确认（未提供或无法验证）。</p>
      </section>
    );
  }
  const compare = other?.known === true;
  const rows = VERSION_FIELDS.filter(
    ({ key }) => versionValue(version, key) !== null || (compare && versionValue(other, key) !== null),
  );
  return (
    <section aria-label={label} className="min-w-0 rounded border border-neutral-800/70 p-2">
      <h3 className="font-medium text-neutral-200">{label}</h3>
      <dl className="mt-2 flex flex-col gap-1">
        {rows.map(({ key, label: term }) => {
          const differs = compare && versionFieldDiffers(version, other, key);
          const value = versionValue(version, key);
          const copyable = value !== null && (key === "commit" || key === "sha256");
          return (
            <div
              key={key}
              className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-1"
              data-differs={differs ? "true" : undefined}
            >
              <dt className="text-neutral-500">{term}</dt>
              <dd className={`flex min-w-0 flex-wrap items-start gap-x-1.5 gap-y-1 ${differs ? "font-medium text-[var(--nx-fg-warning)]" : "text-neutral-300"}`}>
                <span data-value className={copyable ? "min-w-0 break-all font-mono" : "min-w-0 break-words"}>{value ?? "未提供"}</span>
                {differs && <span className="nx-badge nx-badge-amber">不同</span>}
                {copyable && <CopyButton name={`${label}的${term}`} value={value} />}
              </dd>
            </div>
          );
        })}
      </dl>
    </section>
  );
}

function VersionComparison({
  local,
  running,
  compatibility,
  update,
  failed,
  legacy,
}: {
  local: HelperVersionPresentation | null | undefined;
  running: HelperVersionPresentation | null | undefined;
  compatibility: HelperCompatibility | undefined;
  update: HelperUpdatePresentation | null | undefined;
  failed: boolean;
  legacy: boolean;
}) {
  if (local?.known === true && versionsIdentical(local, running)) return <IdenticalVersion version={local} />;
  const anyKnown = local?.known === true || running?.known === true;
  return (
    <div className="flex flex-col gap-2">
      {anyKnown ? (
        <div className="grid gap-2 md:grid-cols-2">
          <VersionSection label="本地携带版本" version={local} other={running} />
          <VersionSection label="远端运行版本" version={running} other={local} />
        </div>
      ) : (
        <p className="text-neutral-500">本地与主机上的组件版本均无法确认。</p>
      )}
      <p className="text-neutral-400">
        <span className="font-medium">兼容性：</span>
        {versionCompatibilityText(compatibility ?? "unknown", local, running, update, failed, legacy)}
      </p>
    </div>
  );
}

// On a healthy stage a plain "already current" result adds nothing beyond the status line, which then says so itself.
function updateIsPlainCurrent(update: HelperUpdatePresentation): boolean {
  return update.status === "current" && !update.detail?.trim() && !update.updated && !update.interrupted && !update.rolledBack;
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
  controlsDisabled = false,
  busyLabel,
  offlineTrust,
  update,
  legacy,
  replaceFailure,
  onRetryPersistent,
  onContinueOrdinary,
  onCancel,
  onConfirmOfflineTrust,
  onUpdate,
  onDeferUpdate,
  onInterruptAndUpdate,
  onReplaceLegacy,
}: HelperOperationViewProps) {
  const id = useId();
  const titleId = `${id}-title`;
  const retryNoteId = `${id}-retry-note`;
  const ordinaryWarningId = `${id}-ordinary-warning`;
  const cancelNoteId = `${id}-cancel-note`;
  const offlineTrustNoteId = `${id}-offline-trust-note`;
  const updateNoteId = `${id}-update-note`;
  const failed = stage === "failed" || restoreFailure;
  const healthy = !failed && HEALTHY_STAGES.has(stage.trim());
  const showVersions =
    localVersion !== undefined || runningVersion !== undefined || compatibility !== undefined;
  const showControls =
    onRetryPersistent !== undefined || onContinueOrdinary !== undefined || onCancel !== undefined;
  const offlineCompatibility = offlineTrust?.compatibility ?? "unknown";
  const canConfirmOfflineTrust =
    offlineTrust !== null &&
    offlineTrust !== undefined &&
    /^[0-9a-f]{64}$/i.test(offlineTrust.sha256?.trim() ?? "") &&
    metadataText(offlineTrust.protocol) !== "未知" &&
    offlineCompatibility === "compatible";
  const plainCurrent = healthy && update ? updateIsPlainCurrent(update) : false;
  const statusText = failed
    ? "持久终端暂时不可用"
    : `${helperStageText(stage)}${plainCurrent ? "，组件已是最新版本" : ""}`;

  // Diagnostics list only what applies to this stage: no placeholders for values that do not exist yet or any more.
  const diagnostics: { term: string; value: string }[] = [];
  // The status line already names a non-failed stage; a failure replaces it, so the stage that failed is listed.
  if (failed || busyLabel) diagnostics.push({ term: "阶段", value: helperStageText(stage) });
  const reasonText = presentText(reason);
  if (reasonText) diagnostics.push({ term: "原因", value: reasonText });
  const attempts = attemptText(attempt, maxAttempts);
  if (attempts && !healthy && (failed || stage === "backoff" || (attempt ?? 0) > 1)) {
    diagnostics.push({ term: "尝试次数", value: attempts });
  }
  const deadlineText = presentText(deadline);
  if (deadlineText && !healthy && stage !== "fallback") diagnostics.push({ term: "截止时间", value: deadlineText });
  const operationText = presentText(operationId);
  if (operationText) diagnostics.push({ term: "操作 ID", value: operationText });
  const pathText = presentText(path);
  if (pathText) diagnostics.push({ term: "路径", value: pathText });
  const transportText = presentText(transport);
  if (transportText) diagnostics.push({ term: "传输", value: transportText });
  const showDiagnostics = diagnostics.length > 0 || showVersions;

  return (
    <section
      aria-labelledby={titleId}
      className="flex flex-col gap-3 px-4 py-3 text-xs text-neutral-300"
    >
      <h2 id={titleId} className="font-medium text-neutral-100">
        持久终端组件
      </h2>

      {busyLabel && <p role="status" className="flex items-center gap-2 text-neutral-300"><IconLoader size={13} className="animate-spin" />{busyLabel}</p>}
      {!busyLabel && <p className={failed ? "text-red-300" : "text-neutral-300"} role={failed ? "alert" : "status"}>
        {statusText}
      </p>}
      {showDiagnostics && (
        // Healthy and in-progress stages keep the details folded; a failure opens them so the cause is visible.
        <details className="text-neutral-400" open={failed}>
          <summary tabIndex={0} className="cursor-pointer text-[11px]">诊断详情</summary>
          <div className="mt-1 flex flex-col gap-2">
            {diagnostics.length > 0 && (
              <dl aria-label="后台操作状态" className={`grid gap-1 sm:grid-cols-2 ${failed ? "text-red-300" : "text-neutral-300"}`}>
                {diagnostics.map((item) => <Definition key={item.term} term={item.term} value={item.value} />)}
              </dl>
            )}
            {showVersions && (
              <VersionComparison
                local={localVersion}
                running={runningVersion}
                compatibility={compatibility}
                update={update}
                failed={failed}
                legacy={Boolean(legacy)}
              />
            )}
          </div>
        </details>
      )}

      {showControls && (
        <div className="flex flex-col gap-2 border-t border-neutral-800/60 pt-2">
          {onRetryPersistent && (
            <p id={retryNoteId} className="text-neutral-500">
              可以重新尝试安装持久终端。
            </p>
          )}
          {onContinueOrdinary && (
            <p id={ordinaryWarningId} className="text-[var(--nx-fg-warning)]">
              {restoreFailure
                ? "将打开一个新的普通终端；原会话不会被恢复。"
                : "普通终端在断开连接后结束，无法转到后台。"}
            </p>
          )}
          {onCancel && (
            <p id={cancelNoteId} className="text-neutral-500">
              停止本次安装，保留当前连接。
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
        </div>
      )}

      {replaceFailure && (
        <section aria-label="组件升级失败" className="flex flex-col gap-1 rounded border border-red-500/40 p-2">
          <h3 className="font-medium text-neutral-200">组件升级失败</h3>
          <p role="alert" className="text-red-300">{replaceFailureText(replaceFailure)}</p>
          {replaceFailure.forced && <p className="text-[var(--nx-fg-warning)]">{FORCED_STOP_NOTE}</p>}
          {replaceFailure.cause && <p className="break-words text-neutral-500">{replaceFailure.cause}</p>}
          <p className="text-neutral-500">不会自动重试；如需再次升级，请重新检查组件状态并确认。</p>
        </section>
      )}

      {legacy && (
        <LegacySection
          key={legacy.consentToken ?? "uninspected"}
          legacy={legacy}
          controlsDisabled={controlsDisabled}
          onReplaceLegacy={onReplaceLegacy}
        />
      )}

      {offlineTrust && (
        <section
          aria-label="信任离线组件文件"
          className="flex flex-col gap-2 rounded border border-neutral-800/70 p-2"
        >
          <h3 className="font-medium text-neutral-200">信任离线组件文件</h3>
          <p id={offlineTrustNoteId} className="text-[var(--nx-fg-warning)]">
            只有核对 SHA-256 且协议兼容后才能信任；发布版本不同本身不代表不兼容。
          </p>
          <dl className="flex flex-col gap-1">
            <Definition term="文件" value={textOrUnknown(offlineTrust.fileName)} />
            <Definition term="SHA-256" value={metadataText(offlineTrust.sha256)} />
            <Definition term="协议" value={metadataText(offlineTrust.protocol)} />
          </dl>
          <p className="text-neutral-400">{offlineCompatibilityText(offlineCompatibility)}</p>
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

      {update && !plainCurrent && (
        <section
          aria-label="组件更新"
          className="flex flex-col gap-2 rounded border border-neutral-800/70 p-2"
        >
          <h3 className="font-medium text-neutral-200">组件更新</h3>
          <div role="status" aria-live="polite" aria-atomic="true">
            {update.status === "current" && <p>持久终端组件已是最新版本。</p>}
            {update.status === "available" && <p>持久终端组件有可用更新。</p>}
            {update.status === "deferred" && <p>当前仍有进程运行，更新已延后。</p>}
            {update.status === "interrupted" && <p>{update.updated ? "已中断运行中的进程并完成更新。" : "活动进程已中断，尚未确认更新完成。"}</p>}
            {update.status === "updated" && !update.updated && <p>持久终端组件已更新。</p>}
            {update.status === "rolled_back" && <p>更新失败，已恢复为原版本。</p>}
            {update.status === "replaced" && (
              <p>
                已终止旧版组件并启用新版持久终端组件，结束了{endedSessionsText(update.endedSessions, update.endedCount)}。
              </p>
            )}
            {update.status === "replaced" && update.forced && <p className="text-[var(--nx-fg-warning)]">{FORCED_STOP_NOTE}</p>}
            {update.status === "unknown" && (
              <p>
                {compatibility === "incompatible"
                  ? `主机上运行的持久终端组件协议不兼容，NexTerm 不会自动替换它。${INCOMPATIBLE_REMEDY}`
                  : "暂时无法确认更新状态。"}
              </p>
            )}
            {update.interrupted && update.status !== "interrupted" && <p>已明确中断活动进程。</p>}
            {update.updated && update.status !== "interrupted" && <p>持久终端组件已更新。</p>}
            {update.rolledBack && <p>已恢复为更新前的版本；更新时结束的进程不会恢复。</p>}
            {update.detail && <details className="mt-1 text-neutral-400"><summary tabIndex={0} className="cursor-pointer">更新详情</summary><p>{update.detail}</p></details>}
            {(update.status === "available" || update.status === "deferred") && <p id={updateNoteId} className="mt-1 text-[var(--nx-fg-warning)]">
              更新会保留正在运行的终端；如需中断进程，会再次确认。
            </p>}
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
                更新组件
              </button>
            )}
            {(update.status === "available" || update.status === "deferred") && onDeferUpdate && (
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
