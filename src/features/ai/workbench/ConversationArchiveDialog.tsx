import { useEffect, useId, useMemo, useRef, useState } from "react";
import { aiApi } from "../../../ipc/commands";
import type { AiConversationExportRequest, AiConversationExportResult, AiConversationStatus } from "../../../ipc/types";
import { useUi } from "../../../app/store";
import { finishSave, pickSavePath } from "../../../ui/dialogs";
import { describeError } from "../../../ui/errorText";
import { isImeKeyEvent, trapOverlayTab, useOverlayFocus } from "../../../ui/DialogHost";
import { Select } from "../../../ui/Select";
import { IconRefresh } from "../../../ui/icons";
import { compactDate, sanitizeFileName, viewerTimeZone } from "../../terminal/transcriptExport";
import {
  RANGE_PRESETS,
  WORKBENCH_STATUSES,
  conversationExportLabels,
  conversationStatusLabel,
  presetDates,
  rangeBounds,
  type RangePreset,
} from "./workbenchShared";
import { t } from "../../../i18n";

type ArchiveScope = "host" | "unlinked" | "all";
type ArchiveFormat = "zip" | "tar.gz";

export interface ConversationArchiveDefaults {
  // unlinked starts from the conversations without a host (the global view's "未关联主机" filter).
  unlinked?: boolean;
  status?: AiConversationStatus;
  query?: string;
  preset?: RangePreset;
  dates?: { from: string; to: string };
}

export function conversationArchiveMessage(result: AiConversationExportResult, where: string): string {
  let message = t("已导出 {count} 个对话到 {where}", { count: result.exported, where });
  if (result.skipped.running > 0) {
    message += t("。已跳过 {count} 个仍在运行的对话", { count: result.skipped.running });
  }
  return message;
}

