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
import { terminalApi } from "../../ipc/commands";
import { CommandBlockManager, type CommandBlock } from "./commandBlocks";

const THEME = {
  background: "#12141a",
  foreground: "#d7dae0",
  cursor: "#4f9cf9",
  selectionBackground: "#2c3e5d",
  black: "#12141a",
  red: "#e06c75",
  green: "#98c379",
  yellow: "#e5c07b",
  blue: "#61afef",
  magenta: "#c678dd",
  cyan: "#56b6c2",
  white: "#d7dae0",
};

/** 终端操作句柄：由 XtermView 建立后交给 TerminalPane 的工具条/块面板使用。 */
export interface TerminalHandle {
  copyBlock: (index: number) => string;
  scrollToBlock: (index: number) => void;
  navigateBlock: (dir: "prev" | "next") => number | null;
  clearBlocks: () => void;
}

export interface XtermViewProps {
  sessionId: string;
  tabId: string;
  winrm?: boolean;
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
  // 回调放 ref：避免它们进入 effect 依赖导致终端被反复重建
  const onBlocksRef = useRef(props.onBlocks);
  const onHandleRef = useRef(props.onHandle);
  onBlocksRef.current = props.onBlocks;
  onHandleRef.current = props.onHandle;

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
      theme: THEME,
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
    fit.fit();

    let kernelTabId = "";
    let disposed = false;

    // ── 命令块（M1-T9）：吃按键流还原命令边界，不拦截不改写 ──
    const blocks = new CommandBlockManager(term, (list) => onBlocksRef.current?.(list));
    onHandleRef.current?.({
      copyBlock: (index) => blocks.getBlockText(index),
      scrollToBlock: (index) => blocks.scrollTo(index),
      navigateBlock: (dir) => blocks.navigate(dir),
      clearBlocks: () => blocks.clear(),
    });

    // ── attach：输出走 Channel 二进制（L1）──
    const channel = createBinaryChannel((bytes) => {
      term.write(bytes);
    });
    const doAttach = async () => {
      try {
        const cols = term.cols;
        const rows = term.rows;
        const id = props.winrm
          ? await import("../../ipc/commands").then((m) =>
              m.sessionApi.openLineTab(props.sessionId, cols, rows, channel),
            )
          : await terminalApi.attach(props.sessionId, cols, rows, channel);
        if (disposed) return;
        kernelTabId = id;
        props.onAttach?.(id);
      } catch (e) {
        term.writeln(`\r\n\x1b[31m[attach 失败] ${String(e)}\x1b[0m`);
      }
    };
    void doAttach();

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
    const observer = new IntersectionObserver((entries) => {
      for (const e of entries) {
        if (kernelTabId) {
          void terminalApi.setVisible(kernelTabId, e.isIntersecting).catch(() => undefined);
        }
        if (e.isIntersecting) {
          fitRef.current?.fit();
        }
      }
    }, { threshold: 0.05 });
    observer.observe(hostRef.current);

    props.registerSearch?.({
      findNext: (t) => search.findNext(t),
      findPrevious: (t) => search.findPrevious(t),
    });

    const ro = new ResizeObserver(() => fit.fit());
    ro.observe(hostRef.current);

    return () => {
      disposed = true;
      observer.disconnect();
      ro.disconnect();
      dataDisposable.dispose();
      resizeDisposable.dispose();
      blocks.dispose();
      if (kernelTabId) {
        void terminalApi.detach(kernelTabId).catch(() => undefined);
      }
      term.dispose();
      termRef.current = null;
    };
      }, [props.sessionId]);

  return <div ref={hostRef} className="h-full w-full min-h-0" />;
}
