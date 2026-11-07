import { useCallback, useEffect, useRef, useState } from "react";
import { transcriptApi } from "../../ipc/commands";
import type { TranscriptRetentionDTO, TranscriptUsageDTO } from "../../ipc/generated/types";
import { useUi } from "../../app/store";
import { ask } from "../../ui/dialogs";
import { describeError } from "../../ui/errorText";
import { IconHistory, IconRefresh, IconTrash } from "../../ui/icons";
import { Select } from "../../ui/Select";
import { formatTranscriptBytes } from "../terminal/transcriptText";
import { t } from "../../i18n";

const DAY_MS = 24 * 60 * 60 * 1000;

const AGE_OPTIONS = [
  { days: 7, label: () => t("7 天") },
  { days: 30, label: () => t("1 个月") },
  { days: 90, label: () => t("3 个月") },
  { days: 180, label: () => t("6 个月") },
  { days: 365, label: () => t("1 年") },
];

const COUNT_OPTIONS = [500, 1000, 2000, 5000, 10000];
const UNLIMITED = "unlimited";

// "all" purges every ended, unstarred transcript; the others purge those that ended before that many days ago.
const PURGE_OPTIONS = [
  { value: "7", label: () => t("7 天前") },
  { value: "30", label: () => t("1 个月前") },
  { value: "90", label: () => t("3 个月前") },
  { value: "all", label: () => t("全部") },
];

function purgeCutoff(value: string): number {
  return value === "all" ? Date.now() + 1 : Date.now() - Number(value) * DAY_MS;
}

