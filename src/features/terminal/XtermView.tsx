// 终端视图：xterm.js + WebGL + attach 到内核通道（M0-T5 / §4.5）。
import { useEffect, useRef } from "react";
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import { WebglAddon } from "@xterm/addon-webgl";
import { CanvasAddon } from "@xterm/addon-canvas";
import { SearchAddon } from "@xterm/addon-search";
import { WebLinksAddon } from "@xterm/addon-web-links";
import "@xterm/xterm/css/xterm.css";

import { createBinaryChannel } from "../../ipc/events";
import { dockerApi, terminalApi } from "../../ipc/commands";
import { describeError } from "../../ui/errorText";
import { CommandBlockManager, type CommandBlock } from "./commandBlocks";

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
   * 高度算出 0，FitAddon 会提出 2x1 这种尺寸并**连带把远端 PTY 也改小**，
   * 所以隐藏期间绝不能 fit。
   */
  visible?: boolean;
  onClosed?: () => void;
  onAttach?: (kernelTabId: string) => void;
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
  const fitRef = useRef<FitAddon | null>(null);
  const searchRef = useRef<SearchAddon | null>(null);
  /** 内核标签 id（跨 effect 访问，主 effect 里的局部变量取不到）。 */
  const kernelTabIdRef = useRef<string>("");
  // 回调放 ref：避免它们进入 effect 依赖导致终端被反复重建
  const onBlocksRef = useRef(props.onBlocks);
  const onHandleRef = useRef(props.onHandle);
  onBlocksRef.current = props.onBlocks;
  onHandleRef.current = props.onHandle;

  /** 只在容器有真实尺寸时 fit —— 隐藏容器的 computed 高度是 0。 */
  const fitIfSized = () => {
    const host = hostRef.current;
    const fit = fitRef.current;
    if (!host || !fit) return;
    if (host.clientWidth <= 0 || host.clientHeight <= 0) return;
    fit.fit();
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
    const fit = new FitAddon();
    const search = new SearchAddon();
    term.loadAddon(fit);
    term.loadAddon(search);
    term.loadAddon(new WebLinksAddon());
    termRef.current = term;
    fitRef.current = fit;
    searchRef.current = search;
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
    fitIfSized();

    let kernelTabId = "";
    let disposed = false;

    // ── 命令块（M1-T9）：吃按键流还原命令边界，不拦截不改写 ──
    const blocks = new CommandBlockManager(term, (list) => onBlocksRef.current?.(list));
    onHandleRef.current?.({
      copyBlock: (index) => blocks.getBlockText(index),
      scrollToBlock: (index) => blocks.scrollTo(index),
      navigateBlock: (dir) => blocks.navigate(dir),
      clearBlocks: () => blocks.clear(),
      clear: () => term.clear(),
      getSelection: () => term.getSelection(),
    });

    // ── attach：输出走 Channel 二进制（L1）──
    const channel = createBinaryChannel((bytes) => {
      term.write(bytes);
    });
    const doAttach = async () => {
      try {
        const cols = term.cols;
        const rows = term.rows;
        // 三种执行通道：容器内 exec / WinRM 行模式 / 服务器 PTY
        const id = props.containerId
          ? await dockerApi.execAttach(
              props.sessionId,
              props.containerId,
              cols,
              rows,
              channel,
            )
          : props.winrm
            ? await import("../../ipc/commands").then((m) =>
                m.sessionApi.openLineTab(props.sessionId, cols, rows, channel),
              )
            : await terminalApi.attach(props.sessionId, cols, rows, channel);
        if (disposed) return;
        kernelTabId = id;
        kernelTabIdRef.current = id;
        props.onAttach?.(id);
      } catch (e) {
        // 不能用 String(e)：内核抛的 AppError 是 { code, message } 对象，
        // String() 只会印出 "[object Object]"，真正的原因（会话没了？PTY 开不出来？）
        // 当场丢失，排查时等于什么都没说。
        term.writeln(`\r\n\x1b[31m[attach 失败] ${describeError(e)}\x1b[0m`);
      }
    };
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
      if (props.onData) {
        props.onData(data);
      } else {
        void terminalApi
          .write(kernelTabId, new TextEncoder().encode(data))
          .catch(() => undefined);
      }
    });
    const resizeDisposable = term.onResize(({ cols, rows }) => {
      if (!kernelTabId) return;
      void terminalApi.resize(kernelTabId, cols, rows).catch(() => undefined);
    });

    // ── 可见性降频（§4.4）──
    // 注意：这里**不再**用 IntersectionObserver 做 fit。fit 只由 props.visible 驱动，
    // 否则「隐藏容器算出 0 高 → fit 出 2x1 → 连带改小远端 PTY」会把 shell 布局搞坏。
    props.registerSearch?.({
      findNext: (t) => search.findNext(t),
      findPrevious: (t) => search.findPrevious(t),
    });

    const ro = new ResizeObserver(() => fitIfSized());
    ro.observe(hostRef.current);

    return () => {
      disposed = true;
      window.clearTimeout(attachTimer);
      ro.disconnect();
      dataDisposable.dispose();
      resizeDisposable.dispose();
      blocks.dispose();
      if (kernelTabId) {
        // 只 detach（断开前端通道），不 close —— 内核标签的生命周期由 store.closeTab
        // 显式调用 terminal_close_tab 来管（§7：关标签 ≠ 断连，但关标签要回收 PTY）。
        void terminalApi.detach(kernelTabId).catch(() => undefined);
      }
      kernelTabIdRef.current = "";
      term.dispose();
      termRef.current = null;
    };
      }, [props.sessionId, props.containerId]);

  // ── 标签激活状态 → 可见性 + 重新 fit（§4.4）──
  // 切标签时组件不再卸载（见 App.tsx 的「全部挂载、隐藏非激活」），
  // 所以这里必须显式把可见性同步给内核，并在重新可见时补一次 fit。
  //
  // 依赖里带 props.tabId：attach 是延迟发的，若 attach 完成时标签已被切走，
  // kernelTabIdRef 才刚有值，需要靠 tabId 变化再补一次同步。
  useEffect(() => {
    const visible = props.visible !== false;
    const tabId = kernelTabIdRef.current;
    if (tabId) {
      void terminalApi.setVisible(tabId, visible).catch(() => undefined);
    }
    if (!visible) return;
    // display 生效后再量尺寸，否则量到的仍是 0
    const raf = requestAnimationFrame(() => {
      fitIfSized();
    });
    return () => cancelAnimationFrame(raf);
  }, [props.visible, props.tabId]);

  return <div ref={hostRef} className="h-full w-full min-h-0" />;
}
