import { Fragment, useCallback, useEffect, useRef, useState, type MouseEvent as ReactMouseEvent, type ReactNode } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ask } from "../../ui/dialogs";
import { isImeKeyEvent } from "../../ui/DialogHost";
import { dockerApi, terminalApi, type ContainerSummary, type ImageSummary } from "../../ipc/commands";
import { useUi } from "../../app/store";
import { createBinaryChannel, disposeChannel, onChannelReopen } from "../../ipc/events";
import { describeError } from "../../ui/errorText";
import { describeDockerError, isDockerNotInstalled } from "./dockerErrors";
import "../../ui/skeleton.css";
import {
  IconArrowLeft,
  IconBox,
  IconDownload,
  IconList,
  IconMonitor,
  IconPlay,
  IconRefresh,
  IconRestart,
  IconStop,
  IconTerminal,
  IconTrash,
} from "../../ui/icons";
import { ContextMenu, type ContextMenuState, type MenuItem } from "../../ui/ContextMenu";
import { useCompactWidth } from "./useCompactWidth";
import { ContainerInsight } from "./ContainerInsight";
import { ComposeStackHeader } from "./ComposeStackHeader";
import { groupContainersByCompose } from "./composeGrouping";
import { DockerDiskPanel } from "./DockerDiskPanel";
import { DockerLogStream } from "./DockerLogStream";
export { createLogLineDecoder } from "./DockerLogStream";

interface LogAttach {
  container: string;
  containerId: string;
  sessionId: string;
  assetId: string | null;
  sessionKnown: boolean;
  hostName: string;
  tabId: string;
  channel: ReturnType<typeof createBinaryChannel>;
  sink: { onBytes: ((bytes: Uint8Array) => void) | null };
}

interface LogIssue {
  text: string;
  ended?: boolean;
}

function imageKey(i: ImageSummary): string {
  return `${i.id}|${i.repository}:${i.tag}`;
}

const CONTAINER_ACTION_LABELS: Record<string, string> = {
  start: "启动",
  stop: "停止",
  restart: "重启",
  remove: "删除",
};

const REFRESH_INTERVAL_OPTIONS = [0, 10_000, 30_000, 60_000];
const DEFAULT_REFRESH_INTERVAL_MS = 30_000;
const REFRESH_INTERVAL_KEY = "nexterm.dockerRefreshMs.v1";

function loadRefreshIntervalMs(): number {
  try {
    const raw = localStorage.getItem(REFRESH_INTERVAL_KEY);
    const value = raw === null ? DEFAULT_REFRESH_INTERVAL_MS : Number(raw);
    return REFRESH_INTERVAL_OPTIONS.includes(value) ? value : DEFAULT_REFRESH_INTERVAL_MS;
  } catch {
    return DEFAULT_REFRESH_INTERVAL_MS;
  }
}

function TableQueryBody({
  colSpan,
  pending,
  error,
  errorPrefix,
  onRetry,
  emptyText,
  isEmpty,
  skeleton,
  children,
}: {
  colSpan: number;
  pending: boolean;
  error: unknown;
  errorPrefix: string;
  onRetry: () => void;
  emptyText: string;
  isEmpty: boolean;
  skeleton: ReactNode;
  children: ReactNode;
}) {
  if (pending) {
    return (
      <>
        {skeleton}
        <tr>
          <td colSpan={colSpan} className="nx-sr-only">
            加载中…
          </td>
        </tr>
      </>
    );
  }
  if (error && isDockerNotInstalled(error)) {
    return (
      <tr>
        <td colSpan={colSpan} className="nx-table-empty">
          这台主机没有安装 Docker，安装后点击重试。
          <button className="nx-btn nx-btn-ghost nx-btn-sm ml-2" onClick={onRetry}>
            <IconRefresh size={12} />
            重试
          </button>
        </td>
      </tr>
    );
  }
  if (error) {
    return (
      <tr>
        <td colSpan={colSpan} className="nx-table-empty">
          <span className="text-red-300">
            {errorPrefix}加载失败 · {describeDockerError(error)}
          </span>
          <button className="nx-btn nx-btn-ghost nx-btn-sm ml-2" onClick={onRetry}>
            <IconRefresh size={12} />
            重试
          </button>
        </td>
      </tr>
    );
  }
  if (isEmpty) {
    return (
      <tr>
        <td colSpan={colSpan} className="nx-table-empty">
          {emptyText}
        </td>
      </tr>
    );
  }
  return <>{children}</>;
}

// Docker refuses to remove a container that is running, paused or restarting, so only settled ones are offered.
function canRemoveContainer(c: ContainerSummary): boolean {
  return c.state === "exited" || c.state === "created" || c.state === "dead";
}

function isUntaggedImage(i: ImageSummary): boolean {
  return !(i.repository && i.repository !== "<none>");
}

function imageRef(i: ImageSummary): string {
  return isUntaggedImage(i) ? i.id : `${i.repository}:${i.tag}`;
}

// Below this pane width the container/image tables are replaced by two-line rows.
const COMPACT_MAX_WIDTH = 820;

function menuAt(e: ReactMouseEvent<HTMLElement>, items: MenuItem[], title?: string): ContextMenuState {
  const rect = e.currentTarget.getBoundingClientRect();
  return { x: rect.right, y: rect.bottom, items, title };
}

const COMPACT_BTN = "nx-icon-btn nx-icon-btn-sm pointer-coarse:h-9 pointer-coarse:w-9";

function MoreButton({ onOpen, label }: { onOpen: (e: ReactMouseEvent<HTMLElement>) => void; label: string }) {
  return (
    <button className={COMPACT_BTN} title={label} aria-label={label} aria-haspopup="menu" onClick={onOpen}>
      <span aria-hidden="true" className="text-[15px] leading-none">⋯</span>
    </button>
  );
}

