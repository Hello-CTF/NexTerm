// Docker 面板（M3）：容器列表 + 镜像管理 + 日志跟随 + 容器终端。
import { useEffect, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ask } from "../../ui/dialogs";
import { dockerApi, terminalApi, type ContainerSummary, type ImageSummary } from "../../ipc/commands";
import { useUi } from "../../app/store";
import { createBinaryChannel } from "../../ipc/events";
import {
  IconArrowLeft,
  IconBox,
  IconDownload,
  IconList,
  IconPlay,
  IconRefresh,
  IconRestart,
  IconStop,
  IconTerminal,
  IconTrash,
} from "../../ui/icons";

export function DockerPanel({ sessionId, visible = true }: { sessionId: string; visible?: boolean }) {
  const qc = useQueryClient();
  const { addTab, pushToast } = useUi();
  const [tab, setTab] = useState<"containers" | "images">("containers");
  const [attached, setAttached] = useState<{ container: string; tabId: string } | null>(null);

  // 标签不可见时停掉轮询（切标签不再卸载面板，所以要显式 gate）
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

  const act = async (c: ContainerSummary, action: string) => {
    if (action === "remove" && !(await ask(`删除容器 ${c.name}？\n\n该操作不可恢复。`))) return;
    try {
      await dockerApi.action(sessionId, c.id, action);
      void qc.invalidateQueries({ queryKey: ["docker-ps", sessionId] });
      pushToast("success", `${c.name} · ${action} 已执行`);
    } catch (e) {
      pushToast("error", `${action} 失败: ${String(e)}`);
    }
  };

  const openLogs = async (c: ContainerSummary) => {
    const channel = createBinaryChannel(() => undefined);
    try {
      const kernelTab = await dockerApi.logsAttach(sessionId, c.id, 500, channel);
      setAttached({ container: c.name, tabId: kernelTab });
    } catch (e) {
      pushToast("error", `日志 attach 失败: ${String(e)}`);
    }
  };

  /** 在容器里开一个真 PTY 终端标签（走 docker exec）。 */
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
            onClick={() => {
              void terminalApi.detach(attached.tabId).catch(() => undefined);
              setAttached(null);
            }}
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
          <LogStream sessionId={sessionId} container={attached.container} />
        </div>
      </div>
    );
  }

  const running = (containers.data ?? []).filter((c) => c.state === "running").length;

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
            <span className="nx-count">{containers.data?.length ?? 0}</span>
          </button>
          <button
            className={`nx-segment-item ${tab === "images" ? "is-active" : ""}`}
            onClick={() => setTab("images")}
          >
            <IconList size={12} />
            镜像
            <span className="nx-count">{images.data?.length ?? 0}</span>
          </button>
        </div>
        <span className="nx-hint">
          {running} 运行中 / 共 {containers.data?.length ?? 0}
        </span>
        <div className="nx-spacer" />
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
        <span className="nx-hint">5s 自动刷新</span>
      </div>

      <div className="min-h-0 flex-1 overflow-auto">
        {tab === "containers" ? (
          <table className="nx-table nx-table-fixed">
            <thead>
              <tr>
                <th style={{ width: 176 }}>名称</th>
                <th style={{ width: 198 }}>状态</th>
                <th>镜像</th>
                <th style={{ width: 148 }}>端口</th>
                <th style={{ width: 130 }} />
              </tr>
            </thead>
            <tbody>
              {(containers.data ?? []).map((c) => (
                <tr key={c.id}>
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
                        {c.state === "running" ? "running" : "exited"}
                      </span>
                      <span
                        className={`truncate text-[11px] ${
                          c.state === "running" ? "text-neutral-500" : "text-red-300"
                        }`}
                        title={c.status}
                      >
                        {c.status.replace(/^Exited \((\d+)\).*$/, "exit $1 · 已停止")}
                      </span>
                    </span>
                  </td>
                  <td className="nx-mono truncate" title={c.image}>
                    {c.image}
                  </td>
                  <td className="nx-mono truncate text-[10.5px]" title={c.ports || "—"}>
                    {c.ports || "—"}
                  </td>
                  <td className="nx-right">
                    <span className="inline-flex items-center gap-0.5">
                      <button className="nx-icon-btn nx-icon-btn-sm" title="查看日志" onClick={() => void openLogs(c)}>
                        <IconList size={13} />
                      </button>
                      <button className="nx-icon-btn nx-icon-btn-sm" title="进入容器终端" onClick={() => openExec(c)}>
                        <IconTerminal size={13} />
                      </button>
                      <button
                        className="nx-icon-btn nx-icon-btn-sm"
                        title="重启"
                        onClick={() => void act(c, "restart")}
                      >
                        <IconRestart size={13} />
                      </button>
                      {c.state === "running" ? (
                        <button
                          className="nx-icon-btn nx-icon-btn-sm"
                          title="停止"
                          onClick={() => void act(c, "stop")}
                        >
                          <IconStop size={12} />
                        </button>
                      ) : (
                        <button
                          className="nx-icon-btn nx-icon-btn-sm"
                          title="启动"
                          onClick={() => void act(c, "start")}
                        >
                          <IconPlay size={12} />
                        </button>
                      )}
                      <button
                        className="nx-icon-btn nx-icon-btn-sm is-danger"
                        title="删除"
                        onClick={() => void act(c, "remove")}
                      >
                        <IconTrash size={13} />
                      </button>
                    </span>
                  </td>
                </tr>
              ))}
              {(containers.data ?? []).length === 0 && (
                <tr>
                  <td colSpan={5} className="nx-table-empty">
                    这台主机上还没有容器
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        ) : (
          <table className="nx-table nx-table-fixed">
            <thead>
              <tr>
                <th>仓库 / 标签</th>
                <th style={{ width: 110 }}>大小</th>
                <th style={{ width: 160 }}>创建时间</th>
                <th style={{ width: 80 }} />
              </tr>
            </thead>
            <tbody>
              {(images.data ?? []).map((i: ImageSummary) => (
                <tr key={i.id}>
                  <td className="nx-mono truncate text-neutral-200" title={`${i.repository}:${i.tag}`}>
                    {i.repository}
                    <span className="text-neutral-500">:{i.tag}</span>
                  </td>
                  <td>{i.size}</td>
                  <td className="text-neutral-500">{i.createdSince}</td>
                  <td className="nx-right">
                    <button
                      className="nx-icon-btn nx-icon-btn-sm is-danger"
                      title="删除镜像"
                      onClick={async () => {
                        if (!(await ask(`删除镜像 ${i.repository}:${i.tag}？`))) return;
                        await dockerApi.imageRemove(sessionId, i.id, false);
                        void qc.invalidateQueries({ queryKey: ["docker-images", sessionId] });
                        pushToast("success", `已删除 ${i.repository}:${i.tag}`);
                      }}
                    >
                      <IconTrash size={13} />
                    </button>
                  </td>
                </tr>
              ))}
              {(images.data ?? []).length === 0 && (
                <tr>
                  <td colSpan={4} className="nx-table-empty">
                    本机没有镜像
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        )}
      </div>

      <PullBar sessionId={sessionId} />
    </div>
  );
}

/** 日志跟随：复用内核的日志通道，逐行追加。 */
function LogStream({ sessionId, container }: { sessionId: string; container: string }) {
  const [lines, setLines] = useState<string[]>([]);
  const attachedRef = useRef(false);
  const scroller = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (attachedRef.current) return;
    attachedRef.current = true;
    void (async () => {
      const channel = createBinaryChannel((bytes) => {
        const text = new TextDecoder().decode(bytes);
        setLines((prev) => [...prev.slice(-4000), ...text.split("\n")]);
      });
      try {
        await dockerApi.logsAttach(sessionId, container, 500, channel);
      } catch {
        setLines((prev) => [...prev, `[日志通道建立失败]`]);
      }
    })();
  }, [sessionId, container]);

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

/** 日志分级着色：ERROR / WARN 才上色，其余保持安静。 */
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
    pushToast("info", `拉取 ${name} 中（最长 10 分钟）…`);
    try {
      await dockerApi.imagePull(sessionId, name);
      pushToast("success", `${name} 拉取完成`);
      setImage("");
      void qc.invalidateQueries({ queryKey: ["docker-images", sessionId] });
    } catch (e) {
      pushToast("error", `拉取失败: ${String(e)}`);
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
          value={image}
          onChange={(e) => setImage(e.target.value)}
          onKeyDown={(e) => e.key === "Enter" && void pull()}
        />
      </div>
      <button className="nx-btn nx-btn-primary nx-btn-sm" disabled={!image.trim() || pulling} onClick={() => void pull()}>
        {pulling ? <IconRefresh size={13} className="animate-spin" /> : <IconDownload size={13} />}
        {pulling ? "拉取中…" : "拉取"}
      </button>
    </div>
  );
}
