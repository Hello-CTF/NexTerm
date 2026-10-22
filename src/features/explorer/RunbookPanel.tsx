import { useCallback, useEffect, useId, useMemo, useRef, useState, type KeyboardEvent as ReactKeyboardEvent } from "react";
import { useQuery } from "@tanstack/react-query";
import { useUi } from "../../app/store";
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
import { IconPlay, IconPlus, IconStop, IconTrash, IconXCircle } from "../../ui/icons";
import { ProductionConfirmationDialog } from "../execbatch/ProductionConfirmationDialog";
import { TargetSetPicker, type TargetSetPickerTarget } from "../execbatch/TargetSetPicker";
import { assetTagValues } from "./assetSearch";
import { useSnippetVariablePrompt } from "./SnippetVariablePrompt";
import { analyzeSnippetTemplate, renderSnippetForTargets } from "./snippetVariables";
import type { Snippet } from "./snippetInsert";

const MAX_TARGETS = 64;

type EditorStep = RunbookStep & { description: string };

const RUN_STATUS_LABELS: Record<string, string> = {
  awaiting_render: "等待变量渲染",
  awaiting_confirmation: "等待步骤确认",
  running: "执行中",
  awaiting_production_confirmation: "等待生产确认",
  success: "成功",
  failed: "失败",
  canceled: "已取消",
};

const STEP_STATUS_LABELS: Record<string, string> = {
  ...RUN_STATUS_LABELS,
  pending: "待执行",
  skipped: "已跳过",
};

