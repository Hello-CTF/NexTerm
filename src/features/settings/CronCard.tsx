import { useCallback, useEffect, useRef, useState } from "react";
import { aiApi, modelApi, type ModelProfilesView } from "../../ipc/commands";
import { cronApi, cronTimeoutMs, type CronJob } from "../../ipc/cron";
import { useUi } from "../../app/store";
import { DEMO } from "../../demo";
import { ask } from "../../ui/dialogs";
import { describeError } from "../../ui/errorText";
import { profileKeyUnavailable as profileKeyMasked } from "../ai/modelLifecycle";
import {
  IconClock,
  IconEdit,
  IconPlus,
  IconRefresh,
  IconTrash,
  IconXCircle,
} from "../../ui/icons";

type ConversationOption = { id: string; title: string };

type RegisterDraft = {
  sessionId: string;
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
  sessions: string[];
  gen: number;
  error: string;
};

function jobStatus(job: CronJob): { label: string; cls: string } {
  if (job.run.id) return { label: "执行中", cls: "nx-badge-blue" };
  if (job.circuitOpenUntil && Date.parse(job.circuitOpenUntil) > Date.now()) {
    return { label: "熔断中", cls: "nx-badge-amber" };
  }
  if (job.lastError) return { label: "上次失败", cls: "nx-badge-red" };
  if (!job.enabled) return { label: "已停用", cls: "" };
  return { label: "等待执行", cls: "nx-badge-green" };
}

function formatTime(rfc3339: string | undefined): string | null {
  if (!rfc3339) return null;
  const ms = Date.parse(rfc3339);
  if (Number.isNaN(ms)) return null;
  return new Date(ms).toLocaleString();
}

