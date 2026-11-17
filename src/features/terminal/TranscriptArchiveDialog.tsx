import { useEffect, useId, useMemo, useRef, useState } from "react";
import { transcriptApi } from "../../ipc/commands";
import type { TranscriptExportArchiveRequest, TranscriptExportArchiveResult } from "../../ipc/generated/types";
import { useUi } from "../../app/store";
import { finishSave, pickSavePath } from "../../ui/dialogs";
import { describeError } from "../../ui/errorText";
import { isImeKeyEvent, trapOverlayTab, useOverlayFocus } from "../../ui/DialogHost";
import { Select } from "../../ui/Select";
import { IconRefresh } from "../../ui/icons";
import { formatTranscriptBytes } from "./transcriptText";
import { compactDate, isoDate, localDayStart, sanitizeFileName, viewerTimeZone } from "./transcriptExport";
import { t } from "../../i18n";

type RangePreset = "1" | "7" | "30" | "90" | "all" | "custom";
type ArchiveContent = "cast" | "text" | "both";
type ArchiveFormat = "zip" | "tar.gz";

const PRESETS: { value: Exclude<RangePreset, "custom">; label: () => string }[] = [
  { value: "1", label: () => t("最近 1 天") },
  { value: "7", label: () => t("最近 7 天") },
  { value: "30", label: () => t("最近 30 天") },
  { value: "90", label: () => t("最近 90 天") },
  { value: "all", label: () => t("全部") },
];

// presetDates returns the inclusive local day range of a quick pick: the last N days end today.
function presetDates(preset: Exclude<RangePreset, "custom">, now: Date): { from: string; to: string } {
  if (preset === "all") return { from: "", to: "" };
  const days = Number(preset);
  const from = new Date(now.getFullYear(), now.getMonth(), now.getDate() - (days - 1));
  return { from: isoDate(from), to: isoDate(now) };
}

// rangeBounds turns the inclusive day inputs into [startedFrom, startedBefore) in Unix ms; 0 is unbounded.
function rangeBounds(from: string, to: string): { startedFrom: number; startedBefore: number; valid: boolean } {
  const start = from ? localDayStart(from) : null;
  const end = to ? localDayStart(to) : null;
  const startedFrom = start ? start.getTime() : 0;
  const startedBefore = end ? new Date(end.getFullYear(), end.getMonth(), end.getDate() + 1).getTime() : 0;
  const valid = (!from || !!start) && (!to || !!end) && (!startedBefore || startedBefore > startedFrom);
  return { startedFrom, startedBefore, valid };
}

function skippedReasons(result: TranscriptExportArchiveResult): string {
  const reasons: string[] = [];
  if (result.skipped.contentOmitted > 0) {
    reasons.push(t("{count} 条只同步了元数据，没有内容", { count: result.skipped.contentOmitted }));
  }
  if (result.skipped.active > 0) reasons.push(t("{count} 条仍在录制", { count: result.skipped.active }));
  return reasons.length === 2 ? t("{first}，{second}", { first: reasons[0], second: reasons[1] }) : reasons.join("");
}

function skippedCount(result: TranscriptExportArchiveResult): number {
  return result.skipped.contentOmitted + result.skipped.active;
}

export function archiveResultMessage(result: TranscriptExportArchiveResult, where: string): string {
  let message = t("已导出 {count} 条记录到 {where}", { count: result.exported, where });
  const skipped = skippedCount(result);
  if (skipped > 0) message += t("。已跳过 {count} 条：{reasons}", { count: skipped, reasons: skippedReasons(result) });
  if (result.fullScreenSegments > 0) {
    message += t("。{count} 段全屏程序（如 vim、top）的画面不在文本中", { count: result.fullScreenSegments });
  }
  if (result.timingEstimated > 0) {
    message += t("。{count} 个录像的部分时间戳缺失，已按固定间隔估算", { count: result.timingEstimated });
  }
  return message;
}

