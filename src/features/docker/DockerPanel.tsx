// Docker 面板（M3）：容器列表 + 镜像管理 + 日志跟随 + 容器终端。
import { useEffect, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ask } from "../../ui/dialogs";
import { dockerApi, terminalApi, type ContainerSummary, type ImageSummary } from "../../ipc/commands";
import { useUi } from "../../app/store";
import { createBinaryChannel, disposeChannel, onChannelReopen } from "../../ipc/events";
import { describeError } from "../../ui/errorText";
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

/** 一次「查看日志」的 attach 结果。 */
interface LogAttach {
  /** 容器名（工具栏标题用）。 */
  container: string;
  /** 容器 id —— 重连自愈时要拿它重新 attach（见下面 `onChannelReopen` 那条 effect）。 */
  containerId: string;
  /** 服务端返回的内核标签 id，关闭时要拿它 detach。 */
  tabId: string;
  /** 唯一的那条二进制通道（也是唯一那个 PTY 的宿主）。 */
  channel: ReturnType<typeof createBinaryChannel>;
  /**
   * 字节的去处。通道在 `openLogs` 里就建好了 —— `createBinaryChannel` 的回调
   * 只在**创建时**绑定，而此刻消费者 `<LogStream>` 还没渲染。所以让通道回调读
   * 这个可变槽位，`LogStream` 挂载时把 `onBytes` 指过去、卸载时置回 null。
   *
   * 不直接把 `channel.onmessage` 交给 LogStream 重绑：那要求三个形态（Tauri
   * `Channel` / `WebChannel` / demo）的 `onmessage` 都可写且能复用 events.ts 里
   * 的字节解码；转发器只依赖一个稳定的对象引用，与通道形态无关。
   */
  sink: { onBytes: ((bytes: Uint8Array) => void) | null };
}

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
  const [attached, setAttached] = useState<LogAttach | null>(null);
  /** 洞察钻取（M61）：选中的容器。key 绑容器 id，切换即重建，旧响应不落新容器。 */
  const [insight, setInsight] = useState<ContainerSummary | null>(null);
  /** 勾选集合：存行 key（容器 = id，镜像 = imageKey）。 */
  const [picked, setPicked] = useState<Set<string>>(() => new Set());
  /** 正在删除的行 key：挡住「点了没反应就再点一下」的并发删除。 */
  const [pending, setPending] = useState<Set<string>>(() => new Set());
  const [bulkBusy, setBulkBusy] = useState(false);
  /** 在途 guard：一次只允许一次 `logsAttach` 在飞（见 `openLogs` 的注释）。 */
  const attachInFlight = useRef(false);

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

  // 日志 attach 的回收：`返回` 与「关闭标签（组件卸载）」共用这一条 cleanup。
  // 之所以放在 effect 而不是按钮里：这两条路径都要「杀掉那个 `docker logs -f` PTY +
  // 关掉那条 WS」，写成两份必然漏一处 —— 而漏卸载那处就是关标签后永久残留一个进程。
  //
  // 职责是**回收 PTY**，所以这里用 `closeTab(..., "kill")` 而不是旧的 detach：一次
  // 「查看日志」= 一次 `logsAttach` = 一个**全新**的 tabId，回看这个面板永远走的是新
  // attach，没有任何路径会重新接管这个标签 —— 留着它只会变成一个用户从没开过的
  // 「后台会话」幽灵（旧版每点一次「查看日志→返回」就永久多一个）。
  // `close_tab` 也不缺原 detach 的语义：它内部先 `detach_frontend` 摘掉订阅、再
  // 取 killer 杀进程，所以这一行是**替换**、不是叠加。`disposeChannel` 仍要保留 ——
  // 本地那条 WS 和 PTY 是两回事，关 WS 并不会杀进程。
  useEffect(() => {
    if (!attached) return;
    const { tabId, channel } = attached;
    return () => {
      void terminalApi.closeTab(tabId, "kill").catch(() => undefined);
      disposeChannel(channel);
    };
  }, [attached]);

  // 断线 / 后退（bfcache）自愈：这条通道的 WS 重连后，重新登记一次订阅。
  //
  // 服务端只以「有没有人重新登记」判订阅；而登记的**唯一**动作就是
  // `docker_logs_attach`（它是给日志跟随打 `is_ephemeral` 标记、并计入
  // `subscribers` 的那条命令）。不补这一步，WS 用同一个通道 id 重连上了、
  // 服务端该标签的 `subscribers` 却恒为 0：
  //   ① 面板从此冻结，不再收帧（改之前就是这个既有行为）；
  //   ② 给日志跟随打的 `is_ephemeral` 标记会在订阅者归零 5s 后被回收，
  //      正在看的人也会被误杀。
  //
  // 为什么是「重新 attach 一口新的」而不是「接回同一口」：服务端没有
  // 「attach 到已存在的日志标签」的命令，`docker_logs_attach` 每次都新起一个
  // `docker logs -f` PTY；而改走既有的 `terminal_attach_tab` 会把清屏转义前缀
  // （`\x1b[2J\x1b[3J\x1b[H`）连同回滚内容一起灌进这条**二进制**通道，而
  // `LogStream` 是纯文本行缓冲，会把转义当普通文字渲染出来。所以「新建一口 +
  // 面板从最近 500 行续上」是当前架构下正确且最省的做法。
  //
  // 重连仍走同一条 `setAttached(...)`，因此会自动触发上面那条 `[attached]`
  // cleanup：旧那口 PTY 被 `closeTab(old, "kill")` 立刻回收、旧通道被
  // `disposeChannel` —— 任一刻只有一口 `docker logs -f`。
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

  const openLogs = async (containerId: string, containerName: string) => {
    // 一次「查看日志」= 一次 attach = 一个 PTY。
    //
    // 这条通道的回调在创建时就绑定好了，但消费者 `<LogStream>` 此刻还没渲染，
    // 于是先把回调接到 `sink` 这个可变槽位，等 LogStream 挂载时再指向它的 setLines。
    // 不再「建两条通道 / attach 两次」：以前这里用容器 id 建一条丢弃字节的空转通道，
    // LogStream 又用容器**名字**另建一条 —— 服务端 `docker_logs_attach` 每次都新起一个
    // `docker logs -f` PTY，于是白起一个没人看的 PTY，且它那条通道的 tabId 从没被记下、
    // 关不掉。现在被追踪的这一次就是唯一的一次。
    //
    // 在途 guard：通道抖动（或页面前后台切换）时 `onChannelReopen` 可能连续触发，
    // 两次 `logsAttach` 并发会多起一口没人看的 PTY。成功与失败都要复位。
    if (attachInFlight.current) return;
    attachInFlight.current = true;
    const sink: LogAttach["sink"] = { onBytes: null };
    const channel = createBinaryChannel((bytes) => sink.onBytes?.(bytes));
    try {
      const kernelTab = await dockerApi.logsAttach(sessionId, containerId, 500, channel);
      setAttached({ container: containerName, containerId, tabId: kernelTab, channel, sink });
    } catch (e) {
      // attach 失败：没有消费者，立刻释放，别漏一条 WS。
      // 成功时**不能**在这里释放 —— 通道现在有消费者（LogStream），提前 dispose 会让
      // 日志静默丢失（服务端未认领的帧缓存进 pending，上限 512）。回收统一交给上面的
      // `[attached]` effect cleanup。
      disposeChannel(channel);
      pushToast("error", `日志 attach 失败: ${describeError(e)}`);
    } finally {
      attachInFlight.current = false;
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
          {/* key 绑 tabId：换容器时即使 React 复用了这个位置，也会重建 LogStream，
              行缓冲不会把上一个容器的日志带过来。 */}
          <LogStream key={attached.tabId} sink={attached.sink} />
        </div>
      </div>
    );
  }

  // 洞察钻取（M61）：与日志跟随同款的整页替换。`key={insight.id}` 保证切换容器时
  // 整个详情树重建 —— 上一个容器的在途响应（inspect / stats / listDir）只能落进
  // 旧缓存键，不会渲染到新容器头上。
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

      {/* 主机概览条（M61）：走 docker_overview，与列表的 5s 轮询分开，30s 慢一拍。
          切到镜像页 / 钻取详情时整条卸载，轮询随之中断。 */}
      {tab === "containers" && <OverviewStrip sessionId={sessionId} visible={visible} />}

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
                      <button className="nx-icon-btn nx-icon-btn-sm" title="详情 / 统计 / 文件" onClick={() => setInsight(c)}>
                        <IconMonitor size={13} />
                      </button>
                      <button className="nx-icon-btn nx-icon-btn-sm" title="查看日志" onClick={() => void openLogs(c.id, c.name)}>
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

