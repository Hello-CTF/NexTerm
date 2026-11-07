import { useCallback, useEffect, useId, useRef, useState } from "react";
import { modelApi, type ModelProfilesView } from "../../ipc/commands";
import { cronApi, cronTimeoutMs, type CronJob } from "../../ipc/cron";
import { useUi } from "../../app/store";
import { ask } from "../../ui/dialogs";
import { describeError } from "../../ui/errorText";
import { Select } from "../../ui/Select";
import { profileKeyUnavailable as profileKeyMasked } from "./modelLifecycle";
import { clipText } from "../../ui/textClip";
import {
  IconChevronDown,
  IconChevronRight,
  IconClock,
  IconClose,
  IconEdit,
  IconPlus,
  IconRefresh,
  IconTrash,
  IconXCircle,
} from "../../ui/icons";
import { t } from "../../i18n";

type RegisterDraft = {
  name: string;
  prompt: string;
  schedule: string;
  timezone: string;
  timeoutSec: string;
  modelProfileId: string;
};

type ReplaceConflict = {
  label: string;
  oldKey: string;
  newKey: string;
  gen: number;
  error: string;
};

function jobStatus(job: CronJob): { label: string; cls: string } {
  if (!job.enabled) return { label: t("已停用"), cls: "" };
  if (job.run.id) return { label: t("执行中"), cls: "nx-badge-blue" };
  if (job.circuitOpenUntil && Date.parse(job.circuitOpenUntil) > Date.now()) {
    return { label: t("已暂停"), cls: "nx-badge-amber" };
  }
  if (job.lastError) return { label: t("上次失败"), cls: "nx-badge-red" };
  return { label: t("等待执行"), cls: "nx-badge-green" };
}

function formatTime(rfc3339: string | undefined): string | null {
  if (!rfc3339) return null;
  const ms = Date.parse(rfc3339);
  if (Number.isNaN(ms)) return null;
  return new Date(ms).toLocaleString();
}

function nextRunText(job: CronJob): string | null {
  if (!job.enabled) return null;
  let latest: number | null = null;
  for (const value of [job.nextRunAt, job.retryAt, job.circuitOpenUntil]) {
    if (!value) continue;
    const ms = Date.parse(value);
    if (!Number.isFinite(ms)) continue;
    if (latest === null || ms > latest) latest = ms;
  }
  return latest === null ? null : new Date(latest).toLocaleString();
}

