import { useEffect, useRef, useState } from "react";
import { Terminal } from "@xterm/xterm";
import { WebglAddon } from "@xterm/addon-webgl";
import { SearchAddon } from "@xterm/addon-search";
import { WebLinksAddon } from "@xterm/addon-web-links";
import "@xterm/xterm/css/xterm.css";

import { channelIdOf, createBinaryChannel, disposeChannel, onChannelReopen } from "../../ipc/events";
import { clientId } from "../../ipc/env";
import { dockerApi, terminalApi } from "../../ipc/commands";
import { describeError } from "../../ui/errorText";
import { IconArrowDown } from "../../ui/icons";
import { RESIZE_END_EVENT } from "../../ui/ResizeHandle";
import {
  getAppearancePrefs,
  getInputPrefs,
  getResolvedTerminalTheme,
  subscribeAppearancePrefs,
  type ResolvedTerminalTheme,
} from "../../app/preferences";
import { CommandBlockManager, type CommandBlock } from "./commandBlocks";
import { createOscStreamFilter } from "./oscStream";
import { measureTerminalGeometry, resizeTerminalToGridPreservingSelection } from "./terminalGeometry";
import { TerminalGridCoordinator, type TerminalGrid } from "./terminalGrid";
import { productionGridRuntime } from "./gridRuntimeAdapter";
import { createSelectionAutoCopy } from "./selectionAutoCopy";
import { wrapBracketedPaste } from "./terminalPaste";

