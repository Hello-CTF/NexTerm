import { useEffect, useMemo, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { useWorkspaceList, type Workspace } from "../../app/store";
import { assetApi, type Asset, type SessionInfo } from "../../ipc/commands";
import {
  createExecBatchId,
  EXEC_BATCH_SHELLS,
  execBatchApi,
  targetSetApi,
  type ExecBatch,
  type ExecBatchRequest,
  type ExecBatchResult,
  type ExecBatchShell,
  type ExecBatchStatus,
  type TargetCapability,
} from "../../ipc/execBatchApi";
import { getProductionChallenge, type ExecBatchProductionChallenge } from "../production/productionChallenge";
import { useAuth } from "../auth/store";
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
import { SavedTargetSets } from "./SavedTargetSets";
import { BatchProgressPanel, liveBatchProgressApi, type BatchProgressHandle, type BatchRerunRequest } from "./BatchProgress";
import { t } from "../../i18n";

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

// A plan with a command rendered per target has no single command; this marker stands in for it and is never shown.
const PER_TARGET_COMMANDS = "\u0000per-target-commands";

function executionPlanBatch(plan: ExecutionPlan): ExecBatch {
  return {
    batchId: plan.requests[0]?.batchId ?? "",
    command: plan.requests.length === 1 ? plan.requests[0]?.command ?? "" : PER_TARGET_COMMANDS,
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

// The tools other than batch execution send POSIX commands, so they keep to SSH hosts and managed devices.
const POSIX_TOOL_KINDS: readonly string[] = ["ssh", "device"];
const EXEC_TARGET_KINDS: readonly string[] = ["ssh", "device", "winrm", "local"];

function collectTargets(
  sessions: readonly SessionInfo[],
  assets: readonly Asset[],
  workspaces: readonly Workspace[],
  kinds: readonly string[] = POSIX_TOOL_KINDS,
): TargetSetPickerTarget[] {
  const assetsById = new Map(assets.map((asset) => [asset.id, asset]));
  return sessions
    .filter((session) => kinds.includes(session.kind) && session.status === "connected")
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
        endpoint: host
          ? `${username ? `${username}@` : ""}${host}`
          : session.kind === "device"
            ? t("受管设备 · 执行前需确认")
            : session.kind === "local"
              ? t("本机会话")
              : null,
        tags: assetTagValues(asset?.tags ?? ""),
        workspaceNames: [...new Set(sources.map((workspace) => workspace.title || workspace.id))],
        envCount: assetEnvCount(asset),
      };
    });
}

const STATUS_LABELS: Record<ExecBatchStatus, string> = {
  success: t("成功"),
  exit_error: t("非零退出"),
  transport_error: t("传输错误"),
  timeout: t("超时"),
  canceled: t("已取消"),
  skipped: t("已跳过"),
};

const SHELL_LABELS: Record<ExecBatchShell, string> = {
  posix: "POSIX shell",
  powershell: "PowerShell",
  cmd: "cmd",
};

const SHELL_PLACEHOLDERS: Record<ExecBatchShell, string> = {
  posix: t("例如：uname -a"),
  powershell: t("例如：Get-CimInstance Win32_OperatingSystem | Select-Object Caption"),
  cmd: t("例如：ver"),
};

const PLATFORM_LABELS: Record<string, string> = {
  posix: "POSIX",
  windows: "Windows",
  unknown: t("平台未知"),
};

function shellSupportFor(capability: TargetCapability | undefined, shell: ExecBatchShell) {
  return capability?.shells.find((support) => support.shell === shell);
}

// shellBadge marks each target with its platform and whether it runs the chosen shell; targets that cannot are
// skipped by the backend and say so here before anything runs.
function shellBadge(capability: TargetCapability | undefined, shell: ExecBatchShell): TargetSetPickerTarget["badge"] {
  if (!capability) return undefined;
  if (capability.error) return { label: t("不可用"), tone: "skip", title: capability.error };
  const support = shellSupportFor(capability, shell);
  const platform = PLATFORM_LABELS[capability.platform] ?? capability.platform;
  if (support?.support === "unsupported") return { label: t("{platform} · 将跳过", { platform }), tone: "skip", title: support.reason };
  if (support?.support === "detect") return { label: t("{platform} · 执行前检测", { platform }), tone: "warn", title: support.reason };
  return { label: platform, tone: "ok" };
}