export function CronPanel({
  conversationId,
  hostName,
  onClose,
  onJobsChanged,
}: {
  conversationId: string;
  hostName?: string;
  onClose: () => void;
  // Called after each successful list load so the caller can refresh counts.
  onJobsChanged?: () => void;
}) {
  const onJobsChangedRef = useRef(onJobsChanged);
  onJobsChangedRef.current = onJobsChanged;
  const pushToast = useUi((s) => s.pushToast);
  const profilesRevision = useUi((s) => s.modelProfilesRevision);

  const [jobs, setJobs] = useState<CronJob[] | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const [profilesView, setProfilesView] = useState<ModelProfilesView | null>(null);
  const [profilesError, setProfilesError] = useState<string | null>(null);

  const [registerOpen, setRegisterOpen] = useState(false);
  const [draft, setDraft] = useState<RegisterDraft | null>(null);
  const [draftBaseline, setDraftBaseline] = useState<RegisterDraft | null>(null);
  const [editingJob, setEditingJob] = useState<CronJob | null>(null);
  const [registering, setRegistering] = useState(false);
  const [registerError, setRegisterError] = useState<string | null>(null);
  const advancedPanelId = useId();
  const [advancedOpen, setAdvancedOpen] = useState(false);

  const replaceConflictRef = useRef<ReplaceConflict | null>(null);
  const [replaceConflict, setReplaceConflictState] = useState<ReplaceConflict | null>(null);
  const applyReplaceConflict = useCallback((conflict: ReplaceConflict | null) => {
    replaceConflictRef.current = conflict;
    setReplaceConflictState(conflict);
  }, []);

  const [jobsGen, setJobsGen] = useState(0);

  const [actionBusy, setActionBusy] = useState<string | null>(null);

  const aliveRef = useRef(true);
  const loadGenRef = useRef(0);
  const invalidatePendingLoads = useCallback(() => {
    aliveRef.current = false;
    loadGenRef.current++;
  }, []);
  useEffect(() => {
    aliveRef.current = true;
    return invalidatePendingLoads;
  }, [invalidatePendingLoads]);

  const loadProfiles = useCallback(async () => {
    try {
      const view = await modelApi.overview();
      if (!aliveRef.current) return;
      setProfilesView(view);
      setProfilesError(null);
    } catch (e) {
      if (!aliveRef.current) return;
      setProfilesView(null);
      setProfilesError(describeError(e));
    }
  }, []);

  useEffect(() => {
    void loadProfiles();
  }, [loadProfiles, profilesRevision]);

  const reload = useCallback(async () => {
    const gen = ++loadGenRef.current;
    setLoading(true);
    setError(null);
    try {
      const sessionJobs = await cronApi.list(conversationId);
      if (!aliveRef.current || gen !== loadGenRef.current) return;
      setJobs(sessionJobs ?? []);
      setJobsGen(gen);
      onJobsChangedRef.current?.();
    } catch (e) {
      if (!aliveRef.current || gen !== loadGenRef.current) return;
      setJobs(null);
      setError(describeError(e));
    } finally {
      if (aliveRef.current && gen === loadGenRef.current) setLoading(false);
    }
  }, [conversationId]);

  useEffect(() => {
    void reload();
  }, [reload]);

  useEffect(() => {
    if (!replaceConflict || !jobs) return;
    if (jobsGen <= replaceConflict.gen) return;
    const present = (key: string) => jobs.some((j) => `${j.sessionId}/${j.id}` === key);
    if (!present(replaceConflict.oldKey) || !present(replaceConflict.newKey)) {
      applyReplaceConflict(null);
    }
  }, [jobs, jobsGen, replaceConflict, applyReplaceConflict]);

  const profileById = (id: string) => profilesView?.profiles.find((p) => p.id === id);
  const activeProfile = profilesView?.activeId ? profileById(profilesView.activeId) : undefined;
  const editActionPending =
    !!editingJob && actionBusy === `${editingJob.sessionId}/${editingJob.id}`;
  const profilesState = profilesError ? "error" : profilesView ? "ready" : "loading";
  const followActiveLabel = () => {
    if (profilesState === "error") return t("跟随当前档案（未能读取）");
    if (profilesState === "loading") return t("跟随当前档案（正在读取…）");
    return activeProfile
      ? t("跟随当前档案「{name}」", { name: activeProfile.name })
      : t("跟随当前档案（当前未设置）");
  };
  const unresolvedProfileLabel = (id: string) => {
    if (profilesState === "error") return t("已存档案（未能读取）· {id}", { id });
    if (profilesState === "loading") return t("已存档案（正在读取…）· {id}", { id });
    return t("未知档案（可能已删除）· {id}", { id });
  };

  const draftDirty =
    !!draft &&
    !!draftBaseline &&
    (draft.name !== draftBaseline.name ||
      draft.prompt !== draftBaseline.prompt ||
      draft.schedule !== draftBaseline.schedule ||
      draft.timezone !== draftBaseline.timezone ||
      draft.timeoutSec !== draftBaseline.timeoutSec ||
      draft.modelProfileId !== draftBaseline.modelProfileId);

  const guardDraft = async (action: string): Promise<boolean> => {
    if (!draftDirty) return true;
    return ask(t("存在未保存的修改，{action}将丢弃这些修改。确定要继续吗？", { action }), {
      title: t("放弃未保存的修改"),
      kind: "warning",
    });
  };

  const openRegister = () => {
    const open = () => {
      setRegisterError(null);
      setEditingJob(null);
      setAdvancedOpen(false);
      const nextDraft = {
        name: "",
        prompt: "",
        schedule: "",
        timezone: "UTC",
        timeoutSec: "",
        modelProfileId: "",
      };
      setDraft(nextDraft);
      setDraftBaseline({ ...nextDraft });
      setRegisterOpen(true);
    };
    if (!draftDirty) {
      open();
      return;
    }
    void guardDraft(t("新建任务")).then((ok) => {
      if (ok) open();
    });
  };

  const openEdit = (job: CronJob) => {
    const open = () => {
      setRegisterError(null);
      setEditingJob(job);
      setAdvancedOpen(false);
      const nextDraft = {
        name: job.name ?? "",
        prompt: job.prompt,
        schedule: job.schedule,
        timezone: job.timezone,
        timeoutSec: job.timeout > 0 ? String(Math.round(cronTimeoutMs(job) / 1000)) : "",
        modelProfileId: job.modelProfileId ?? "",
      };
      setDraft(nextDraft);
      setDraftBaseline({ ...nextDraft });
      setRegisterOpen(true);
    };
    if (!draftDirty) {
      open();
      return;
    }
    void guardDraft(t("编辑其他任务")).then((ok) => {
      if (ok) open();
    });
  };

  const closeRegister = () => {
    const close = () => {
      setRegisterOpen(false);
      setDraft(null);
      setDraftBaseline(null);
      setEditingJob(null);
      setRegisterError(null);
    };
    if (!draftDirty) {
      close();
      return;
    }
    void guardDraft(t("关闭编辑")).then((ok) => {
      if (ok) close();
    });
  };

  const requestClose = () => {
    if (!draftDirty) {
      onClose();
      return;
    }
    void guardDraft(t("关闭定时任务面板")).then((ok) => {
      if (ok) onClose();
    });
  };

  const submitRegister = async () => {
    if (!draft || registering) return;
    if (editingJob && actionBusy === `${editingJob.sessionId}/${editingJob.id}`) return;
    if (!draft.prompt.trim()) {
      setRegisterError(t("请填写任务内容"));
      return;
    }
    if (!draft.schedule.trim()) {
      setRegisterError(t("请填写执行时间（cron 表达式）"));
      return;
    }
    const timeoutSec = draft.timeoutSec.trim() === "" ? null : Number(draft.timeoutSec);
    if (timeoutSec !== null && (!Number.isFinite(timeoutSec) || timeoutSec <= 0)) {
      setRegisterError(t("超时时间必须大于 0 秒"));
      return;
    }
    const modelProfileId = draft.modelProfileId.trim();
    if (
      modelProfileId &&
      profilesView &&
      !profilesView.profiles.some((p) => p.id === modelProfileId)
    ) {
      setRegisterError(t("所选模型已被删除，请重新选择"));
      return;
    }
    setRegistering(true);
    setRegisterError(null);
    try {
      if (!editingJob) {
        await cronApi.register({
          sessionId: conversationId,
          ...(draft.name.trim() ? { name: draft.name.trim() } : {}),
          prompt: draft.prompt.trim(),
          schedule: draft.schedule.trim(),
          timezone: draft.timezone.trim() || "UTC",
          ...(timeoutSec !== null ? { timeoutMs: Math.round(timeoutSec * 1000) } : {}),
          ...(modelProfileId ? { modelProfileId } : {}),
        });
        pushToast("success", t("定时任务已创建"));
        setRegisterOpen(false);
        setDraft(null);
        await reload();
        return;
      }
      const created = await cronApi.register({
        sessionId: conversationId,
        ...(draft.name.trim() ? { name: draft.name.trim() } : {}),
        prompt: draft.prompt.trim(),
        schedule: draft.schedule.trim(),
        timezone: draft.timezone.trim() || "UTC",
        ...(timeoutSec !== null ? { timeoutMs: Math.round(timeoutSec * 1000) } : {}),
        ...(modelProfileId ? { modelProfileId } : {}),
        disabled: true,
      });
      try {
        await cronApi.unregister(editingJob.sessionId, editingJob.id);
      } catch (e) {
        try {
          await cronApi.unregister(created.sessionId, created.id);
          setRegisterError(
            t("未能保存任务：无法删除原任务（{value}）。原任务保持不变，请重试", { value: describeError(e) }),
          );
        } catch (rollbackError) {
          applyReplaceConflict({
            label: draft.name.trim() || clipText(draft.prompt.trim(), 40),
            oldKey: `${editingJob.sessionId}/${editingJob.id}`,
            newKey: `${created.sessionId}/${created.id}`,
            gen: loadGenRef.current,
            error: describeError(rollbackError),
          });
          setRegisterOpen(false);
          setDraft(null);
          setEditingJob(null);
          await reload();
        }
        return;
      }
      if (editingJob.enabled) {
        try {
          await cronApi.setEnabled(created.sessionId, created.id, true);
        } catch (e) {
          pushToast("error", t("任务已保存，但未能启用：{value}。任务目前为停用状态，请手动启用", { value: describeError(e) }));
          setRegisterOpen(false);
          setDraft(null);
          setEditingJob(null);
          await reload();
          return;
        }
      }
      pushToast("success", t("定时任务已更新"));
      setRegisterOpen(false);
      setDraft(null);
      setEditingJob(null);
      await reload();
    } catch (e) {
      setRegisterError(describeError(e));
    } finally {
      if (aliveRef.current) setRegistering(false);
    }
  };

  const toggleEnabled = async (job: CronJob) => {
    if (actionBusy) return;
    const key = `${job.sessionId}/${job.id}`;
    setActionBusy(key);
    try {
      const updated = await cronApi.setEnabled(job.sessionId, job.id, !job.enabled);
      if (!aliveRef.current) return;
      setJobs((prev) =>
        prev?.map((j) => (j.sessionId === job.sessionId && j.id === job.id ? updated : j)) ?? prev,
      );
      setEditingJob((prev) =>
        prev && prev.sessionId === updated.sessionId && prev.id === updated.id ? updated : prev,
      );
      pushToast("success", updated.enabled ? t("任务已启用") : t("任务已停用"));
    } catch (e) {
      pushToast("error", t("未能{value}任务：{value2}", { value: job.enabled ? t("停用") : t("启用"), value2: describeError(e) }));
      if (aliveRef.current) await reload();
    } finally {
      if (aliveRef.current) setActionBusy(null);
    }
  };

  const unregister = async (job: CronJob) => {
    const display = job.name?.trim() || clipText(job.prompt, 40);
    const ok = await ask(
      t("确定要删除定时任务「{display}」吗？\n表达式：{schedule}（{timezone}）\n\n删除后将不再执行，且无法恢复。", { display, schedule: job.schedule, timezone: job.timezone }),
      { title: t("删除定时任务"), kind: "warning" },
    );
    if (!ok) return;
    const key = `${job.sessionId}/${job.id}`;
    setActionBusy(key);
    try {
      await cronApi.unregister(job.sessionId, job.id);
      pushToast("success", t("定时任务已删除"));
      await reload();
    } catch (e) {
      pushToast("error", t("未能删除任务：{value}", { value: describeError(e) }));
    } finally {
      if (aliveRef.current) setActionBusy(null);
    }
  };

  return (
    <div className="max-h-[55%] min-w-0 shrink-0 overflow-x-hidden overflow-y-auto border-b border-neutral-800/60 bg-neutral-900/60 p-2.5">
      <div className="mb-1.5 flex items-center gap-1.5 text-[11.5px] font-medium text-neutral-200">
        <IconClock size={12} />
        {t("定时任务")}
        {jobs && <span className="nx-badge">{jobs.length} {t("个")}</span>}
        <span className="nx-spacer" />
        <button
          className="nx-icon-btn nx-icon-btn-sm"
          title={t("刷新")}
          aria-label={t("刷新定时任务")}
          aria-busy={loading}
          onClick={() => {
            void reload();
            void loadProfiles();
          }}
        >
          <IconRefresh size={11} className={loading ? "animate-spin" : undefined} />
        </button>
        <button className="nx-icon-btn nx-icon-btn-sm" title={t("关闭")} onClick={() => void requestClose()}>
          <IconClose size={11} />
        </button>
      </div>
      {hostName && (
        <div className="nx-hint mb-1.5 truncate text-[11px]" title={hostName}>
          {t("所属主机：")}{hostName}
        </div>
      )}
      <p className="nx-hint mb-2 text-[11px]">{t("到点后 AI 自动执行，需确认的操作会被拒绝；不会新建终端，需要时请明确要求 AI 直接运行命令。")}</p>

      {registerOpen && draft ? (
        <div className="mb-2 flex flex-col gap-2 border-b border-neutral-800/60 pb-2">
          <div className="text-[12px] font-semibold text-neutral-200">
            {editingJob ? t("编辑定时任务") : t("新建定时任务")}
          </div>
          <input
            className="nx-input nx-input-sm"
            placeholder={t("名称（可选），例如 磁盘巡检")}
            aria-label={t("任务名称")}
            value={draft.name}
            onChange={(e) => setDraft({ ...draft, name: e.target.value })}
          />
          <textarea
            className="nx-input min-h-[60px] font-mono text-[12px]"
            placeholder={t("任务内容（提示词）")}
            aria-label={t("任务提示词")}
            value={draft.prompt}
            onChange={(e) => setDraft({ ...draft, prompt: e.target.value })}
          />
          <div className="flex flex-wrap items-center gap-2">
            <label className="nx-hint text-[11px]" htmlFor="cron-register-model-profile">
              {t("模型档案")}
            </label>
            <Select
              id="cron-register-model-profile"
              className="min-w-0 flex-1"
              triggerClassName="nx-input nx-input-sm nx-select-trigger"
              ariaLabel={t("模型档案")}
              value={draft.modelProfileId}
              onChange={(value) => setDraft({ ...draft, modelProfileId: value })}
              options={[
                { value: "", label: followActiveLabel() },
                ...(draft.modelProfileId && !profileById(draft.modelProfileId)
                  ? [{ value: draft.modelProfileId, label: unresolvedProfileLabel(draft.modelProfileId) }]
                  : []),
                ...(profilesView?.profiles ?? []).map((p) => ({
                  value: p.id,
                  label: `${p.name} · ${p.model}${profileKeyMasked(p) ? t("（密钥已保存）") : ""}`,
                })),
              ]}
            />
          </div>
          {profilesError && (
            <div className="nx-hint text-[11px] text-red-300" role="alert">
              {t("未能读取模型列表：")}{profilesError}{t("。列表可能不完整，「跟随当前档案」不受影响。")}
            </div>
          )}
          {editingJob &&
            editingJob.modelProfileId &&
            !profileById(editingJob.modelProfileId) &&
            profilesState !== "loading" && (
              <div className="nx-hint text-[11px] text-amber-300">
                {profilesState === "error"
                  ? t("无法确认该任务所用的模型，请刷新重试，或改选其他模型。")
                  : t("该任务所用的模型已被删除，请选择其他模型，或改回「跟随当前档案」。")}
              </div>
            )}
          <div className="flex flex-wrap items-center gap-2">
            <input
              className="nx-input nx-input-sm w-[130px] font-mono"
              placeholder="0 2 * * *"
              aria-label={t("cron 表达式")}
              value={draft.schedule}
              onChange={(e) => setDraft({ ...draft, schedule: e.target.value })}
            />
          </div>
          <div>
            <button
              type="button"
              className="flex items-center gap-1.5 text-[12px] text-neutral-400 transition-colors hover:text-neutral-100"
              aria-expanded={advancedOpen}
              aria-controls={advancedPanelId}
              onClick={() => setAdvancedOpen((v) => !v)}
            >
              {advancedOpen ? (
                <IconChevronDown size={12} className="shrink-0" />
              ) : (
                <IconChevronRight size={12} className="shrink-0" />
              )}
              {t("高级设置")}
            </button>
            {advancedOpen && (
              <div id={advancedPanelId} className="mt-2 flex flex-wrap items-center gap-2">
                <input
                  className="nx-input nx-input-sm w-[110px]"
                  placeholder={t("时区 UTC")}
                  aria-label={t("时区")}
                  value={draft.timezone}
                  onChange={(e) => setDraft({ ...draft, timezone: e.target.value })}
                />
                <input
                  className="nx-input nx-input-sm w-[110px]"
                  placeholder={t("超时（秒）")}
                  aria-label={t("单次执行超时（秒）")}
                  value={draft.timeoutSec}
                  onChange={(e) => setDraft({ ...draft, timeoutSec: e.target.value })}
                />
              </div>
            )}
          </div>
          <div className="flex items-center gap-2">
            <div className="nx-spacer" />
            <button
              className="nx-btn nx-btn-primary nx-btn-sm"
              disabled={registering || editActionPending}
              title={editActionPending ? t("正在启用或停用此任务，完成后再保存") : undefined}
              onClick={() => void submitRegister()}
            >
              {registering
                ? editingJob
                  ? t("保存中…")
                  : t("创建中…")
                : editingJob
                  ? t("保存")
                  : t("创建")}
            </button>
            <button
              className="nx-btn nx-btn-ghost nx-btn-sm"
              disabled={registering}
              onClick={() => void closeRegister()}
            >
              {t("取消")}
            </button>
          </div>
          <p className="nx-hint text-[11px]">
            {t("cron 为 5 段（分 时 日 月 周），默认 UTC、超时 60 秒；停用期间错过的任务不会补做。")}
            {editingJob ? t(" 保存会重新创建任务，原来的执行记录不会保留。") : ""}
          </p>
          {registerError && (
            <div className="nx-alert nx-alert-danger flex items-start gap-2 text-[12px]" role="alert">
              <IconXCircle size={13} className="mt-0.5 shrink-0" />
              <span className="min-w-0 break-words">{registerError}</span>
            </div>
          )}
        </div>
      ) : (
        <div className="mb-2">
          <button className="nx-btn nx-btn-outline nx-btn-sm" onClick={() => void openRegister()}>
            <IconPlus size={11} />
            {t("新建定时任务")}
          </button>
        </div>
      )}

      {replaceConflict && (
        <div
          className="nx-alert nx-alert-danger mb-2 flex items-start gap-2 text-[12px]"
          role="alert"
        >
          <IconXCircle size={13} className="mt-0.5 shrink-0" />
          <div className="min-w-0 flex-1">
            <div className="break-words">
              「{replaceConflict.label}{t("」未保存成功：原任务仍然保留，新建的副本（已停用）也未能自动删除（")}
              {replaceConflict.error}{t("）。两个任务不会同时运行，请手动删除其中一个。")}
            </div>
            <div className="mt-1 break-words font-mono text-[11px]">
              {t("旧")} {replaceConflict.oldKey} {t("· 新")} {replaceConflict.newKey}
            </div>
            <button
              className="nx-btn nx-btn-outline nx-btn-sm mt-2"
              onClick={() => void reload()}
            >
              <IconRefresh size={11} />
              {t("刷新")}
            </button>
          </div>
        </div>
      )}

      {jobs === null ? (
        error ? (
          <div className="nx-alert nx-alert-danger flex items-start gap-2" role="alert">
            <IconXCircle size={13} className="mt-0.5 shrink-0" />
            <div className="min-w-0 flex-1">
              <div className="break-words">{t("未能读取定时任务：")}{error}</div>
              <button className="nx-btn nx-btn-outline nx-btn-sm mt-2" onClick={() => void reload()}>
                <IconRefresh size={11} />
                {t("重试")}
              </button>
            </div>
          </div>
        ) : (
          <div className="nx-hint py-2 text-[12px]" aria-busy="true">
            {t("正在读取…")}
          </div>
        )
      ) : (
        <div className="flex flex-col gap-1.5">
          {jobs.length === 0 ? (
            <div className="nx-hint py-2 text-[12px]">{t("此会话暂无定时任务。")}</div>
          ) : (
            jobs.map((job) => {
              const status = jobStatus(job);
              const key = `${job.sessionId}/${job.id}`;
              const busy = actionBusy === key;
              const nextRun = nextRunText(job);
              const lastRun = formatTime(job.lastRunAt);
              const jobProfileId = job.modelProfileId ?? "";
              const jobProfile = jobProfileId ? profileById(jobProfileId) : undefined;
              const profileUnknown = !!jobProfileId && !!profilesView && !jobProfile;
              const profileKeySaved = !!jobProfile && profileKeyMasked(jobProfile);
              const editingThis =
                !!editingJob && editingJob.sessionId === job.sessionId && editingJob.id === job.id;
              const conflicted =
                !!replaceConflict &&
                (replaceConflict.oldKey === key || replaceConflict.newKey === key);
              return (
                <div key={key} className="flex items-start gap-2">
                  <div className="min-w-0 flex-1">
                    <div className="flex flex-wrap items-center gap-1.5">
                      <span className="min-w-0 break-words text-[12.5px] text-neutral-200">
                        {job.name?.trim() || clipText(job.prompt, 40)}
                      </span>
                      <span className={`nx-badge ${status.cls}`}>{status.label}</span>
                      {job.consecutiveFailures ? (
                        <span className="nx-badge nx-badge-red">
                          {t("连续失败")} {job.consecutiveFailures} {t("次")}
                        </span>
                      ) : null}
                      {profileUnknown && <span className="nx-badge nx-badge-red">{t("档案已删除")}</span>}
                      {profileKeySaved && <span className="nx-badge">{t("档案密钥已保存")}</span>}
                    </div>
                    <div className="nx-hint mt-0.5 text-[11px]">
                      <span className="font-mono">
                        {job.schedule}（{job.timezone}）
                      </span>{" "}
                      {t("· 超时")} {Math.round(cronTimeoutMs(job) / 1000)}s ·{" "}
                      {jobProfile ? (
                        <>{t("档案「")}{jobProfile.name}」</>
                      ) : jobProfileId ? (
                        <span className="break-words font-mono" title={jobProfileId}>
                          {t("档案")} {jobProfileId}
                        </span>
                      ) : (
                        <>{t("跟随当前档案")}{activeProfile ? `「${activeProfile.name}」` : ""}</>
                      )}
                    </div>
                    <div className="nx-hint text-[11px]">
                      {nextRun ? t("下次执行 {nextRun}", { nextRun }) : t("暂无待执行时间")}
                      {lastRun ? t(" · 上次执行 {lastRun}", { lastRun }) : ""}
                    </div>
                    {job.lastError && (
                      <div
                        className="mt-0.5 break-words font-mono text-[11px] text-red-300"
                        title={job.lastError}
                      >
                        {t("上次错误：")}{job.lastError}
                      </div>
                    )}
                  </div>
                  <div className="flex shrink-0 flex-col gap-1">
                    <button
                      className="nx-btn nx-btn-outline nx-btn-sm"
                      disabled={
                        actionBusy !== null ||
                        !!job.run.id ||
                        conflicted ||
                        (editingThis && registerOpen)
                      }
                      title={
                        job.run.id
                          ? t("任务正在执行，暂时不能编辑")
                          : conflicted
                            ? t("存在未保存成功的任务，请先按上方提示处理")
                            : editingThis && registerOpen
                              ? t("正在编辑")
                              : t("编辑定时任务")
                      }
                      onClick={() => void openEdit(job)}
                    >
                      <IconEdit size={11} />
                      {t("编辑")}
                    </button>
                    <button
                      className="nx-btn nx-btn-outline nx-btn-sm"
                      disabled={actionBusy !== null || (editingThis && registering)}
                      onClick={() => void toggleEnabled(job)}
                    >
                      {busy ? (
                        <IconRefresh size={11} className="animate-spin" />
                      ) : (
                        <IconClock size={11} />
                      )}
                      {job.enabled ? t("停用") : t("启用")}
                    </button>
                    <button
                      className="nx-btn nx-btn-danger nx-btn-sm"
                      disabled={actionBusy !== null || (editingThis && registerOpen)}
                      title={
                        editingThis && registerOpen
                          ? t("正在编辑，请先保存或取消")
                          : t("删除定时任务 {value}", { value: job.name?.trim() || job.id })
                      }
                      onClick={() => void unregister(job)}
                    >
                      <IconTrash size={11} />
                      {t("删除")}
                    </button>
                  </div>
                </div>
              );
            })
          )}
        </div>
      )}

      <p className="nx-hint mt-2 border-t border-neutral-800/60 pt-2 text-[11px]">
        {t("启用、停用和删除只影响所选任务。删除 AI 会话时，该会话的全部定时任务也会一并删除。")}
      </p>
    </div>
  );
}
