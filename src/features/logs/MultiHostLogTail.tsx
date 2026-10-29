import { useEffect, useMemo, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { useWorkspaceList, type Workspace } from "../../app/store";
import { assetApi, type Asset, type SessionInfo } from "../../ipc/commands";
import { WEB } from "../../ipc/env";
import { createJsonChannel, disposeChannel, type IpcChannel } from "../../ipc/events";
import {
  createLogTailRunId,
  logTailApi,
  type LogTailEvent,
  type LogTailSource,
  type LogTailSourceKind,
  type LogTailSourceStatus,
} from "../../ipc/logTailApi";
import { saveBrowserBlob } from "../../ipc/webFiles";
import { describeError } from "../../ui/errorText";
import { pickSavePath } from "../../ui/dialogs";
import { IconChevronDown, IconDownload, IconInfo, IconPlay, IconServer, IconSettings, IconStop, IconTrash } from "../../ui/icons";
import { LogPopover } from "./LogPopover";
import { assetTagValues } from "../explorer/assetSearch";
import { TargetSetPicker, type TargetSetPickerTarget } from "../execbatch/TargetSetPicker";
import {
  compileLogFilters,
  exportMergedLines,
  filterMergedLines,
  highlightSegments,
  hostColor,
  LOG_STATUS_LABELS as STATUS_LABELS,
  MergedLogBuffer,
  regexErrorNotice,
} from "./mergedLog";

const MAX_TARGETS = 64;
const MAX_CONCURRENT = 32;
const MAX_LINES = 50_000;
const STOP_CONFIRM_TIMEOUT_MS = 31_000;
const STOP_CONFIRM_ERROR = "日志流已停止，但结束状态未确认；未收到的末尾事件已放弃。";
const CANCEL_UNCONFIRMED_ERROR = "取消未确认，远端可能仍在运行。";

interface SourceDraft {
  kind: LogTailSourceKind;
  value: string;
}

interface SourceViewStatus {
  status: LogTailSourceStatus | "idle";
  error?: string;
}

interface RunSource {
  sourceId: string;
  sessionId: string;
  host: string;
  kind: LogTailSourceKind;
  value: string;
}

interface ActiveRun {
  id: string;
  channel: IpcChannel<unknown>;
  closed: boolean;
  cancelRequested: boolean;
  cancelConfirmed: boolean;
  stopTimer: number | null;
}

function clearStopTimer(run: ActiveRun): void {
  if (run.stopTimer !== null) window.clearTimeout(run.stopTimer);
  run.stopTimer = null;
}

function allSourcesTerminal(
  sources: ReadonlyMap<string, RunSource>,
  statuses: Record<string, SourceViewStatus>,
): boolean {
  if (sources.size === 0) return false;
  for (const sourceId of sources.keys()) {
    const status = statuses[sourceId]?.status;
    if (status !== "exited" && status !== "failed" && status !== "canceled") return false;
  }
  return true;
}

const INFO_TEXT =
  "只读：仅执行只读的日志命令，不会修改主机，无需生产确认。仅支持当前已连接的 SSH / 受管设备（Windows 设备暂不支持），不会自动连接。";

const SOURCE_KIND_LABELS: Record<LogTailSourceKind, string> = {
  file: "文件",
  journal: "journalctl",
  docker: "Docker 容器",
};

const SOURCE_PLACEHOLDERS: Record<LogTailSourceKind, string> = {
  file: "/var/log/app.log",
  journal: "nginx.service",
  docker: "container-name",
};

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
    .filter((session) => (session.kind === "ssh" || session.kind === "device") && session.status === "connected")
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
        endpoint: host ? `${username ? `${username}@` : ""}${host}` : session.kind === "device" ? "受管设备" : null,
        tags: assetTagValues(asset?.tags ?? ""),
        workspaceNames: [...new Set(sources.map((workspace) => workspace.title || workspace.id))],
        envCount: assetEnvCount(asset),
      };
    });
}

function statusTone(status: SourceViewStatus["status"]): string {
  if (status === "running") return "text-green-300";
  if (status === "failed") return "text-red-300";
  if (status === "queued" || status === "starting") return "text-amber-300";
  return "text-neutral-500";
}

