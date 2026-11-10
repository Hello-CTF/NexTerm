import { useCallback, useEffect, useId, useMemo, useRef, useState, type KeyboardEvent as ReactKeyboardEvent } from "react";
import { useQuery } from "@tanstack/react-query";
import { useSessionList } from "../../app/store";
import { assetApi, type Asset, type SessionInfo } from "../../ipc/commands";
import type { ExecBatchResult, ExecBatchResultRow, ExecBatchStatus } from "../../ipc/execBatchApi";
import {
  createRunbookId,
  runbookApi,
  type RunbookConfirmationPolicy,
  type RunbookHistory,
  type RunbookRunState,
  type RunbookStep,
  type RunbookStepState,
} from "../../ipc/runbookApi";
import { describeError } from "../../ui/errorText";
import { isImeKeyEvent, trapOverlayTab, useOverlayFocus } from "../../ui/DialogHost";
import { IconPlay, IconStop, IconXCircle } from "../../ui/icons";
import type { Prompt } from "../../ipc/generated/types";
import { ScriptActivity, ScriptDirectiveHelp, ScriptPrompts } from "./RunbookScriptActivity";
import { ProductionConfirmationDialog } from "../execbatch/ProductionConfirmationDialog";
import { TargetSetPicker, type TargetSetPickerTarget } from "../execbatch/TargetSetPicker";
import { assetTagValues } from "./assetSearch";
import { useSnippetVariablePrompt } from "./SnippetVariablePrompt";
import { analyzeSnippetSteps, renderSnippetForTargets, type SnippetVariableValues } from "./snippetVariables";
import { isMultiStep, type Snippet } from "./snippetInsert";
import { Select } from "../../ui/Select";
import { t } from "../../i18n";

const MAX_TARGETS = 64;

// A single-command snippet runs as one step named after the snippet.
function snippetRunSteps(snippet: Snippet): RunbookStep[] {
  if (isMultiStep(snippet)) {
    return (snippet.steps ?? []).map((step, index) => ({
      id: step.id,
      name: step.name.trim() || t("步骤 {value}", { value: index + 1 }),
      ...(step.description ? { description: step.description } : {}),
      command: step.command,
      ...(step.confirm ? { confirm: true } : {}),
      ...(step.interactive ? { interactive: true } : {}),
    }));
  }
  return [{ id: `${snippet.id}-step-1`, name: snippet.name, command: snippet.body }];
}

const RUN_STATUS_LABELS: Record<string, string> = {
  awaiting_render: t("等待填写变量"),
  awaiting_confirmation: t("等待确认"),
  running: t("执行中"),
  awaiting_production_confirmation: t("等待生产确认"),
  success: t("成功"),
  failed: t("失败"),
  canceled: t("已取消"),
};

const STEP_STATUS_LABELS: Record<string, string> = {
  ...RUN_STATUS_LABELS,
  pending: t("待执行"),
  skipped: t("已跳过"),
};

const TARGET_STATUS_LABELS: Record<ExecBatchStatus, string> = {
  success: t("成功"),
  exit_error: t("命令失败"),
  transport_error: t("连接出错"),
  timeout: t("超时"),
  canceled: t("已取消"),
};

function terminalRun(status: string): boolean {
  return status === "success" || status === "failed" || status === "canceled";
}

function statusClass(status: string): string {
  if (status === "success") return "text-green-300";
  if (status === "canceled" || status === "pending" || status === "skipped") return "text-neutral-400";
  if (status === "timeout" || status.includes("confirmation")) return "text-amber-300";
  if (status === "failed" || status === "exit_error" || status === "transport_error") return "text-red-300";
  return "text-blue-300";
}

function collectTargets(sessions: readonly SessionInfo[], assets: readonly Asset[]): TargetSetPickerTarget[] {
  const assetsById = new Map(assets.map((asset) => [asset.id, asset]));
  return sessions
    .filter((session) => (session.kind === "ssh" || session.kind === "device") && session.status === "connected")
    .map((session) => {
      const asset = session.assetId ? assetsById.get(session.assetId) : undefined;
      return {
        id: session.id,
        label: asset?.name.trim() || session.name,
        sessionName: session.name,
        endpoint: asset?.host ?? (session.kind === "device" ? t("受管设备 · 执行前需确认") : null),
        tags: assetTagValues(asset?.tags ?? ""),
        workspaceNames: [],
        envCount: 0,
      };
    });
}

