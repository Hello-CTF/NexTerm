import { useEffect, useMemo, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { useUi, type Workspace } from "../../app/store";
import { assetApi, type Asset, type SessionInfo } from "../../ipc/commands";
import {
  createExecBatchId,
  execBatchApi,
  getExecBatchConfirmationChallenge,
  type ExecBatch,
  type ExecBatchConfirmationChallenge,
  type ExecBatchRequest,
  type ExecBatchResult,
  type ExecBatchStatus,
} from "../../ipc/execBatchApi";
import { describeError } from "../../ui/errorText";
import { IconPlay, IconServer, IconStop } from "../../ui/icons";
import { assetTagValues } from "../explorer/assetSearch";
import { useSnippetVariablePrompt } from "../explorer/SnippetVariablePrompt";
import { analyzeSnippetTemplate, renderSnippetForTargets } from "../explorer/snippetVariables";
import { ProductionConfirmationDialog } from "./ProductionConfirmationDialog";
import { DriftComparePanel } from "./DriftComparePanel";
import { ProcessFinderPanel } from "../services/ProcessFinderPanel";
import { ServicesPanel } from "../services/ServicesPanel";
import { ExecBatchHistory } from "./ExecBatchHistory";
import { TargetSetPicker, type TargetSetPickerTarget } from "./TargetSetPicker";

const MAX_TARGETS = 64;
const MAX_CONCURRENT = 32;

type ExecutionPlan = {
  requests: ExecBatchRequest[];
  index: number;
  results: ExecBatchResult[];
  startedAt: number;
  finishedAt: number;
  cancelRequested: boolean;
};

function executionPlanBatch(plan: ExecutionPlan): ExecBatch {
  return {
    batchId: plan.requests[0]?.batchId ?? "",
    command: plan.requests.length === 1 ? plan.requests[0]?.command ?? "" : "多条逐目标渲染命令",
    startedAt: plan.startedAt,
    finishedAt: plan.finishedAt,
    results: [...plan.results],
  };
}

function workspaceIncludesSession(workspace: Workspace, sessionId: string): boolean {
  if (workspace.sessionId === sessionId) return true;
  return workspace.panes.some((pane) => pane.tabs.some((tab) => tab.sessionId === sessionId));
}

function assetEnvCount(asset: Asset | undefined): number {
  const env = asset?.options?.env;
  return env && typeof env === "object" && !Array.isArray(env) ? Object.keys(env).length : 0;
}

function collectTargets(
  sessions: readonly SessionInfo[],
  assets: readonly Asset[],
  workspaces: readonly Workspace[],
): TargetSetPickerTarget[] {
  const assetsById = new Map(assets.map((asset) => [asset.id, asset]));
  return sessions
    .filter((session) => session.kind === "ssh" && session.status === "connected")
    .map((session) => {
      const asset = session.assetId ? assetsById.get(session.assetId) : undefined;
      const sources = workspaces.filter((workspace) => workspaceIncludesSession(workspace, session.id));
      const quickTarget = sources.find((workspace) => workspace.quickTarget)?.quickTarget;
      const host = asset?.host ?? quickTarget?.host ?? null;
      const username = asset?.username ?? quickTarget?.username ?? null;
      return {
        id: session.id,
        label: asset?.name.trim() || session.name,
        sessionName: session.name,
        endpoint: host ? `${username ? `${username}@` : ""}${host}` : null,
        tags: assetTagValues(asset?.tags ?? ""),
        workspaceNames: [...new Set(sources.map((workspace) => workspace.title || workspace.id))],
        envCount: assetEnvCount(asset),
      };
    });
}

const STATUS_LABELS: Record<ExecBatchStatus, string> = {
  success: "成功",
  exit_error: "非零退出",
  transport_error: "传输错误",
  timeout: "超时",
  canceled: "已取消",
};

function statusClass(status: ExecBatchStatus): string {
  if (status === "success") return "text-green-300";
  if (status === "canceled") return "text-neutral-400";
  if (status === "timeout") return "text-amber-300";
  return "text-red-300";
}

function resultSummary(results: ExecBatchResult[]): string {
  const counts = new Map<ExecBatchStatus, number>();
  for (const result of results) counts.set(result.status, (counts.get(result.status) ?? 0) + 1);
  return (Object.keys(STATUS_LABELS) as ExecBatchStatus[])
    .filter((status) => counts.has(status))
    .map((status) => `${STATUS_LABELS[status]} ${counts.get(status)}`)
    .join(" · ");
}

function ResultOutput({ result }: { result: ExecBatchResult }) {
  if (!result.stdout && !result.stderr) return <span className="text-neutral-500">无输出</span>;
  return (
    <details className="min-w-40">
      <summary className="cursor-pointer select-none text-neutral-400">查看输出</summary>
      {result.stdout && (
        <div className="mt-2">
          <div className="mb-1 text-[10px] text-neutral-500">stdout</div>
          <pre className="nx-pre max-h-56 overflow-auto whitespace-pre-wrap break-all">{result.stdout}</pre>
        </div>
      )}
      {result.stderr && (
        <div className="mt-2">
          <div className="mb-1 text-[10px] text-neutral-500">stderr</div>
          <pre className="nx-pre max-h-56 overflow-auto whitespace-pre-wrap break-all">{result.stderr}</pre>
        </div>
      )}
    </details>
  );
}

export function ExecBatchPanel({ sessions }: { sessions: SessionInfo[] }) {
  const workspaces = useUi((state) => state.workspaces);
  const assetsQuery = useQuery({ queryKey: ["assets"], queryFn: () => assetApi.list(), retry: false });
  const targets = useMemo(
    () => collectTargets(sessions, assetsQuery.data ?? [], workspaces),
    [sessions, assetsQuery.data, workspaces],
  );
  const [view, setView] = useState<"execute" | "drift" | "services" | "finder">("execute");
  const [selected, setSelected] = useState<string[]>([]);
  const [command, setCommand] = useState("");
  const [concurrency, setConcurrency] = useState("4");
  const [timeoutSeconds, setTimeoutSeconds] = useState("30");
  const [runningId, setRunningId] = useState<string | null>(null);
  const [canceling, setCanceling] = useState(false);
  const [batch, setBatch] = useState<ExecBatch | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [cancelError, setCancelError] = useState<string | null>(null);
  const [productionConfirmation, setProductionConfirmation] = useState<{
    request: ExecBatchRequest;
    challenge: ExecBatchConfirmationChallenge;
  } | null>(null);
  const activeRef = useRef<string | null>(null);
  const executionPlanRef = useRef<ExecutionPlan | null>(null);
  const { promptForValues, promptElement, prompting } = useSnippetVariablePrompt();

  useEffect(() => {
    const available = new Set(targets.map((target) => target.id));
    setSelected((current) => {
      const next = current.filter((id) => available.has(id));
      return next.length === current.length ? current : next;
    });
  }, [targets]);

  useEffect(
    () => () => {
      const batchId = activeRef.current;
      activeRef.current = null;
      executionPlanRef.current = null;
      if (batchId) void execBatchApi.cancel(batchId).catch(() => undefined);
    },
    [],
  );

  const selectedIds = targets.filter((target) => selected.includes(target.id)).map((target) => target.id);
  const running = runningId !== null;

  const runExecute = async (request: ExecBatchRequest) => {
    if (activeRef.current) return;
    const batchId = request.batchId;
    let nextRequest: ExecBatchRequest | null = null;
    activeRef.current = batchId;
    setRunningId(batchId);
    setCanceling(false);
    setBatch(null);
    setError(null);
    setCancelError(null);
    setProductionConfirmation(null);
    try {
      const result = await execBatchApi.execute(request);
      if (activeRef.current === batchId) {
        const plan = executionPlanRef.current;
        if (plan && plan.requests[plan.index]?.batchId === batchId) {
          plan.results.push(...result.results);
          if (plan.startedAt === 0) plan.startedAt = result.startedAt;
          plan.finishedAt = result.finishedAt;
          if (plan.cancelRequested || plan.index === plan.requests.length - 1) {
            setBatch(executionPlanBatch(plan));
            executionPlanRef.current = null;
          } else {
            plan.index += 1;
            nextRequest = plan.requests[plan.index] ?? null;
          }
        } else {
          setBatch(result);
        }
      }
    } catch (cause) {
      if (activeRef.current === batchId) {
        const challenge = getExecBatchConfirmationChallenge(cause);
        if (challenge?.batchId === batchId) {
          const unconfirmedRequest = { ...request };
          delete unconfirmedRequest.productionConfirmation;
          setProductionConfirmation({ request: unconfirmedRequest, challenge });
        } else {
          const plan = executionPlanRef.current;
          if (plan) {
            if (plan.startedAt !== 0) setBatch(executionPlanBatch(plan));
            setError(`批次 ${batchId} 失败，后续目标未执行：${describeError(cause)}`);
            executionPlanRef.current = null;
          } else {
            setError(describeError(cause));
          }
        }
      }
    } finally {
      if (activeRef.current === batchId) {
        activeRef.current = null;
        setRunningId(null);
        setCanceling(false);
        if (nextRequest) void runExecute(nextRequest);
      }
    }
  };

  const execute = async () => {
    if (activeRef.current || prompting) return;
    const maxConcurrent = Number(concurrency);
    const timeout = Number(timeoutSeconds);
    if (!command.trim()) {
      setError("请输入要执行的单条命令");
      return;
    }
    if (command.includes("\n") || command.includes("\r") || command.includes("\0")) {
      setError("批量执行只接受一条单行命令");
      return;
    }
    if (selectedIds.length === 0) {
      setError("请至少选择一个已连接 SSH 会话");
      return;
    }
    if (selectedIds.length > MAX_TARGETS) {
      setError(`每批最多选择 ${MAX_TARGETS} 个目标，当前已选 ${selectedIds.length} 个`);
      return;
    }
    if (!Number.isInteger(maxConcurrent) || maxConcurrent < 1 || maxConcurrent > MAX_CONCURRENT) {
      setError(`并发数必须是 1-${MAX_CONCURRENT} 的整数`);
      return;
    }
    if (!Number.isInteger(timeout) || timeout < 1 || timeout > 600) {
      setError("单目标超时必须是 1-600 秒的整数");
      return;
    }

    let analysis: ReturnType<typeof analyzeSnippetTemplate>;
    try {
      analysis = analyzeSnippetTemplate(command);
    } catch (cause) {
      setError(`变量解析失败：${describeError(cause)}`);
      return;
    }
    if (!analysis.hasVariables) {
      await runExecute({
        batchId: createExecBatchId(),
        sessionIds: selectedIds,
        command,
        timeoutMs: timeout * 1000,
        maxConcurrent,
      });
      return;
    }
    if (analysis.unknownVariables.length > 0) {
      setError(`未知变量：${analysis.unknownVariables.map((name) => `「${name}」`).join("、")}`);
      return;
    }

    const values = await promptForValues("填写批量执行变量", analysis.variables);
    if (values === null || activeRef.current) return;
    const targetsById = new Map(targets.map((target) => [target.id, target]));
    const sessionsById = new Map(sessions.map((session) => [session.id, session]));
    const assetsById = new Map((assetsQuery.data ?? []).map((asset) => [asset.id, asset]));
    const renderTargets = selectedIds.map((id) => {
      const session = sessionsById.get(id);
      const asset = session?.assetId ? assetsById.get(session.assetId) : undefined;
      return { id, label: targetsById.get(id)?.label ?? session?.name ?? id, asset };
    });

    try {
      const rendered = renderSnippetForTargets(command, values, renderTargets);
      const commandGroups = new Map<string, string[]>();
      for (const target of rendered) {
        if (!target.command.trim()) throw new Error(`目标「${target.id}」渲染后的命令不能为空`);
        if (target.command.includes("\n") || target.command.includes("\r") || target.command.includes("\0")) {
          throw new Error(`目标「${target.id}」渲染后的命令包含换行或 NUL；批量执行只接受一条单行命令`);
        }
        const group = commandGroups.get(target.command) ?? [];
        group.push(target.id);
        commandGroups.set(target.command, group);
      }
      const requests = [...commandGroups].map(([renderedCommand, sessionIds]): ExecBatchRequest => ({
        batchId: createExecBatchId(),
        sessionIds,
        command: renderedCommand,
        timeoutMs: timeout * 1000,
        maxConcurrent,
      }));
      const first = requests[0];
      if (!first) return;
      if (requests.length === 1) {
        await runExecute(first);
        return;
      }
      executionPlanRef.current = {
        requests,
        index: 0,
        results: [],
        startedAt: 0,
        finishedAt: 0,
        cancelRequested: false,
      };
      await runExecute(first);
    } catch (cause) {
      executionPlanRef.current = null;
      setError(`变量渲染失败：${describeError(cause)}`);
    }
  };

  const confirmProduction = () => {
    const pending = productionConfirmation;
    if (!pending || activeRef.current) return;
    void runExecute({
      ...pending.request,
      productionConfirmation: {
        confirmed: true,
        targetSetHash: pending.challenge.targetSetHash,
        commandHash: pending.challenge.commandHash,
      },
    });
  };

  const discardProductionConfirmation = () => {
    setProductionConfirmation(null);
    const plan = executionPlanRef.current;
    executionPlanRef.current = null;
    if (plan?.startedAt) {
      setBatch(executionPlanBatch(plan));
      setError("已取消生产确认；已完成目标保留在结果中，后续目标未执行");
    }
  };

  const cancel = async () => {
    const batchId = activeRef.current;
    if (!batchId || canceling) return;
    const plan = executionPlanRef.current;
    if (plan) plan.cancelRequested = true;
    setCanceling(true);
    setCancelError(null);
    try {
      await execBatchApi.cancel(batchId);
    } catch (cause) {
      if (activeRef.current === batchId) setCancelError(`取消失败：${describeError(cause)}`);
    } finally {
      if (activeRef.current === batchId) setCanceling(false);
    }
  };

  return (
    <section className="nx-pane" aria-label="批量执行" data-testid="exec-batch-panel">
      <div className="nx-toolbar flex-wrap">
        <IconServer size={14} className="text-neutral-500" />
        <h2 className="nx-toolbar-title">多主机工具</h2>
        <span className="nx-hint">仅当前已连接 SSH · 手动触发</span>
        <span className="nx-spacer" />
        <button type="button" className="nx-btn nx-btn-sm" aria-pressed={view === "execute"} disabled={running || prompting} onClick={() => setView("execute")}>批量执行</button>
        <button type="button" className="nx-btn nx-btn-sm" aria-pressed={view === "drift"} disabled={running || prompting} onClick={() => { discardProductionConfirmation(); setView("drift"); }}>漂移对比</button>
        <button type="button" className="nx-btn nx-btn-sm" aria-pressed={view === "services"} disabled={running || prompting} onClick={() => { discardProductionConfirmation(); setView("services"); }}>服务</button>
        <button type="button" className="nx-btn nx-btn-sm" aria-pressed={view === "finder"} disabled={running || prompting} onClick={() => { discardProductionConfirmation(); setView("finder"); }}>端口 / 进程</button>
      </div>

      {view === "services" ? (
        <ServicesPanel
          targets={targets}
          sessions={sessions}
          metadataError={assetsQuery.isError ? `资产详情加载失败：${describeError(assetsQuery.error)}` : null}
        />
      ) : view === "finder" ? (
        <ProcessFinderPanel
          targets={targets}
          sessions={sessions}
          metadataError={assetsQuery.isError ? `资产详情加载失败：${describeError(assetsQuery.error)}` : null}
        />
      ) : view === "drift" ? (
        <DriftComparePanel
          targets={targets}
          metadataError={assetsQuery.isError ? `资产详情加载失败：${describeError(assetsQuery.error)}` : null}
        />
      ) : (
        <>
      <form
        className="flex flex-col gap-3 px-3 pb-3"
        onSubmit={(event) => {
          event.preventDefault();
          void execute();
        }}
      >
        <TargetSetPicker
          targets={targets}
          selectedIds={selectedIds}
          onChange={setSelected}
          disabled={running}
          maxTargets={MAX_TARGETS}
          metadataError={
            assetsQuery.isError
              ? `资产详情加载失败，别名、主机与标签搜索可能不完整：${describeError(assetsQuery.error)}`
              : null
          }
        />

        <label className="flex flex-col gap-1 text-[11px] text-neutral-500">
          命令
          <input
            className="nx-input font-mono text-xs"
            value={command}
            disabled={running}
            placeholder="例如：uname -a"
            aria-label="批量执行命令"
            onChange={(event) => {
              setCommand(event.target.value);
              discardProductionConfirmation();
            }}
          />
        </label>

        <div className="flex flex-wrap items-end gap-2">
          <label className="flex flex-col gap-1 text-[11px] text-neutral-500">
            并发数
            <input
              type="number"
              className="nx-input w-24"
              min={1}
              max={MAX_CONCURRENT}
              step={1}
              value={concurrency}
              disabled={running}
              onChange={(event) => setConcurrency(event.target.value)}
            />
          </label>
          <label className="flex flex-col gap-1 text-[11px] text-neutral-500">
            单目标超时（秒）
            <input
              type="number"
              className="nx-input w-28"
              min={1}
              max={600}
              step={1}
              value={timeoutSeconds}
              disabled={running}
              onChange={(event) => setTimeoutSeconds(event.target.value)}
            />
          </label>
          <span className="nx-spacer" />
          {running ? (
            <button type="button" className="nx-btn nx-btn-danger" disabled={canceling} onClick={() => void cancel()}>
              <IconStop size={13} />
              {canceling ? "取消中…" : "取消本批次"}
            </button>
          ) : (
            <button type="submit" className="nx-btn nx-btn-primary" disabled={targets.length === 0}>
              <IconPlay size={13} />
              执行
            </button>
          )}
        </div>

        <div className="text-[11px] leading-relaxed text-neutral-500">
          无变量命令在全部目标预检通过后启动；含变量命令按逐目标渲染结果分组顺序执行。非零退出与连接错误分别显示。
          输出有大小上限，结果仅保留在当前面板，审计不保存 stdout/stderr。
        </div>
        {error && (
          <div role="alert" className="rounded border border-red-900/60 bg-red-950/20 px-2 py-1.5 text-[11px] text-red-300">
            {error}
          </div>
        )}
        {cancelError && (
          <div role="alert" className="text-[11px] text-red-300">
            {cancelError}
          </div>
        )}
        {running && (
          <div className="text-[11px] text-blue-300" role="status">
            正在执行批次 {runningId}；取消请求与执行请求可并行，等待各目标返回。
          </div>
        )}
      </form>

      {promptElement}
      {productionConfirmation && (
        <ProductionConfirmationDialog
          key={`${productionConfirmation.challenge.targetSetHash}:${productionConfirmation.challenge.commandHash}`}
          challenge={productionConfirmation.challenge}
          contentHash={productionConfirmation.challenge.commandHash}
          command={productionConfirmation.request.command}
          onCancel={discardProductionConfirmation}
          onConfirm={confirmProduction}
        />
      )}

      {batch && (
        <div className="border-t border-neutral-800/70">
          <div className="flex flex-wrap items-center gap-2 px-3 py-2 text-[11px]">
            <span className="font-medium text-neutral-300">结果矩阵</span>
            <span className="nx-hint">{resultSummary(batch.results)}</span>
            <span className="nx-spacer" />
            <span className="nx-hint" title={batch.batchId}>
              批次 {batch.batchId.slice(0, 8)}
            </span>
          </div>
          <div className="overflow-x-auto px-3 pb-3">
            <table className="nx-table nx-table-fixed min-w-[720px]">
              <thead>
                <tr>
                  <th className="w-1/5">目标</th>
                  <th className="w-28">状态</th>
                  <th className="w-20">退出码</th>
                  <th className="w-24">耗时</th>
                  <th>输出 / 错误</th>
                </tr>
              </thead>
              <tbody>
                {batch.results.map((result) => (
                  <tr key={result.sessionId}>
                    <td>
                      <div className="truncate text-neutral-300" title={result.sessionId}>
                        {result.sessionName}
                      </div>
                      <div className="truncate font-mono text-[10px] text-neutral-500">{result.sessionId}</div>
                    </td>
                    <td className={statusClass(result.status)}>
                      {STATUS_LABELS[result.status]}
                      {result.truncated && <span className="ml-1 text-[10px] text-amber-300">已截断</span>}
                    </td>
                    <td className="nx-mono">
                      {result.exitCode ?? "—"}
                      {result.signal ? ` (${result.signal})` : ""}
                    </td>
                    <td className="nx-mono">{result.durationMs} ms</td>
                    <td>
                      {result.error && <div className="mb-1 whitespace-pre-wrap break-all text-red-300">{result.error}</div>}
                      <ResultOutput result={result} />
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      )}
      <ExecBatchHistory />
        </>
      )}
    </section>
  );
}
