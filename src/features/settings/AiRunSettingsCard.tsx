import { useCallback, useEffect, useState } from "react";
import { AI_MAX_TURNS_MAX, AI_MAX_TURNS_MIN, aiRunSettingsApi } from "../../ipc/aiRunSettingsApi";
import { useUi } from "../../app/store";
import { DESKTOP } from "../../ipc/env";
import { describeError } from "../../ui/errorText";
import { IconActivity } from "../../ui/icons";
import { SyncScopeBadge } from "./SyncScopeBadge";
import { t } from "../../i18n";

function parseMaxTurns(text: string): number | null {
  if (!/^\d+$/.test(text.trim())) return null;
  const value = Number(text.trim());
  return value >= AI_MAX_TURNS_MIN && value <= AI_MAX_TURNS_MAX ? value : null;
}

// AiRunSettingsCard edits the step limit of one AI assistant run. The backend reads it when each run starts, so a saved
// value applies to the next run. The desktop keeps it on this device; the server keeps it per account. It does not sync.
export function AiRunSettingsCard() {
  const pushToast = useUi((s) => s.pushToast);
  const accountEpoch = useUi((s) => s.accountEpoch);
  const [saved, setSaved] = useState<number | null>(null);
  const [draft, setDraft] = useState("");
  const [loadError, setLoadError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [recordEvidence, setRecordEvidence] = useState<boolean | null>(null);

  const load = useCallback(() => {
    let cancelled = false;
    setLoadError(null);
    Promise.resolve()
      .then(() => aiRunSettingsApi.get())
      .then((settings) => {
        if (cancelled) return;
        setSaved(settings.maxTurns);
        setDraft(String(settings.maxTurns));
        setRecordEvidence(settings.recordEvidence);
      })
      .catch((e: unknown) => {
        if (!cancelled) setLoadError(describeError(e));
      });
    return () => {
      cancelled = true;
    };
  }, []);
  useEffect(() => {
    setSaved(null);
    setRecordEvidence(null);
    return load();
  }, [accountEpoch, load]);

  const value = parseMaxTurns(draft);
  const unchanged = value === null || value === saved;
  const save = () => {
    if (value === null || saving) return;
    setSaving(true);
    aiRunSettingsApi
      .set({ maxTurns: value })
      .then((settings) => {
        setSaved(settings.maxTurns);
        setDraft(String(settings.maxTurns));
        pushToast("success", t("已保存单轮最大步数"));
      })
      .catch((e: unknown) => pushToast("error", t("保存单轮最大步数失败：{value}", { value: describeError(e) })))
      .finally(() => setSaving(false));
  };

  const saveRecordEvidence = (next: boolean) => {
    if (saved === null || saving) return;
    setSaving(true);
    aiRunSettingsApi
      .set({ maxTurns: saved, recordEvidence: next })
      .then((settings) => {
        setRecordEvidence(settings.recordEvidence);
        pushToast("success", settings.recordEvidence ? t("已开启运行证据记录") : t("已关闭运行证据记录"));
      })
      .catch((e: unknown) => pushToast("error", t("保存运行证据记录设置失败：{value}", { value: describeError(e) })))
      .finally(() => setSaving(false));
  };

  return (
    <section id="settings-ai-run" className="nx-card scroll-mt-12">
      <div className="mb-1 flex flex-wrap items-center gap-x-2 gap-y-1.5">
        <IconActivity size={15} className="text-neutral-400" />
        <span className="nx-card-title">{t("AI 运行")}</span>
        {/* On the server the limit belongs to the account, so it is not a device setting there. */}
        {DESKTOP && <SyncScopeBadge scope="local" />}
      </div>
      <p className="nx-hint mb-3.5">
        {t("AI 助手每轮回答最多执行的步数（每次调用模型算一步）。达到上限时本轮暂停，可点击「继续」接着执行。")}
      </p>
      {loadError && (
        <div role="alert" className="nx-alert nx-alert-danger mb-2 flex flex-wrap items-center gap-2">
          <span className="min-w-0 flex-1 break-words">{t("读取 AI 运行设置失败：{loadError}", { loadError })}</span>
          <button type="button" className="nx-btn nx-btn-ghost nx-btn-sm" onClick={() => load()}>
            {t("重试")}
          </button>
        </div>
      )}
      <div className="flex flex-wrap items-center gap-2">
        <span className="text-[12.5px] text-neutral-200" data-setting="ai-max-turns">
          {t("单轮最大步数")}
        </span>
        <input
          type="number"
          className="nx-input nx-input-sm w-[96px]"
          aria-label={t("单轮最大步数")}
          min={AI_MAX_TURNS_MIN}
          max={AI_MAX_TURNS_MAX}
          step={1}
          value={draft}
          disabled={saved === null}
          aria-invalid={draft !== "" && value === null ? true : undefined}
          onChange={(e) => setDraft(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") {
              e.preventDefault();
              if (!unchanged) save();
            }
          }}
        />
        <span className="nx-hint">{t("范围 {min}-{max}，默认 {max}",{ min: AI_MAX_TURNS_MIN, max: AI_MAX_TURNS_MAX })}</span>
        <button
          type="button"
          className="nx-btn nx-btn-outline nx-btn-sm"
          disabled={unchanged || saving}
          title={value === null ? t("请输入 {min} 到 {max} 之间的整数", { min: AI_MAX_TURNS_MIN, max: AI_MAX_TURNS_MAX }) : unchanged ? t("尚无改动") : undefined}
          onClick={save}
        >
          {saving ? t("正在保存…") : t("保存")}
        </button>
      </div>
      <label className="mt-3.5 flex cursor-pointer items-center gap-3 border-t border-neutral-800/60 pt-3">
        <div className="min-w-0 flex-1">
          <div className="text-[12.5px] text-neutral-200" data-setting="ai-record-evidence">
            {t("记录运行证据")}
          </div>
          <p className="nx-hint mt-0.5">
            {t("每轮运行把工具输出和最终回答封存为防篡改的证据记录。关闭后新的运行不再记录，已有记录不受影响。")}
          </p>
        </div>
        <input
          type="checkbox"
          className="h-4 w-4 shrink-0"
          aria-label={t("记录运行证据")}
          checked={recordEvidence ?? true}
          disabled={recordEvidence === null || saved === null || saving}
          onChange={(event) => saveRecordEvidence(event.target.checked)}
        />
      </label>
    </section>
  );
}
