// Docker 面板（M3）：容器列表 + 镜像管理 + 日志跟随 + 容器终端。
import { useEffect, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ask } from "../../ui/dialogs";
import { dockerApi, terminalApi, type ContainerSummary, type ImageSummary } from "../../ipc/commands";
import { useUi } from "../../app/store";
import { createBinaryChannel } from "../../ipc/events";
import { describeError } from "../../ui/errorText";
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

/** 同一个 image ID 会挂多个 repository（本地 tag + 镜像站 tag）。
 *  只用 id 当 React key 必然重复 —— 重复 key 的协调行为是未定义的，
 *  表现就是列表残影 / 删了一条少两条。所以行 key 一律带上 `repository:tag`。 */
function imageKey(i: ImageSummary): string {
  return `${i.id}|${i.repository}:${i.tag}`;
}

/** 删镜像时给 docker 的引用：优先 `repository:tag`。
 *
 *  这不是风格问题：同一镜像被多个 repository 引用时，`docker rmi <image-id>`
 *  会被 daemon 直接拒掉（`must be forced - image is referenced in multiple
 *  repositories`）。而这恰好是最常见的多 tag 场景 —— 以前传 id，删除必失败，
 *  偏偏又没有 catch，于是界面一片安静，用户只能反复点。 */
function imageRef(i: ImageSummary): string {
  return i.repository && i.repository !== "<none>" ? `${i.repository}:${i.tag}` : i.id;
}