export function CronCard() {
  const { pushToast } = useUi();
  const profilesRevision = useUi((s) => s.modelProfilesRevision);

  const [conversations, setConversations] = useState<ConversationOption[] | null>(null);
  const [jobs, setJobs] = useState<CronJob[] | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [partialErrors, setPartialErrors] = useState<string[]>([]);

  const [profilesView, setProfilesView] = useState<ModelProfilesView | null>(null);
  const [profilesError, setProfilesError] = useState<string | null>(null);

  const [registerOpen, setRegisterOpen] = useState(false);
  const [draft, setDraft] = useState<RegisterDraft | null>(null);
  const [editingJob, setEditingJob] = useState<CronJob | null>(null);
  const [registering, setRegistering] = useState(false);
  const [registerError, setRegisterError] = useState<string | null>(null);

  const replaceConflictRef = useRef<ReplaceConflict | null>(null);
  const [replaceConflict, setReplaceConflictState] = useState<ReplaceConflict | null>(null);
  const applyReplaceConflict = useCallback((conflict: ReplaceConflict | null) => {
    replaceConflictRef.current = conflict;
    setReplaceConflictState(conflict);
  }, []);

  const [jobsGen, setJobsGen] = useState(0);
  const [coveredSessionIds, setCoveredSessionIds] = useState<string[]>([]);

  const [actionBusy, setActionBusy] = useState<string | null>(null);

  const aliveRef = useRef(true);
  const loadGenRef = useRef(0);
  useEffect(() => {
    aliveRef.current = true;
    return () => {
      aliveRef.current = false;
      loadGenRef.current++;
    };
  }, []);

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
    if (DEMO) return;
    void loadProfiles();
  }, [loadProfiles, profilesRevision]);

  const reload = useCallback(async () => {
    const gen = ++loadGenRef.current;
    setLoading(true);
    setError(null);
    setPartialErrors([]);
    try {
      const list = await aiApi.conversationList();
      if (!aliveRef.current || gen !== loadGenRef.current) return;
      const options = list.map((c) => ({ id: c.id, title: c.title }));
      setConversations(options);

      const conflict = replaceConflictRef.current;
      const listedIds = new Set(options.map((c) => c.id));
      const targets = [
        ...options,
        ...(conflict
          ? conflict.sessions.filter((s) => !listedIds.has(s)).map((s) => ({ id: s, title: s }))
          : []),
      ];
      const results = await Promise.all(
        targets.map(async (c) => {
          try {
            const sessionJobs = await cronApi.list(c.id);
            return { ok: true as const, id: c.id, title: c.title, jobs: sessionJobs ?? [] };
          } catch (e) {
            return { ok: false as const, id: c.id, title: c.title, message: describeError(e) };
          }
        }),
      );
      if (!aliveRef.current || gen !== loadGenRef.current) return;
      const failed = results.filter((r) => !r.ok);
      const merged = results.flatMap((r) => (r.ok ? r.jobs : []));
      setJobs(merged);
      setJobsGen(gen);
      setCoveredSessionIds(results.filter((r) => r.ok).map((r) => r.id));
      if (failed.length > 0) {
        const detail = failed.map((f) => `「${f.title}」：${f.message}`).join("；");
        if (failed.length === results.length) {
          setJobs(null);
          setError(`读取定时任务失败：${detail}`);
        } else {
          setPartialErrors([`部分会话的任务读取失败：${detail}`]);
        }
      }
    } catch (e) {
      if (!aliveRef.current || gen !== loadGenRef.current) return;
      setJobs(null);
      setError(describeError(e));
    } finally {
      if (aliveRef.current && gen === loadGenRef.current) setLoading(false);
    }
  }, []);

  useEffect(() => {
    if (DEMO) return;
    void reload();
  }, [reload]);

  useEffect(() => {
    if (!replaceConflict || !jobs) return;
    if (jobsGen <= replaceConflict.gen) return;
    if (!replaceConflict.sessions.every((s) => coveredSessionIds.includes(s))) return;
    const present = (key: string) => jobs.some((j) => `${j.sessionId}/${j.id}` === key);
    if (!present(replaceConflict.oldKey) || !present(replaceConflict.newKey)) {
      applyReplaceConflict(null);
    }
  }, [jobs, jobsGen, coveredSessionIds, replaceConflict, applyReplaceConflict]);

  if (DEMO) return null;

  const titleOf = (sessionId: string) =>
    conversations?.find((c) => c.id === sessionId)?.title ?? sessionId;

  const profileById = (id: string) => profilesView?.profiles.find((p) => p.id === id);
  const activeProfile = profilesView?.activeId ? profileById(profilesView.activeId) : undefined;
  const editActionPending =
    !!editingJob && actionBusy === `${editingJob.sessionId}/${editingJob.id}`;
  const profilesState = profilesError ? "error" : profilesView ? "ready" : "loading";
  const followActiveLabel = () => {
    if (profilesState === "error") return "跟随当前激活档案（读取失败，状态未知）";
    if (profilesState === "loading") return "跟随当前激活档案（读取中…）";
    return activeProfile
      ? `跟随当前激活档案「${activeProfile.name}」`
      : "跟随当前激活档案（当前未设置）";
  };
  const unresolvedProfileLabel = (id: string) => {
    if (profilesState === "error") return `已存档案（读取失败，状态未知）· ${id}`;
    if (profilesState === "loading") return `已存档案（读取中…）· ${id}`;
    return `未知档案（可能已删除）· ${id}`;
  };

  const openRegister = () => {
    setRegisterError(null);
    setEditingJob(null);
    setDraft({
      sessionId: conversations?.[0]?.id ?? "",
      name: "",
      prompt: "",
      schedule: "",
      timezone: "UTC",
      timeoutSec: "",
      modelProfileId: "",
    });
    setRegisterOpen(true);
  };

  const openEdit = (job: CronJob) => {
    setRegisterError(null);
    setEditingJob(job);
    setDraft({
      sessionId: job.sessionId,
      name: job.name ?? "",
      prompt: job.prompt,
      schedule: job.schedule,
      timezone: job.timezone,
      timeoutSec: job.timeout > 0 ? String(Math.round(cronTimeoutMs(job) / 1000)) : "",
      modelProfileId: job.modelProfileId ?? "",
    });
    setRegisterOpen(true);
  };

  const submitRegister = async () => {
    if (!draft || registering) return;
    if (editingJob && actionBusy === `${editingJob.sessionId}/${editingJob.id}`) return;
    if (!draft.sessionId) {
      setRegisterError("先选择一个 AI 会话");
      return;
    }
    if (!draft.prompt.trim()) {
      setRegisterError("任务内容（提示词）不能为空");
      return;
    }
    if (!draft.schedule.trim()) {
      setRegisterError("cron 表达式不能为空");
      return;
    }
    const timeoutSec = draft.timeoutSec.trim() === "" ? null : Number(draft.timeoutSec);
    if (timeoutSec !== null && (!Number.isFinite(timeoutSec) || timeoutSec <= 0)) {
      setRegisterError("超时必须是正数（秒）");
      return;
    }
    const modelProfileId = draft.modelProfileId.trim();
    if (
      modelProfileId &&
      profilesView &&
      !profilesView.profiles.some((p) => p.id === modelProfileId)
    ) {
      setRegisterError("所选模型档案已不存在，请重新选择");
      return;
    }
    setRegistering(true);
    setRegisterError(null);
    try {
      if (!editingJob) {
        await cronApi.register({
          sessionId: draft.sessionId,
          ...(draft.name.trim() ? { name: draft.name.trim() } : {}),
          prompt: draft.prompt.trim(),
          schedule: draft.schedule.trim(),
          timezone: draft.timezone.trim() || "UTC",
          ...(timeoutSec !== null ? { timeoutMs: Math.round(timeoutSec * 1000) } : {}),
          ...(modelProfileId ? { modelProfileId } : {}),
        });
        pushToast("success", "定时任务已注册");
        setRegisterOpen(false);
        setDraft(null);
        await reload();
        return;
      }
      const created = await cronApi.register({
        sessionId: draft.sessionId,
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
            `保存失败：旧任务注销未成功（${describeError(e)}）。已清理重建的新任务，旧任务保持原样，可重试保存。`,
          );
        } catch (rollbackError) {
          applyReplaceConflict({
            label: draft.name.trim() || draft.prompt.trim().slice(0, 40),
            oldKey: `${editingJob.sessionId}/${editingJob.id}`,
            newKey: `${created.sessionId}/${created.id}`,
            sessions: [...new Set([editingJob.sessionId, created.sessionId])],
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
          pushToast("error", `任务已重建但启用失败：${describeError(e)}；任务当前为停用状态`);
          setRegisterOpen(false);
          setDraft(null);
          setEditingJob(null);
          await reload();
          return;
        }
      }
      pushToast("success", "定时任务已更新");
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
      pushToast("success", updated.enabled ? "任务已启用" : "任务已停用");
    } catch (e) {
      pushToast("error", `${job.enabled ? "停用" : "启用"}失败：${describeError(e)}`);
      if (aliveRef.current) await reload();
    } finally {
      if (aliveRef.current) setActionBusy(null);
    }
  };

  const unregister = async (job: CronJob) => {
    const display = job.name?.trim() || job.prompt.slice(0, 40);
    const ok = await ask(
      `注销定时任务「${display}」？\n所属会话：${titleOf(job.sessionId)}\n表达式：${job.schedule}（${job.timezone}）\n\n注销后到点不再执行，且不可恢复。`,
      { title: "注销定时任务", kind: "warning" },
    );
    if (!ok) return;
    const key = `${job.sessionId}/${job.id}`;
    setActionBusy(key);
    try {
      await cronApi.unregister(job.sessionId, job.id);
      pushToast("success", "定时任务已注销");
      await reload();
    } catch (e) {
      pushToast("error", `注销失败：${describeError(e)}`);
    } finally {
      if (aliveRef.current) setActionBusy(null);
    }
  };

  return (
    <section className="nx-card">
      <div className="mb-1 flex items-center gap-2">
        <IconClock size={15} className="text-neutral-400" />
        <span className="nx-card-title">定时任务</span>
        {jobs && <span className="nx-badge">{jobs.length} 个</span>}
        <div className="nx-spacer" />
        <button
          className="nx-btn nx-btn-ghost nx-btn-sm"
          onClick={() => {
            void reload();
            void loadProfiles();
          }}
        >
          <IconRefresh size={11} className={loading ? "animate-spin" : undefined} />
          刷新
        </button>
      </div>
      <p className="nx-hint mb-3.5">
        到点以某个 AI 会话为上下文发起无人值守执行。需要人工确认或被禁止的操作会被明确拒绝，
        原因记在该任务的「上次错误」里；任务持久保存在本机，重启后继续生效。
      </p>

      {registerOpen && draft ? (
        <div className="mb-3 flex flex-col gap-2 border-b border-neutral-800/60 pb-3">
          <div className="text-[12.5px] font-semibold text-neutral-200">
            {editingJob ? "编辑定时任务" : "注册定时任务"}
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <label className="nx-hint text-[11px]" htmlFor="cron-register-session">
              所属会话
            </label>
            <select
              id="cron-register-session"
              className="nx-select nx-input-sm min-w-0 flex-1"
              aria-label="所属 AI 会话"
              value={draft.sessionId}
              onChange={(e) => setDraft({ ...draft, sessionId: e.target.value })}
            >
              {(conversations ?? []).map((c) => (
                <option key={c.id} value={c.id}>
                  {c.title}
                </option>
              ))}
            </select>
          </div>
          <input
            className="nx-input nx-input-sm"
            placeholder="名称（可选），例如 磁盘巡检"
            aria-label="任务名称"
            value={draft.name}
            onChange={(e) => setDraft({ ...draft, name: e.target.value })}
          />
          <textarea
            className="nx-input min-h-[60px] font-mono text-[12px]"
            placeholder="到点要执行的任务内容（提示词）"
            aria-label="任务提示词"
            value={draft.prompt}
            onChange={(e) => setDraft({ ...draft, prompt: e.target.value })}
          />
          <div className="flex flex-wrap items-center gap-2">
            <label className="nx-hint text-[11px]" htmlFor="cron-register-model-profile">
              模型档案
            </label>
            <select
              id="cron-register-model-profile"
              className="nx-select nx-input-sm min-w-0 flex-1"
              aria-label="模型档案"
              value={draft.modelProfileId}
              onChange={(e) => setDraft({ ...draft, modelProfileId: e.target.value })}
            >
              <option value="">{followActiveLabel()}</option>
              {draft.modelProfileId && !profileById(draft.modelProfileId) && (
                <option value={draft.modelProfileId}>
                  {unresolvedProfileLabel(draft.modelProfileId)}
                </option>
              )}
              {(profilesView?.profiles ?? []).map((p) => (
                <option key={p.id} value={p.id}>
                  {`${p.name} · ${p.model}${profileKeyMasked(p) ? "（密钥已保存）" : ""}`}
                </option>
              ))}
            </select>
          </div>
          {profilesError && (
            <div className="nx-hint text-[11px] text-red-300" role="alert">
              模型档案读取失败：{profilesError}（档案列表可能不完整；「跟随当前激活档案」不受影响）
            </div>
          )}
          {editingJob &&
            editingJob.modelProfileId &&
            !profileById(editingJob.modelProfileId) &&
            profilesState !== "loading" && (
              <div className="nx-hint text-[11px] text-amber-300">
                {profilesState === "error"
                  ? "该任务保存的模型档案状态未知（读取失败）；请刷新重试，或改选其它档案。"
                  : "该任务保存的模型档案已不存在（可能已删除）；请选择其它档案，或改回「跟随当前激活档案」。"}
              </div>
            )}
          <div className="flex flex-wrap items-center gap-2">
            <input
              className="nx-input nx-input-sm w-[130px] font-mono"
              placeholder="0 2 * * *"
              aria-label="cron 表达式"
              value={draft.schedule}
              onChange={(e) => setDraft({ ...draft, schedule: e.target.value })}
            />
            <input
              className="nx-input nx-input-sm w-[110px]"
              placeholder="时区 UTC"
              aria-label="时区"
              value={draft.timezone}
              onChange={(e) => setDraft({ ...draft, timezone: e.target.value })}
            />
            <input
              className="nx-input nx-input-sm w-[110px]"
              placeholder="超时（秒）"
              aria-label="单次执行超时（秒）"
              value={draft.timeoutSec}
              onChange={(e) => setDraft({ ...draft, timeoutSec: e.target.value })}
            />
          </div>
          <div className="flex items-center gap-2">
            <div className="nx-spacer" />
            <button
              className="nx-btn nx-btn-primary nx-btn-sm"
              disabled={registering || editActionPending}
              title={editActionPending ? "该行启停保存中，完成后再保存" : undefined}
              onClick={() => void submitRegister()}
            >
              {registering
                ? editingJob
                  ? "保存中…"
                  : "注册中…"
                : editingJob
                  ? "保存"
                  : "注册"}
            </button>
            <button
              className="nx-btn nx-btn-ghost nx-btn-sm"
              disabled={registering}
              onClick={() => {
                setRegisterOpen(false);
                setDraft(null);
                setEditingJob(null);
                setRegisterError(null);
              }}
            >
              取消
            </button>
          </div>
          <p className="nx-hint text-[11px]">
            表达式为 5 段 cron（分 时 日 月 周），时区缺省 UTC；超时缺省 60 秒。
            重新启用时从当前时刻起算下一次执行，不补跑停用期间的点。
            {editingJob
              ? " 保存会以这些值重新创建任务：任务标识与执行历史不保留，下次执行从当前时刻起算。"
              : ""}
          </p>
          {registerError && (
            <div className="nx-alert nx-alert-danger flex items-start gap-2 text-[12px]" role="alert">
              <IconXCircle size={13} className="mt-0.5 shrink-0" />
              <span className="min-w-0 break-words">{registerError}</span>
            </div>
          )}
        </div>
      ) : (
        <div className="mb-3">
          <button
            className="nx-btn nx-btn-outline nx-btn-sm"
            disabled={!conversations || conversations.length === 0}
            title={
              conversations && conversations.length === 0
                ? "还没有 AI 会话，先在 AI 侧栏开始一个会话"
                : undefined
            }
            onClick={openRegister}
          >
            <IconPlus size={11} />
            注册定时任务
          </button>
          {conversations && conversations.length === 0 && (
            <span className="nx-hint ml-2 text-[11px]">
              还没有 AI 会话：定时任务挂在会话上，请先在 AI 侧栏开始一个会话。
            </span>
          )}
        </div>
      )}

      {jobs === null ? (
        error ? (
          <div className="nx-alert nx-alert-danger flex items-start gap-2" role="alert">
            <IconXCircle size={13} className="mt-0.5 shrink-0" />
            <div className="min-w-0 flex-1">
              <div className="break-words">{error}</div>
              <button className="nx-btn nx-btn-outline nx-btn-sm mt-2" onClick={() => void reload()}>
                <IconRefresh size={11} />
                重试
              </button>
            </div>
          </div>
        ) : (
          <div className="nx-hint py-2 text-[12px]" aria-busy="true">
            读取中…
          </div>
        )
      ) : (
        <div className="flex flex-col gap-1.5">
          {replaceConflict && (
            <div
              className="nx-alert nx-alert-danger flex items-start gap-2 text-[12px]"
              role="alert"
            >
              <IconXCircle size={13} className="mt-0.5 shrink-0" />
              <div className="min-w-0 flex-1">
                <div className="break-words">
                  编辑「{replaceConflict.label}」未完成：旧任务保留，重建的新任务（已停用）也未能自动清理（
                  {replaceConflict.error}）。两条任务不会同时执行，但请手动注销其中一条。
                </div>
                <div className="mt-1 break-words font-mono text-[11px]">
                  旧 {replaceConflict.oldKey} · 新 {replaceConflict.newKey}
                </div>
                <button
                  className="nx-btn nx-btn-outline nx-btn-sm mt-2"
                  onClick={() => void reload()}
                >
                  <IconRefresh size={11} />
                  刷新
                </button>
              </div>
            </div>
          )}
          {partialErrors.map((message) => (
            <div
              key={message}
              className="nx-alert nx-alert-danger flex items-center gap-2 text-[12px]"
              role="alert"
            >
              <IconXCircle size={13} className="shrink-0" />
              <span className="min-w-0 break-words">{message}</span>
            </div>
          ))}
          {jobs.length === 0 ? (
            partialErrors.length === 0 ? (
              <div className="nx-hint py-2 text-[12px]">还没有定时任务。</div>
            ) : (
              <div className="nx-hint py-2 text-[12px]">
                成功读取的会话里没有任何任务；失败会话的任务未知，不能据此断定没有任务。
                <button
                  className="nx-btn nx-btn-outline nx-btn-sm ml-2"
                  onClick={() => void reload()}
                >
                  <IconRefresh size={11} />
                  重试
                </button>
              </div>
            )
          ) : (
            jobs.map((job) => {
            const status = jobStatus(job);
            const key = `${job.sessionId}/${job.id}`;
            const busy = actionBusy === key;
            const nextRun = formatTime(job.nextRunAt);
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
                      {job.name?.trim() || job.prompt.slice(0, 40)}
                    </span>
                    <span className={`nx-badge ${status.cls}`}>{status.label}</span>
                    {job.consecutiveFailures ? (
                      <span className="nx-badge nx-badge-red">
                        连续失败 {job.consecutiveFailures} 次
                      </span>
                    ) : null}
                    {profileUnknown && <span className="nx-badge nx-badge-red">档案已删除</span>}
                    {profileKeySaved && <span className="nx-badge">档案密钥已保存</span>}
                  </div>
                  <div className="nx-hint mt-0.5 text-[11px]">
                    会话「{titleOf(job.sessionId)}」 ·{" "}
                    <span className="font-mono">
                      {job.schedule}（{job.timezone}）
                    </span>{" "}
                    · 超时 {Math.round(cronTimeoutMs(job) / 1000)}s ·{" "}
                    {jobProfile ? (
                      <>档案「{jobProfile.name}」</>
                    ) : jobProfileId ? (
                      <span className="break-words font-mono" title={jobProfileId}>
                        档案 {jobProfileId}
                      </span>
                    ) : (
                      <>跟随激活档案{activeProfile ? `「${activeProfile.name}」` : ""}</>
                    )}
                  </div>
                  <div className="nx-hint text-[11px]">
                    {nextRun ? `下次执行 ${nextRun}` : "没有待执行的时间点"}
                    {lastRun ? ` · 上次执行 ${lastRun}` : ""}
                  </div>
                  {job.lastError && (
                    <div
                      className="mt-0.5 break-words font-mono text-[11px] text-red-300"
                      title={job.lastError}
                    >
                      上次错误：{job.lastError}
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
                        ? "执行中不能编辑"
                        : conflicted
                          ? "存在未完成的编辑冲突，请先手动处理"
                          : editingThis && registerOpen
                            ? "正在编辑"
                            : "编辑定时任务"
                    }
                    onClick={() => openEdit(job)}
                  >
                    <IconEdit size={11} />
                    编辑
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
                    {job.enabled ? "停用" : "启用"}
                  </button>
                  <button
                    className="nx-btn nx-btn-danger nx-btn-sm"
                    disabled={actionBusy !== null || (editingThis && registerOpen)}
                    title={
                      editingThis && registerOpen
                        ? "编辑中，请先保存或取消编辑"
                        : `注销定时任务 ${job.name?.trim() || job.id}`
                    }
                    onClick={() => void unregister(job)}
                  >
                    <IconTrash size={11} />
                    注销
                  </button>
                </div>
              </div>
            );
          })
          )}
        </div>
      )}

      <p className="nx-hint mt-3 border-t border-neutral-800/60 pt-2 text-[11px]">
        启停只影响这一条任务；注销是唯一删除路径，需逐个确认。管理任何会话的任务都不会
        改动其它会话的任务。
      </p>
    </section>
  );
}