function LiveResultMatrix({ step }: { step: RunbookStepState }) {
  if (step.results.length === 0 && !step.error) return null;
  return (
    <div className="mt-2 overflow-x-auto" data-testid={`runbook-step-results-${step.number}`}>
      {step.error && <div className="mb-2 whitespace-pre-wrap break-all text-[11px] text-red-300">{step.error}</div>}
      {step.results.length > 0 && (
        <table className="nx-table min-w-[560px]">
          <thead><tr><th>{t("目标")}</th><th>{t("状态")}</th><th>{t("退出码")}</th><th>{t("输出 / 错误")}</th></tr></thead>
          <tbody>
            {step.results.map((result) => (
              <LiveResultRow key={result.sessionId} result={result} />
            ))}
          </tbody>
        </table>
      )}
    </div>
  );
}

function LiveResultRow({ result }: { result: ExecBatchResult }) {
  return (
    <tr>
      <td>
        <div className="truncate text-neutral-300" title={result.sessionId}>{result.sessionName}</div>
      </td>
      <td className={statusClass(result.status)}>{TARGET_STATUS_LABELS[result.status as ExecBatchStatus] ?? result.status}</td>
      <td className="nx-mono">{result.exitCode ?? "—"}{result.signal ? ` (${result.signal})` : ""}</td>
      <td className="max-w-80">
        {result.error && <div className="whitespace-pre-wrap break-all text-red-300">{result.error}</div>}
        {(result.stdout || result.stderr) && (
          <details>
            <summary className="cursor-pointer text-neutral-400">{t("查看输出")}</summary>
            {result.stdout && <pre className="nx-pre mt-1 max-h-32 overflow-auto whitespace-pre-wrap break-all">{result.stdout}</pre>}
            {result.stderr && <pre className="nx-pre mt-1 max-h-32 overflow-auto whitespace-pre-wrap break-all">{result.stderr}</pre>}
          </details>
        )}
      </td>
    </tr>
  );
}

function HistoryResult({ result }: { result: ExecBatchResultRow }) {
  return (
    <tr>
      <td title={result.sessionId}>{result.sessionName}</td>
      <td className={statusClass(result.status)}>{TARGET_STATUS_LABELS[result.status as ExecBatchStatus] ?? result.status}</td>
      <td className="nx-mono">{result.exitCode ?? "—"}</td>
      <td>{result.errorKind || "—"}</td>
    </tr>
  );
}

function RunHistoryView({ history }: { history: RunbookHistory }) {
  return (
    <div className="mt-3 border-t border-neutral-800 pt-3" data-testid="runbook-history">
      <div className="mb-2 flex flex-wrap gap-2 text-[11px] text-neutral-400">
        <span title={t("片段 {runbookId} · 运行 {runId}", { runbookId: history.runbookId, runId: history.runId })}>{t("本次运行")}</span>
        <span>{t("执行到第 {reachedStep} / {stepCount} 步", { reachedStep: history.reachedStep, stepCount: history.stepCount })}</span>
        <span className={statusClass(history.status)}>{RUN_STATUS_LABELS[history.status] ?? history.status}</span>
      </div>
      {history.batches.map((batch) => (
        <div key={batch.batch.id} className="mb-3 rounded border border-neutral-800 p-2">
          <div className="mb-1 text-[11px] text-neutral-400">
            <span title={t("批次 {id}", { id: batch.batch.id })}>{t("第")} {batch.batch.stepNumber} {t("步")}</span> · {RUN_STATUS_LABELS[batch.batch.status] ?? batch.batch.status}
          </div>
          <table className="nx-table min-w-[520px]">
            <thead><tr><th>{t("目标")}</th><th>{t("状态")}</th><th>{t("退出码")}</th><th>{t("失败原因")}</th></tr></thead>
            <tbody>{batch.results.map((result) => <HistoryResult key={result.sessionId} result={result} />)}</tbody>
          </table>
        </div>
      ))}
    </div>
  );
}

