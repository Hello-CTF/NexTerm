import { useEffect, useMemo, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { useUi, type Workspace } from "../../app/store";
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
import { IconDownload, IconPlay, IconServer, IconStop, IconTrash } from "../../ui/icons";
import { assetTagValues } from "../explorer/assetSearch";
import { TargetSetPicker, type TargetSetPickerTarget } from "../execbatch/TargetSetPicker";
import {
  compileLogFilters,
  exportMergedLines,
  filterMergedLines,
  highlightSegments,
  hostColor,
  MergedLogBuffer,
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

const STATUS_LABELS: Record<LogTailSourceStatus | "idle", string> = {
  idle: "未启动",
  queued: "等待并发槽",
  starting: "启动中",
  running: "跟随中",
  exited: "已退出",
  failed: "失败",
  canceled: "已取消",
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

function statusTone(status: SourceViewStatus["status"]): string {
  if (status === "running") return "text-green-300";
  if (status === "failed") return "text-red-300";
  if (status === "queued" || status === "starting") return "text-amber-300";
  return "text-neutral-500";
}

export function MultiHostLogTail({ sessions }: { sessions: SessionInfo[] }) {
  const workspaces = useUi((state) => state.workspaces);
  const assetsQuery = useQuery({ queryKey: ["assets"], queryFn: () => assetApi.list(), retry: false });
  const targets = useMemo(
    () => collectTargets(sessions, assetsQuery.data ?? [], workspaces),
    [sessions, assetsQuery.data, workspaces],
  );
  const [selected, setSelected] = useState<string[]>([]);
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
      setRunError("请至少选择一个已连接 SSH 会话");
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
        setRunError(`请填写目标「${target?.label ?? sessionId}」的日志来源`);
        return;
      }
      sources.push({ sourceId: sessionId, sessionId, kind: draft.kind, value: draft.value });
      nextRunSources.push({ sourceId: sessionId, sessionId, host: target?.label ?? sessionId, ...draft });
    }

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

  return (
    <section className="nx-pane" aria-label="多主机日志合并" data-testid="multi-host-log-tail">
      <div className="nx-toolbar flex-wrap">
        <IconServer size={14} className="text-neutral-500" />
        <h2 className="nx-toolbar-title">多主机日志</h2>
        <span className="nx-badge nx-badge-amber" title="只执行只读日志命令，无需生产确认">
          只读 · 无 PTY
        </span>
        <span className="nx-hint">仅当前已连接 SSH，不自动连接</span>
      </div>

      <div className="flex max-h-[46%] min-h-[220px] flex-col gap-3 overflow-y-auto border-b border-neutral-800/70 px-3 py-3">
        <TargetSetPicker
          targets={targets}
          selectedIds={selectedIds}
          onChange={setSelected}
          disabled={running}
          maxTargets={MAX_TARGETS}
          metadataError={assetsQuery.isError ? `资产详情加载失败：${describeError(assetsQuery.error)}` : null}
        />
        {selectedIds.length > 0 && (
          <div className="grid grid-cols-1 gap-2 xl:grid-cols-2">
            {selectedIds.map((sessionId) => {
              const target = targets.find((candidate) => candidate.id === sessionId);
              const draft = drafts[sessionId] ?? { kind: "file" as const, value: "" };
              return (
                <div key={sessionId} className="flex min-w-0 items-center gap-2 rounded border border-neutral-800/70 p-2">
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
                    className="nx-input nx-input-sm min-w-0 flex-1 font-mono"
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
        <div className="flex flex-wrap items-end gap-2">
          <label className="flex flex-col gap-1 text-[11px] text-neutral-500">
            并发数
            <input
              type="number"
              className="nx-input w-24"
              min={1}
              max={MAX_CONCURRENT}
              value={concurrency}
              disabled={running}
              onChange={(event) => setConcurrency(event.target.value)}
            />
          </label>
          <label className="flex flex-col gap-1 text-[11px] text-neutral-500">
            保留行数
            <input
              type="number"
              className="nx-input w-28"
              min={100}
              max={MAX_LINES}
              value={lineCap}
              disabled={running}
              onChange={(event) => setLineCap(event.target.value)}
            />
          </label>
          <span className="nx-spacer" />
          {running ? (
            <button type="button" className="nx-btn nx-btn-danger" disabled={stopping} onClick={() => void stop()}>
              <IconStop size={13} />
              {stopping ? "正在取消…" : "停止全部"}
            </button>
          ) : (
            <button type="button" className="nx-btn nx-btn-primary" disabled={targets.length === 0} onClick={start}>
              <IconPlay size={13} />
              开始跟随
            </button>
          )}
        </div>
      </div>

      <div className="flex flex-wrap items-center gap-2 border-b border-neutral-800/70 px-3 py-2">
        <input
          className="nx-input nx-input-sm w-48 font-mono"
          value={include}
          placeholder="包含 regex，例如 ERROR|WARN"
          aria-label="包含正则"
          onChange={(event) => setInclude(event.target.value)}
        />
        <input
          className="nx-input nx-input-sm w-44 font-mono"
          value={exclude}
          placeholder="排除 regex"
          aria-label="排除正则"
          onChange={(event) => setExclude(event.target.value)}
        />
        <button
          type="button"
          className="nx-btn nx-btn-ghost nx-btn-sm"
          onClick={() => {
            const next = !paused;
            bufferRef.current.setPaused(next);
            setPaused(next);
            setRevision((current) => current + 1);
          }}
        >
          {paused ? "继续显示" : "暂停显示"}
        </button>
        <button
          type="button"
          className="nx-icon-btn"
          title="清空当前保留日志"
          aria-label="清空日志"
          onClick={() => {
            bufferRef.current.clear();
            setRevision((current) => current + 1);
          }}
        >
          <IconTrash size={13} />
        </button>
        <button
          type="button"
          className="nx-btn nx-btn-ghost nx-btn-sm"
          disabled={saving || bufferRef.current.retainedLines().length === 0}
          onClick={() => void save()}
        >
          <IconDownload size={13} />
          {saving ? "保存中…" : "保存日志"}
        </button>
        <span className="nx-spacer" />
        <span className="nx-hint" role="status">
          显示 {visibleLines.length} · 保留 {bufferRef.current.retainedLines().length} / {capacity} · 已丢弃 {bufferRef.current.droppedCount}
        </span>
      </div>
      {filters.error && (
        <div role="alert" className="border-b border-amber-900/50 bg-amber-950/20 px-3 py-1.5 text-[11px] text-amber-300">
          正则无效，当前显示未过滤日志：{filters.error}
        </div>
      )}
      {runError && <div role="alert" className="px-3 py-1.5 text-[11px] text-red-300">{runError}</div>}
      {saveMessage && <div role="status" className="px-3 py-1.5 text-[11px] text-green-300">{saveMessage}</div>}
      {paused && (
        <div role="status" className="border-b border-neutral-800/70 px-3 py-1 text-[11px] text-amber-300">
          显示已暂停；后台仍在有界接收，恢复后展示最新保留内容。
        </div>
      )}

      {runSources.length > 0 && (
        <details className="shrink-0 border-b border-neutral-800/70 px-3 py-1.5" open={runSources.some((source) => statuses[source.sourceId]?.status === "failed")}>
          <summary className="cursor-pointer select-none text-[11px] text-neutral-400">
            来源状态（{runSources.length}）
          </summary>
          <div className="mt-1 grid grid-cols-1 gap-1 xl:grid-cols-2">
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

      <div
        role="log"
        aria-label="多主机合并日志内容"
        className="min-h-0 flex-1 overflow-auto bg-term px-2 py-2 font-mono text-[12px] leading-[1.55]"
      >
        {visibleLines.length === 0 ? (
          <div className="px-2 py-4 text-center text-[11px] text-neutral-500">
            {bufferRef.current.retainedLines().length === 0 ? "等待日志…" : "没有匹配当前过滤条件的日志"}
          </div>
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
    </section>
  );
}