// initialSelected preselects targets when the view opens, such as the active host workspace's session; ids that are not
// connected SSH or device sessions are dropped.
export function MultiHostLogTail({
  sessions,
  initialSelected,
}: {
  sessions: SessionInfo[];
  initialSelected?: readonly string[];
}) {
  const workspaces = useWorkspaceList();
  const assetsQuery = useQuery({ queryKey: ["assets"], queryFn: () => assetApi.list(), retry: false });
  const targets = useMemo(
    () => collectTargets(sessions, assetsQuery.data ?? [], workspaces),
    [sessions, assetsQuery.data, workspaces],
  );
  const [selected, setSelected] = useState<string[]>(() => [...(initialSelected ?? [])]);
  const [drafts, setDrafts] = useState<Record<string, SourceDraft>>({});
  const [concurrency, setConcurrency] = useState("4");
  const [lineCap, setLineCap] = useState("5000");
  const [include, setInclude] = useState("");
  const [exclude, setExclude] = useState("");
  const [paused, setPaused] = useState(false);
  const [running, setRunning] = useState(false);
  const [stopping, setStopping] = useState(false);
  const [saving, setSaving] = useState(false);
  const [runSources, setRunSources] = useState<RunSource[]>([]);
  const [statuses, setStatuses] = useState<Record<string, SourceViewStatus>>({});
  const [runError, setRunError] = useState<string | null>(null);
  const [saveMessage, setSaveMessage] = useState<string | null>(null);
  const [, setRevision] = useState(0);
  const [capacity, setCapacity] = useState(5000);
  const [hostsOpen, setHostsOpen] = useState(() => (initialSelected?.length ?? 0) === 0);
  const [moreOpen, setMoreOpen] = useState(false);

  const bufferRef = useRef(new MergedLogBuffer(5000));
  const activeRef = useRef<ActiveRun | null>(null);
  const metadataRef = useRef(new Map<string, RunSource>());
  const statusesRef = useRef<Record<string, SourceViewStatus>>({});
  const eventRunIdRef = useRef<string | null>(null);
  const eventQueueRef = useRef<LogTailEvent[]>([]);
  const flushTimerRef = useRef<number | null>(null);

  useEffect(() => {
    const available = new Set(targets.map((target) => target.id));
    setSelected((current) => {
      const next = current.filter((id) => available.has(id));
      return next.length === current.length ? current : next;
    });
  }, [targets]);

  useEffect(
    () => () => {
      if (flushTimerRef.current !== null) window.clearTimeout(flushTimerRef.current);
      flushTimerRef.current = null;
      eventQueueRef.current = [];
      const run = activeRef.current;
      activeRef.current = null;
      if (!run) return;
      run.cancelRequested = true;
      clearStopTimer(run);
      run.closed = true;
      void logTailApi
        .cancel(run.id)
        .catch(() => undefined)
        .finally(() => disposeChannel(run.channel));
    },
    [],
  );

  const finishRun = (run: ActiveRun, message?: string, clearError = false) => {
    if (run.closed) return;
    const current = activeRef.current === run;
    clearStopTimer(run);
    run.closed = true;
    disposeChannel(run.channel);
    if (!current) return;
    activeRef.current = null;
    if (message) setRunError(message);
    else if (clearError) setRunError(null);
    setRunning(false);
    setStopping(false);
  };

  const flushEvents = () => {
    flushTimerRef.current = null;
    const events = eventQueueRef.current.splice(0);
    if (events.length === 0) return;
    let nextStatuses = statusesRef.current;
    let statusesChanged = false;
    for (const event of events) {
      if (event.runId !== eventRunIdRef.current) continue;
      if (event.type === "status" && event.sourceId && event.status && metadataRef.current.has(event.sourceId)) {
        if (!statusesChanged) {
          nextStatuses = { ...nextStatuses };
          statusesChanged = true;
        }
        nextStatuses[event.sourceId] = { status: event.status, error: event.error || undefined };
      } else if (event.type === "line" && event.sourceId) {
        const source = metadataRef.current.get(event.sourceId);
        if (!source) continue;
        bufferRef.current.append({
          sequence: event.sequence,
          sourceId: event.sourceId,
          sessionId: event.sessionId ?? source.sessionId,
          host: source.host,
          text: event.text ?? "",
          stderr: event.stderr === true,
          timeMs: event.timeMs,
        });
      } else if (event.type === "done") {
        const active = activeRef.current;
        if (active?.id === event.runId) finishRun(active, undefined, true);
      }
    }
    if (statusesChanged) {
      statusesRef.current = nextStatuses;
      setStatuses(nextStatuses);
    }
    const active = activeRef.current;
    if (active?.cancelRequested && allSourcesTerminal(metadataRef.current, statusesRef.current)) {
      finishRun(active, undefined, true);
    }
    setRevision((current) => current + 1);
  };

  const enqueueEvent = (run: ActiveRun, event: LogTailEvent) => {
    if (run.closed || event.runId !== run.id) return;
    eventQueueRef.current.push(event);
    if (flushTimerRef.current === null) flushTimerRef.current = window.setTimeout(flushEvents, 40);
  };

  const selectedIds = targets.filter((target) => selected.includes(target.id)).map((target) => target.id);
  const start = () => {
    if (activeRef.current) return;
    const maxConcurrent = Number(concurrency);
    const maxLines = Number(lineCap);
    setRunError(null);
    setSaveMessage(null);
    if (selectedIds.length === 0) {
      setHostsOpen(true);
      setRunError("请至少选择一个已连接的 SSH 或受管设备会话");
      return;
    }
    if (!Number.isInteger(maxConcurrent) || maxConcurrent < 1 || maxConcurrent > MAX_CONCURRENT) {
      setRunError(`并发数必须是 1-${MAX_CONCURRENT} 的整数`);
      return;
    }
    if (!Number.isInteger(maxLines) || maxLines < 100 || maxLines > MAX_LINES) {
      setRunError(`行数上限必须是 100-${MAX_LINES} 的整数`);
      return;
    }
    const targetsById = new Map(targets.map((target) => [target.id, target]));
    const sources: LogTailSource[] = [];
    const nextRunSources: RunSource[] = [];
    for (const sessionId of selectedIds) {
      const target = targetsById.get(sessionId);
      const draft = drafts[sessionId] ?? { kind: "file" as const, value: "" };
      if (!draft.value.trim()) {
        setHostsOpen(true);
        setRunError(`请填写目标「${target?.label ?? sessionId}」的日志来源`);
        return;
      }
      sources.push({ sourceId: sessionId, sessionId, kind: draft.kind, value: draft.value });
      nextRunSources.push({ sourceId: sessionId, sessionId, host: target?.label ?? sessionId, ...draft });
    }

    setHostsOpen(false);
    setMoreOpen(false);
    const id = createLogTailRunId();
    bufferRef.current = new MergedLogBuffer(maxLines);
    setCapacity(maxLines);
    setPaused(false);
    setRunSources(nextRunSources);
    metadataRef.current = new Map(nextRunSources.map((source) => [source.sourceId, source]));
    const initialStatuses = Object.fromEntries(
      nextRunSources.map((source) => [source.sourceId, { status: "queued" as const }]),
    );
    statusesRef.current = initialStatuses;
    setStatuses(initialStatuses);
    eventRunIdRef.current = id;
    eventQueueRef.current = [];
    if (flushTimerRef.current !== null) window.clearTimeout(flushTimerRef.current);
    flushTimerRef.current = null;
    setRevision((current) => current + 1);
    setRunning(true);
    setStopping(false);
    const run = {} as ActiveRun;
    run.id = id;
    run.closed = false;
    run.cancelRequested = false;
    run.cancelConfirmed = false;
    run.stopTimer = null;
    run.channel = createJsonChannel((raw) => enqueueEvent(run, raw as unknown as LogTailEvent));
    activeRef.current = run;
    void logTailApi.start({ runId: id, sources, maxConcurrent }, run.channel)
      .then(() => {
        if (run.cancelRequested) void logTailApi.cancel(run.id).catch(() => undefined);
      })
      .catch((cause) => {
        if (run.closed) return;
        const message = describeError(cause);
        const failedStatuses = Object.fromEntries(
          nextRunSources.map((source) => [source.sourceId, { status: "failed" as const, error: message }]),
        );
        statusesRef.current = failedStatuses;
        setStatuses(failedStatuses);
        setRunError(message);
        finishRun(run);
        setRevision((current) => current + 1);
      });
  };

  const stop = async () => {
    const run = activeRef.current;
    if (!run || stopping) return;
    run.cancelRequested = true;
    if (run.stopTimer === null) {
      run.stopTimer = window.setTimeout(() => {
        if (run.closed) return;
        clearStopTimer(run);
        if (run.cancelConfirmed) {
          finishRun(run, STOP_CONFIRM_ERROR);
        } else if (activeRef.current === run) {
          setRunError(CANCEL_UNCONFIRMED_ERROR);
          setStopping(false);
        }
      }, STOP_CONFIRM_TIMEOUT_MS);
    }
    setStopping(true);
    setRunError(null);
    try {
      await logTailApi.cancel(run.id);
      if (run.closed) return;
      run.cancelConfirmed = true;
      if (run.stopTimer === null) finishRun(run, STOP_CONFIRM_ERROR);
    } catch (cause) {
      if (run.closed) return;
      clearStopTimer(run);
      if (activeRef.current === run) {
        setRunError(`${CANCEL_UNCONFIRMED_ERROR} ${describeError(cause)}`);
      }
    } finally {
      if (activeRef.current === run && !run.closed) setStopping(false);
    }
  };

  const filters = useMemo(() => compileLogFilters(include, exclude), [include, exclude]);
  const visibleLines = filterMergedLines(bufferRef.current.visibleLines(), filters);

  useEffect(() => {
    if (!saveMessage) return;
    const timer = window.setTimeout(() => setSaveMessage(null), 3500);
    return () => window.clearTimeout(timer);
  }, [saveMessage]);

  const save = async () => {
    const content = exportMergedLines(bufferRef.current.retainedLines());
    if (!content || saving) return;
    setSaving(true);
    setRunError(null);
    setSaveMessage(null);
    const name = `nexterm-merged-${new Date().toISOString().replace(/[:.]/g, "-")}.log`;
    try {
      if (WEB) {
        const saved = await saveBrowserBlob(name, new Blob([content], { type: "text/plain;charset=utf-8" }));
        if (saved) setSaveMessage(`已保存 ${bufferRef.current.retainedLines().length} 行合并日志`);
      } else {
        const path = await pickSavePath(name);
        if (!path) return;
        const result = await logTailApi.save(path, content);
        setSaveMessage(`已保存到 ${result.path}`);
      }
    } catch (cause) {
      setRunError(`保存失败：${describeError(cause)}`);
    } finally {
      setSaving(false);
    }
  };

  const retained = bufferRef.current.retainedLines().length;
  const failedSources = runSources.filter((source) => statuses[source.sourceId]?.status === "failed").length;
  const hostsSummary = selectedIds.length === 0 ? "选择主机" : `${selectedIds.length} 台主机`;
  const emptyMessage =
    targets.length === 0
      ? "没有已连接的 SSH / 受管设备。先在左侧连接主机，再回到这里合并跟随日志。"
      : retained > 0
        ? "没有匹配当前过滤条件的日志"
        : running
          ? "等待日志…"
          : selectedIds.length === 0
            ? "点击上方「选择主机」，勾选主机并填写日志来源，然后开始跟随。"
            : "点击「开始跟随」查看合并日志。";

  return (
    <section className="nx-pane" aria-label="多主机日志合并" data-testid="multi-host-log-tail">
      <div className="flex flex-none flex-wrap items-center gap-1.5 border-b border-neutral-800/70 px-2.5 py-1.5">
        <IconServer size={14} className="text-neutral-500" />
        <h2 className="nx-toolbar-title">多主机日志</h2>
        <span
          className="inline-flex text-neutral-500"
          role="img"
          tabIndex={0}
          title={INFO_TEXT}
          aria-label={INFO_TEXT}
        >
          <IconInfo size={13} />
        </span>
        <LogPopover
          open={hostsOpen}
          onOpenChange={setHostsOpen}
          label="选择主机与来源"
          trigger={
            <button
              type="button"
              className="nx-btn nx-btn-ghost nx-btn-sm"
              aria-expanded={hostsOpen}
              aria-label="选择主机与来源"
              onClick={() => setHostsOpen((current) => !current)}
            >
              {hostsSummary}
              <IconChevronDown size={12} />
            </button>
          }
        >
          <div className="flex flex-col gap-3">
            <TargetSetPicker
              targets={targets}
              selectedIds={selectedIds}
              onChange={setSelected}
              disabled={running}
              maxTargets={MAX_TARGETS}
              metadataError={assetsQuery.isError ? `未能加载资产详情：${describeError(assetsQuery.error)}` : null}
            />
            {selectedIds.length > 0 && (
              <div className="flex flex-col gap-2">
                {selectedIds.map((sessionId) => {
                  const target = targets.find((candidate) => candidate.id === sessionId);
                  const draft = drafts[sessionId] ?? { kind: "file" as const, value: "" };
                  return (
                    <div key={sessionId} className="flex min-w-0 flex-wrap items-center gap-2 rounded border border-neutral-800/70 p-2 sm:flex-nowrap">
                      <span className={`w-24 shrink-0 truncate text-[11px] ${hostColor(sessionId)}`} title={target?.label}>
                        {target?.label ?? sessionId}
                      </span>
                      <select
                        className="nx-input nx-input-sm w-28 shrink-0"
                        value={draft.kind}
                        disabled={running}
                        aria-label={`来源类型 ${target?.label ?? sessionId}`}
                        onChange={(event) =>
                          setDrafts((current) => ({
                            ...current,
                            [sessionId]: { ...draft, kind: event.target.value as LogTailSourceKind },
                          }))
                        }
                      >
                        {Object.entries(SOURCE_KIND_LABELS).map(([kind, label]) => (
                          <option key={kind} value={kind}>{label}</option>
                        ))}
                      </select>
                      <input
                        className="nx-input nx-input-sm min-w-[8rem] flex-1 basis-full font-mono sm:basis-0"
                        value={draft.value}
                        disabled={running}
                        placeholder={SOURCE_PLACEHOLDERS[draft.kind]}
                        aria-label={`来源 ${target?.label ?? sessionId}`}
                        onChange={(event) =>
                          setDrafts((current) => ({
                            ...current,
                            [sessionId]: { ...draft, value: event.target.value },
                          }))
                        }
                      />
                    </div>
                  );
                })}
              </div>
            )}
          </div>
        </LogPopover>
        <input
          className="nx-input nx-input-sm min-w-0 flex-1 basis-40 font-mono sm:max-w-xs"
          value={include}
          placeholder="只显示匹配的行（正则表达式），例如 ERROR|WARN"
          aria-label="包含正则"
          onChange={(event) => setInclude(event.target.value)}
        />
        <span className="nx-spacer" />
        {running ? (
          <button type="button" className="nx-btn nx-btn-danger nx-btn-sm" disabled={stopping} onClick={() => void stop()}>
            <IconStop size={13} />
            {stopping ? "正在取消…" : "停止全部"}
          </button>
        ) : (
          <button
            type="button"
            className="nx-btn nx-btn-primary nx-btn-sm"
            disabled={targets.length === 0}
            title={targets.length === 0 ? "当前没有已连接的 SSH 或受管设备会话，请先连接主机" : undefined}
            onClick={start}
          >
            <IconPlay size={13} />
            开始跟随
          </button>
        )}
        <button
          type="button"
          className="nx-btn nx-btn-ghost nx-btn-sm"
          aria-pressed={paused}
          onClick={() => {
            const next = !paused;
            bufferRef.current.setPaused(next);
            setPaused(next);
            setRevision((current) => current + 1);
          }}
        >
          {paused ? "继续显示" : "暂停显示"}
        </button>
        <LogPopover
          open={moreOpen}
          onOpenChange={setMoreOpen}
          label="更多选项"
          align="right"
          widthClass="w-[min(18rem,calc(100vw-1.5rem))]"
          trigger={
            <button
              type="button"
              className="nx-icon-btn"
              title="更多选项"
              aria-label="更多选项"
              aria-expanded={moreOpen}
              onClick={() => setMoreOpen((current) => !current)}
            >
              <IconSettings size={13} />
            </button>
          }
        >
          <div className="flex flex-col gap-3">
            <label className="flex flex-col gap-1 text-[11px] text-neutral-500">
              排除（正则表达式）
              <input
                className="nx-input nx-input-sm font-mono"
                value={exclude}
                placeholder="隐藏匹配的行（正则表达式）"
                aria-label="排除正则"
                onChange={(event) => setExclude(event.target.value)}
              />
            </label>
            <div className="flex gap-2">
              <label className="flex flex-1 flex-col gap-1 text-[11px] text-neutral-500">
                并发数
                <input
                  type="number"
                  className="nx-input nx-input-sm"
                  min={1}
                  max={MAX_CONCURRENT}
                  value={concurrency}
                  disabled={running}
                  onChange={(event) => setConcurrency(event.target.value)}
                />
              </label>
              <label className="flex flex-1 flex-col gap-1 text-[11px] text-neutral-500">
                保留行数
                <input
                  type="number"
                  className="nx-input nx-input-sm"
                  min={100}
                  max={MAX_LINES}
                  value={lineCap}
                  disabled={running}
                  onChange={(event) => setLineCap(event.target.value)}
                />
              </label>
            </div>
            <div className="flex gap-2">
              <button
                type="button"
                className="nx-btn nx-btn-ghost nx-btn-sm"
                disabled={saving || retained === 0}
                onClick={() => void save()}
              >
                <IconDownload size={13} />
                {saving ? "正在保存…" : "保存日志"}
              </button>
              <button
                type="button"
                className="nx-btn nx-btn-ghost nx-btn-sm"
                onClick={() => {
                  bufferRef.current.clear();
                  setRevision((current) => current + 1);
                }}
              >
                <IconTrash size={13} />
                清空日志
              </button>
            </div>
          </div>
        </LogPopover>
      </div>
      {filters.error && (
        <div role="alert" className="border-b border-amber-900/50 bg-amber-950/20 px-3 py-1.5 text-[11px] text-amber-300">
          {regexErrorNotice(filters.error)}
        </div>
      )}
      {runError && <div role="alert" className="px-3 py-1.5 text-[11px] text-red-300">{runError}</div>}
      {saveMessage && <div role="status" className="px-3 py-1.5 text-[11px] text-green-300">{saveMessage}</div>}
      {paused && (
        <div role="status" className="border-b border-neutral-800/70 px-3 py-1 text-[11px] text-amber-300">
          显示已暂停；后台仍在接收（保留数量有上限），恢复后将显示最新内容。
        </div>
      )}

      <div
        role="log"
        aria-label="多主机合并日志内容"
        className="min-h-0 flex-1 overflow-auto bg-term px-2 py-2 font-mono text-[12px] leading-[1.55]"
      >
        {visibleLines.length === 0 ? (
          <div className="px-2 py-4 text-center text-[11px] text-neutral-500">{emptyMessage}</div>
        ) : (
          visibleLines.map((line) => (
            <div key={line.sequence} className="flex min-w-max gap-2 rounded px-1 hover:bg-neutral-800/30">
              <span className={`shrink-0 select-none ${hostColor(line.sessionId)}`} title={line.sessionId}>
                [{line.host}]
              </span>
              <span className={`whitespace-pre ${line.stderr ? "text-rose-300" : "text-neutral-300"}`}>
                {highlightSegments(line.text, filters.highlight).map((segment, index) =>
                  segment.matched ? (
                    <mark key={index} className="rounded-[2px] bg-amber-300/40 px-0 text-inherit">{segment.text}</mark>
                  ) : (
                    <span key={index}>{segment.text}</span>
                  ),
                )}
              </span>
            </div>
          ))
        )}
      </div>

      {runSources.length > 0 && (
        <details className="shrink-0 border-t border-neutral-800/70 px-3 py-1" open={failedSources > 0}>
          <summary className="cursor-pointer select-none text-[11px] text-neutral-400">
            来源状态（{runSources.length}{failedSources > 0 ? `，${failedSources} 个失败` : ""}）
          </summary>
          <div className="mt-1 grid max-h-32 grid-cols-1 gap-1 overflow-y-auto xl:grid-cols-2">
            {runSources.map((source) => {
              const state = statuses[source.sourceId] ?? { status: "idle" as const };
              return (
                <div key={source.sourceId} className="flex min-w-0 items-start gap-2 rounded px-1 py-1 text-[11px]">
                  <span className={`shrink-0 font-mono ${hostColor(source.sessionId)}`}>[{source.host}]</span>
                  <span className="shrink-0 text-neutral-500">{SOURCE_KIND_LABELS[source.kind]}</span>
                  <span className={`shrink-0 ${statusTone(state.status)}`}>{STATUS_LABELS[state.status]}</span>
                  {state.error && <span className="min-w-0 break-all text-red-300">{state.error}</span>}
                </div>
              );
            })}
          </div>
        </details>
      )}
      <div className="flex-none border-t border-neutral-800/70 px-3 py-1">
        <span className="nx-hint" role="status">
          显示 {visibleLines.length} · 保留 {retained} / {capacity} · 已丢弃 {bufferRef.current.droppedCount}
        </span>
      </div>
    </section>
  );
}
