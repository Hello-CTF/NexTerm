// 终端视图：xterm.js + WebGL + attach 到内核通道（M0-T5 / §4.5）。
import { useEffect, useRef } from "react";
import { Terminal } from "@xterm/xterm";
import { WebglAddon } from "@xterm/addon-webgl";
import { CanvasAddon } from "@xterm/addon-canvas";
import { SearchAddon } from "@xterm/addon-search";
import { WebLinksAddon } from "@xterm/addon-web-links";
import "@xterm/xterm/css/xterm.css";

import { channelIdOf, createBinaryChannel, disposeChannel, onChannelReopen } from "../../ipc/events";
import { clientId } from "../../ipc/env";
import { dockerApi, terminalApi } from "../../ipc/commands";
import { describeError } from "../../ui/errorText";
import { RESIZE_END_EVENT } from "../../ui/ResizeHandle";
import { CommandBlockManager, type CommandBlock } from "./commandBlocks";
import { measureTerminalGeometry, resizeTerminalToGrid } from "./terminalGeometry";
import { TerminalGridCoordinator } from "./terminalGrid";
import { productionGridRuntime } from "./gridRuntimeAdapter";

// 终端配色：与 styles.css 的令牌保持一致（画布 #101217），
// 前景与 ANSI 十六色统一压低饱和度，和整体界面（微冷深灰）同调。
//
// 光标色不写死：它是**强调色**，而强调色是可能整体换掉的设计令牌
// （见 styles.css 的 --color-accent）。写死在这里的话，换色时终端光标会
// 偷偷留在旧颜色上——这种"只漏一处"的不一致最难发现。
//
// 但 blue / brightBlue **故意不等于强调色**：它们是 ANSI 语义色（`ls` 的目录、
// git 输出都靠它），要比强调色亮一档才读得清；跟着强调色一起变会变暗。
// 所以换强调色时**不要**顺手同步这两个值。
const THEME = {
  background: "#101217",
  foreground: "#c6cbd6",
  cursorAccent: "#101217",
  selectionBackground: "#2c3e5d",
  black: "#101217",
  brightBlack: "#5c6472",
  red: "#e87b7b",
  brightRed: "#f0a0a0",
  green: "#7fd6a4",
  brightGreen: "#a3e6bd",
  yellow: "#e9c489",
  brightYellow: "#f0d6a4",
  blue: "#79a8f8",
  brightBlue: "#9dc0fb",
  magenta: "#b79ce8",
  brightMagenta: "#cdb9f0",
  cyan: "#7fc7d6",
  brightCyan: "#a3dbe6",
  white: "#c6cbd6",
  brightWhite: "#eef1f6",
};

/** 运行期从 CSS 令牌读强调色，读不到再退回一个安全值（仅样式表还没生效时会走到）。 */
function accentColor(): string {
  const v = getComputedStyle(document.documentElement).getPropertyValue("--color-accent").trim();
  // 兜底值要跟着 styles.css 的 --color-accent 一起改，否则极端情况下会退回旧色
  return v || "#516cd6";
}

/** 终端操作句柄：由 XtermView 建立后交给 TerminalPane 的工具条/块面板使用。 */
export interface TerminalHandle {
  copyBlock: (index: number) => string;
  scrollToBlock: (index: number) => void;
  navigateBlock: (dir: "prev" | "next") => number | null;
  clearBlocks: () => void;
  /** 清屏（只清视口，滚回去还能看到 —— 和 shell 的 clear 语义一致）。 */
  clear: () => void;
  /** 当前选中的文本（没选中时是空串）。右键「向 AI 提问」用它取内容。 */
  getSelection: () => string;
  /** 聚焦终端输入代理；移动端用它主动唤起系统键盘。 */
  focus: () => void;
  /** 接管控制权后按当前宿主重新计算行列。 */
  fit: () => void;
  /** 当前终端尺寸。接管控制权后要按它把 PTY 尺寸推过去（最后活跃者赢）。 */
  dimensions: () => { cols: number; rows: number };
}