export function TranscriptArchiveDialog({
  assetId,
  hostName,
  allowAllHosts,
  onClose,
}: {
  // The host the panel shows; null when none is selected.
  assetId: string | null;
  hostName: string;
  // Only the global history tab offers exporting every host.
  allowAllHosts: boolean;
  onClose: () => void;
}) {
  const titleId = useId();
  const modalRef = useRef<HTMLDivElement>(null);
  const layer = useOverlayFocus(true, modalRef);
  const pushToast = useUi((state) => state.pushToast);
  const [preset, setPreset] = useState<RangePreset>("7");
  const [dates, setDates] = useState(() => presetDates("7", new Date()));
  const [scope, setScope] = useState<"host" | "all">(assetId ? "host" : "all");
  const [content, setContent] = useState<ArchiveContent>("cast");
  const [timestamps, setTimestamps] = useState(false);
  const [format, setFormat] = useState<ArchiveFormat>("zip");
  const [starredOnly, setStarredOnly] = useState(false);
  const [preview, setPreview] = useState<TranscriptExportArchiveResult | null>(null);
  const [previewError, setPreviewError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const aliveRef = useRef(true);
  useEffect(() => {
    aliveRef.current = true;
    return () => {
      aliveRef.current = false;
    };
  }, []);

  const effectiveScope = allowAllHosts ? scope : "host";
  const bounds = rangeBounds(dates.from, dates.to);
  const request = useMemo<TranscriptExportArchiveRequest | null>(() => {
    if (!bounds.valid) return null;
    if (effectiveScope === "host" && !assetId) return null;
    return {
      format,
      content,
      timestamps: content !== "cast" && timestamps,
      starredOnly,
      ...(effectiveScope === "host" && assetId ? { assetId } : {}),
      ...(bounds.startedFrom ? { startedFrom: bounds.startedFrom } : {}),
      ...(bounds.startedBefore ? { startedBefore: bounds.startedBefore } : {}),
      ...viewerTimeZone(),
    };
  }, [bounds.valid, bounds.startedFrom, bounds.startedBefore, effectiveScope, assetId, format, content, timestamps, starredOnly]);

  // The dry run is a metadata query, so the count follows every change of the filter.
  useEffect(() => {
    if (!request) {
      setPreview(null);
      return;
    }
    let current = true;
    transcriptApi.exportArchive({ ...request, dryRun: true }).then(
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
    const scopeName = effectiveScope === "host" ? sanitizeFileName(hostName || "host").replace(/\s+/g, "-") : "all";
    const from = dates.from ? dates.from.replace(/-/g, "") : "all";
    const to = dates.to ? dates.to.replace(/-/g, "") : compactDate(new Date());
    return `nexterm-history-${scopeName}-${from}-${to}.${format}`;
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
      const result = await transcriptApi.exportArchive({ ...request, path });
      const where = await finishSave(path, name);
      if (!where) {
        pushToast("info", t("已取消保存"));
      } else {
        pushToast("success", archiveResultMessage(result, where));
      }
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
        data-testid="transcript-archive-dialog"
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
            {t("批量导出终端历史")}
          </h2>

          <div className="flex flex-col gap-1.5">
            <span className="nx-hint">{t("时间范围（按记录开始时间）")}</span>
            <div className="nx-segment flex-wrap" role="group" aria-label={t("快速选择时间范围")}>
              {PRESETS.map((option) => (
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

          {allowAllHosts && (
            <label className="flex flex-col gap-1.5">
              <span className="nx-hint">{t("范围")}</span>
              <Select
                ariaLabel={t("导出范围")}
                value={scope}
                options={[
                  { value: "host", label: assetId ? t("当前主机：{name}", { name: hostName }) : t("当前主机"), disabled: !assetId },
                  { value: "all", label: t("全部主机") },
                ]}
                onChange={(value) => setScope(value === "all" ? "all" : "host")}
              />
            </label>
          )}

          <div className="flex flex-wrap gap-3">
            <label className="flex min-w-[180px] flex-1 flex-col gap-1.5">
              <span className="nx-hint">{t("内容")}</span>
              <Select
                ariaLabel={t("导出内容")}
                value={content}
                options={[
                  { value: "cast", label: t("asciicast 录像（.cast）") },
                  { value: "text", label: t("纯文本（.txt）") },
                  { value: "both", label: t("录像和纯文本") },
                ]}
                onChange={(value) => setContent(value as ArchiveContent)}
              />
            </label>
            <label className="flex min-w-[140px] flex-1 flex-col gap-1.5">
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

          {content !== "cast" && (
            <label className="flex items-center gap-2">
              <input type="checkbox" checked={timestamps} onChange={(event) => setTimestamps(event.target.checked)} />
              {t("纯文本每行带时间戳")}
            </label>
          )}
          <label className="flex items-center gap-2">
            <input type="checkbox" checked={starredOnly} onChange={(event) => setStarredOnly(event.target.checked)} />
            {t("只导出星标记录")}
          </label>

          <div className="nx-hint" data-testid="transcript-archive-preview" aria-live="polite">
            {previewError
              ? t("无法统计记录：{value}", { value: previewError })
              : preview
                ? (
                  <>
                    {t("匹配 {matched} 条记录，将导出 {exported} 条（录像约 {size}，导出后大小会不同）", {
                      matched: preview.matched,
                      exported: preview.exported,
                      size: formatTranscriptBytes(preview.estimatedBytes),
                    })}
                    {skippedCount(preview) > 0 && (
                      <div className="text-amber-300">
                        {t("将跳过 {count} 条：{reasons}", { count: skippedCount(preview), reasons: skippedReasons(preview) })}
                      </div>
                    )}
                  </>
                )
                : request
                  ? t("正在统计…")
                  : null}
          </div>
          <p className="nx-hint text-[11.5px]">{t("导出内容不含键盘输入。压缩包内含 manifest.json，列出每条记录和跳过的原因。")}</p>
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