function statusClass(status: ExecBatchStatus): string {
  if (status === "success") return "text-green-300";
  if (status === "canceled") return "text-neutral-400";
  if (status === "skipped") return "text-amber-300";
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

// shellCommandText names a non-POSIX shell next to the command the production confirmation shows.
function shellCommandText(shell: string | undefined, command: string): string {
  if (!shell || shell === "posix") return command;
  return `[${SHELL_LABELS[shell as ExecBatchShell] ?? shell}] ${command}`;
}

function ResultOutput({ result }: { result: ExecBatchResult }) {
  if (!result.stdout && !result.stderr) return <span className="text-neutral-500">{t("无输出")}</span>;
  return (
    <details className="min-w-40">
      <summary className="cursor-pointer select-none text-neutral-400">{t("查看输出")}</summary>
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

export function ExecBatchPanel({ sessions, progressPollIntervalMs }: { sessions: SessionInfo[]; progressPollIntervalMs?: number }) {
  const workspaces = useWorkspaceList();
  const assetsQuery = useQuery({ queryKey: ["assets"], queryFn: () => assetApi.list(), retry: false });
  const targets = useMemo(
    () => collectTargets(sessions, assetsQuery.data ?? [], workspaces),
    [sessions, assetsQuery.data, workspaces],
  );
  const execTargetBase = useMemo(
    () => collectTargets(sessions, assetsQuery.data ?? [], workspaces, EXEC_TARGET_KINDS),
    [sessions, assetsQuery.data, workspaces],
  );
  const execTargetIds = useMemo(() => execTargetBase.map((target) => target.id).sort(), [execTargetBase]);
  const shellsQuery = useQuery({
    queryKey: ["exec-batch-target-shells", execTargetIds],
    queryFn: () => execBatchApi.targetShells(execTargetIds),
    enabled: execTargetIds.length > 0,
    retry: false,
  });
  const capabilitiesById = useMemo(
    () => new Map((shellsQuery.data ?? []).map((capability) => [capability.sessionId, capability])),
    [shellsQuery.data],
  );
  const [view, setView] = useState<"execute" | "drift" | "services" | "finder">("execute");
  const [selected, setSelected] = useState<string[]>([]);
  const [command, setCommand] = useState("");
  const [shell, setShell] = useState<ExecBatchShell>("posix");
  const shellChosenRef = useRef(false);
  const [concurrency, setConcurrency] = useState("4");
  const [timeoutSeconds, setTimeoutSeconds] = useState("30");
  const [runningId, setRunningId] = useState<string | null>(null);
  const [canceling, setCanceling] = useState(false);
  const [batch, setBatch] = useState<ExecBatch | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [cancelError, setCancelError] = useState<string | null>(null);
  const [productionConfirmation, setProductionConfirmation] = useState<{
    request: ExecBatchRequest;
    challenge: ExecBatchProductionChallenge;
  } | null>(null);
  const [rerunId, setRerunId] = useState<string | null>(null);
  const [rerunConfirmation, setRerunConfirmation] = useState<{
    request: BatchRerunRequest;
    challenge: ExecBatchProductionChallenge;
  } | null>(null);
  const activeRef = useRef<string | null>(null);
  const executionPlanRef = useRef<ExecutionPlan | null>(null);
  const progressRef = useRef<BatchProgressHandle>(null);
  const { promptForValues, promptElement, prompting } = useSnippetVariablePrompt();
  const accountID = useAuth((state) => state.user?.id ?? null);
  // Rebuild the API identity on account switch so the saved-sets guards
  // drop in-flight responses from the previous owner.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  const targetSetsApi = useMemo(() => ({ ...targetSetApi }), [accountID]);
  const execTargets = useMemo(
    () => execTargetBase.map((target) => ({ ...target, badge: shellBadge(capabilitiesById.get(target.id), shell) })),
    [execTargetBase, capabilitiesById, shell],
  );
  const availableIds = useMemo(() => execTargets.map((target) => target.id), [execTargets]);
  const progressBatchId = runningId ?? rerunId ?? batch?.batchId ?? null;
  const progressCommand = batch && batch.command !== PER_TARGET_COMMANDS ? batch.command : command;
  const progressShell = batch?.shell || shell;

  useEffect(() => {
    const available = new Set(execTargetBase.map((target) => target.id));
    setSelected((current) => {
      const next = current.filter((id) => available.has(id));
      return next.length === current.length ? current : next;
    });
  }, [execTargetBase]);

  useEffect(
    () => () => {
      const batchId = activeRef.current;
      activeRef.current = null;
      executionPlanRef.current = null;
      if (batchId) void execBatchApi.cancel(batchId).catch(() => undefined);
    },
    [],
  );

  const selectedIds = execTargets.filter((target) => selected.includes(target.id)).map((target) => target.id);
  const selectedKey = selectedIds.join("\u0000");
  const running = runningId !== null;

  // Until the user picks a shell, a selection made only of Windows targets defaults to PowerShell.
  useEffect(() => {
    if (shellChosenRef.current || running) return;
    const ids = selectedKey ? selectedKey.split("\u0000") : [];
    const platforms = ids.map((id) => capabilitiesById.get(id)?.platform);
    const allWindows = platforms.length > 0 && platforms.every((platform) => platform === "windows");
    setShell(allWindows ? "powershell" : "posix");
  }, [selectedKey, capabilitiesById, running]);

  const skippedTargets = selectedIds.flatMap((id) => {
    const capability = capabilitiesById.get(id);
    const support = shellSupportFor(capability, shell);
    if (capability?.error) return [{ id, reason: capability.error }];
    return support?.support === "unsupported" ? [{ id, reason: support.reason ?? "" }] : [];
  });
  const detectCount = selectedIds.filter((id) => shellSupportFor(capabilitiesById.get(id), shell)?.support === "detect").length;
  const targetLabels = new Map(execTargets.map((target) => [target.id, target.label]));

  const runExecute = async (request: ExecBatchRequest) => {
    if (activeRef.current) return;
    const batchId = request.batchId;
    let nextRequest: ExecBatchRequest | null = null;
    activeRef.current = batchId;
    setRunningId(batchId);
    setCanceling(false);
    setBatch(null);
    setRerunId(null);
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
        const challenge = getProductionChallenge(cause, "exec_batch_production_confirmation");
        if (challenge?.batchId === batchId) {
          const unconfirmedRequest = { ...request };
          delete unconfirmedRequest.productionConfirmation;
          setProductionConfirmation({ request: unconfirmedRequest, challenge });
        } else {
          const plan = executionPlanRef.current;
          if (plan) {
            if (plan.startedAt !== 0) setBatch(executionPlanBatch(plan));
            setError(t("批次 {batchId} 失败，后续目标未执行：{value}", { batchId, value: describeError(cause) }));
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
      setError(t("请输入要执行的单条命令"));
      return;
    }
    if (command.includes("\n") || command.includes("\r") || command.includes("\0")) {
      setError(t("批量执行只接受一条单行命令"));
      return;
    }
    if (selectedIds.length === 0) {
      setError(t("请至少选择一个已连接的会话"));
      return;
    }
    if (selectedIds.length > MAX_TARGETS) {
      setError(t("每批最多选择 {MAX_TARGETS} 个目标，当前已选 {length} 个", { MAX_TARGETS, length: selectedIds.length }));
      return;
    }
    if (!Number.isInteger(maxConcurrent) || maxConcurrent < 1 || maxConcurrent > MAX_CONCURRENT) {
      setError(t("并发数必须是 1-{MAX_CONCURRENT} 的整数", { MAX_CONCURRENT }));
      return;
    }
    if (!Number.isInteger(timeout) || timeout < 1 || timeout > 600) {
      setError(t("单目标超时必须是 1-600 秒的整数"));
      return;
    }

    // POSIX is the backend default, so a POSIX request keeps its original shape.
    const shellField = shell === "posix" ? {} : { shell };
    let analysis: ReturnType<typeof analyzeSnippetTemplate>;
    try {
      analysis = analyzeSnippetTemplate(command);
    } catch (cause) {
      setError(t("变量解析失败：{value}", { value: describeError(cause) }));
      return;
    }
    if (!analysis.hasVariables) {
      await runExecute({
        batchId: createExecBatchId(),
        sessionIds: selectedIds,
        command,
        ...shellField,
        timeoutMs: timeout * 1000,
        maxConcurrent,
      });
      return;
    }
    if (analysis.unknownVariables.length > 0) {
      setError(t("未知变量：{join}", { join: analysis.unknownVariables.map((name) => `「${name}」`).join("、") }));
      return;
    }

    const values = await promptForValues(t("填写批量执行变量"), analysis.variables);
    if (values === null || activeRef.current) return;
    const targetsById = new Map(execTargets.map((target) => [target.id, target]));
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
        if (!target.command.trim()) throw new Error(t("目标「{id}」渲染后的命令不能为空", { id: target.id }));
        if (target.command.includes("\n") || target.command.includes("\r") || target.command.includes("\0")) {
          throw new Error(t("目标「{id}」渲染后的命令包含换行或 NUL；批量执行只接受一条单行命令", { id: target.id }));
        }
        const group = commandGroups.get(target.command) ?? [];
        group.push(target.id);
        commandGroups.set(target.command, group);
      }
      const requests = [...commandGroups].map(([renderedCommand, sessionIds]): ExecBatchRequest => ({
        batchId: createExecBatchId(),
        sessionIds,
        command: renderedCommand,
        ...shellField,
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
      setError(t("变量渲染失败：{value}", { value: describeError(cause) }));
    }
  };

  const confirmProduction = (environmentNames?: string[]) => {
    const pending = productionConfirmation;
    if (!pending || activeRef.current) return;
    void runExecute({
      ...pending.request,
      productionConfirmation: {
        confirmed: true,
        targetSetHash: pending.challenge.targetSetHash,
        commandHash: pending.challenge.commandHash,
        ...(environmentNames ? { environmentNames } : {}),
      },
    });
  };

  const discardProductionConfirmation = () => {
    setProductionConfirmation(null);
    const plan = executionPlanRef.current;
    executionPlanRef.current = null;
    if (plan?.startedAt) {
      setBatch(executionPlanBatch(plan));
      setError(t("已取消生产确认；已完成目标保留在结果中，后续目标未执行"));
    }
  };

  const handleRerunStart = (nextBatchId: string) => {
    setBatch(null);
    setRerunId(nextBatchId);
  };

  const handleRerunConfirmation = (detail: unknown, request: BatchRerunRequest) => {
    const challenge = getProductionChallenge({ code: "needs_confirm", detail }, "exec_batch_production_confirmation");
    if (!challenge || challenge.batchId !== request.batchId) return;
    setRerunConfirmation({ request, challenge });
  };

  const confirmRerunProduction = (environmentNames?: string[]) => {
    const pending = rerunConfirmation;
    if (!pending) return;
    setRerunConfirmation(null);
    // Retry through the progress panel's own rerun continuation so the
    // generation/API/batch guards, startup polling, and final-batch
    // rendering apply exactly as they do for the button path.
    progressRef.current?.rerun({
      ...pending.request,
      productionConfirmation: {
        confirmed: true,
        targetSetHash: pending.challenge.targetSetHash,
        commandHash: pending.challenge.commandHash,
        ...(environmentNames ? { environmentNames } : {}),
      },
    });
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
      if (activeRef.current === batchId) setCancelError(t("取消失败：{value}", { value: describeError(cause) }));
    } finally {
      if (activeRef.current === batchId) setCanceling(false);
    }
  };

  return (
    <section className="nx-pane" aria-label={t("批量执行")} data-testid="exec-batch-panel">
      <div className="nx-toolbar nx-wrap-toolbar flex-wrap">
        <IconServer size={14} className="text-neutral-500" />
        <h2 className="nx-toolbar-title">{t("多主机工具")}</h2>
        <span className="nx-hint">{t("只能对当前已连接的会话执行；批量执行也支持 WinRM 与本机会话。受管设备无环境标记，执行前需要确认。")}</span>
        <span className="nx-spacer" />
        <div className="nx-toolbar-views contents">
        <button type="button" className="nx-btn nx-btn-sm" aria-pressed={view === "execute"} disabled={running || prompting} onClick={() => setView("execute")}>{t("批量执行")}</button>
        <button type="button" className="nx-btn nx-btn-sm" aria-pressed={view === "drift"} disabled={running || prompting} onClick={() => { discardProductionConfirmation(); setView("drift"); }}>{t("漂移对比")}</button>
        <button type="button" className="nx-btn nx-btn-sm" aria-pressed={view === "services"} disabled={running || prompting} onClick={() => { discardProductionConfirmation(); setView("services"); }}>{t("服务")}</button>
        <button type="button" className="nx-btn nx-btn-sm" aria-pressed={view === "finder"} disabled={running || prompting} onClick={() => { discardProductionConfirmation(); setView("finder"); }}>{t("端口 / 进程")}</button>
        </div>
      </div>

      {view === "services" ? (
        <ServicesPanel
          targets={targets}
          sessions={sessions}
          metadataError={assetsQuery.isError ? t("未能加载资产详情：{value}", { value: describeError(assetsQuery.error) }) : null}
        />
      ) : view === "finder" ? (
        <ProcessFinderPanel
          targets={targets}
          sessions={sessions}
          metadataError={assetsQuery.isError ? t("未能加载资产详情：{value}", { value: describeError(assetsQuery.error) }) : null}
        />
      ) : view === "drift" ? (
        <DriftComparePanel
          targets={targets}
          metadataError={assetsQuery.isError ? t("未能加载资产详情：{value}", { value: describeError(assetsQuery.error) }) : null}
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
        <SavedTargetSets
          api={targetSetsApi}
          availableTargetIds={availableIds}
          selectedIds={selectedIds}
          onSelect={setSelected}
          disabled={running}
        />
        <TargetSetPicker
          targets={execTargets}
          emptyMessage={t("没有已连接的 SSH、受管设备、WinRM 或本机会话。请先手动连接目标，批量执行不会自动连接。")}
          selectedIds={selectedIds}
          onChange={setSelected}
          disabled={running}
          maxTargets={MAX_TARGETS}
          metadataError={
            assetsQuery.isError
              ? t("资产详情加载失败，别名、主机与标签搜索可能不完整：{value}", { value: describeError(assetsQuery.error) })
              : null
          }
        />

        <div className="flex flex-wrap items-center gap-2 text-[11px] text-neutral-500" role="group" aria-label={t("命令 shell")}>
          <span>{t("Shell")}</span>
          {EXEC_BATCH_SHELLS.map((value) => (
            <button
              key={value}
              type="button"
              className="nx-btn nx-btn-sm"
              aria-pressed={shell === value}
              disabled={running}
              onClick={() => {
                shellChosenRef.current = true;
                setShell(value);
                discardProductionConfirmation();
              }}
            >
              {SHELL_LABELS[value]}
            </button>
          ))}
          {shellsQuery.isError && (
            <span className="text-amber-300">{t("未能读取目标平台：{value}", { value: describeError(shellsQuery.error) })}</span>
          )}
        </div>
        {(skippedTargets.length > 0 || detectCount > 0) && (
          <div className="flex flex-col gap-0.5 text-[11px] text-amber-300" data-testid="exec-batch-skip-preview" role="status">
            {skippedTargets.length > 0 && (
              <span>{t("将跳过 {count} 个目标，不会执行：", { count: skippedTargets.length })}</span>
            )}
            {skippedTargets.map((target) => (
              <span key={target.id} className="truncate pl-2" title={target.reason}>
                {t("{label}：{reason}", { label: targetLabels.get(target.id) ?? target.id, reason: target.reason })}
              </span>
            ))}
            {detectCount > 0 && (
              <span>{t("{count} 个 SSH 目标将在执行前检测是否为 Windows，不是则跳过", { count: detectCount })}</span>
            )}
          </div>
        )}

        <label className="flex flex-col gap-1 text-[11px] text-neutral-500">
          {t("命令")}
          <input
            className="nx-input font-mono text-xs"
            value={command}
            disabled={running}
            placeholder={SHELL_PLACEHOLDERS[shell]}
            aria-label={t("批量执行命令")}
            onChange={(event) => {
              setCommand(event.target.value);
              discardProductionConfirmation();
            }}
          />
        </label>

        <div className="flex flex-wrap items-end gap-2">
          <label className="flex flex-col gap-1 text-[11px] text-neutral-500">
            {t("并发数")}
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
            {t("单目标超时（秒）")}
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
              {canceling ? t("正在取消…") : t("取消本批次")}
            </button>
          ) : (
            <button type="submit" className="nx-btn nx-btn-primary" disabled={execTargets.length === 0}>
              <IconPlay size={13} />
              {t("执行")}
            </button>
          )}
        </div>

        <div className="text-[11px] leading-relaxed text-neutral-500">
          {t("输出过长会被截断；结果只保留在当前面板，审计记录不含命令输出。")}
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
            {t("正在执行批次 {runningId}；取消请求与执行请求可并行，等待各目标返回。", { runningId })}
          </div>
        )}
      </form>

      {progressBatchId !== null && (
        <div className="border-t border-neutral-800/70 px-3 py-2">
          <BatchProgressPanel
            ref={progressRef}
            api={liveBatchProgressApi}
            batchId={progressBatchId}
            pollIntervalMs={progressPollIntervalMs}
            original={{
              command: progressCommand,
              shell: progressShell,
              timeoutMs: Number(timeoutSeconds) * 1000,
              maxConcurrent: Number(concurrency),
            }}
            onConfirmationRequired={handleRerunConfirmation}
            onRerunStart={handleRerunStart}
            rerunDisabled={batch !== null && batch.command === PER_TARGET_COMMANDS}
            newBatchId={createExecBatchId}
          />
        </div>
      )}

      {promptElement}
      {productionConfirmation && (
        <ProductionConfirmationDialog
          key={`${productionConfirmation.challenge.targetSetHash}:${productionConfirmation.challenge.commandHash}`}
          challenge={productionConfirmation.challenge}
          contentHash={productionConfirmation.challenge.commandHash}
          command={shellCommandText(productionConfirmation.request.shell, productionConfirmation.request.command)}
          onCancel={discardProductionConfirmation}
          onConfirm={confirmProduction}
        />
      )}

      {rerunConfirmation && (
        <ProductionConfirmationDialog
          key={`rerun:${rerunConfirmation.challenge.targetSetHash}:${rerunConfirmation.challenge.commandHash}`}
          challenge={rerunConfirmation.challenge}
          contentHash={rerunConfirmation.challenge.commandHash}
          command={shellCommandText(rerunConfirmation.request.shell, rerunConfirmation.request.command)}
          onCancel={() => setRerunConfirmation(null)}
          onConfirm={confirmRerunProduction}
        />
      )}

      {batch && (
        <div className="border-t border-neutral-800/70">
          <div className="flex flex-wrap items-center gap-2 px-3 py-2 text-[11px]">
            <span className="font-medium text-neutral-300">{t("结果矩阵")}</span>
            <span className="nx-hint">{resultSummary(batch.results)}</span>
            <span className="nx-badge">{SHELL_LABELS[(batch.shell || "posix") as ExecBatchShell] ?? batch.shell}</span>
            <span className="nx-spacer" />
            <span className="nx-hint" title={batch.batchId}>
              {t("批次 {slice}", { slice: batch.batchId.slice(0, 8) })}
            </span>
          </div>
          <div className="overflow-x-auto px-3 pb-3">
            <table className="nx-table nx-table-cards nx-table-fixed min-w-[720px]">
              <thead>
                <tr>
                  <th className="w-1/5">{t("目标")}</th>
                  <th className="w-28">{t("状态")}</th>
                  <th className="w-20">{t("退出码")}</th>
                  <th className="w-24">{t("耗时")}</th>
                  <th>{t("输出 / 错误")}</th>
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
                    <td data-label={t("状态")} className={statusClass(result.status)}>
                      {STATUS_LABELS[result.status]}
                      {result.truncated && <span className="ml-1 text-[10px] text-amber-300">{t("已截断")}</span>}
                    </td>
                    <td data-label={t("退出码")} className="nx-mono">
                      {result.exitCode ?? "—"}
                      {result.signal ? ` (${result.signal})` : ""}
                    </td>
                    <td data-label={t("耗时")} className="nx-mono">{result.durationMs} ms</td>
                    <td>
                      {result.error && (
                        <div className={`mb-1 whitespace-pre-wrap break-all ${result.status === "skipped" ? "text-amber-300" : "text-red-300"}`}>
                          {result.error}
                        </div>
                      )}
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