export interface XtermViewProps {
  sessionId: string;
  tabId: string;
  winrm?: boolean;
  /** 非空表示这是一个「容器内终端」，attach 走 docker exec 而不是 SSH PTY。 */
  containerId?: string;
  /**
   * 所在标签是否处于激活状态（§4.4）。
   *
   * 由标签激活状态驱动而不是 IntersectionObserver：容器 `display:none` 时
   * 高度为 0，隐藏期间不提交测量，也不改变远端 PTY。
   */
  /**
   * 要**接管**的内核标签 id（从服务端恢复工作区时带上来）。
   *
   * 非空 ⇒ 走 `terminal_attach_tab`：不新开 shell，把服务端那条连接上已有的
   * 回滚内容回放回来。为空 ⇒ 走 `terminal_attach` 新建（会开一个新的 shell）。
   *
   * ⚠️ 这个 prop 是「关掉网页再打开还能接回原终端」的总开关。它一旦有值，
   * 组件就不能再走新建分支 —— 否则每恢复一次就多泄漏一个远端 shell。
   */
  resumeTabId?: string;
  /**
   * attach 结果（含「我现在是不是控制者」）。
   *
   * 交给上层决定要不要挂观察者遮罩 —— 这个组件只负责画终端，不掌握多端语义。
   */
  onAttachInfo?: (info: {
    tabId: string;
    controller: string | null;
    /** 通道数（含自己）：同一台设备多开一个页面就会 +1。 */
    subscribers: number;
    /** 观看设备数（按 clientId 去重）：界面「N 个设备正在观看」用这个。 */
    viewers: number;
    exited: boolean;
  }) => void;
  /**
   * 是否允许本视图改 PTY 尺寸（默认 true）。
   *
   * 观察者必须传 false：`terminal_resize` 会校验控制权（非控制者报 `not_controller`），
   * 而且观察者本来就不该改尺寸 —— 一个 PTY 只有一组 `cols`/`rows`，谁操作谁决定，
   * 否则别的设备一打开页面就会把正在操作那端的画面重排。
   */
  canResize?: boolean;
  /** 控制端提交的权威网格；observer 按 revision 应用，不在本地回写。 */
  remoteGrid?: { cols: number; rows: number; revision: number };
  visible?: boolean;
  onClosed?: () => void;
  onAttach?: (kernelTabId: string) => void;
  /**
   * attach 失败且错误码是 `not_found` —— 即「连接已失效」时回调。
   *
   * 覆盖两条路径：接管已有标签（服务端重启后 tabId 已不存在）与新建（会话已被回收）。
   * 两者对用户是同一件事：这个标签背后的连接不在服务端了，需要一个明确的下一步，
   * 而不是在终端里留一行黄字让人干瞪眼。
   *
   * ⚠️ 这个组件**不掌握 store**（不知道 storeTabId），所以清失效 `tabId`、弹遮罩、
   * 重连都由上层 `TerminalPane` 在收到回调后处理。
   */
  onAttachFailed?: (code: string) => void;
  registerSearch?: (api: { findNext: (t: string) => void; findPrevious: (t: string) => void }) => void;
  onData?: (data: string) => void;
  /** 命令块列表变化（M1-T9）。 */
  onBlocks?: (blocks: CommandBlock[]) => void;
  /** 终端就绪后交出操作句柄。 */
  onHandle?: (h: TerminalHandle) => void;
}