export function ConversationArchiveDialog({
  assetId,
  hostName,
  allowAllHosts,
  defaults = {},
  hostNames,
  onClose,
}: {
  // The host the view shows; null when none is selected.
  assetId: string | null;
  hostName: string;
  // Only the global views offer exporting every host or the conversations without one.
  allowAllHosts: boolean;
  defaults?: ConversationArchiveDefaults;
  // Names of the hosts the viewer knows, used for the archive's directories.
  hostNames?: Record<string, string>;
  onClose: () => void;
}) {
  const titleId = useId();
  const modalRef = useRef<HTMLDivElement>(null);
  const layer = useOverlayFocus(true, modalRef);
  const pushToast = useUi((state) => state.pushToast);
  const [preset, setPreset] = useState<RangePreset>(defaults.preset ?? "all");
  const [dates, setDates] = useState(() =>
    defaults.dates ?? presetDates(defaults.preset && defaults.preset !== "custom" ? defaults.preset : "all", new Date()));
  const [scope, setScope] = useState<ArchiveScope>(assetId ? "host" : defaults.unlinked ? "unlinked" : "all");
  const [status, setStatus] = useState<AiConversationStatus | "">(defaults.status ?? "");
  const [includeJson, setIncludeJson] = useState(false);
  const [format, setFormat] = useState<ArchiveFormat>("zip");
  const [preview, setPreview] = useState<AiConversationExportResult | null>(null);
  const [previewError, setPreviewError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const aliveRef = useRef(true);
  useEffect(() => {
    aliveRef.current = true;
    return () => {
      aliveRef.current = false;
    };
  }, []);

  const query = defaults.query?.trim() ?? "";
  const effectiveScope: ArchiveScope = allowAllHosts ? scope : "host";
  const bounds = rangeBounds(dates.from, dates.to);
  const request = useMemo<AiConversationExportRequest | null>(() => {
    if (!bounds.valid) return null;
    if (effectiveScope === "host" && !assetId) return null;
    return {
      format,
      includeJson,
      ...(effectiveScope === "host" && assetId ? { assetId } : {}),
      ...(effectiveScope === "unlinked" ? { unlinked: true } : {}),
      ...(status ? { status } : {}),
      ...(query ? { query } : {}),
      ...(bounds.updatedFrom ? { updatedFrom: bounds.updatedFrom } : {}),
      ...(bounds.updatedBefore ? { updatedBefore: bounds.updatedBefore } : {}),
      ...viewerTimeZone(),
    };
  }, [bounds.valid, bounds.updatedFrom, bounds.updatedBefore, effectiveScope, assetId, format, includeJson, status, query]);

  // The dry run only reads metadata, so the count follows every change of the selection.
  useEffect(() => {
    if (!request) {
      setPreview(null);
      return;
    }
    let current = true;
    aiApi.conversationExport({ ...request, dryRun: true }).then(
      (result) => {
        if (!current) return;
        setPreview(result);
        setPreviewError(null);
      },
      (error: unknown) => {
        if (!current) return;
        setPreview(null);
        setPreviewError(describeError(error));
      },
    );
    return () => {
      current = false;
    };
  }, [request]);

  const choosePreset = (value: Exclude<RangePreset, "custom">) => {
    setPreset(value);
    setDates(presetDates(value, new Date()));
  };

  const archiveName = () => {
    const scopeName =
      effectiveScope === "host" ? sanitizeFileName(hostName || "host").replace(/\s+/g, "-") : effectiveScope === "unlinked" ? "unlinked" : "all";
    const from = dates.from ? dates.from.replace(/-/g, "") : "all";
    const to = dates.to ? dates.to.replace(/-/g, "") : compactDate(new Date());
    return `nexterm-ai-${scopeName}-${from}-${to}.${format}`;
  };

  const close = () => {
    if (!busy) onClose();
  };

  const runExport = async () => {
    if (!request || busy) return;
    const name = archiveName();
    const path = await pickSavePath(name);
    if (!path) return;
    setBusy(true);
    try {
      const names = { ...(hostNames ?? {}), ...(assetId && hostName ? { [assetId]: hostName } : {}) };
      const result = await aiApi.conversationExport({ ...request, path, labels: conversationExportLabels(), hostNames: names });
      const where = await finishSave(path, name);
      if (!where) pushToast("info", t("已取消保存"));
      else pushToast("success", conversationArchiveMessage(result, where));
      if (aliveRef.current) onClose();
    } catch (error) {
      pushToast("error", t("导出失败：{value}。请更换保存位置后重试", { value: describeError(error) }));
    } finally {
      if (aliveRef.current) setBusy(false);
    }
  };

  const canExport = !!request && !!preview && preview.exported > 0 && !busy;

  return (
    <div className="nx-overlay" onClick={close}>
      <div
        ref={modalRef}
        className="nx-modal max-w-[520px]"
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        data-testid="ai-archive-dialog"
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
        <div className="nx-modal-body flex max-h-[75vh] flex-col gap-3 overflow-y-auto text-[12.5px]">
          <h2 id={titleId} className="text-[14px] font-semibold text-neutral-100">
            {t("批量导出 AI 对话")}
          </h2>

          <div className="flex flex-col gap-1.5">
            <span className="nx-hint">{t("时间范围（按最后活动时间）")}</span>
            <div className="nx-segment flex-wrap" role="group" aria-label={t("快速选择时间范围")}>
              {RANGE_PRESETS.map((option) => (
                <button
                  key={option.value}
                  type="button"
                  className={`nx-segment-item ${preset === option.value ? "is-active" : ""}`}
                  aria-pressed={preset === option.value}
                  onClick={() => choosePreset(option.value)}
                >
                  {option.label()}
                </button>
              ))}
            </div>
            <div className="flex flex-wrap items-center gap-2">
              <input
                type="date"
                className="nx-input w-[150px]"
                aria-label={t("开始日期")}
                value={dates.from}
                onChange={(event) => {
                  setPreset("custom");
                  setDates((current) => ({ ...current, from: event.target.value }));
                }}
              />
              <span className="nx-hint">-</span>
              <input
                type="date"
                className="nx-input w-[150px]"
                aria-label={t("结束日期")}
                value={dates.to}
                onChange={(event) => {
                  setPreset("custom");
                  setDates((current) => ({ ...current, to: event.target.value }));
                }}
              />
            </div>
            {!bounds.valid && <div className="text-[12px] text-red-300" role="alert">{t("结束日期不能早于开始日期")}</div>}
          </div>

          <div className="flex flex-wrap gap-3">
            {allowAllHosts && (
              <label className="flex min-w-[180px] flex-1 flex-col gap-1.5">
                <span className="nx-hint">{t("范围")}</span>
                <Select
                  ariaLabel={t("导出范围")}
                  value={scope}
                  options={[
                    { value: "host", label: assetId ? t("当前主机：{name}", { name: hostName }) : t("当前主机"), disabled: !assetId },
                    { value: "unlinked", label: t("未关联主机") },
                    { value: "all", label: t("全部主机") },
                  ]}
                  onChange={(value) => setScope(value === "all" ? "all" : value === "unlinked" ? "unlinked" : "host")}
                />
              </label>
            )}
            <label className="flex min-w-[140px] flex-1 flex-col gap-1.5">
              <span className="nx-hint">{t("状态")}</span>
              <Select
                ariaLabel={t("导出的对话状态")}
                value={status}
                options={[{ value: "", label: t("全部状态") }, ...WORKBENCH_STATUSES.map((value) => ({ value, label: conversationStatusLabel(value) }))]}
                onChange={(value) => setStatus(value as AiConversationStatus | "")}
              />
            </label>
            <label className="flex min-w-[120px] flex-1 flex-col gap-1.5">
              <span className="nx-hint">{t("压缩格式")}</span>
              <Select
                ariaLabel={t("压缩格式")}
                value={format}
                options={[
                  { value: "zip", label: "zip" },
                  { value: "tar.gz", label: "tar.gz" },
                ]}
                onChange={(value) => setFormat(value as ArchiveFormat)}
              />
            </label>
          </div>
          {query ? <div className="nx-hint">{t("只导出匹配搜索「{query}」的对话", { query })}</div> : null}

          <label className="flex items-center gap-2">
            <input type="checkbox" checked={includeJson} onChange={(event) => setIncludeJson(event.target.checked)} />
            {t("同时导出原始 JSON 记录")}
          </label>

          <div className="nx-hint" data-testid="ai-archive-preview" aria-live="polite">
            {previewError
              ? t("无法统计对话：{value}", { value: previewError })
              : preview
                ? (
                  <>
                    {t("匹配 {matched} 个对话，将导出 {exported} 个", { matched: preview.matched, exported: preview.exported })}
                    {preview.skipped.running > 0 && (
                      <div className="text-amber-300">
                        {t("将跳过 {count} 个仍在运行的对话", { count: preview.skipped.running })}
                      </div>
                    )}
                  </>
                )
                : request
                  ? t("正在统计…")
                  : null}
          </div>
          <p className="nx-hint text-[11.5px]">
            {t("每个对话导出为 Markdown：对话文字、工具调用摘要、文件变更差异和确认记录。压缩包内含 manifest.json，列出每个对话和跳过的原因。")}
          </p>
        </div>
        <div className="nx-modal-footer">
          <button type="button" className="nx-btn nx-btn-ghost" disabled={busy} onClick={close}>
            {t("取消")}
          </button>
          <button type="button" className="nx-btn nx-btn-primary" disabled={!canExport} onClick={() => void runExport()}>
            {busy ? <IconRefresh size={12} className="animate-spin" /> : null}
            {busy ? t("正在导出…") : t("导出")}
          </button>
        </div>
      </div>
    </div>
  );
}