function CompactNotice({
  pending, error, errorPrefix, onRetry, emptyText, isEmpty, children,
}: {
  pending: boolean; error: unknown; errorPrefix: string; onRetry: () => void; emptyText: string; isEmpty: boolean; children: ReactNode;
}) {
  if (pending) {
    return (
      <div aria-hidden="false">
        {Array.from({ length: 5 }, (_, i) => (
          <div key={i} aria-hidden="true" className="flex items-center gap-2 border-b border-neutral-800/45 px-2.5 py-2.5">
            <span className="nx-skeleton nx-skeleton-icon" />
            <span className="nx-skeleton w-2/3" />
          </div>
        ))}
        <span className="nx-sr-only">加载中…</span>
      </div>
    );
  }
  if (error) {
    return (
      <div className="nx-table-empty">
        {isDockerNotInstalled(error) ? (
          "这台主机没有安装 Docker，安装后点击重试。"
        ) : (
          <span className="text-red-300">{errorPrefix}加载失败 · {describeDockerError(error)}</span>
        )}
        <button className="nx-btn nx-btn-ghost nx-btn-sm ml-2" onClick={onRetry}>
          <IconRefresh size={12} />
          重试
        </button>
      </div>
    );
  }
  if (isEmpty) return <div className="nx-table-empty">{emptyText}</div>;
  return <>{children}</>;
}