export function TranscriptCleanupCard() {
  const pushToast = useUi((s) => s.pushToast);
  const [retention, setRetention] = useState<TranscriptRetentionDTO | null>(null);
  const [usage, setUsage] = useState<TranscriptUsageDTO | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [purgeRange, setPurgeRange] = useState("30");
  const [purging, setPurging] = useState(false);
  const aliveRef = useRef(true);

  useEffect(() => {
    aliveRef.current = true;
    return () => {
      aliveRef.current = false;
    };
  }, []);

  const reload = useCallback(async () => {
    try {
      const [nextRetention, nextUsage] = await Promise.all([transcriptApi.retention(), transcriptApi.usage(Date.now() + 1)]);
      if (!aliveRef.current) return;
      setRetention(nextRetention);
      setUsage(nextUsage);
      setError(null);
    } catch (e) {
      if (aliveRef.current) setError(describeError(e));
    }
  }, []);

  useEffect(() => {
    void reload();
  }, [reload]);

  const save = async (next: TranscriptRetentionDTO) => {
    const previous = retention;
    setRetention(next);
    setSaving(true);
    try {
      await transcriptApi.setRetention({
        enabled: next.enabled,
        maxAgeMs: next.maxAgeMs,
        maxCount: next.maxCount,
        unlimitedCount: next.unlimitedCount,
      });
    } catch (e) {
      if (aliveRef.current) setRetention(previous);
      pushToast("error", t("无法保存自动清理设置：{error}", { error: describeError(e) }));
    } finally {
      if (aliveRef.current) setSaving(false);
    }
  };

  const purge = async () => {
    const cutoff = purgeCutoff(purgeRange);
    setPurging(true);
    try {
      const preview = await transcriptApi.usage(cutoff);
      if (preview.purgeable === 0) {
        pushToast("info", t("没有可清理的会话记录"));
        return;
      }
      const ok = await ask(
        t("将删除 {count} 条会话记录（{size}）。\n\n加星标和进行中的会话会保留。已开启同步的记录也会从其他设备删除，除非那台设备给它加了星标。", {
          count: preview.purgeable,
          size: formatTranscriptBytes(preview.purgeableBytes),
        }),
        { title: t("清理历史会话"), kind: "warning" },
      );
      if (!ok) return;
      const result = await transcriptApi.purge(cutoff);
      pushToast("success", t("已清理 {count} 条会话记录", { count: result.deleted }));
      await reload();
    } catch (e) {
      pushToast("error", t("清理失败：{error}", { error: describeError(e) }));
    } finally {
      if (aliveRef.current) setPurging(false);
    }
  };

  const ageDays = retention ? Math.round(retention.maxAgeMs / DAY_MS) : 30;
  const ageOptions = AGE_OPTIONS.some((option) => option.days === ageDays)
    ? AGE_OPTIONS.map((option) => ({ value: String(option.days), label: option.label() }))
    : [...AGE_OPTIONS.map((option) => ({ value: String(option.days), label: option.label() })), { value: String(ageDays), label: t("{days} 天", { days: ageDays }) }];
  const countValue = retention?.unlimitedCount ? UNLIMITED : String(retention?.maxCount ?? 2000);
  const countOptions = [
    ...COUNT_OPTIONS.map((count) => ({ value: String(count), label: t("{count} 条", { count }) })),
    ...(retention && !retention.unlimitedCount && !COUNT_OPTIONS.includes(retention.maxCount)
      ? [{ value: String(retention.maxCount), label: t("{count} 条", { count: retention.maxCount }) }]
      : []),
    { value: UNLIMITED, label: t("不限数量") },
  ];
  const editable = retention?.editable === true;
  const autoDisabled = !retention || !editable || saving;

  return (
    <section className="nx-card" data-testid="transcript-cleanup">
      <div className="mb-1 flex flex-wrap items-center gap-x-2 gap-y-1.5">
        <IconHistory size={15} className="text-neutral-400" />
        <span className="nx-card-title">{t("历史会话")}</span>
        {usage && (
          <span className="nx-badge" data-testid="transcript-usage">
            {t("{total} 条 · {size} · {starred} 条星标", {
              total: usage.total,
              size: formatTranscriptBytes(usage.storedBytes),
              starred: usage.starred,
            })}
          </span>
        )}
        <div className="nx-spacer" />
        <button className="nx-btn nx-btn-ghost nx-btn-sm" onClick={() => void reload()}>
          <IconRefresh size={11} />
          {t("刷新")}
        </button>
      </div>
      <p className="nx-hint mb-3.5">
        {t("终端历史记录会话输出。加星标的会话不会被自动或手动清理，进行中的会话也不会。")}
      </p>
      {error && <div className="nx-alert nx-alert-danger mb-3">{t("未能读取：{error}", { error })}</div>}

      <div className="flex flex-col gap-3">
        <label className="flex cursor-pointer items-center gap-3" data-setting="transcript-auto-cleanup">
          <div className="min-w-0 flex-1">
            <div className="text-[12.5px] text-neutral-200">{t("自动清理")}</div>
            <p className="nx-hint mt-0.5">
              {retention && !editable
                ? t("多用户服务端的自动清理由服务端统一设置")
                : t("定期删除超过保留时长或超出数量上限的已结束会话")}
            </p>
          </div>
          <input
            type="checkbox"
            className="h-4 w-4 shrink-0"
            aria-label={t("自动清理")}
            checked={retention?.enabled ?? true}
            disabled={autoDisabled}
            onChange={(event) => retention && void save({ ...retention, enabled: event.target.checked })}
          />
        </label>
        <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
          <div className="flex items-center gap-2">
            <span className="text-[12px] text-neutral-400">{t("保留时长")}</span>
            <Select
              ariaLabel={t("保留时长")}
              value={String(ageDays)}
              disabled={autoDisabled || !retention?.enabled}
              options={ageOptions}
              onChange={(value) => retention && void save({ ...retention, maxAgeMs: Number(value) * DAY_MS })}
            />
          </div>
          <div className="flex items-center gap-2">
            <span className="text-[12px] text-neutral-400">{t("最多保留")}</span>
            <Select
              ariaLabel={t("最多保留")}
              value={countValue}
              disabled={autoDisabled || !retention?.enabled}
              options={countOptions}
              onChange={(value) =>
                retention &&
                void save(
                  value === UNLIMITED
                    ? { ...retention, unlimitedCount: true }
                    : { ...retention, unlimitedCount: false, maxCount: Number(value) },
                )
              }
            />
          </div>
        </div>

        <div className="flex flex-wrap items-center gap-2 border-t border-neutral-800/60 pt-3" data-setting="transcript-manual-cleanup">
          <span className="text-[12.5px] text-neutral-200">{t("手动清理")}</span>
          <div className="nx-spacer" />
          <Select ariaLabel={t("清理范围")} value={purgeRange} options={PURGE_OPTIONS.map((option) => ({ value: option.value, label: option.label() }))} onChange={setPurgeRange} />
          <button className="nx-btn nx-btn-outline nx-btn-sm" disabled={purging} aria-busy={purging} onClick={() => void purge()}>
            <IconTrash size={11} />
            {t("清理…")}
          </button>
        </div>
      </div>
    </section>
  );
}
