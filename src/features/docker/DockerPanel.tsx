import { useEffect, useRef, useState, type ReactNode } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ask } from "../../ui/dialogs";
import { isImeKeyEvent } from "../../ui/DialogHost";
import { dockerApi, terminalApi, type ContainerSummary, type ImageSummary } from "../../ipc/commands";
import { useUi } from "../../app/store";
import { createBinaryChannel, disposeChannel, onChannelReopen } from "../../ipc/events";
import { describeError } from "../../ui/errorText";
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
import { ContainerInsight } from "./ContainerInsight";

interface LogAttach {
  container: string;
  containerId: string;
  tabId: string;
  channel: ReturnType<typeof createBinaryChannel>;
  sink: { onBytes: ((bytes: Uint8Array) => void) | null };
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
  if (error) {
    return (
      <tr>
        <td colSpan={colSpan} className="nx-table-empty">
          <span className="text-red-300">
            {errorPrefix}加载失败 · {describeError(error)}
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

function imageRef(i: ImageSummary): string {
  return i.repository && i.repository !== "<none>" ? `${i.repository}:${i.tag}` : i.id;
}

export function DockerPanel({ sessionId, visible = true }: { sessionId: string; visible?: boolean }) {
  const qc = useQueryClient();
  const { addTab, pushToast } = useUi();
  const [tab, setTab] = useState<"containers" | "images">("containers");
  const [attached, setAttached] = useState<LogAttach | null>(null);
  const [insight, setInsight] = useState<ContainerSummary | null>(null);
  const [picked, setPicked] = useState<Set<string>>(() => new Set());
  const [pending, setPending] = useState<Set<string>>(() => new Set());
  const [bulkBusy, setBulkBusy] = useState(false);
  const attachInFlight = useRef(false);

  const containers = useQuery({
    queryKey: ["docker-ps", sessionId],
    queryFn: () => dockerApi.ps(sessionId),
    refetchInterval: visible ? 5000 : false,
  });
  const images = useQuery({
    queryKey: ["docker-images", sessionId],
    queryFn: () => dockerApi.images(sessionId),
    refetchInterval: visible ? 30000 : false,
  });

  const cRows = containers.data ?? [];
  const iRows = images.data ?? [];

  useEffect(() => {
    setPicked(new Set());
  }, [tab]);

  useEffect(() => {
    if (!attached) return;
    const { tabId, channel } = attached;
    return () => {
      void terminalApi.closeTab(tabId, "kill").catch(() => undefined);
      disposeChannel(channel);
    };
  }, [attached]);

  useEffect(() => {
    if (!attached) return;
    const { channel, containerId, container } = attached;
    let cancelled = false;
    const off = onChannelReopen(channel, () => {
      if (!cancelled) void openLogs(containerId, container);
    });
    return () => {
      cancelled = true;
      off();
    };
  }, [attached]);

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
      if (!(await ask(`删除容器 ${c.name}？\n\n该操作不可恢复。`, { kind: "warning" }))) return;
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
    if (!(await ask(`删除镜像 ${ref}？\n\n该操作不可恢复。`, { kind: "warning" }))) return;
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
    if (!(await ask(`删除选中的 ${keys.length} 个${label}？\n\n该操作不可恢复。`, { kind: "warning" })))
      return;

    setBulkBusy(true);
    const targets = isContainer
      ? cRows.filter((c) => picked.has(c.id))
      : iRows.filter((i) => picked.has(imageKey(i)));

    if (isContainer) {
      qc.setQueryData<ContainerSummary[]>(["docker-ps", sessionId], (old) =>
        (old ?? []).filter((c) => !picked.has(c.id)),
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
        errs.push(`${name}: ${describeError(e)}`);
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

  const openLogs = async (containerId: string, containerName: string) => {
    if (attachInFlight.current) return;
    attachInFlight.current = true;
    const sink: LogAttach["sink"] = { onBytes: null };
    const channel = createBinaryChannel((bytes) => sink.onBytes?.(bytes));
    try {
      const kernelTab = await dockerApi.logsAttach(sessionId, containerId, 500, channel);
      setAttached({ container: containerName, containerId, tabId: kernelTab, channel, sink });
    } catch (e) {
      disposeChannel(channel);
      pushToast("error", `读取日志失败：${describeError(e)}`);
    } finally {
      attachInFlight.current = false;
    }
  };

  const openExec = (c: ContainerSummary) => {
    addTab({
      id: `exec-${c.id}-${Date.now()}`,
      kind: "terminal",
      title: c.name,
      sessionId,
      containerId: c.id,
      closable: true,
    });
  };

  if (attached) {
    return (
      <div className="nx-pane bg-term">
        <div className="nx-toolbar">
          <button
            className="nx-btn nx-btn-ghost nx-btn-sm"
            onClick={() => setAttached(null)}
          >
            <IconArrowLeft size={13} />
            返回
          </button>
          <span className="nx-toolbar-title">{attached.container}</span>
          <span className="nx-badge">日志跟随</span>
          <span className="nx-dot nx-dot-pulse bg-green-400" />
          <div className="nx-spacer" />
          <span className="nx-hint">跟随中 · 关闭此标签或返回即停止</span>
        </div>
        <div className="min-h-0 flex-1">
          <LogStream key={attached.tabId} sink={attached.sink} />
        </div>
      </div>
    );
  }

  if (insight) {
    return (
      <ContainerInsight
        key={insight.id}
        sessionId={sessionId}
        container={insight}
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

  return (
    <div className="nx-pane">
      <div className="nx-toolbar">
        <div className="nx-segment">
          <button
            className={`nx-segment-item ${tab === "containers" ? "is-active" : ""}`}
            onClick={() => setTab("containers")}
          >
            <IconBox size={12} />
            容器
            <span className="nx-count">{containers.data ? cRows.length : "—"}</span>
          </button>
          <button
            className={`nx-segment-item ${tab === "images" ? "is-active" : ""}`}
            onClick={() => setTab("images")}
          >
            <IconList size={12} />
            镜像
            <span className="nx-count">{images.data ? iRows.length : "—"}</span>
          </button>
        </div>
        <span className="nx-hint hidden min-[560px]:inline">
          {containers.data ? `${running} 运行中 / 共 ${cRows.length}` : "容器状态加载中"}
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
              删除选中 ({picked.size})
            </button>
          </>
        )}
        <button
          className="nx-btn nx-btn-ghost nx-btn-sm"
          onClick={() => {
            void qc.invalidateQueries({ queryKey: ["docker-ps", sessionId] });
            void qc.invalidateQueries({ queryKey: ["docker-images", sessionId] });
          }}
        >
          <IconRefresh size={13} />
          刷新
        </button>
        <span className="nx-hint hidden min-[560px]:inline">容器 5s · 镜像 30s 自动刷新</span>
      </div>

      {tab === "containers" && <OverviewStrip sessionId={sessionId} visible={visible} />}

      <div className="min-h-0 flex-1 overflow-auto">
        {tab === "containers" ? (
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
                <th style={{ width: 176 }}>名称</th>
                <th style={{ width: 198 }}>状态</th>
                <th>镜像</th>
                <th style={{ width: 148 }}>端口</th>
                <th style={{ width: 168 }} className="right-0 shadow-[inset_1px_0_0_var(--nx-border)]" />
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
                {cRows.map((c) => (
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
                      <button className="nx-icon-btn nx-icon-btn-sm pointer-coarse:h-7 pointer-coarse:w-7" title="详情 / 统计 / 文件" onClick={() => setInsight(c)}>
                        <IconMonitor size={13} />
                      </button>
                      <button className="nx-icon-btn nx-icon-btn-sm pointer-coarse:h-7 pointer-coarse:w-7" title="查看日志" onClick={() => void openLogs(c.id, c.name)}>
                        <IconList size={13} />
                      </button>
                      <button className="nx-icon-btn nx-icon-btn-sm pointer-coarse:h-7 pointer-coarse:w-7" title="进入容器终端" onClick={() => openExec(c)}>
                        <IconTerminal size={13} />
                      </button>
                      <button
                        className="nx-icon-btn nx-icon-btn-sm pointer-coarse:h-7 pointer-coarse:w-7"
                        title="重启"
                        onClick={() => void act(c, "restart")}
                      >
                        <IconRestart size={13} />
                      </button>
                      {c.state === "running" ? (
                        <button
                          className="nx-icon-btn nx-icon-btn-sm pointer-coarse:h-7 pointer-coarse:w-7"
                          title="停止"
                          onClick={() => void act(c, "stop")}
                        >
                          <IconStop size={12} />
                        </button>
                      ) : (
                        <button
                          className="nx-icon-btn nx-icon-btn-sm pointer-coarse:h-7 pointer-coarse:w-7"
                          title="启动"
                          onClick={() => void act(c, "start")}
                        >
                          <IconPlay size={12} />
                        </button>
                      )}
                      <button
                        className="nx-icon-btn nx-icon-btn-sm pointer-coarse:h-7 pointer-coarse:w-7 is-danger"
                        title="删除"
                        disabled={pending.has(c.id)}
                        onClick={() => void act(c, "remove")}
                      >
                        <IconTrash size={13} />
                      </button>
                    </span>
                  </td>
                </tr>
                ))}
              </TableQueryBody>
            </tbody>
          </table>
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
                      {i.repository === "<none>" && <span className="nx-badge ml-1.5">悬空</span>}
                    </td>
                    <td>{i.size}</td>
                    <td className="text-neutral-500">{i.createdSince}</td>
                    <td className="nx-right sticky right-0 bg-[var(--nx-bg-pane)] shadow-[inset_1px_0_0_var(--nx-border)]">
                      <button
                        className="nx-icon-btn nx-icon-btn-sm pointer-coarse:h-7 pointer-coarse:w-7 is-danger"
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

      <PullBar sessionId={sessionId} />
    </div>
  );
}

function LogStream({ sink }: { sink: LogAttach["sink"] }) {
  const [lines, setLines] = useState<string[]>([]);
  const scroller = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const decoder = new TextDecoder();
    sink.onBytes = (bytes) => {
      const text = decoder.decode(bytes);
      setLines((prev) => [...prev.slice(-4000), ...text.split("\n")]);
    };
    return () => {
      sink.onBytes = null;
    };
  }, [sink]);

  useEffect(() => {
    const el = scroller.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [lines]);

  return (
    <div
      ref={scroller}
      className="h-full overflow-auto bg-term px-3 py-2 font-mono text-[11.5px] leading-relaxed text-neutral-300"
    >
      {lines.length === 0 ? (
        <span className="text-neutral-600">等待日志…</span>
      ) : (
        lines.map((l, i) => (
          <div key={i} className="whitespace-pre-wrap">
            {highlightLog(l)}
          </div>
        ))
      )}
    </div>
  );
}

function highlightLog(line: string) {
  if (/\b(ERROR|FATAL|panic)\b/i.test(line)) return <span className="text-red-300">{line}</span>;
  if (/\b(WARN|Warning)\b/i.test(line)) return <span className="text-amber-300">{line}</span>;
  if (/^\s*#/.test(line)) return <span className="text-neutral-600">{line}</span>;
  return line;
}

function PullBar({ sessionId }: { sessionId: string }) {
  const qc = useQueryClient();
  const { pushToast } = useUi();
  const [image, setImage] = useState("");
  const [pulling, setPulling] = useState(false);

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
    </div>
  );
}

const HOST_STAT_LABELS: Record<string, string> = {
  containersRunning: "运行中",
  containersTotal: "容器总数",
  images: "镜像",
  cpuPercent: "CPU",
  memUsedMb: "内存占用 (MB)",
  memTotalMb: "内存总量 (MB)",
  diskPercent: "磁盘",
};

function OverviewStrip({ sessionId, visible }: { sessionId: string; visible: boolean }) {
  const overview = useQuery({
    queryKey: ["docker-overview", sessionId],
    queryFn: () => dockerApi.overview(sessionId),
    refetchInterval: visible ? 30000 : false,
  });

  if (overview.isPending) {
    return (
      <div
        className="flex shrink-0 items-center gap-1.5 overflow-x-auto border-b border-neutral-800/60 px-3 py-1.5"
        role="status"
        aria-label="主机概览加载中"
      >
        <span className="nx-skeleton nx-skeleton-chip w-16" aria-hidden="true" />
        {Array.from({ length: 5 }, (_, i) => (
          <span key={i} className="nx-skeleton nx-skeleton-chip w-24" aria-hidden="true" />
        ))}
        <span className="nx-sr-only">主机概览加载中…</span>
      </div>
    );
  }
  if (overview.isError) {
    return (
      <div className="flex shrink-0 items-center gap-2 border-b border-neutral-800/60 px-3 py-1.5">
        <span className="nx-hint">主机概览加载失败 · {describeError(overview.error)}</span>
        <button className="nx-btn nx-btn-ghost nx-btn-sm" onClick={() => void overview.refetch()}>
          <IconRefresh size={12} />
          重试
        </button>
      </div>
    );
  }

  const hostStats = overview.data.hostStats ?? {};
  const entries = Object.entries(hostStats);
  return (
    <div className="flex shrink-0 items-center gap-1.5 overflow-x-auto border-b border-neutral-800/60 px-3 py-1.5">
      <span className="shrink-0 text-[11px] text-neutral-500">主机概览</span>
      {entries.length === 0 && <span className="nx-hint">内核未返回主机统计</span>}
      {entries.map(([key, value]) => (
        <span key={key} className="nx-chip shrink-0" title={key}>
          {HOST_STAT_LABELS[key] ?? key}
          <span className="nx-mono text-neutral-200">
            {typeof value === "number" && !Number.isInteger(value) ? value.toFixed(1) : String(value)}
          </span>
        </span>
      ))}
    </div>
  );
}