export const XTERM_DARK_THEME = {
  background: "#101217",
  foreground: "#c6cbd6",
  cursorAccent: "#101217",
  selectionBackground: "#2c3e5d",
  scrollbarSliderBackground: "#31363f",
  scrollbarSliderHoverBackground: "#3e444f",
  scrollbarSliderActiveBackground: "#4d5563",
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

export const XTERM_LIGHT_THEME = {
  background: "#f4f6fa",
  foreground: "#2f3642",
  cursorAccent: "#f4f6fa",
  selectionBackground: "#c9d6f2",
  scrollbarSliderBackground: "#ccd3de",
  scrollbarSliderHoverBackground: "#aeb7c5",
  scrollbarSliderActiveBackground: "#8a94a6",
  black: "#2f3642",
  brightBlack: "#5d6572",
  red: "#c23636",
  brightRed: "#a12222",
  green: "#1a6b41",
  brightGreen: "#226e46",
  yellow: "#8a5a00",
  brightYellow: "#96660a",
  blue: "#3c59cc",
  brightBlue: "#324a9e",
  magenta: "#6440b8",
  brightMagenta: "#4c2f96",
  cyan: "#0e5a9e",
  brightCyan: "#0b4a80",
  white: "#5d6572",
  brightWhite: "#10141b",
};

export function xtermThemeFor(resolved: ResolvedTerminalTheme) {
  return resolved === "light" ? XTERM_LIGHT_THEME : XTERM_DARK_THEME;
}

function accentColor(): string {
  const v = getComputedStyle(document.documentElement).getPropertyValue("--color-accent").trim();
  return v || "#516cd6";
}

export interface TerminalHandle {
  copyBlock: (index: number) => string;
  scrollToBlock: (index: number) => void;
  navigateBlock: (dir: "prev" | "next") => number | null;
  clearBlocks: () => void;
  clear: () => void;
  getSelection: () => string;
  focus: () => void;
  fit: () => void;
  paste: (text: string) => void;
  dimensions: () => { cols: number; rows: number };
}

export interface XtermViewProps {
  sessionId: string;
  tabId: string;
  winrm?: boolean;
  containerId?: string;
  resumeTabId?: string;
  onAttachInfo?: (info: {
    tabId: string;
    controller: string | null;
    subscribers: number;
    viewers: number;
    exited: boolean;
  }) => void;
  canResize?: boolean;
  remoteGrid?: { cols: number; rows: number; revision: number };
  visible?: boolean;
  onClosed?: () => void;
  onAttach?: (kernelTabId: string) => void;
  onAttachFailed?: (code: string) => void;
  registerSearch?: (api: { findNext: (t: string) => void; findPrevious: (t: string) => void }) => void;
  onData?: (data: string) => void;
  onBlocks?: (blocks: CommandBlock[]) => void;
  onHandle?: (h: TerminalHandle) => void;
  onTitle?: (title: string) => void;
  onNotification?: (body: string) => void;
  onClipboard?: (payload: string) => void;
  onSelectionCopy?: (text: string, error: unknown | null) => void;
}

export function XtermView(props: XtermViewProps) {
  const hostRef = useRef<HTMLDivElement>(null);
  const termRef = useRef<Terminal | null>(null);
  const gridRef = useRef<TerminalGridCoordinator | null>(null);
  const onGeometryRef = useRef<(() => void) | null>(null);
  const kernelTabIdRef = useRef<string>("");
  const [linesBelow, setLinesBelow] = useState(0);
  const onBlocksRef = useRef(props.onBlocks);
  const onHandleRef = useRef(props.onHandle);
  const onAttachInfoRef = useRef(props.onAttachInfo);
  const onAttachFailedRef = useRef(props.onAttachFailed);
  const onDataRef = useRef(props.onData);
  const onTitleRef = useRef(props.onTitle);
  const onNotificationRef = useRef(props.onNotification);
  const onClipboardRef = useRef(props.onClipboard);
  const onSelectionCopyRef = useRef(props.onSelectionCopy);
  onBlocksRef.current = props.onBlocks;
  onHandleRef.current = props.onHandle;
  onAttachInfoRef.current = props.onAttachInfo;
  onAttachFailedRef.current = props.onAttachFailed;
  onDataRef.current = props.onData;
  onTitleRef.current = props.onTitle;
  onNotificationRef.current = props.onNotification;
  onClipboardRef.current = props.onClipboard;
  onSelectionCopyRef.current = props.onSelectionCopy;

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
      scrollback: 100_000,
      allowProposedApi: true,
      overviewRuler: { width: 14 },
      fontFamily: "'Cascadia Mono', 'Cascadia Code', Consolas, 'Courier New', monospace",
      fontSize: getAppearancePrefs().terminalFontSize,
      cursorBlink: true,
      theme: { ...xtermThemeFor(getResolvedTerminalTheme()), cursor: accentColor() },
    });
    const search = new SearchAddon();
    term.loadAddon(search);
    term.loadAddon(new WebLinksAddon());
    termRef.current = term;
    term.open(hostRef.current);
    try {
      term.loadAddon(new WebglAddon());
    } catch {
    }

    let kernelTabId = "";
    let disposed = false;
    const applyGrid = (grid: TerminalGrid) => resizeTerminalToGridPreservingSelection(term, grid);
    const coordinator = new TerminalGridCoordinator(
      productionGridRuntime,
      applyGrid,
      { visible: props.visible !== false, canResize: props.canResize !== false },
    );
    gridRef.current = coordinator;
    fitIfSized();

    const blocks = new CommandBlockManager(term, (list) => onBlocksRef.current?.(list));
    const sendInput = (data: string, blockText = data) => {
      blocks.feedInput(blockText);
      if (!kernelTabId) return;
      const onData = onDataRef.current;
      if (onData) {
        onData(data);
      } else {
        void terminalApi
          .write(kernelTabId, new TextEncoder().encode(data))
          .catch(() => undefined);
      }
    };
    const updateLinesBelow = () => {
      const buffer = term.buffer.active;
      const below = Math.max(0, buffer.baseY - buffer.viewportY);
      setLinesBelow((prev) => (prev === below ? prev : below));
    };
    const scrollDisposable = term.onScroll(updateLinesBelow);
    onHandleRef.current?.({
      copyBlock: (index) => blocks.getBlockText(index),
      scrollToBlock: (index) => blocks.scrollTo(index),
      navigateBlock: (dir) => blocks.navigate(dir),
      clearBlocks: () => blocks.clear(),
      clear: () => term.clear(),
      getSelection: () => term.getSelection(),
      focus: () => term.focus(),
      fit: () => fitIfSized(true),
      paste: (text) =>
        sendInput(wrapBracketedPaste(text, term.modes.bracketedPasteMode), text),
      dimensions: () => ({ cols: term.cols, rows: term.rows }),
    });

    const oscFilter = createOscStreamFilter((event) => {
      if (event.kind === "notification") onNotificationRef.current?.(event.body);
      else if (event.kind === "command") blocks.feedOSC133(event.phase, event.exitCode);
      else onClipboardRef.current?.(event.payload);
    });
    const channel = createBinaryChannel((bytes) => {
      const out = oscFilter.push(bytes);
      if (out.length > 0) term.write(out, updateLinesBelow);
    });
    const applyRemoteDimensions = (cols: number, rows: number) => {
      applyGrid({ cols, rows });
    };
    let attaching = false;
    const doAttach = async () => {
      if (disposed || kernelTabId || attaching) return;
      const resume = props.resumeTabId;
      const dimensions = coordinator.desiredGrid();
      if (!resume && !dimensions) return;
      attaching = true;
      try {
        let initialGrid = dimensions;
        let id: string;
        if (resume) {
          const info = await terminalApi.attachTab(resume, channel);
          id = info.tabId;
          if (disposed) {
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
          void terminalApi.detach(id, channelIdOf(channel)).catch(() => undefined);
          return;
        }
        coordinator.attach(id, initialGrid, false);
        kernelTabId = id;
        kernelTabIdRef.current = id;
        props.onAttach?.(id);
      } catch (e) {
        const code = (e as { code?: string } | null)?.code;
        if (code === "not_found") {
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

    const reattachOnReopen = () => {
      const id = kernelTabId;
      if (!id) return;
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
          if ((e as { code?: string } | null)?.code === "not_found") {
            onAttachFailedRef.current?.("not_found");
          }
        }
      })();
    };
    const offReopen = onChannelReopen(channel, reattachOnReopen);

    const attachTimer = window.setTimeout(() => {
      void doAttach();
    }, 0);

    const dataDisposable = term.onData((data) => sendInput(data));
    const titleDisposable = term.onTitleChange((t) => onTitleRef.current?.(t));
    const autoCopy = createSelectionAutoCopy({
      isEnabled: () => getInputPrefs().selectionAutoCopy,
      getSelection: () => term.getSelection(),
      onCopied: (text) => onSelectionCopyRef.current?.(text, null),
      onError: (error) => onSelectionCopyRef.current?.("", error),
    });
    const selectionDisposable = term.onSelectionChange(() => autoCopy.notifySelectionChanged());
    props.registerSearch?.({
      findNext: (t) => search.findNext(t),
      findPrevious: (t) => search.findPrevious(t),
    });

    let measureFrame: number | null = null;
    let finalFrame = false;
    let windowResizeTimer: number | null = null;
    let layoutWidth = window.innerWidth;
    let layoutHeight = window.innerHeight;
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
      if (window.innerWidth === layoutWidth && window.innerHeight === layoutHeight) return;
      layoutWidth = window.innerWidth;
      layoutHeight = window.innerHeight;
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
      scrollDisposable.dispose();
      titleDisposable.dispose();
      selectionDisposable.dispose();
      autoCopy.dispose();
      blocks.dispose();
      if (kernelTabId) {
        void terminalApi.detach(kernelTabId, channelIdOf(channel)).catch(() => undefined);
      }
      disposeChannel(channel);
      kernelTabIdRef.current = "";
      term.dispose();
      termRef.current = null;
    };
  }, [props.sessionId, props.containerId, props.resumeTabId]);

  useEffect(() => {
    const applyAppearance = () => {
      const term = termRef.current;
      if (!term) return;
      const prefs = getAppearancePrefs();
      if (term.options.fontSize !== prefs.terminalFontSize) {
        term.options.fontSize = prefs.terminalFontSize;
        fitIfSized(false, true);
      }
      term.options.theme = { ...xtermThemeFor(getResolvedTerminalTheme()), cursor: accentColor() };
    };
    return subscribeAppearancePrefs(applyAppearance);
  }, []);

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

  return (
    <div className="relative h-full w-full min-h-0">
      <div ref={hostRef} className="h-full w-full min-h-0" />
      {linesBelow > 0 && (
        <button
          type="button"
          className="nx-btn nx-btn-sm absolute bottom-1.5 right-3 z-10 border border-neutral-700/80 bg-neutral-800/95 text-neutral-100 shadow-lg"
          onClick={() => termRef.current?.scrollToBottom()}
        >
          <IconArrowDown size={12} />
          回到底部 · {linesBelow} 行
        </button>
      )}
    </div>
  );
}