export function DockerPanel({ sessionId, visible = true }: { sessionId: string; visible?: boolean }) {
  const qc = useQueryClient();
  const addTab = useUi((s) => s.addTab);
  const pushToast = useUi((s) => s.pushToast);
  const session = useUi((state) => state.sessions.find((entry) => entry.id === sessionId));
  const sessionStatus = session?.status;
  const sessionAssetId = session?.assetId ?? null;
  const [tab, setTab] = useState<"containers" | "images" | "disk">("containers");
  const [attached, setAttached] = useState<LogAttach | null>(null);
  const [logsIssue, setLogsIssue] = useState<LogIssue | null>(null);
  const [logsResumeNote, setLogsResumeNote] = useState<string | null>(null);
  const [insight, setInsight] = useState<ContainerSummary | null>(null);
  const [picked, setPicked] = useState<Set<string>>(() => new Set());
  const [pending, setPending] = useState<Set<string>>(() => new Set());
  const [bulkBusy, setBulkBusy] = useState(false);
  const [refreshMs, setRefreshMs] = useState(loadRefreshIntervalMs);
  const [menu, setMenu] = useState<ContextMenuState | null>(null);
  const [paneRef, compact] = useCompactWidth(COMPACT_MAX_WIDTH);
  const attachInFlight = useRef<number | null>(null);
  const nextAttachId = useRef(0);
  const pendingReconnect = useRef(false);
  const attachGeneration = useRef(0);
  const currentSessionId = useRef(sessionId);
  const previousIdentity = useRef({ sessionId, assetId: sessionAssetId });
  currentSessionId.current = sessionId;
  const attachedMatchesSession = attached?.sessionId === sessionId && attached.assetId === sessionAssetId &&
    (!attached.sessionKnown || Boolean(session));

  const openLogs = useCallback(async (containerId: string, containerName: string, reattach = false) => {
    if (attachInFlight.current !== null) return;
    const attachId = ++nextAttachId.current;
    attachInFlight.current = attachId;
    const generation = attachGeneration.current;
    const target = useUi.getState().sessions.find((entry) => entry.id === sessionId);
    const targetAssetId = target?.assetId ?? null;
    const stillOnTarget = () => {
      const current = useUi.getState().sessions.find((entry) => entry.id === sessionId);
      return currentSessionId.current === sessionId && generation === attachGeneration.current &&
        (target
          ? Boolean(current && current.assetId === targetAssetId && current.status === "connected")
          : !current);
    };
    if (reattach && attached && (attached.sessionId !== sessionId || (attached.sessionKnown && (!target || target.assetId !== attached.assetId)))) {
      attachInFlight.current = null;
      setLogsIssue({ text: "原主机已改变，请返回后重新选择容器", ended: true });
      return;
    }
    if (target && target.status !== "connected") {
      attachInFlight.current = null;
      setLogsIssue({ text: "主机连接中断，连接恢复后将重新读取最近 500 行日志" });
      pendingReconnect.current = true;
      return;
    }
    const sink: LogAttach["sink"] = { onBytes: null };
    const channel = createBinaryChannel((bytes) => sink.onBytes?.(bytes));
    try {
      const kernelTab = await dockerApi.logsAttach(sessionId, containerId, 500, channel);
      if (!stillOnTarget()) {
        void terminalApi.closeTab(kernelTab, "kill", sessionId).catch(() => undefined);
        disposeChannel(channel);
        if (currentSessionId.current !== sessionId) return;
        const current = useUi.getState().sessions.find((entry) => entry.id === sessionId);
        if (reattach) {
          setLogsIssue({
            text: current?.assetId === targetAssetId
              ? "主机连接发生变化，日志跟随已中断；请重新跟随"
              : "原主机已改变，请返回后重新选择容器",
            ended: current?.assetId !== targetAssetId,
          });
        } else {
          pushToast("info", "打开日志时主机连接发生变化，请重新打开日志");
        }
        return;
      }
      setAttached({ container: containerName, containerId, sessionId, assetId: targetAssetId, sessionKnown: Boolean(target), hostName: target?.name ?? sessionId, tabId: kernelTab, channel, sink });
      setLogsIssue(null);
      setLogsResumeNote(reattach
        ? "已重新读取此主机最近 500 行日志；先前画面已替换，断线期间超过 500 行的内容可能缺失。"
        : null);
    } catch (e) {
      disposeChannel(channel);
      if (!stillOnTarget()) return;
      if (reattach) {
        setLogsIssue({ text: `重新跟随失败：${describeError(e)}` });
      } else {
        pushToast("error", `读取日志失败：${describeError(e)}`);
      }
    } finally {
      if (attachInFlight.current === attachId) attachInFlight.current = null;
    }
  }, [sessionId, attached, pushToast]);

  useEffect(() => {
    if (previousIdentity.current.sessionId !== sessionId || previousIdentity.current.assetId !== sessionAssetId) {
      attachGeneration.current++;
      attachInFlight.current = null;
      pendingReconnect.current = false;
    }
    previousIdentity.current = { sessionId, assetId: sessionAssetId };
  }, [sessionId, sessionAssetId]);

  useEffect(() => useUi.subscribe((next, previous) => {
    const before = previous.sessions.find((entry) => entry.id === sessionId);
    const after = next.sessions.find((entry) => entry.id === sessionId);
    if (!before) return;
    if (before.assetId !== after?.assetId || (before.status === "connected" && after?.status !== "connected")) {
      attachGeneration.current++;
      if (before.assetId !== after?.assetId) attachInFlight.current = null;
    }
  }), [sessionId]);

  const changeRefreshMs = (ms: number) => {
    setRefreshMs(ms);
    try {
      localStorage.setItem(REFRESH_INTERVAL_KEY, String(ms));
    } catch {
    }
  };

  const autoRefreshMs = visible && refreshMs > 0 ? refreshMs : false;
  const containers = useQuery({
    queryKey: ["docker-ps", sessionId],
    queryFn: () => dockerApi.ps(sessionId),
    refetchInterval: tab === "containers" ? autoRefreshMs : false,
  });
  const images = useQuery({
    queryKey: ["docker-images", sessionId],
    queryFn: () => dockerApi.images(sessionId),
    refetchInterval: tab === "images" ? autoRefreshMs : false,
  });
  const compose = useQuery({
    queryKey: ["docker-compose-stacks", sessionId],
    queryFn: () => dockerApi.composeStacks(sessionId),
    refetchInterval: tab === "containers" ? autoRefreshMs : false,
  });

  const cRows = containers.data ?? [];
  const iRows = images.data ?? [];

  const changeTab = (next: "containers" | "images" | "disk") => {
    if (next === tab) return;
    setTab(next);
    if (!visible) return;
    if (next === "disk") void qc.invalidateQueries({ queryKey: ["docker-disk", sessionId] });
    else void (next === "containers" ? containers.refetch() : images.refetch());
  };

  useEffect(() => {
    setPicked(new Set());
  }, [tab]);

  useEffect(() => {
    const available =
      tab === "containers"
        ? containers.data
          ? new Set(containers.data.map((c) => c.id))
          : null
        : images.data
          ? new Set(images.data.map(imageKey))
          : null;
    if (!available) return;
    setPicked((prev) => {
      const next = new Set([...prev].filter((key) => available.has(key)));
      return next.size === prev.size ? prev : next;
    });
  }, [tab, containers.data, images.data]);

  useEffect(() => {
    if (!attached) return;
    const { tabId, channel } = attached;
    return () => {
      void terminalApi
        .closeTab(tabId, "kill", attached.sessionId)
        .then(() => useUi.getState().resyncSessions({ silent: true }))
        .catch(() => undefined);
      disposeChannel(channel);
    };
  }, [attached]);

  useEffect(() => {
    if (!attached) return;
    const { channel, containerId, container } = attached;
    let cancelled = false;
    const off = onChannelReopen(channel, () => {
      if (cancelled) return;
      const current = useUi.getState().sessions.find((entry) => entry.id === attached.sessionId);
      if (attached.sessionId !== currentSessionId.current || attached.assetId !== (current?.assetId ?? null) ||
          (attached.sessionKnown && !current)) {
        return;
      }
      if (current && current.status !== "connected") {
        pendingReconnect.current = true;
        setLogsIssue({ text: "主机连接中断，连接恢复后将重新读取最近 500 行日志" });
        return;
      }
      const state = qc.getQueryData<ContainerSummary[]>(["docker-ps", sessionId])?.find(
        (c) => c.id === containerId,
      )?.state;
      if (state !== undefined && state !== "running") {
        setLogsIssue({ text: "容器已停止，不再产生新日志", ended: true });
        return;
      }
      void openLogs(containerId, container, true);
    });
    return () => {
      cancelled = true;
      off();
    };
  }, [attached, openLogs, qc, sessionId]);

  useEffect(() => {
    if (!attached) return;
    if (!attachedMatchesSession) {
      pendingReconnect.current = false;
      attachGeneration.current++;
      attachInFlight.current = null;
      setAttached(null);
      setLogsIssue(null);
      setLogsResumeNote(null);
      return;
    }
    if (!sessionStatus || sessionStatus === "connected") {
      if (sessionStatus === "connected" && pendingReconnect.current && attachInFlight.current === null) {
        pendingReconnect.current = false;
        void openLogs(attached.containerId, attached.container, true);
      }
      return;
    }
    pendingReconnect.current = true;
    setLogsIssue({ text: "主机连接中断，连接恢复后将重新读取最近 500 行日志" });
  }, [attached, attachedMatchesSession, openLogs, sessionStatus]);

  const markPending = (key: string, on: boolean) =>
    setPending((prev) => {
      const next = new Set(prev);
      if (on) next.add(key);
      else next.delete(key);
      return next;
    });

  const togglePick = (key: string, on: boolean) =>
    setPicked((prev) => {
      const next = new Set(prev);
      if (on) next.add(key);
      else next.delete(key);
      return next;
    });

  const act = async (c: ContainerSummary, action: string) => {
    const key = c.id;
    if (action === "remove") {
      if (pending.has(key)) return;
      const go = await ask(
        `删除容器 ${c.name}？\n\n容器内未挂载到卷或主机目录的数据会丢失，不可恢复；挂载卷和主机目录不会被删除。`,
        { kind: "warning" },
      );
      if (!go) return;
    }
    if (action === "stop" || action === "restart") {
      if (pending.has(key)) return;
      const go = await ask(
        action === "stop"
          ? `停止容器 ${c.name}？\n\n容器内正在运行的进程会退出，它对外提供的服务会中断；容器本身和它的数据保留，之后可以重新启动。`
          : `重启容器 ${c.name}？\n\n容器会先停止再启动，期间它对外提供的服务会中断；正在运行的进程会退出，容器数据保留。`,
        { kind: "warning" },
      );
      if (!go) return;
    }
    markPending(key, true);
    const snapshot = qc.getQueryData<ContainerSummary[]>(["docker-ps", sessionId]);
    if (action === "remove") {
      qc.setQueryData<ContainerSummary[]>(["docker-ps", sessionId], (old) =>
        (old ?? []).filter((x) => x.id !== key),
      );
    }
    try {
      await dockerApi.action(sessionId, c.id, action);
      pushToast("success", `已${CONTAINER_ACTION_LABELS[action] ?? action} ${c.name}`);
    } catch (e) {
      if (action === "remove" && snapshot) qc.setQueryData(["docker-ps", sessionId], snapshot);
      pushToast(
        "error",
        `${CONTAINER_ACTION_LABELS[action] ?? action} ${c.name} 失败：${describeError(e)}`,
      );
    } finally {
      markPending(key, false);
      void qc.invalidateQueries({ queryKey: ["docker-ps", sessionId] });
    }
  };

  const removeImage = async (i: ImageSummary) => {
    const key = imageKey(i);
    const ref = imageRef(i);
    if (pending.has(key)) return;
    const consequence = isUntaggedImage(i)
      ? "将删除镜像本体，之后无法再用它创建容器"
      : "将删除此镜像标签；如果这是最后一个标签，镜像本体也会被删除";
    if (!(await ask(`删除镜像 ${ref}？\n\n${consequence}`, { kind: "warning" })))
      return;
    markPending(key, true);
    const snapshot = qc.getQueryData<ImageSummary[]>(["docker-images", sessionId]);
    qc.setQueryData<ImageSummary[]>(["docker-images", sessionId], (old) =>
      (old ?? []).filter((x) => imageKey(x) !== key),
    );
    try {
      await dockerApi.imageRemove(sessionId, ref, false);
      pushToast("success", `已删除 ${ref}`);
    } catch (e) {
      if (snapshot) qc.setQueryData(["docker-images", sessionId], snapshot);
      pushToast("error", `删除 ${ref} 失败：${describeError(e)}`);
    } finally {
      markPending(key, false);
      void qc.invalidateQueries({ queryKey: ["docker-images", sessionId] });
    }
  };

  const removePicked = async () => {
    const keys = [...picked];
    if (!keys.length || bulkBusy) return;
    const isContainer = tab === "containers";
    const label = isContainer ? "容器" : "镜像";
    const removableContainers = isContainer ? cRows.filter((c) => picked.has(c.id) && canRemoveContainer(c)) : [];
    const skippedContainers = isContainer ? keys.length - removableContainers.length : 0;
    if (isContainer && removableContainers.length === 0) {
      pushToast("info", "选中的容器还没有停止，请先停止后再删除");
      return;
    }
    const imageTargets = isContainer ? [] : iRows.filter((i) => picked.has(imageKey(i)));
    const untaggedCount = imageTargets.filter(isUntaggedImage).length;
    let consequence: string;
    if (isContainer) {
      consequence = "容器内未挂载到卷或主机目录的数据会丢失，不可恢复；挂载卷和主机目录不会被删除。";
    } else if (untaggedCount === 0) {
      consequence = "将删除选中的镜像标签；镜像的最后一个标签被删除时，镜像本体也会被删除";
    } else if (untaggedCount === imageTargets.length) {
      consequence = "将删除这些无标签镜像的本体，之后无法再用它们创建容器";
    } else {
      consequence = "无标签镜像的本体将被删除；其余镜像只删除对应标签，但如果删除的是最后一个标签，镜像本体也会被删除";
    }
    const count = isContainer ? removableContainers.length : keys.length;
    const skippedNote = skippedContainers > 0 ? `\n\n另有 ${skippedContainers} 个容器还没有停止，不会被删除；请先停止它们。` : "";
    if (!(await ask(`删除选中的 ${count} 个${label}？\n\n${consequence}${skippedNote}`, { kind: "warning" })))
      return;

    setBulkBusy(true);
    const targets = isContainer ? removableContainers : imageTargets;

    if (isContainer) {
      const removing = new Set(removableContainers.map((c) => c.id));
      qc.setQueryData<ContainerSummary[]>(["docker-ps", sessionId], (old) =>
        (old ?? []).filter((c) => !removing.has(c.id)),
      );
    } else {
      qc.setQueryData<ImageSummary[]>(["docker-images", sessionId], (old) =>
        (old ?? []).filter((i) => !picked.has(imageKey(i))),
      );
    }

    let ok = 0;
    const errs: string[] = [];
    for (const t of targets) {
      const name = isContainer
        ? (t as ContainerSummary).name
        : imageRef(t as ImageSummary);
      markPending(isContainer ? (t as ContainerSummary).id : imageKey(t as ImageSummary), true);
      try {
        if (isContainer) await dockerApi.action(sessionId, (t as ContainerSummary).id, "remove");
        else await dockerApi.imageRemove(sessionId, imageRef(t as ImageSummary), false);
        ok++;
      } catch (e) {
        errs.push(`${name}：${describeError(e)}`);
      } finally {
        markPending(isContainer ? (t as ContainerSummary).id : imageKey(t as ImageSummary), false);
      }
    }

    if (ok) pushToast("success", `已删除 ${ok} 个${label}`);
    if (errs.length) {
      pushToast("error", `${errs.length} 个未能删除 · ${errs[0]}`);
    }

    setPicked(new Set());
    setBulkBusy(false);
    void qc.invalidateQueries({ queryKey: ["docker-ps", sessionId] });
    void qc.invalidateQueries({ queryKey: ["docker-images", sessionId] });
  };

  const openExec = (c: ContainerSummary) => {
    addTab({
      id: `exec-${c.id}-${Date.now()}`,
      kind: "terminal",
      title: c.name,
      sessionId,
      assetId: sessionAssetId ?? undefined,
      containerId: c.id,
      closable: true,
    });
  };

  if (attached && attachedMatchesSession) {
    return (
      <div className="nx-pane bg-term">
        <div className="nx-toolbar">
          <button
            className="nx-btn nx-btn-ghost nx-btn-sm"
            onClick={() => {
              setAttached(null);
              setLogsIssue(null);
              setLogsResumeNote(null);
              pendingReconnect.current = false;
              attachGeneration.current++;
            }}
          >
            <IconArrowLeft size={13} />
            返回
          </button>
          <span className="nx-toolbar-title">{attached.container}</span>
          <span className="nx-badge">日志跟随</span>
          {logsIssue ? (
            <span className={`nx-badge ${logsIssue.ended ? "nx-badge-amber" : "nx-badge-red"}`}>
              {logsIssue.ended ? "跟随结束" : "跟随中断"}
            </span>
          ) : (
            <span className="nx-dot nx-dot-pulse bg-green-400" />
          )}
          <div className="nx-spacer" />
          {!logsIssue && (
            <span className="nx-hint">跟随中 · 关闭此标签或返回即停止</span>
          )}
        </div>
        {logsIssue && (
          <div role="alert" className="flex shrink-0 flex-wrap items-center gap-2 border-b border-amber-800/50 bg-amber-950/30 px-2.5 py-1.5 text-xs text-amber-300">
            <span>{attached.hostName}：{logsIssue.text}</span>
            {!logsIssue.ended && (
              <button
                className="nx-btn nx-btn-ghost nx-btn-sm"
                disabled={sessionStatus !== undefined && sessionStatus !== "connected"}
                onClick={() => void openLogs(attached.containerId, attached.container, true)}
              >
                <IconRefresh size={12} />
                重新跟随
              </button>
            )}
          </div>
        )}
        {logsResumeNote && !logsIssue && (
          <div role="status" className="border-b border-amber-800/50 bg-amber-950/30 px-2.5 py-1.5 text-xs text-amber-300">
            {logsResumeNote}
          </div>
        )}
        <div className="min-h-0 flex-1">
          <DockerLogStream key={attached.tabId} sink={attached.sink} />
        </div>
      </div>
    );
  }

  const insightContainer = insight
    ? (cRows.find((c) => c.id === insight.id) ?? insight)
    : null;
  if (insightContainer) {
    return (
      <ContainerInsight
        key={insightContainer.id}
        sessionId={sessionId}
        container={insightContainer}
        visible={visible}
        onClose={() => setInsight(null)}
      />
    );
  }

  const running = cRows.filter((c) => c.state === "running").length;
  const rowCount = tab === "containers" ? cRows.length : iRows.length;
  const allPicked =
    rowCount > 0 &&
    (tab === "containers"
      ? cRows.every((c) => picked.has(c.id))
      : iRows.every((i) => picked.has(imageKey(i))));

  const containerMenu = (c: ContainerSummary): MenuItem[] => [
    { kind: "item", label: "查看日志", icon: <IconList size={13} />, onSelect: () => void openLogs(c.id, c.name) },
    { kind: "item", label: "进入容器终端", icon: <IconTerminal size={13} />, disabled: c.state !== "running", onSelect: () => openExec(c) },
    { kind: "item", label: "重启", icon: <IconRestart size={13} />, onSelect: () => void act(c, "restart") },
    ...(canRemoveContainer(c)
      ? [{ kind: "item" as const, label: "删除", icon: <IconTrash size={13} />, danger: true, disabled: pending.has(c.id), onSelect: () => void act(c, "remove") }]
      : []),
  ];

  const compactComposeGroups = groupContainersByCompose(cRows, compose.data?.stacks ?? []);

  const compactContainers = (
    <div key="containers-compact">
      <CompactNotice
        pending={containers.isPending}
        error={containers.isError && !containers.data ? containers.error : null}
        errorPrefix="容器列表"
        onRetry={() => void containers.refetch()}
        emptyText="这台主机上还没有容器"
        isEmpty={cRows.length === 0}
      >
        <label className="flex items-center gap-2 border-b border-neutral-800/45 px-2.5 py-1.5 text-[11px] text-neutral-500">
          <input
            className="nx-check pointer-coarse:size-6 pointer-coarse:-m-[5px]"
            type="checkbox"
            aria-label="全选容器"
            checked={allPicked}
            ref={(el) => {
              if (el) el.indeterminate = picked.size > 0 && !allPicked;
            }}
            onChange={(e) => setPicked(e.target.checked ? new Set(cRows.map((c) => c.id)) : new Set())}
          />
          全选
        </label>
        {compactComposeGroups.map((group) => (
          <Fragment key={group.project ?? "ungrouped"}>
            {group.project ? (
              <ComposeStackHeader
                compact
                sessionId={sessionId}
                project={group.project}
                stack={group.stack}
                onChanged={() => {
                  void qc.invalidateQueries({ queryKey: ["docker-ps", sessionId] });
                  void qc.invalidateQueries({ queryKey: ["docker-compose-stacks", sessionId] });
                }}
              />
            ) : (
              <span className="block bg-neutral-950/35 px-2.5 py-1.5 text-[11px] text-neutral-500">未分组容器</span>
            )}
            {group.containers.map((c) => (
              <div
                key={c.id}
                className={`flex items-center gap-2 border-b border-neutral-800/45 px-2.5 py-1.5 ${pending.has(c.id) ? "opacity-50" : ""}`}
              >
                <input
                  className="nx-check shrink-0 pointer-coarse:size-6 pointer-coarse:-m-[5px]"
                  type="checkbox"
                  aria-label={`选择 ${c.name}`}
                  checked={picked.has(c.id)}
                  onChange={(e) => togglePick(c.id, e.target.checked)}
                />
                <div className="min-w-0 flex-1" onClick={() => setInsight(c)}>
                  <div className="flex min-w-0 items-center gap-1.5">
                    <span className="truncate text-[12.5px] font-medium text-neutral-100" title={c.name}>{c.name}</span>
                    <span className={`nx-badge shrink-0 ${c.state === "running" ? "nx-badge-green" : "nx-badge-red"}`}>
                      <span className="nx-dot" />
                      {c.state === "running" ? "运行中" : c.state === "exited" ? "已停止" : c.state}
                    </span>
                  </div>
                  <div className="nx-mono mt-0.5 truncate text-[10.5px] text-neutral-500" title={`${c.image}${c.ports ? ` · ${c.ports}` : ""}`}>
                    {c.image}{c.ports ? ` · ${c.ports}` : ""}
                  </div>
                  <div className={`truncate text-[10.5px] ${c.state === "running" ? "text-neutral-500" : "text-red-300"}`} title={c.status}>{c.status}</div>
                </div>
                <div className="flex shrink-0 items-center gap-0.5">
                  <button className={COMPACT_BTN} title="详情 / 统计 / 文件" onClick={() => setInsight(c)}>
                    <IconMonitor size={13} />
                  </button>
                  {c.state === "running" ? (
                    <button className={COMPACT_BTN} title="停止" onClick={() => void act(c, "stop")}>
                      <IconStop size={12} />
                    </button>
                  ) : (
                    <button className={COMPACT_BTN} title="启动" onClick={() => void act(c, "start")}>
                      <IconPlay size={12} />
                    </button>
                  )}
                  <MoreButton label={`${c.name} 更多操作`} onOpen={(e) => setMenu(menuAt(e, containerMenu(c), c.name))} />
                </div>
              </div>
            ))}
          </Fragment>
        ))}
      </CompactNotice>
    </div>
  );

  const compactImages = (
    <div key="images-compact">
      <CompactNotice
        pending={images.isPending}
        error={images.isError && !images.data ? images.error : null}
        errorPrefix="镜像列表"
        onRetry={() => void images.refetch()}
        emptyText="这台主机上还没有镜像"
        isEmpty={iRows.length === 0}
      >
        <label className="flex items-center gap-2 border-b border-neutral-800/45 px-2.5 py-1.5 text-[11px] text-neutral-500">
          <input
            className="nx-check pointer-coarse:size-6 pointer-coarse:-m-[5px]"
            type="checkbox"
            aria-label="全选镜像"
            checked={allPicked}
            ref={(el) => {
              if (el) el.indeterminate = picked.size > 0 && !allPicked;
            }}
            onChange={(e) => setPicked(e.target.checked ? new Set(iRows.map(imageKey)) : new Set())}
          />
          全选
        </label>
        {iRows.map((i) => {
          const key = imageKey(i);
          return (
            <div key={key} className={`flex items-center gap-2 border-b border-neutral-800/45 px-2.5 py-1.5 ${pending.has(key) ? "opacity-50" : ""}`}>
              <input
                className="nx-check shrink-0 pointer-coarse:size-6 pointer-coarse:-m-[5px]"
                type="checkbox"
                aria-label={`选择 ${i.repository}:${i.tag}`}
                checked={picked.has(key)}
                onChange={(e) => togglePick(key, e.target.checked)}
              />
              <div className="min-w-0 flex-1">
                <div className="nx-mono truncate text-[12px] text-neutral-200" title={`${i.repository}:${i.tag}`}>
                  {i.repository}
                  <span className="text-neutral-500">:{i.tag}</span>
                  {i.repository === "<none>" && <span className="nx-badge ml-1.5">无标签</span>}
                </div>
                <div className="mt-0.5 truncate text-[10.5px] text-neutral-500">{i.size} · {i.createdSince}</div>
              </div>
              <button
                className={`${COMPACT_BTN} is-danger shrink-0`}
                title="删除镜像"
                disabled={pending.has(key)}
                onClick={() => void removeImage(i)}
              >
                <IconTrash size={13} />
              </button>
            </div>
          );
        })}
      </CompactNotice>
    </div>
  );

  return (
    <div ref={paneRef} className="nx-pane">
      <ContextMenu state={menu} onClose={() => setMenu(null)} />
      <div className="nx-toolbar">
        <div className="nx-segment">
          <button
            className={`nx-segment-item whitespace-nowrap ${tab === "containers" ? "is-active" : ""}`}
            onClick={() => changeTab("containers")}
          >
            <IconBox size={12} />
            容器
            <span className="nx-count">{containers.data ? cRows.length : "—"}</span>
          </button>
          <button
            className={`nx-segment-item whitespace-nowrap ${tab === "images" ? "is-active" : ""}`}
            onClick={() => changeTab("images")}
          >
            <IconList size={12} />
            镜像
            <span className="nx-count">{images.data ? iRows.length : "—"}</span>
          </button>
          <button
            className={`nx-segment-item whitespace-nowrap ${tab === "disk" ? "is-active" : ""}`}
            onClick={() => changeTab("disk")}
          >
            <IconTrash size={12} />
            磁盘
          </button>
        </div>
        <span className="nx-hint hidden whitespace-nowrap min-[560px]:inline">
          {containers.data
            ? `${running} 运行中 / 共 ${cRows.length}`
            : containers.isError
              ? isDockerNotInstalled(containers.error) ? "未安装 Docker" : "容器状态加载失败"
              : "容器状态加载中"}
        </span>
        <div className="nx-spacer" />
        {picked.size > 0 && (
          <>
            <span className="nx-hint">已选 {picked.size}</span>
            <button
              className="nx-btn nx-btn-ghost nx-btn-sm"
              onClick={() => setPicked(new Set())}
              disabled={bulkBusy}
            >
              取消选择
            </button>
            <button
              className="nx-btn nx-btn-danger nx-btn-sm"
              onClick={() => void removePicked()}
              disabled={bulkBusy}
            >
              {bulkBusy ? <IconRefresh size={13} className="animate-spin" /> : <IconTrash size={13} />}
              删除选中（{picked.size}）
            </button>
          </>
        )}
        {tab !== "disk" && (
          <>
            <button
              className="nx-btn nx-btn-ghost nx-btn-sm"
              onClick={() => void (tab === "containers" ? containers.refetch() : images.refetch())}
            >
              <IconRefresh size={13} />
              刷新
            </button>
            <label className="nx-hint hidden items-center gap-1.5 whitespace-nowrap min-[560px]:inline-flex">
              自动刷新
              <select
                className="nx-select nx-input-sm"
                aria-label="自动刷新间隔"
                value={refreshMs}
                onChange={(e) => changeRefreshMs(Number(e.target.value))}
              >
                {REFRESH_INTERVAL_OPTIONS.map((ms) => (
                  <option key={ms} value={ms}>
                    {ms === 0 ? "关闭" : `${ms / 1000} 秒`}
                  </option>
                ))}
              </select>
            </label>
          </>
        )}
      </div>

      <div className="min-h-0 flex-1 overflow-auto">
        {tab === "disk" ? (
          <DockerDiskPanel sessionId={sessionId} visible={visible} />
        ) : tab === "containers" && compact ? (
          compactContainers
        ) : tab === "containers" ? (
          <table key="containers" className="nx-table nx-table-fixed">
            <thead>
              <tr>
                <th style={{ width: 36 }}>
                  <input
                    className="nx-check pointer-coarse:size-6 pointer-coarse:-m-[5px]"
                    type="checkbox"
                    aria-label="全选容器"
                    checked={allPicked}
                    ref={(el) => {
                      if (el) el.indeterminate = picked.size > 0 && !allPicked;
                    }}
                    onChange={(e) =>
                      setPicked(e.target.checked ? new Set(cRows.map((c) => c.id)) : new Set())
                    }
                  />
                </th>
                <th className="w-[176px] pointer-coarse:w-[150px]">名称</th>
                <th className="w-[198px] pointer-coarse:w-[150px]">状态</th>
                <th>镜像</th>
                <th className="w-[148px] pointer-coarse:w-[120px]">端口</th>
                <th className="right-0 w-[168px] pointer-coarse:w-[224px] shadow-[inset_1px_0_0_var(--nx-border)]" />
              </tr>
            </thead>
            <tbody>
              <TableQueryBody
                colSpan={6}
                pending={containers.isPending}
                error={containers.isError && !containers.data ? containers.error : null}
                errorPrefix="容器列表"
                onRetry={() => void containers.refetch()}
                emptyText="这台主机上还没有容器"
                isEmpty={cRows.length === 0}
                skeleton={
                  <>
                    {Array.from({ length: 5 }, (_, i) => (
                      <tr key={i} aria-hidden="true">
                        <td>
                          <span className="nx-skeleton nx-skeleton-icon" />
                        </td>
                        <td>
                          <span className="nx-skeleton w-3/5" />
                        </td>
                        <td>
                          <span className="nx-skeleton w-4/5" />
                        </td>
                        <td>
                          <span className="nx-skeleton w-2/3" />
                        </td>
                        <td>
                          <span className="nx-skeleton w-3/4" />
                        </td>
                        <td>
                          <span className="nx-skeleton ml-auto w-24" />
                        </td>
                      </tr>
                    ))}
                  </>
                }
              >
                {groupContainersByCompose(cRows, compose.data?.stacks ?? []).map((group) => (
                  <Fragment key={group.project ?? "ungrouped"}>
                    <tr>
                      <td colSpan={6} className="p-0">
                        {group.project ? (
                          <ComposeStackHeader
                            sessionId={sessionId}
                            project={group.project}
                            stack={group.stack}
                            onChanged={() => {
                              void qc.invalidateQueries({ queryKey: ["docker-ps", sessionId] });
                              void qc.invalidateQueries({ queryKey: ["docker-compose-stacks", sessionId] });
                            }}
                          />
                        ) : (
                          <span className="block bg-neutral-950/35 px-2 py-1.5 text-[11px] text-neutral-500">未分组容器</span>
                        )}
                      </td>
                    </tr>
                    {group.containers.map((c) => (
                <tr key={c.id} className={pending.has(c.id) ? "opacity-50" : undefined}>
                  <td>
                    <input
                      className="nx-check pointer-coarse:size-6 pointer-coarse:-m-[5px]"
                      type="checkbox"
                      aria-label={`选择 ${c.name}`}
                      checked={picked.has(c.id)}
                      onChange={(e) => togglePick(c.id, e.target.checked)}
                    />
                  </td>
                  <td className="truncate font-medium text-neutral-100" title={c.name}>
                    <IconBox size={13} className="mr-1.5 inline align-[-2px] text-neutral-500" />
                    {c.name}
                    {c.composeProject && (
                      <span className="nx-badge nx-badge-purple ml-1.5">{c.composeProject}</span>
                    )}
                  </td>
                  <td>
                    <span className="flex min-w-0 items-center gap-1.5">
                      <span
                        className={`nx-badge shrink-0 ${
                          c.state === "running" ? "nx-badge-green" : "nx-badge-red"
                        }`}
                      >
                        <span className="nx-dot" />
                        {c.state === "running" ? "运行中" : c.state === "exited" ? "已停止" : c.state}
                      </span>
                      <span
                        className={`truncate text-[11px] ${
                          c.state === "running" ? "text-neutral-500" : "text-red-300"
                        }`}
                        title={c.status}
                      >
                        {c.status}
                      </span>
                    </span>
                  </td>
                  <td className="nx-mono truncate" title={c.image}>
                    {c.image}
                  </td>
                  <td className="nx-mono truncate text-[10.5px]" title={c.ports || "—"}>
                    {c.ports || "—"}
                  </td>
                  <td className="nx-right sticky right-0 bg-[var(--nx-bg-pane)] shadow-[inset_1px_0_0_var(--nx-border)]">
                    <span className="inline-flex flex-wrap items-center justify-end gap-0.5">
                      <button className="nx-icon-btn nx-icon-btn-sm pointer-coarse:h-8 pointer-coarse:w-8" title="详情 / 统计 / 文件" onClick={() => setInsight(c)}>
                        <IconMonitor size={13} />
                      </button>
                      <button className="nx-icon-btn nx-icon-btn-sm pointer-coarse:h-8 pointer-coarse:w-8" title="查看日志" onClick={() => void openLogs(c.id, c.name)}>
                        <IconList size={13} />
                      </button>
                      <button
                        className="nx-icon-btn nx-icon-btn-sm pointer-coarse:h-8 pointer-coarse:w-8"
                        title={c.state === "running" ? "进入容器终端" : "容器已停止，启动后可进入终端"}
                        disabled={c.state !== "running"}
                        onClick={() => openExec(c)}
                      >
                        <IconTerminal size={13} />
                      </button>
                      <button
                        className="nx-icon-btn nx-icon-btn-sm pointer-coarse:h-8 pointer-coarse:w-8"
                        title="重启"
                        onClick={() => void act(c, "restart")}
                      >
                        <IconRestart size={13} />
                      </button>
                      {c.state === "running" ? (
                        <button
                          className="nx-icon-btn nx-icon-btn-sm pointer-coarse:h-8 pointer-coarse:w-8"
                          title="停止"
                          onClick={() => void act(c, "stop")}
                        >
                          <IconStop size={12} />
                        </button>
                      ) : (
                        <button
                          className="nx-icon-btn nx-icon-btn-sm pointer-coarse:h-8 pointer-coarse:w-8"
                          title="启动"
                          onClick={() => void act(c, "start")}
                        >
                          <IconPlay size={12} />
                        </button>
                      )}
                      {canRemoveContainer(c) && (
                        <button
                          className="nx-icon-btn nx-icon-btn-sm pointer-coarse:h-8 pointer-coarse:w-8 is-danger"
                          title="删除"
                          disabled={pending.has(c.id)}
                          onClick={() => void act(c, "remove")}
                        >
                          <IconTrash size={13} />
                        </button>
                      )}
                    </span>
                  </td>
                </tr>
                    ))}
                  </Fragment>
                ))}
              </TableQueryBody>
            </tbody>
          </table>
        ) : compact ? (
          compactImages
        ) : (
          <table key="images" className="nx-table nx-table-fixed">
            <thead>
              <tr>
                <th style={{ width: 36 }}>
                  <input
                    className="nx-check pointer-coarse:size-6 pointer-coarse:-m-[5px]"
                    type="checkbox"
                    aria-label="全选镜像"
                    checked={allPicked}
                    ref={(el) => {
                      if (el) el.indeterminate = picked.size > 0 && !allPicked;
                    }}
                    onChange={(e) =>
                      setPicked(e.target.checked ? new Set(iRows.map(imageKey)) : new Set())
                    }
                  />
                </th>
                <th>仓库 / 标签</th>
                <th style={{ width: 110 }}>大小</th>
                <th style={{ width: 160 }}>创建时间</th>
                <th style={{ width: 80 }} className="right-0 shadow-[inset_1px_0_0_var(--nx-border)]" />
              </tr>
            </thead>
            <tbody>
              <TableQueryBody
                colSpan={5}
                pending={images.isPending}
                error={images.isError && !images.data ? images.error : null}
                errorPrefix="镜像列表"
                onRetry={() => void images.refetch()}
                emptyText="这台主机上还没有镜像"
                isEmpty={iRows.length === 0}
                skeleton={
                  <>
                    {Array.from({ length: 5 }, (_, i) => (
                      <tr key={i} aria-hidden="true">
                        <td>
                          <span className="nx-skeleton nx-skeleton-icon" />
                        </td>
                        <td>
                          <span className="nx-skeleton w-2/3" />
                        </td>
                        <td>
                          <span className="nx-skeleton w-3/4" />
                        </td>
                        <td>
                          <span className="nx-skeleton w-4/5" />
                        </td>
                        <td>
                          <span className="nx-skeleton ml-auto w-10" />
                        </td>
                      </tr>
                    ))}
                  </>
                }
              >
                {iRows.map((i) => {
                const key = imageKey(i);
                return (
                  <tr key={key} className={pending.has(key) ? "opacity-50" : undefined}>
                    <td>
                      <input
                        className="nx-check pointer-coarse:size-6 pointer-coarse:-m-[5px]"
                        type="checkbox"
                        aria-label={`选择 ${i.repository}:${i.tag}`}
                        checked={picked.has(key)}
                        onChange={(e) => togglePick(key, e.target.checked)}
                      />
                    </td>
                    <td className="nx-mono truncate text-neutral-200" title={`${i.repository}:${i.tag}`}>
                      {i.repository}
                      <span className="text-neutral-500">:{i.tag}</span>
                      {i.repository === "<none>" && <span className="nx-badge ml-1.5">无标签</span>}
                    </td>
                    <td>{i.size}</td>
                    <td className="text-neutral-500">{i.createdSince}</td>
                    <td className="nx-right sticky right-0 bg-[var(--nx-bg-pane)] shadow-[inset_1px_0_0_var(--nx-border)]">
                      <button
                        className="nx-icon-btn nx-icon-btn-sm pointer-coarse:h-8 pointer-coarse:w-8 is-danger"
                        title="删除镜像"
                        disabled={pending.has(key)}
                        onClick={() => void removeImage(i)}
                      >
                        <IconTrash size={13} />
                      </button>
                    </td>
                  </tr>
                );
                })}
              </TableQueryBody>
            </tbody>
          </table>
        )}
      </div>

      {tab !== "disk" && <PullBar sessionId={sessionId} compact={compact} />}
    </div>
  );
}