export function XtermView(props: XtermViewProps) {
  const hostRef = useRef<HTMLDivElement>(null);
  const termRef = useRef<Terminal | null>(null);
  const gridRef = useRef<TerminalGridCoordinator | null>(null);
  const onGeometryRef = useRef<(() => void) | null>(null);
  /** 内核标签 id（跨 effect 访问，主 effect 里的局部变量取不到）。 */
  const kernelTabIdRef = useRef<string>("");
  // 回调放 ref：避免它们进入 effect 依赖导致终端被反复重建
  const onBlocksRef = useRef(props.onBlocks);
  const onHandleRef = useRef(props.onHandle);
  const onAttachInfoRef = useRef(props.onAttachInfo);
  const onAttachFailedRef = useRef(props.onAttachFailed);
  /**
   * 输入回调也放 ref：`term.onData` 的 handler 是在 attach effect 里注册的，
   * 而那个 effect 的依赖只有 sessionId / containerId / resumeTabId —— 直接读
   * `props.onData` 的话，拿到的会是首次渲染那一份闭包（里面的 kernelTabId 还是
   * null、控制权态也还是旧的），于是观察者拦截、最新 tabId 全都对不上。
   */
  const onDataRef = useRef(props.onData);
  onBlocksRef.current = props.onBlocks;
  onHandleRef.current = props.onHandle;
  onAttachInfoRef.current = props.onAttachInfo;
  onAttachFailedRef.current = props.onAttachFailed;
  onDataRef.current = props.onData;

  const fitIfSized = (afterClaim = false, final = false) => {
    const host = hostRef.current;
    const term = termRef.current;
    const coordinator = gridRef.current;
    if (!host || !term || !coordinator) return;
    if (afterClaim) coordinator.setCanResize(true);
    const geometry = measureTerminalGeometry(term, host);
    if (!geometry) return;
    coordinator.update(geometry.viewport, geometry.metrics);
    if (final) coordinator.flush();
    onGeometryRef.current?.();
  };

  useEffect(() => {
    if (!hostRef.current || termRef.current) return;
    const term = new Terminal({
      scrollback: 100_000, // §4.5
      allowProposedApi: true,
      // 命令块刻点用的尺子（§4.5 块分隔）；不设则 ruler 隐藏
      overviewRulerWidth: 14,
      fontFamily: "'Cascadia Mono', 'Cascadia Code', Consolas, 'Courier New', monospace",
      fontSize: 13,
      cursorBlink: true,
      theme: { ...THEME, cursor: accentColor() },
    });
    const search = new SearchAddon();
    term.loadAddon(search);
    term.loadAddon(new WebLinksAddon());
    termRef.current = term;
    term.open(hostRef.current);
    // WebGL 优先，失败降级 Canvas（§4.5）
    try {
      term.loadAddon(new WebglAddon());
    } catch {
      try {
        term.loadAddon(new CanvasAddon());
      } catch {
        // 软件渲染兜底
      }
    }

    let kernelTabId = "";
    let disposed = false;
    const coordinator = new TerminalGridCoordinator(
      productionGridRuntime,
      (grid) => resizeTerminalToGrid(term, grid),
      { visible: props.visible !== false, canResize: props.canResize !== false },
    );
    gridRef.current = coordinator;
    fitIfSized();

    // ── 命令块（M1-T9）：吃按键流还原命令边界，不拦截不改写 ──
    const blocks = new CommandBlockManager(term, (list) => onBlocksRef.current?.(list));
    onHandleRef.current?.({
      copyBlock: (index) => blocks.getBlockText(index),
      scrollToBlock: (index) => blocks.scrollTo(index),
      navigateBlock: (dir) => blocks.navigate(dir),
      clearBlocks: () => blocks.clear(),
      clear: () => term.clear(),
      getSelection: () => term.getSelection(),
      focus: () => term.focus(),
      fit: () => fitIfSized(true),
      dimensions: () => ({ cols: term.cols, rows: term.rows }),
    });

    // ── attach：输出走 Channel 二进制（L1）──
    const channel = createBinaryChannel((bytes) => {
      term.write(bytes);
    });
    const applyRemoteDimensions = (cols: number, rows: number) => {
      resizeTerminalToGrid(term, { cols, rows });
    };
    let attaching = false;
    const doAttach = async () => {
      if (disposed || kernelTabId || attaching) return;
      const resume = props.resumeTabId;
      const dimensions = coordinator.desiredGrid();
      // 新 PTY 必须使用真实测量尺寸；隐藏宿主先等待可见后的 ResizeObserver。
      if (!resume && !dimensions) return;
      attaching = true;
      try {
        let initialGrid = dimensions;
        // ⚠️ 依赖里带 resumeTabId：恢复出来的标签必须走"接管"分支。
        // 忘了加，就会在每次恢复时新开一个 shell（用户看到的是"我原来的任务不见了，
        // 眼前是个空终端"，而远端悄悄多了一个泄漏的 shell）。
        let id: string;
        if (resume) {
          // 接管**优先于** containerId / winrm 分支。
          //
          // 容器 exec、WinRM 行模式的标签同样存在内核的标签表里（见 docker.rs 的
          // attach_exec_tab），能用同一条 `terminal_attach_tab` 接回来。如果让
          // containerId / winrm 分支排在前面，每次恢复都会重新 exec / 重开一条
          // 行模式标签 —— 和"新开 shell"是同一类泄漏，只是更隐蔽。
          //
          // 不传 cols/rows —— 接管方无权改 PTY 尺寸（见 terminalApi.attachTab）。
          const info = await terminalApi.attachTab(resume, channel);
          id = info.tabId;
          if (disposed) {
            // 等待期间组件已卸载：cleanup 已经 term.dispose()，这时再
            // applyRemoteDimensions 就是在已销毁的终端上 resize —— xterm 会抛
            // Viewport.syncScrollArea TypeError（demo 里「关掉最后一个标签再重连」
            // 必现）。服务端订阅也要按通道定向摘掉，同下方统一检查。
            void terminalApi.detach(id, channelIdOf(channel)).catch(() => undefined);
            return;
          }
          initialGrid = { cols: info.cols, rows: info.rows };
          applyRemoteDimensions(info.cols, info.rows);
          onAttachInfoRef.current?.({
            tabId: info.tabId,
            controller: info.controller,
            subscribers: info.subscribers,
            viewers: info.viewers,
            exited: info.exited,
          });
        } else if (props.containerId) {
          if (!dimensions) return;
          id = await dockerApi.execAttach(
            props.sessionId,
            props.containerId,
            dimensions.cols,
            dimensions.rows,
            channel,
          );
          onAttachInfoRef.current?.({
            tabId: id,
            controller: null,
            subscribers: 1,
            viewers: 1,
            exited: false,
          });
        } else if (props.winrm) {
          if (!dimensions) return;
          id = await import("../../ipc/commands").then((m) =>
            m.sessionApi.openLineTab(props.sessionId, dimensions.cols, dimensions.rows, channel),
          );
          onAttachInfoRef.current?.({
            tabId: id,
            controller: null,
            subscribers: 1,
            viewers: 1,
            exited: false,
          });
        } else {
          if (!dimensions) return;
          id = await terminalApi.attach(props.sessionId, dimensions.cols, dimensions.rows, channel);
          onAttachInfoRef.current?.({
            tabId: id,
            controller: clientId(),
            subscribers: 1,
            viewers: 1,
            exited: false,
          });
        }
        if (disposed) {
          // 组件在 attach 等待期间被卸载：此刻服务端已按这条 channel 登记了订阅者，
          // 而 cleanup 当时看到的 kernelTabId 还是空、不会替我们摘 —— 必须在这里自己补一次。
          //
          // ⚠️ 必须带 channelId（只摘自己这条通道）；不带就是「清空全部」，会把同一终端上
          // 其他设备的推送一起掐掉（它们那边只表现为"画面不动了"，几乎无从排查）。
          // 安全性依据：channel id 含 clientId + 序号 + 随机段，每个 XtermView 实例唯一，
          // 所以这次定向 detach 不可能误摘别人的通道。
          void terminalApi.detach(id, channelIdOf(channel)).catch(() => undefined);
          return;
        }
        coordinator.attach(id, initialGrid, false);
        kernelTabId = id;
        kernelTabIdRef.current = id;
        props.onAttach?.(id);
      } catch (e) {
        // 不能用 String(e)：内核抛的 AppError 是 { code, message } 对象，
        // String() 只会印出 "[object Object]"，真正的原因（会话没了？PTY 开不出来？）
        // 当场丢失，排查时等于什么都没说。
        const code = (e as { code?: string } | null)?.code;
        if (code === "not_found") {
          // 连接已失效（服务端重启后 tabId 全失效 / 会话被回收）。
          //
          // ⚠️ 上层**必须**处理：清掉 store 里这条失效的 `tabId`，否则
          //   ①`term.onData` 里的 `if (!kernelTabId) return` 会把输入全吃掉，
          //     标签永久敲不动；
          //   ②失效的 id 会被反复持久化成"这里有个活着的终端"，重启一次多一个死标签。
          // 这里只把失效这件事上报（上层做清理 + 挂「连接已失效」遮罩给下一步），
          // 终端里留一行痕迹给遮罩后面的画面。
          if (!disposed) onAttachFailedRef.current?.(code);
          term.writeln("\r\n\x1b[33m[连接已失效] 这台终端所属的连接在服务端已经不在了。\x1b[0m");
        } else {
          term.writeln(`\r\n\x1b[31m[attach 失败] ${describeError(e)}\x1b[0m`);
        }
      } finally {
        attaching = false;
      }
    };
    onGeometryRef.current = () => void doAttach();

    /**
     * 通道 WS 重连后**重新登记订阅**（bfcache 恢复 / 网络抖动都会走到这里）。
     *
     * # 为什么需要它
     * WS 断开时服务端会把这条通道在终端里的订阅摘掉（`sinks` 变空，控制者还会被
     * 释放控制权）。transport 随后用**同一 id** 自动重连，但那只是重建字节管道，
     * 不会重放 app 层的订阅；而 bfcache 恢复是同一文档、React 不重挂 ⇒ 没人再调
     * attach ⇒ 终端「看着在、敲了没反应」（写入被 `not_controller` 拒掉）。
     *
     * # 为什么只走"接管"分支
     * 必须用 `attachTab(已有内核 id)`。走 `doAttach` 的"新建"分支会在服务端**再开一个
     * PTY**、把原来那个泄漏掉。所以只在 `kernelTabId` 已存在时才动作。
     *
     * ⚠️ `attachTab` 在服务端会清屏 + 回放 scrollback（`session/mod.rs` 的
     * `attach_existing_tab`），所以每次重连画面会重绘一次。这是复用同一内核标签的代价，
     * 换来的是订阅随连接自愈。
     */
    const reattachOnReopen = () => {
      const id = kernelTabId;
      if (!id) return; // 首次 attach 还没落地：那条路径自己会登记订阅
      void (async () => {
        try {
          const info = await terminalApi.attachTab(id, channel);
          if (disposed) return;
          kernelTabIdRef.current = info.tabId;
          applyRemoteDimensions(info.cols, info.rows);
          coordinator.attach(info.tabId, { cols: info.cols, rows: info.rows }, true);
          onAttachInfoRef.current?.({
            tabId: info.tabId,
            controller: info.controller,
            subscribers: info.subscribers,
            viewers: info.viewers,
            exited: info.exited,
          });
        } catch (e) {
          if (disposed) return;
          // 内核里那条标签也没了（例如服务端重启过）⇒ 走既有的失效上报，
          // 让上层清 tabId + 挂「连接已失效」遮罩。
          if ((e as { code?: string } | null)?.code === "not_found") {
            onAttachFailedRef.current?.("not_found");
          }
        }
      })();
    };
    const offReopen = onChannelReopen(channel, reattachOnReopen);

    // attach 延迟到一个宏任务再发。
    //
    // React StrictMode 在开发模式下会「挂载 → 卸载 → 再挂载」，同步 attach 会开出
    // **两个 PTY**：第一个的 cleanup 只做 detach（不断开内核，通道没人收但泵照跑），
    // 于是远端多出一个泄漏的 shell。放进宏任务后，第一次的 timer 会在 cleanup 里被清掉，
    // 最终只 attach 一次。生产构建没有 StrictMode 双调用，行为不变（只是晚 0ms）。
    const attachTimer = window.setTimeout(() => {
      void doAttach();
    }, 0);

    // ── 输入：onData → 命令块记账 + terminal_write（§4.5）──
    const dataDisposable = term.onData((data) => {
      blocks.feedInput(data);
      if (!kernelTabId) return;
      const onData = onDataRef.current;
      if (onData) {
        onData(data);
      } else {
        void terminalApi
          .write(kernelTabId, new TextEncoder().encode(data))
          .catch(() => undefined);
      }
    });
    props.registerSearch?.({
      findNext: (t) => search.findNext(t),
      findPrevious: (t) => search.findPrevious(t),
    });

    let measureFrame: number | null = null;
    let finalFrame = false;
    let windowResizeTimer: number | null = null;
    const scheduleMeasure = (final = false) => {
      finalFrame = finalFrame || final;
      if (measureFrame !== null) return;
      measureFrame = requestAnimationFrame(() => {
        measureFrame = null;
        const accurate = finalFrame;
        finalFrame = false;
        fitIfSized(false, accurate);
      });
    };
    const onFinalResize = () => scheduleMeasure(true);
    const onWindowResize = () => {
      scheduleMeasure();
      if (windowResizeTimer !== null) window.clearTimeout(windowResizeTimer);
      windowResizeTimer = window.setTimeout(() => {
        windowResizeTimer = null;
        scheduleMeasure(true);
      }, 120);
    };
    const ro = new ResizeObserver(() => scheduleMeasure());
    ro.observe(hostRef.current);
    window.addEventListener(RESIZE_END_EVENT, onFinalResize);
    window.addEventListener("resize", onWindowResize);
    window.visualViewport?.addEventListener("resize", onWindowResize);

    return () => {
      disposed = true;
      coordinator.close();
      gridRef.current = null;
      onGeometryRef.current = null;
      offReopen();
      window.clearTimeout(attachTimer);
      if (windowResizeTimer !== null) window.clearTimeout(windowResizeTimer);
      if (measureFrame !== null) cancelAnimationFrame(measureFrame);
      window.removeEventListener(RESIZE_END_EVENT, onFinalResize);
      window.removeEventListener("resize", onWindowResize);
      window.visualViewport?.removeEventListener("resize", onWindowResize);
      ro.disconnect();
      dataDisposable.dispose();
      blocks.dispose();
      if (kernelTabId) {
        // 只 detach（断开前端通道），不 close —— 内核标签的生命周期由 store.closeTab
        // 显式调用 terminal_close_tab 来管（§7：关标签 ≠ 断连，但关标签要回收 PTY）。
        //
        // ⚠️ 必须带上 channelId（服务端）：不带就是「清空全部」，会把同一个终端上
        // 其他设备的推送一起掐掉 —— 它们那边只表现为"画面不动了"，几乎无从排查。
        // 桌面模式下 channelIdOf 返回 undefined，退回「清空全部」，语义不变。
        void terminalApi.detach(kernelTabId, channelIdOf(channel)).catch(() => undefined);
      }
      // detach 之后关闭通道本身：它只摘服务端订阅，那条 WS 仍挂着。
      // 不关的话 `entry.disposed` 永远 false，`ws.onclose` 会退避重连，
      // 于是每开一个标签就永久多一条通道（实测 liveChannels 1→2→…→6）。
      // 顺序：detach 要用 channelIdOf(channel) 取 id 发给服务端（读 channel.id，
      // 不受 dispose 影响），所以先 detach 后 dispose 最省心；两端重复摘订阅幂等。
      disposeChannel(channel);
      kernelTabIdRef.current = "";
      term.dispose();
      termRef.current = null;
    };
  }, [props.sessionId, props.containerId, props.resumeTabId]);

  useEffect(() => {
    const visible = props.visible !== false;
    gridRef.current?.setVisible(visible);
    const tabId = kernelTabIdRef.current;
    if (tabId) void terminalApi.setVisible(tabId, visible).catch(() => undefined);
    if (!visible) return;
    const raf = requestAnimationFrame(() => fitIfSized());
    return () => cancelAnimationFrame(raf);
  }, [props.visible, props.tabId]);

  useEffect(() => {
    gridRef.current?.setCanResize(props.canResize !== false);
  }, [props.canResize]);

  useEffect(() => {
    const remote = props.remoteGrid;
    if (remote) gridRef.current?.observe(remote.revision, remote);
  }, [props.remoteGrid]);

  return <div ref={hostRef} className="h-full w-full min-h-0" />;
}
