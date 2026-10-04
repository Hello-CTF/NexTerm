import { useCallback, useEffect, useRef, useState } from "react";
import { aiApi } from "../../ipc/commands";
import { cronApi, cronTimeoutMs, type CronJob } from "../../ipc/cron";
import { useUi } from "../../app/store";
import { DEMO } from "../../demo";
import { ask } from "../../ui/dialogs";
import { describeError } from "../../ui/errorText";
import {
  IconClock,
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

  const [conversations, setConversations] = useState<ConversationOption[] | null>(null);
  const [jobs, setJobs] = useState<CronJob[] | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [partialErrors, setPartialErrors] = useState<string[]>([]);

  const [registerOpen, setRegisterOpen] = useState(false);
  const [draft, setDraft] = useState<RegisterDraft | null>(null);
  const [registering, setRegistering] = useState(false);
  const [registerError, setRegisterError] = useState<string | null>(null);

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

      const results = await Promise.all(
        options.map(async (c) => {
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

  if (DEMO) return null;

  const titleOf = (sessionId: string) =>
    conversations?.find((c) => c.id === sessionId)?.title ?? sessionId;

  const openRegister = () => {
    setRegisterError(null);
    setDraft({
      sessionId: conversations?.[0]?.id ?? "",
      name: "",
      prompt: "",
      schedule: "",
      timezone: "UTC",
      timeoutSec: "",
    });
    setRegisterOpen(true);
  };

  const submitRegister = async () => {
    if (!draft || registering) return;
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
    setRegistering(true);
    setRegisterError(null);
    try {
      await cronApi.register({
        sessionId: draft.sessionId,
        ...(draft.name.trim() ? { name: draft.name.trim() } : {}),
        prompt: draft.prompt.trim(),
        schedule: draft.schedule.trim(),
        timezone: draft.timezone.trim() || "UTC",
        ...(timeoutSec !== null ? { timeoutMs: Math.round(timeoutSec * 1000) } : {}),
      });
      pushToast("success", "定时任务已注册");
      setRegisterOpen(false);
      setDraft(null);
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
        <button className="nx-btn nx-btn-ghost nx-btn-sm" onClick={() => void reload()}>
          <IconRefresh size={11} className={loading ? "animate-spin" : undefined} />
          刷新
        </button>
      </div>
      <p className="nx-hint mb-3.5">
        到点以某个 AI 会话为上下文发起无人值守执行。需要人工确认或被禁止的操作会被明确拒绝，
        原因记在该任务的「上次错误」里；任务持久保存在本机，重启后照常对账。
      </p>

      {registerOpen && draft ? (
        <div className="mb-3 flex flex-col gap-2 border-b border-neutral-800/60 pb-3">
          <div className="text-[12.5px] font-semibold text-neutral-200">注册定时任务</div>
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
              disabled={registering}
              onClick={() => void submitRegister()}
            >
              {registering ? "注册中…" : "注册"}
            </button>
            <button
              className="nx-btn nx-btn-ghost nx-btn-sm"
              disabled={registering}
              onClick={() => {
                setRegisterOpen(false);
                setDraft(null);
                setRegisterError(null);
              }}
            >
              取消
            </button>
          </div>
          <p className="nx-hint text-[11px]">
            表达式为 5 段 cron（分 时 日 月 周），时区缺省 UTC；超时缺省 60 秒。
            重新启用时从当前时刻起算下一次执行，不补跑停用期间的点。
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
              还没有 AI 会话 —— 定时任务挂在会话上，请先在 AI 侧栏开始一个会话。
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
                  </div>
                  <div className="nx-hint mt-0.5 text-[11px]">
                    会话「{titleOf(job.sessionId)}」 ·{" "}
                    <span className="font-mono">
                      {job.schedule}（{job.timezone}）
                    </span>{" "}
                    · 超时 {Math.round(cronTimeoutMs(job) / 1000)}s
                  </div>
                  <div className="nx-hint text-[11px]">
                    {nextRun ? `下次执行 ${nextRun}` : "没有待执行的点"}
                    {lastRun ? ` · 上次执行 ${lastRun}` : ""}
                  </div>
                  {job.lastError && (
                    <div
                      className="mt-0.5 break-words font-mono text-[11px] text-red-300"
                      title={job.lastError}
                    >
                      上次错误:{job.lastError}
                    </div>
                  )}
                </div>
                <div className="flex shrink-0 flex-col gap-1">
                  <button
                    className="nx-btn nx-btn-outline nx-btn-sm"
                    disabled={actionBusy !== null}
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
                    disabled={actionBusy !== null}
                    title={`注销定时任务 ${job.name?.trim() || job.id}`}
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