export function DockerPanel({ sessionId, visible = true }: { sessionId: string; visible?: boolean }) {
  const qc = useQueryClient();
  const { addTab, pushToast } = useUi();
  const [tab, setTab] = useState<"containers" | "images">("containers");
  const [attached, setAttached] = useState<{ container: string; tabId: string } | null>(null);
  /** 勾选集合：存行 key（容器 = id，镜像 = imageKey）。 */
  const [picked, setPicked] = useState<Set<string>>(() => new Set());
  /** 正在删除的行 key：挡住「点了没反应就再点一下」的并发删除。 */
  const [pending, setPending] = useState<Set<string>>(() => new Set());
  const [bulkBusy, setBulkBusy] = useState(false);

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

  const cRows = containers.data ?? [];
  const iRows = images.data ?? [];

  // 两个列表的行 key 语义不同，切 tab 时留着旧的会误删别的东西
  useEffect(() => {
    setPicked(new Set());
  }, [tab]);

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

  /** 容器动作（含删除）：乐观 + 行级 pending。 */
  const act = async (c: ContainerSummary, action: string) => {
    const key = c.id;
    if (action === "remove") {
      if (pending.has(key)) return;
      if (!(await ask(`删除容器 ${c.name}？\n\n该操作不可恢复。`))) return;
    }
    markPending(key, true);
    const snapshot = qc.getQueryData<ContainerSummary[]>(["docker-ps", sessionId]);
    if (action === "remove") {
      // 乐观：先拿掉，别等 docker rm + refetch 一来一回
      qc.setQueryData<ContainerSummary[]>(["docker-ps", sessionId], (old) =>
        (old ?? []).filter((x) => x.id !== key),
      );
    }
    try {
      await dockerApi.action(sessionId, c.id, action);
      pushToast("success", `${c.name} · ${action} 已执行`);
    } catch (e) {
      // 回滚乐观移除，否则界面显示的是一份不存在的状态
      if (action === "remove" && snapshot) qc.setQueryData(["docker-ps", sessionId], snapshot);
      pushToast("error", `${action} 失败: ${describeError(e)}`);
    } finally {
      markPending(key, false);
      void qc.invalidateQueries({ queryKey: ["docker-ps", sessionId] });
    }
  };

  /** 删镜像：引用用 `repository:tag`（见 imageRef），乐观 + 回滚 + 行级 pending。 */
  const removeImage = async (i: ImageSummary) => {
    const key = imageKey(i);
    const ref = imageRef(i);
    if (pending.has(key)) return;
    if (!(await ask(`删除镜像 ${ref}？\n\n该操作不可恢复。`))) return;
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
      pushToast("error", `删除 ${ref} 失败: ${describeError(e)}`);
    } finally {
      markPending(key, false);
      void qc.invalidateQueries({ queryKey: ["docker-images", sessionId] });
    }
  };

  /** 批量删除：串行（docker daemon 对 rmi/rm 是串行的，并发只会互相打架），
   *  结束时报成功 / 失败条数，失败原因逐条带上。 */
  const removePicked = async () => {
    const keys = [...picked];
    if (!keys.length || bulkBusy) return;
    const isContainer = tab === "containers";
    const label = isContainer ? "容器" : "镜像";
    if (!(await ask(`删除选中的 ${keys.length} 个${label}？\n\n该操作不可恢复。`))) return;

    setBulkBusy(true);
    const targets = isContainer
      ? cRows.filter((c) => picked.has(c.id))
      : iRows.filter((i) => picked.has(imageKey(i)));

    // 乐观：整批先从列表拿掉
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
      // 失败要吵出来 —— 静默失败正是上一版让人反复点的原因
      pushToast("error", `${errs.length} 个未能删除 · ${errs[0]}`);
    }

    setPicked(new Set());
    setBulkBusy(false);
    void qc.invalidateQueries({ queryKey: ["docker-ps", sessionId] });
    void qc.invalidateQueries({ queryKey: ["docker-images", sessionId] });
  };

  const openLogs = async (c: ContainerSummary) => {
    const channel = createBinaryChannel(() => undefined);
    try {
      const kernelTab = await dockerApi.logsAttach(sessionId, c.id, 500, channel);
      setAttached({ container: c.name, tabId: kernelTab });
    } catch (e) {
      pushToast("error", `日志 attach 失败: ${describeError(e)}`);
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
            <span className="nx-count">{cRows.length}</span>
          </button>
          <button
            className={`nx-segment-item ${tab === "images" ? "is-active" : ""}`}
            onClick={() => setTab("images")}
          >
            <IconList size={12} />
            镜像
            <span className="nx-count">{iRows.length}</span>
          </button>
        </div>
        <span className="nx-hint">
          {running} 运行中 / 共 {cRows.length}
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
        <span className="nx-hint">5s 自动刷新</span>
      </div>

      <div className="min-h-0 flex-1 overflow-auto">
        {/* 两个分支的 <table> 同类型同位置，React 会复用同一个 DOM 节点。
            给它们各自的 key 强制换 tab 时重建 —— 否则上一个列表的行有概率
            残留在下一个列表里（正是「容器页里混着镜像行」那个现象）。 */}
        {tab === "containers" ? (
          <table key="containers" className="nx-table nx-table-fixed">
            <thead>
              <tr>
                <th style={{ width: 36 }}>
                  <input
                    className="nx-check"
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
                <th style={{ width: 130 }} />
              </tr>
            </thead>
            <tbody>
              {cRows.map((c) => (
                <tr key={c.id} className={pending.has(c.id) ? "opacity-50" : undefined}>
                  <td>
                    <input
                      className="nx-check"
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
                        disabled={pending.has(c.id)}
                        onClick={() => void act(c, "remove")}
                      >
                        <IconTrash size={13} />
                      </button>
                    </span>
                  </td>
                </tr>
              ))}
              {cRows.length === 0 && (
                <tr>
                  <td colSpan={6} className="nx-table-empty">
                    这台主机上还没有容器
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        ) : (
          <table key="images" className="nx-table nx-table-fixed">
            <thead>
              <tr>
                <th style={{ width: 36 }}>
                  <input
                    className="nx-check"
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
                <th style={{ width: 80 }} />
              </tr>
            </thead>
            <tbody>
              {iRows.map((i) => {
                const key = imageKey(i);
                return (
                  <tr key={key} className={pending.has(key) ? "opacity-50" : undefined}>
                    <td>
                      <input
                        className="nx-check"
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
                    <td className="nx-right">
                      <button
                        className="nx-icon-btn nx-icon-btn-sm is-danger"
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
              {iRows.length === 0 && (
                <tr>
                  <td colSpan={5} className="nx-table-empty">
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
      pushToast("error", `拉取失败: ${describeError(e)}`);
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