function PullBar({ sessionId, compact }: { sessionId: string; compact: boolean }) {
  const qc = useQueryClient();
  const pushToast = useUi((s) => s.pushToast);
  const [image, setImage] = useState("");
  const [pulling, setPulling] = useState(false);
  const [open, setOpen] = useState(false);

  const pull = async () => {
    const name = image.trim();
    if (!name) return;
    setPulling(true);
    pushToast("info", `正在拉取 ${name}（最长 10 分钟）…`);
    try {
      await dockerApi.imagePull(sessionId, name);
      pushToast("success", `${name} 拉取完成`);
      setImage("");
      void qc.invalidateQueries({ queryKey: ["docker-images", sessionId] });
    } catch (e) {
      pushToast("error", `拉取失败：${describeError(e)}`);
    } finally {
      setPulling(false);
    }
  };

  if (compact && !open && !pulling) {
    return (
      <div className="flex shrink-0 items-center border-t border-neutral-800/60 bg-neutral-950/40 px-2.5 py-1">
        <button className="nx-btn nx-btn-ghost nx-btn-sm" aria-expanded={false} onClick={() => setOpen(true)}>
          <IconDownload size={13} />
          + 拉取镜像
        </button>
      </div>
    );
  }

  return (
    <div className="flex shrink-0 items-center gap-2 border-t border-neutral-800/60 bg-neutral-950/40 px-2.5 py-2">
      <div className="nx-field">
        <span className="nx-field-icon">
          <IconDownload size={13} />
        </span>
        <input
          className="nx-input nx-input-sm font-mono"
          placeholder="拉取镜像，如 nginx:alpine"
          aria-label="拉取镜像名称"
          value={image}
          onChange={(e) => setImage(e.target.value)}
          onKeyDown={(e) => {
            if (isImeKeyEvent(e)) return;
            if (e.key === "Enter") void pull();
          }}
        />
      </div>
      <button className="nx-btn nx-btn-primary nx-btn-sm" disabled={!image.trim() || pulling} onClick={() => void pull()}>
        {pulling ? <IconRefresh size={13} className="animate-spin" /> : <IconDownload size={13} />}
        {pulling ? "拉取中…" : "拉取"}
      </button>
      {compact && !pulling && (
        <button className="nx-btn nx-btn-ghost nx-btn-sm" onClick={() => setOpen(false)}>收起</button>
      )}
    </div>
  );
}