/** 日志跟随：**纯消费者**。
 *
 *  通道与 attach 全部由 `openLogs` 完成（见 `LogAttach` 的说明），这里只把通道
 *  字节接到 `sink.onBytes` 上 —— 挂载时接、卸载时摘。它不再建通道、不再调
 *  `logsAttach`，所以「一次点击 = 一次 attach = 一个 PTY」。
 *
 *  上一版那个 `attachedRef` 防重入 guard 已删除：它的存在前提是本组件自己 attach
 *  （effect 依赖变化时靠 ref 复位才能为**新容器**重挂）。现在 attach 不在这里，
 *  「换容器」由父组件 `setAttached(null)` → 卸载本组件 → 用新 sink 重新挂载完成，
 *  `key={attached.tabId}` 保证复用位置时也重建。没有需要复位的状态，guard 失去意义。 */
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
      // 摘掉自己：通道可能比本组件活得久（父组件卸载顺序 / 位置复用），
      // 留着闭包会让已卸载组件的 setLines 被调用。
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

/** 主机概览条（M61）：docker_overview 的 hostStats。键名分两套契约 ——
 *  Go 后端给 containersRunning/containersTotal/images，演示模式给
 *  cpuPercent/memUsedMb/…，未知键按原键名直出，两种契约都能渲染。 */
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
      <div className="shrink-0 border-b border-neutral-800/60 px-3 py-1.5">
        <span className="nx-hint">主机概览加载中…</span>
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