const TARGET_STATUS_LABELS: Record<ExecBatchStatus, string> = {
  success: "成功",
  exit_error: "非零退出",
  transport_error: "传输错误",
  timeout: "超时",
  canceled: "已取消",
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
    .filter((session) => session.kind === "ssh" && session.status === "connected")
    .map((session) => {
      const asset = session.assetId ? assetsById.get(session.assetId) : undefined;
      return {
        id: session.id,
        label: asset?.name.trim() || session.name,
        sessionName: session.name,
        endpoint: asset?.host ?? null,
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
          <thead><tr><th>目标</th><th>状态</th><th>退出码</th><th>输出 / 错误</th></tr></thead>
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
        <div className="truncate text-neutral-300">{result.sessionName}</div>
        <div className="truncate font-mono text-[10px] text-neutral-500">{result.sessionId}</div>
      </td>
      <td className={statusClass(result.status)}>{TARGET_STATUS_LABELS[result.status as ExecBatchStatus] ?? result.status}</td>
      <td className="nx-mono">{result.exitCode ?? "—"}{result.signal ? ` (${result.signal})` : ""}</td>
      <td className="max-w-80">
        {result.error && <div className="whitespace-pre-wrap break-all text-red-300">{result.error}</div>}
        {(result.stdout || result.stderr) && (
          <details>
            <summary className="cursor-pointer text-neutral-400">查看输出</summary>
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
      <td>{result.sessionName}<div className="font-mono text-[10px] text-neutral-500">{result.sessionId}</div></td>
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
        <span>历史：{history.runbookId}</span>
        <span>运行 {history.runId}</span>
        <span>到第 {history.reachedStep} / {history.stepCount} 步</span>
        <span className={statusClass(history.status)}>{RUN_STATUS_LABELS[history.status] ?? history.status}</span>
      </div>
      {history.batches.map((batch) => (
        <div key={batch.batch.id} className="mb-3 rounded border border-neutral-800 p-2">
          <div className="mb-1 text-[11px] text-neutral-400">
            第 {batch.batch.stepNumber} 步 · 批次 {batch.batch.id} · {RUN_STATUS_LABELS[batch.batch.status] ?? batch.batch.status}
          </div>
          <table className="nx-table min-w-[520px]">
            <thead><tr><th>目标</th><th>状态</th><th>退出码</th><th>错误分类</th></tr></thead>
            <tbody>{batch.results.map((result) => <HistoryResult key={result.sessionId} result={result} />)}</tbody>
          </table>
        </div>
      ))}
    </div>
  );
}

export function RunbookPanel({ snippets, onClose }: { snippets: readonly Snippet[]; onClose: () => void }) {
  const sessions = useUi((state) => state.sessions);
  const assetsQuery = useQuery({ queryKey: ["assets"], queryFn: () => assetApi.list(), retry: false });
  const targets = useMemo(() => collectTargets(sessions, assetsQuery.data ?? []), [sessions, assetsQuery.data]);
  const [runbookId] = useState(createRunbookId);
  const [name, setName] = useState("新建 Runbook");
  const [steps, setSteps] = useState<EditorStep[]>([]);
  const [snippetId, setSnippetId] = useState("");
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
    if (!step) throw new Error("当前步骤不存在");
    const analysis = analyzeSnippetTemplate(step.step.command);
    if (analysis.unknownVariables.length > 0) {
      throw new Error(`未知变量：${analysis.unknownVariables.map((variable) => `「${variable}」`).join("、")}`);
    }
    const values = analysis.hasVariables
      ? await promptForValues(`填写 Runbook 步骤变量 · ${step.step.name || `第 ${step.number} 步`}`, analysis.variables)
      : {};
    if (values === null) {
      if (isActiveEpoch(epoch)) setError("已取消变量输入；可重新准备当前步，或取消 Runbook");
      return null;
    }
    const current = runRef.current;
    if (!isActiveEpoch(epoch) || !current || current.runId !== state.runId || terminalRun(current.status)) return null;
    const sessionsById = new Map(sessions.map((session) => [session.id, session]));
    const assetsById = new Map((assetsQuery.data ?? []).map((asset) => [asset.id, asset]));
    const targetsById = new Map(targets.map((target) => [target.id, target]));
    const rendered = renderSnippetForTargets(step.step.command, values, state.sessionIds.map((id) => {
      const session = sessionsById.get(id);
      const asset = session?.assetId ? assetsById.get(session.assetId) : undefined;
      return { id, label: targetsById.get(id)?.label ?? session?.name ?? id, asset };
    }));
    for (const target of rendered) {
      if (!target.command.trim()) throw new Error(`目标「${target.id}」渲染后的命令不能为空`);
      if (/[\r\n\0]/.test(target.command)) {
        throw new Error(`目标「${target.id}」渲染后的命令包含换行或 NUL；Runbook 只接受单行命令`);
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
      if (state.status === "awaiting_confirmation" && state.confirmationPolicy === "production_only") {
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
    const trimmedName = name.trim();
    if (!trimmedName) {
      setError("请输入 Runbook 名称");
      return;
    }
    if (steps.length === 0 || steps.some((step) => !step.command.trim())) {
      setError("请至少添加一个步骤，并填写每步命令");
      return;
    }
    if (selected.length === 0) {
      setError("请至少选择一个已连接 SSH 会话");
      return;
    }
    setHistory(null);
    void transition(async (epoch) => {
      const selectedIds = targets.filter((target) => selected.includes(target.id)).map((target) => target.id);
      const runId = createRunbookId();
      pendingRunIdRef.current = runId;
      try {
        const initial = await runbookApi.start({
          runId, runbookId, runbookName: trimmedName,
          steps: steps.map((step) => ({ ...step, name: step.name.trim() || "未命名步骤" })),
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
      if (isActiveEpoch(epoch)) setError(`取消失败：${describeError(cause)}`);
    } finally {
      if (isActiveEpoch(epoch)) setCanceling(false);
    }
  };

  const closePanel = () => {
    invalidateAndCancel();
    onClose();
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
      if (isActiveEpoch(epoch)) setHistoryError(`历史读取失败：${describeError(cause)}`);
    } finally {
      if (isActiveEpoch(epoch)) setHistoryLoading(false);
    }
  };

  const updateStep = (id: string, patch: Partial<EditorStep>) => {
    setSteps((current) => current.map((step) => step.id === id ? { ...step, ...patch } : step));
  };
  const moveStep = (index: number, offset: -1 | 1) => {
    setSteps((current) => {
      const destination = index + offset;
      if (destination < 0 || destination >= current.length) return current;
      const next = [...current];
      [next[index], next[destination]] = [next[destination], next[index]];
      return next;
    });
  };
  const addSnippet = () => {
    const snippet = snippets.find((candidate) => candidate.id === snippetId);
    if (!snippet) return;
    setSteps((current) => [...current, {
      id: createRunbookId(), snippetId: snippet.id, name: snippet.name,
      description: "", command: snippet.body,
    }]);
    setSnippetId("");
  };

  const onModalKeyDown = (event: ReactKeyboardEvent<HTMLDivElement>) => {
    event.stopPropagation();
    if (!layer.isTopmost()) return;
    if (event.key === "Escape" && !event.repeat && !isImeKeyEvent(event)) {
      event.preventDefault();
      closePanel();
      return;
    }
    trapOverlayTab(event, modalRef.current);
  };

  const active = run !== null && !terminalRun(run.status);

  return (
    <div className="nx-overlay" onClick={(event) => { event.stopPropagation(); closePanel(); }}>
      <div
        ref={modalRef}
        className="nx-modal flex max-w-[900px] flex-col"
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        tabIndex={-1}
        onClick={(event) => event.stopPropagation()}
        onKeyDown={onModalKeyDown}
      >
        <div className="nx-modal-header shrink-0">
          <span id={titleId} className="text-[13px] font-semibold text-neutral-100">Runbook</span>
          <span className="nx-hint">有序步骤 · 逐目标渲染 · execbatch 历史</span>
        </div>
        <div className="nx-modal-body min-h-0 flex-1 overflow-y-auto">
          {error && <div role="alert" className="mb-3 flex items-start gap-1.5 text-[12px] text-red-400"><IconXCircle size={13} className="mt-0.5 shrink-0" />{error}</div>}
          {!run ? (
            <>
              <div className="nx-form-row">
                <label className="nx-label" htmlFor="runbook-name">名称</label>
                <input id="runbook-name" className="nx-input" value={name} onChange={(event) => setName(event.target.value)} />
              </div>
              <div className="mb-3 flex flex-wrap items-end gap-2">
                <label className="flex min-w-52 flex-1 flex-col gap-1 text-[11px] text-neutral-500">
                  从命令片段添加
                  <select className="nx-input" aria-label="选择要添加的片段" value={snippetId} onChange={(event) => setSnippetId(event.target.value)}>
                    <option value="">选择片段</option>
                    {snippets.map((snippet) => <option key={snippet.id} value={snippet.id}>{snippet.name}</option>)}
                  </select>
                </label>
                <button type="button" className="nx-btn" disabled={!snippetId} onClick={addSnippet}><IconPlus size={12} />添加片段步骤</button>
                <button type="button" className="nx-btn" onClick={() => setSteps((current) => [...current, { id: createRunbookId(), name: `步骤 ${current.length + 1}`, description: "", command: "" }])}>
                  <IconPlus size={12} />添加命令步骤
                </button>
              </div>
              <div className="mb-3 flex flex-col gap-2" aria-label="Runbook 步骤列表">
                {steps.length === 0 && <div className="rounded border border-dashed border-neutral-800 p-4 text-center text-[11px] text-neutral-500">从片段或空白命令创建第一个步骤。</div>}
                {steps.map((step, index) => (
                  <div key={step.id} className="rounded border border-neutral-800 p-2.5" data-testid={`runbook-editor-step-${index + 1}`}>
                    <div className="mb-2 flex flex-wrap items-center gap-2">
                      <span className="nx-badge">{index + 1}</span>
                      <input className="nx-input min-w-40 flex-1" aria-label={`步骤 ${index + 1} 名称`} value={step.name} onChange={(event) => updateStep(step.id, { name: event.target.value })} />
                      <button type="button" className="nx-btn nx-btn-sm" disabled={index === 0} aria-label={`上移步骤 ${index + 1}`} onClick={() => moveStep(index, -1)}>上移</button>
                      <button type="button" className="nx-btn nx-btn-sm" disabled={index === steps.length - 1} aria-label={`下移步骤 ${index + 1}`} onClick={() => moveStep(index, 1)}>下移</button>
                      <button type="button" className="nx-icon-btn nx-icon-btn-sm is-danger" aria-label={`删除步骤 ${index + 1}`} onClick={() => setSteps((current) => current.filter((candidate) => candidate.id !== step.id))}><IconTrash size={12} /></button>
                    </div>
                    <input className="nx-input mb-2" aria-label={`步骤 ${index + 1} 说明`} placeholder="可选说明" value={step.description} onChange={(event) => updateStep(step.id, { description: event.target.value })} />
                    <textarea className="nx-textarea font-mono text-[11.5px]" rows={2} aria-label={`步骤 ${index + 1} 命令`} value={step.command} onChange={(event) => updateStep(step.id, { command: event.target.value })} />
                  </div>
                ))}
              </div>
              <TargetSetPicker
                targets={targets}
                selectedIds={selected}
                onChange={setSelected}
                maxTargets={MAX_TARGETS}
                legend="Runbook 目标"
                searchAriaLabel="搜索 Runbook 目标"
                metadataError={assetsQuery.isError ? `资产详情加载失败：${describeError(assetsQuery.error)}` : null}
              />
              <div className="mt-3 flex flex-wrap items-center gap-3 text-[11px] text-neutral-400">
                <label className="flex items-center gap-1.5">
                  确认策略
                  <select className="nx-input" aria-label="Runbook 确认策略" value={policy} onChange={(event) => setPolicy(event.target.value as RunbookConfirmationPolicy)}>
                    <option value="every_step">全部逐步确认（默认）</option>
                    <option value="production_only">仅生产目标确认</option>
                  </select>
                </label>
                <label className="flex items-center gap-1.5">
                  <input type="checkbox" className="accent-blue-500" checked={stopOnFailure} onChange={(event) => setStopOnFailure(event.target.checked)} />
                  任一目标失败后停止（默认）
                </label>
              </div>
            </>
          ) : (
            <div data-testid="runbook-run-view">
              <div className="mb-3 flex flex-wrap items-center gap-2">
                <span className="font-medium text-neutral-200">{run.runbookName}</span>
                <span className={statusClass(run.status)}>{RUN_STATUS_LABELS[run.status] ?? run.status}</span>
                <span className="nx-hint">第 {run.currentStep} / {run.stepCount} 步 · {run.stopOnFailure ? "失败即停" : "失败后继续"}</span>
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
                        <div className="mb-1 text-[11px] text-amber-300">确认以下已渲染命令；执行后才会进入下一步。</div>
                        {(step.renderedCommands ?? []).map((rendered) => (
                          <div key={rendered.sessionId} className="mb-1">
                            <div className="font-mono text-[10px] text-neutral-500">{rendered.sessionId}</div>
                            <pre className="nx-pre whitespace-pre-wrap break-all text-[11px]">{rendered.command}</pre>
                          </div>
                        ))}
                      </div>
                    )}
                    <LiveResultMatrix step={step} />
                  </section>
                ))}
              </div>
              {run.status === "awaiting_render" && !busy && (
                <button type="button" className="nx-btn mt-3" onClick={retryPrepare}>重新准备当前步</button>
              )}
              {run.status === "awaiting_confirmation" && (
                <button type="button" className="nx-btn nx-btn-primary mt-3" disabled={busy || canceling} onClick={confirmStep}>
                  <IconPlay size={13} />确认并执行第 {run.currentStep} 步
                </button>
              )}
              {run.status === "running" && <div role="status" className="mt-3 text-[11px] text-blue-300">正在执行当前步{run.activeBatchId ? ` · 批次 ${run.activeBatchId}` : ""}</div>}
              {terminalRun(run.status) && run.steps.some((step) => step.batchIds.length > 0) && (
                <button type="button" className="nx-btn mt-3" disabled={historyLoading} onClick={() => void loadHistory()}>
                  {historyLoading ? "读取历史中…" : "查看本次运行历史"}
                </button>
              )}
              {historyError && <div role="alert" className="mt-2 text-[11px] text-red-300">{historyError}</div>}
              {history && <RunHistoryView history={history} />}
            </div>
          )}
        </div>
        <div className="nx-modal-footer shrink-0">
          <span className="nx-hint mr-auto">取消只停止当前 execbatch 与后续步骤，不关闭终端。</span>
          {run && terminalRun(run.status) && <button type="button" className="nx-btn" onClick={() => { runRef.current = null; setRun(null); setHistory(null); setError(null); }}>返回编辑</button>}
          {active && <button type="button" className="nx-btn nx-btn-danger" disabled={canceling} onClick={() => void cancelRun()}><IconStop size={12} />{canceling ? "取消中…" : "取消 Runbook"}</button>}
          {!run && <button type="button" className="nx-btn nx-btn-primary" disabled={busy || targets.length === 0} onClick={start}><IconPlay size={13} />开始运行</button>}
          <button type="button" className="nx-btn nx-btn-ghost" onClick={closePanel}>关闭</button>
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