export function MultiHostRunPanel({ snippet, onClose, onBack }: { snippet: Snippet; onClose: () => void; onBack?: () => void }) {
  const sessions = useSessionList();
  const assetsQuery = useQuery({ queryKey: ["assets"], queryFn: () => assetApi.list(), retry: false });
  const targets = useMemo(() => collectTargets(sessions, assetsQuery.data ?? []), [sessions, assetsQuery.data]);
  const steps = useMemo(() => snippetRunSteps(snippet), [snippet]);
  const [selected, setSelected] = useState<string[]>([]);
  const [policy, setPolicy] = useState<RunbookConfirmationPolicy>("every_step");
  const [stopOnFailure, setStopOnFailure] = useState(true);
  const [run, setRun] = useState<RunbookRunState | null>(null);
  const [busy, setBusy] = useState(false);
  const [canceling, setCanceling] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [history, setHistory] = useState<RunbookHistory | null>(null);
  const [historyError, setHistoryError] = useState<string | null>(null);
  const [historyLoading, setHistoryLoading] = useState(false);
  const runRef = useRef<RunbookRunState | null>(null);
  const pendingRunIdRef = useRef<string | null>(null);
  const lifecycleEpochRef = useRef(0);
  const transitionRef = useRef(false);
  // One set of values is collected before the run and shared by every step.
  const valuesRef = useRef<SnippetVariableValues>({});
  const mountedRef = useRef(true);
  const modalRef = useRef<HTMLDivElement>(null);
  const titleId = useId();
  const layer = useOverlayFocus(true, modalRef);
  const { promptForValues, promptElement } = useSnippetVariablePrompt();

  const isActiveEpoch = (epoch: number) => mountedRef.current && lifecycleEpochRef.current === epoch;

  const applyRun = (next: RunbookRunState, epoch: number): RunbookRunState | null => {
    if (!isActiveEpoch(epoch)) return null;
    const current = runRef.current;
    if (current && current.runId !== next.runId) return null;
    if (!current && pendingRunIdRef.current !== next.runId) return null;
    if (current?.runId === next.runId && terminalRun(current.status) && !terminalRun(next.status)) return current;
    runRef.current = next;
    setRun(next);
    return next;
  };

  useEffect(() => {
    const available = new Set(targets.map((target) => target.id));
    setSelected((current) => {
      const next = current.filter((id) => available.has(id));
      return next.length === current.length ? current : next;
    });
  }, [targets]);

  const invalidateAndCancel = useCallback(() => {
    mountedRef.current = false;
    lifecycleEpochRef.current += 1;
    const pendingRunId = pendingRunIdRef.current;
    const current = runRef.current;
    pendingRunIdRef.current = null;
    runRef.current = null;
    const runIds = new Set<string>();
    if (pendingRunId) runIds.add(pendingRunId);
    if (current && !terminalRun(current.status)) runIds.add(current.runId);
    for (const runId of runIds) void runbookApi.cancel(runId).catch(() => undefined);
  }, []);

  useEffect(() => {
    mountedRef.current = true;
    return invalidateAndCancel;
  }, [invalidateAndCancel]);

  const prepareState = async (state: RunbookRunState, epoch: number): Promise<RunbookRunState | null> => {
    if (!isActiveEpoch(epoch)) return null;
    const step = state.steps[state.currentStep - 1];
    if (!step) throw new Error(t("当前步骤不存在"));
    const current = runRef.current;
    if (!isActiveEpoch(epoch) || !current || current.runId !== state.runId || terminalRun(current.status)) return null;
    const sessionsById = new Map(sessions.map((session) => [session.id, session]));
    const assetsById = new Map((assetsQuery.data ?? []).map((asset) => [asset.id, asset]));
    const targetsById = new Map(targets.map((target) => [target.id, target]));
    const rendered = renderSnippetForTargets(step.step.command, valuesRef.current, state.sessionIds.map((id) => {
      const session = sessionsById.get(id);
      const asset = session?.assetId ? assetsById.get(session.assetId) : undefined;
      return { id, label: targetsById.get(id)?.label ?? session?.name ?? id, asset };
    }));
    for (const target of rendered) {
      if (!target.command.trim()) throw new Error(t("目标「{id}」渲染后的命令不能为空", { id: target.id }));
      if (/[\r\n\0]/.test(target.command)) {
        throw new Error(t("目标「{id}」填入变量后的命令包含换行或空字符；多主机执行只支持单行命令", { id: target.id }));
      }
    }
    if (!isActiveEpoch(epoch)) return null;
    const prepared = await runbookApi.prepare({
      runId: state.runId,
      stepNumber: state.currentStep,
      commands: rendered.map((target) => ({ sessionId: target.id, command: target.command })),
    });
    return applyRun(prepared, epoch);
  };

  const advanceState = async (
    state: RunbookRunState,
    confirmed: boolean,
    epoch: number,
    productionConfirmation?: { confirmed: true; targetSetHash: string; commandHash: string; environmentNames?: string[] },
  ): Promise<RunbookRunState | null> => {
    if (!isActiveEpoch(epoch) || runRef.current?.runId !== state.runId) return null;
    const next = await runbookApi.advance({
      runId: state.runId,
      stepNumber: state.currentStep,
      confirmed,
      ...(productionConfirmation ? { productionConfirmation } : {}),
    });
    return applyRun(next, epoch);
  };

  const drive = async (initial: RunbookRunState, epoch: number) => {
    let state: RunbookRunState | null = initial;
    while (state && isActiveEpoch(epoch) && runRef.current?.runId === state.runId && !terminalRun(state.status)) {
      if (state.status === "awaiting_render") {
        state = await prepareState(state, epoch);
        continue;
      }
      if (
        state.status === "awaiting_confirmation" &&
        state.confirmationPolicy === "production_only" &&
        state.steps[state.currentStep - 1]?.step.confirm !== true
      ) {
        state = await advanceState(state, false, epoch);
        continue;
      }
      return;
    }
  };

  const transition = async (work: (epoch: number) => Promise<void>) => {
    if (transitionRef.current || !mountedRef.current) return;
    const epoch = lifecycleEpochRef.current;
    transitionRef.current = true;
    setBusy(true);
    setError(null);
    setHistoryError(null);
    try {
      await work(epoch);
    } catch (cause) {
      if (isActiveEpoch(epoch)) setError(describeError(cause));
    } finally {
      transitionRef.current = false;
      if (isActiveEpoch(epoch)) setBusy(false);
    }
  };

  const start = () => {
    if (selected.length === 0) {
      setError(t("请至少选择一个已连接的 SSH 或受管设备会话"));
      return;
    }
    setHistory(null);
    void transition(async (epoch) => {
      const analysis = analyzeSnippetSteps(steps.map((step) => step.command));
      if (analysis.unknownVariables.length > 0) {
        throw new Error(t("未知变量：{join}", { join: analysis.unknownVariables.map((variable) => `「${variable}」`).join("、") }));
      }
      const values = analysis.hasVariables
        ? await promptForValues(t("填写变量 · {name}", { name: snippet.name }), analysis.variables)
        : {};
      if (values === null) {
        if (isActiveEpoch(epoch)) setError(t("已取消填写变量，未开始执行"));
        return;
      }
      if (!isActiveEpoch(epoch)) return;
      valuesRef.current = values;
      const selectedIds = targets.filter((target) => selected.includes(target.id)).map((target) => target.id);
      const runId = createRunbookId();
      pendingRunIdRef.current = runId;
      try {
        const initial = await runbookApi.start({
          runId, snippetId: snippet.id,
          sessionIds: selectedIds,
          confirmationPolicy: policy,
          stopOnFailure,
          timeoutMs: 30000,
          maxConcurrent: 4,
        });
        if (!isActiveEpoch(epoch) || pendingRunIdRef.current !== runId) {
          void runbookApi.cancel(runId).catch(() => undefined);
          return;
        }
        const applied = applyRun(initial, epoch);
        if (!applied) {
          void runbookApi.cancel(runId).catch(() => undefined);
          return;
        }
        await drive(applied, epoch);
      } finally {
        if (pendingRunIdRef.current === runId) pendingRunIdRef.current = null;
      }
    });
  };

  const confirmStep = () => {
    if (!run) return;
    void transition(async (epoch) => {
      const next = await advanceState(run, true, epoch);
      if (next) await drive(next, epoch);
    });
  };

  const retryPrepare = () => {
    if (!run) return;
    void transition((epoch) => drive(run, epoch));
  };

  const confirmProduction = (environmentNames?: string[]) => {
    const challenge = run?.productionChallenge;
    if (!run || !challenge) return;
    void transition(async (epoch) => {
      const next = await advanceState(run, false, epoch, {
        confirmed: true,
        targetSetHash: challenge.targetSetHash,
        commandHash: challenge.commandHash,
        ...(environmentNames ? { environmentNames } : {}),
      });
      if (next) await drive(next, epoch);
    });
  };

  const cancelRun = async () => {
    const current = runRef.current;
    if (!current || terminalRun(current.status) || canceling) return;
    const epoch = lifecycleEpochRef.current;
    setCanceling(true);
    setError(null);
    try {
      const canceled = await runbookApi.cancel(current.runId);
      if (isActiveEpoch(epoch) && runRef.current?.runId === current.runId) applyRun(canceled, epoch);
    } catch (cause) {
      if (isActiveEpoch(epoch)) setError(t("取消失败：{value}", { value: describeError(cause) }));
    } finally {
      if (isActiveEpoch(epoch)) setCanceling(false);
    }
  };

  // A poll that started before, or overlaps, a respond may still carry the prompt that was just answered.
  const respondingRef = useRef(0);
  const respondGenerationRef = useRef(0);
  const runId = run?.runId;
  const polling = run?.status === "running";
  useEffect(() => {
    if (!runId || !polling) return undefined;
    // The advance call blocks while scripts run, so the live state (prompts,
    // sub-steps, progress) is read separately. Only running snapshots are applied:
    // the advance response stays the single source of every state transition.
    const epoch = lifecycleEpochRef.current;
    let inFlight = false;
    const timer = window.setInterval(() => {
      if (inFlight) return;
      inFlight = true;
      const generation = respondGenerationRef.current;
      void Promise.resolve(runbookApi.get(runId))
        .then((next) => {
          const current = runRef.current;
          if (respondingRef.current > 0 || respondGenerationRef.current !== generation) return;
          if (!next || next.status !== "running" || !current || current.runId !== runId || current.status !== "running") return;
          applyRun(next, epoch);
        })
        .catch(() => undefined)
        .finally(() => { inFlight = false; });
    }, 700);
    return () => window.clearInterval(timer);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [runId, polling]);

  const respondToPrompt = async (prompt: Prompt, value: string, applyAll: boolean) => {
    const current = runRef.current;
    if (!current) return;
    const epoch = lifecycleEpochRef.current;
    setError(null);
    respondingRef.current += 1;
    try {
      const next = await runbookApi.respond({ runId: current.runId, promptId: prompt.id, value, ...(applyAll ? { applyAll: true } : {}) });
      if (next && next.status === "running" && runRef.current?.status === "running") applyRun(next, epoch);
    } catch (cause) {
      if (isActiveEpoch(epoch)) setError(t("提交回答失败：{value}", { value: describeError(cause) }));
    } finally {
      respondingRef.current -= 1;
      respondGenerationRef.current += 1;
    }
  };

  const closePanel = () => {
    invalidateAndCancel();
    onClose();
  };

  const backToSnippets = () => {
    invalidateAndCancel();
    onBack?.();
  };

  const loadHistory = async () => {
    if (!run || historyLoading) return;
    const epoch = lifecycleEpochRef.current;
    const runId = run.runId;
    setHistoryLoading(true);
    setHistoryError(null);
    try {
      const result = await runbookApi.historyGet(runId);
      if (isActiveEpoch(epoch) && runRef.current?.runId === runId) setHistory(result);
    } catch (cause) {
      if (isActiveEpoch(epoch)) setHistoryError(t("未能读取历史：{value}", { value: describeError(cause) }));
    } finally {
      if (isActiveEpoch(epoch)) setHistoryLoading(false);
    }
  };

  const onModalKeyDown = (event: ReactKeyboardEvent<HTMLDivElement>) => {
    event.stopPropagation();
    if (!layer.isTopmost()) return;
    if (event.key === "Escape" && !event.repeat && !isImeKeyEvent(event)) {
      event.preventDefault();
      if (onBack) backToSnippets();
      else closePanel();
      return;
    }
    trapOverlayTab(event, modalRef.current);
  };

  const active = run !== null && !terminalRun(run.status);

  return (
    <div className="nx-overlay" onClick={(event) => { event.stopPropagation(); if (onBack) backToSnippets(); else closePanel(); }}>
      <div
        ref={modalRef}
        className="nx-modal flex nx-modal-lg max-h-[88dvh] flex-col"
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        tabIndex={-1}
        onClick={(event) => event.stopPropagation()}
        onKeyDown={onModalKeyDown}
      >
        <div className="nx-modal-header shrink-0 flex-wrap">
          {onBack && (
            <button type="button" className="nx-btn nx-btn-ghost nx-btn-sm" onClick={backToSnippets}>
              {t("← 命令片段")}
            </button>
          )}
          <span id={titleId} className="text-[13px] font-semibold text-neutral-100">{t("在多台主机执行 · {name}", { name: snippet.name })}</span>
          <span className="nx-hint hidden sm:inline">{t("按顺序在多台主机上执行片段的每一步，默认每一步都先确认")}</span>
        </div>
        <div className="nx-modal-body min-h-0 flex-1 overflow-y-auto">
          {error && <div role="alert" className="mb-3 flex items-start gap-1.5 text-[12px] text-red-400"><IconXCircle size={13} className="mt-0.5 shrink-0" />{error}</div>}
          {!run ? (
            <div className="grid gap-4 md:grid-cols-[minmax(0,1fr)_280px]">
            <div className="min-w-0">
              {snippet.description && <p className="mb-2 text-[11.5px] text-neutral-400">{snippet.description}</p>}
              {snippet.steps?.some((step) => step.waitFor) && (
                <p className="nx-hint mb-2" role="note" data-testid="multi-host-wait-ignored">
                  {t("多主机执行时每一步都会等上一步的命令结束再开始，步骤里的“等待输出”条件不会使用。")}
                </p>
              )}
              <ScriptDirectiveHelp />
              <div className="mb-3 flex flex-col gap-2" aria-label={t("片段步骤")}>
                {steps.map((step, index) => (
                  <div key={step.id} className="rounded border border-neutral-800 p-2.5" data-testid={`multi-host-step-${index + 1}`}>
                    <div className="mb-1.5 flex flex-wrap items-center gap-2 text-[12px]">
                      <span className="nx-badge">{index + 1}</span>
                      <span className="font-medium text-neutral-200">{step.name}</span>
                      {step.confirm && <span className="nx-badge text-amber-300">{t("执行前确认")}</span>}
                      {step.interactive && <span className="nx-badge">{t("交互指令")}</span>}
                    </div>
                    {step.description && <p className="mb-1.5 text-[11px] text-neutral-500">{step.description}</p>}
                    <pre className="nx-pre whitespace-pre-wrap break-all text-[11px]">{step.command}</pre>
                  </div>
                ))}
              </div>
            </div>
            <div className="min-w-0">
              <TargetSetPicker
                targets={targets}
                selectedIds={selected}
                onChange={setSelected}
                maxTargets={MAX_TARGETS}
                legend={t("在哪些主机上执行")}
                searchAriaLabel={t("搜索执行目标")}
                metadataError={assetsQuery.isError ? t("未能加载资产详情：{value}", { value: describeError(assetsQuery.error) }) : null}
              />
              <div className="mt-3 flex flex-col gap-2 text-[11px] text-neutral-400">
                <div className="flex flex-col gap-1">
                  <span>{t("执行前确认")}</span>
                  <Select
                    ariaLabel={t("执行前确认")}
                    value={policy}
                    options={[
                      { value: "every_step", label: t("每一步都确认") },
                      { value: "production_only", label: t("只确认标记了『执行前确认』的步骤和生产主机") },
                    ]}
                    onChange={(value) => setPolicy(value as RunbookConfirmationPolicy)}
                  />
                </div>
                <label className="flex items-center gap-1.5">
                  <input type="checkbox" className="accent-blue-500" checked={stopOnFailure} onChange={(event) => setStopOnFailure(event.target.checked)} />
                  {t("有主机失败就停止后续步骤（默认）")}
                </label>
              </div>
            </div>
            </div>
          ) : (
            <div data-testid="runbook-run-view">
              <div className="mb-3 flex flex-wrap items-center gap-2">
                <span className="font-medium text-neutral-200">{run.runbookName}</span>
                <span className={statusClass(run.status)}>{RUN_STATUS_LABELS[run.status] ?? run.status}</span>
                <span className="nx-hint">{t("第 {currentStep} / {stepCount} 步 · {value}", { currentStep: run.currentStep, stepCount: run.stepCount, value: run.stopOnFailure ? t("失败即停") : t("失败后继续") })}</span>
              </div>
              <div className="flex flex-col gap-2">
                {run.steps.map((step) => (
                  <section key={step.step.id} className={`rounded border p-2.5 ${step.number === run.currentStep && active ? "border-blue-900/70" : "border-neutral-800"}`}>
                    <div className="flex flex-wrap items-center gap-2 text-[12px]">
                      <span className="nx-badge">{step.number}</span>
                      <span className="font-medium text-neutral-200">{step.step.name}</span>
                      <span className={statusClass(step.status)}>{STEP_STATUS_LABELS[step.status] ?? step.status}</span>
                    </div>
                    {step.step.description && <p className="mt-1 text-[11px] text-neutral-500">{step.step.description}</p>}
                    {step.number === run.currentStep && step.status === "awaiting_confirmation" && (
                      <div className="mt-2" data-testid="runbook-step-confirmation">
                        <div className="mb-1 text-[11px] text-amber-300">{t("请确认下列命令；该步骤执行完成后才会进入下一步。")}</div>
                        {(step.renderedCommands ?? []).map((rendered) => (
                          <div key={rendered.sessionId} className="mb-1">
                            <div className="text-[10.5px] text-neutral-500" title={rendered.sessionId}>{targets.find((target) => target.id === rendered.sessionId)?.label ?? rendered.sessionId}</div>
                            <pre className="nx-pre whitespace-pre-wrap break-all text-[11px]">{rendered.command}</pre>
                          </div>
                        ))}
                      </div>
                    )}
                    <LiveResultMatrix step={step} />
                  </section>
                ))}
              </div>
              <ScriptPrompts prompts={run.prompts ?? []} disabled={canceling || terminalRun(run.status)} onRespond={respondToPrompt} />
              <ScriptActivity live={run.live ?? []} answers={run.answers ?? []} />
              {run.status === "awaiting_render" && !busy && (
                <button type="button" className="nx-btn mt-3" onClick={retryPrepare}>{t("重新准备当前步骤")}</button>
              )}
              {run.status === "awaiting_confirmation" && (
                <button type="button" className="nx-btn nx-btn-primary mt-3" disabled={busy || canceling} onClick={confirmStep}>
                  <IconPlay size={13} />{t("确认并执行第 {currentStep} 步", { currentStep: run.currentStep })}
                </button>
              )}
              {run.status === "running" && <div role="status" className="mt-3 text-[11px] text-blue-300" title={run.activeBatchId ? t("批次 {activeBatchId}", { activeBatchId: run.activeBatchId }) : undefined}>{t("正在执行第 {currentStep} 步…", { currentStep: run.currentStep })}</div>}
              {terminalRun(run.status) && run.steps.some((step) => step.batchIds.length > 0) && (
                <button type="button" className="nx-btn mt-3" disabled={historyLoading} onClick={() => void loadHistory()}>
                  {historyLoading ? t("正在读取历史…") : t("查看本次运行历史")}
                </button>
              )}
              {historyError && <div role="alert" className="mt-2 text-[11px] text-red-300">{historyError}</div>}
              {history && <RunHistoryView history={history} />}
            </div>
          )}
        </div>
        <div className="nx-modal-footer shrink-0">
          <span className="nx-hint mr-auto">{t("取消会停止当前步骤和后续步骤，不会关闭终端。")}</span>
          {run && terminalRun(run.status) && <button type="button" className="nx-btn" onClick={() => { runRef.current = null; setRun(null); setHistory(null); setError(null); }}>{t("返回")}</button>}
          {active && <button type="button" className="nx-btn nx-btn-danger" disabled={canceling} onClick={() => void cancelRun()}><IconStop size={12} />{canceling ? t("正在取消…") : t("取消执行")}</button>}
          {!run && <button type="button" className="nx-btn nx-btn-primary" disabled={busy || targets.length === 0} onClick={start}><IconPlay size={13} />{t("开始运行")}</button>}
          <button type="button" className="nx-btn nx-btn-ghost" onClick={closePanel}>{t("关闭")}</button>
        </div>
      </div>
      {promptElement}
      {run?.productionChallenge && run.status === "awaiting_production_confirmation" && (
        <ProductionConfirmationDialog
          key={`${run.productionChallenge.targetSetHash}:${run.productionChallenge.commandHash}`}
          challenge={run.productionChallenge}
          contentHash={run.productionChallenge.commandHash}
          command={run.productionCommand ?? ""}
          onConfirm={confirmProduction}
          onCancel={() => void cancelRun()}
        />
      )}
    </div>
  );
}
