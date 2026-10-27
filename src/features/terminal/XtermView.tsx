import { useEffect, useRef, useState } from "react";
import { Terminal } from "@xterm/xterm";
import { attachLazyAddons, scheduleWebglRenderer, type WebglRenderer } from "./xtermAddons";
import { attachTerminalInput } from "./terminalInput/attachTerminalInput";
import { kittyKeyboardSwitch } from "./terminalInput/kittyKeyboard";
import { applyStickyModifiersToTyped } from "./terminalInput/stickyModifiers";
import "@xterm/xterm/css/xterm.css";
import "./terminalLinks.css";

import { channelIdOf, createBinaryChannel, disposeChannel, onChannelReopen, sendChannelInput } from "../../ipc/events";
import { WEB, clientId } from "../../ipc/env";
import { assetApi, call, dockerApi, terminalApi } from "../../ipc/commands";
import { describeError } from "../../ui/errorText";
import { IconArrowDown } from "../../ui/icons";
import { RESIZE_END_EVENT } from "../../ui/ResizeHandle";
import { useUi } from "../../app/store";
import {
  resetTerminalLinesBelow,
  setTerminalHistoryView,
  setTerminalLinesBelow,
  useTerminalLinesBelow,
} from "../../app/terminalViewStore";
import {
  getAppearancePrefs,
  getInputPrefs,
  getTerminalPrefs,
  subscribeTerminalPrefs,
  waitAccountPreferences,
  getResolvedTerminalTheme,
  subscribeAppearancePrefs,
} from "../../app/preferences";
import { TERMINAL_FONT_STACKS, xtermThemeFor } from "./xtermTheme";
import { CommandBlockManager, type CommandBlock } from "./commandBlocks";
import {
  createTerminalStreamWriter,
  STREAM_HIGH_WATER_BYTES,
  STREAM_LOW_WATER_BYTES,
  type TerminalStreamBacklog,
} from "./terminalStream";
import { terminalOutputScheduler, type ScheduledTerminalOutput } from "./outputScheduler";
import { createTerminalResponseLimiter, isAutomaticTerminalReply } from "./terminalResponses";
import { createUserInputGate } from "./userInputGate";
import { pruneSpans, splitByClass, type StreamClassSpan, type StreamMode, type StreamRun } from "./streamClasses";
import {
  attachLinkLongPress,
  createTerminalLinkController,
  openTerminalLink,
  probeTerminalLink,
  type TerminalLinkMenuRequest,
} from "./terminalLinks";
import { terminalLinkDeps } from "./terminalLinkActions";
import { measureTerminalGeometry, resizeTerminalToGridPreservingSelection } from "./terminalGeometry";
import { TerminalGridCoordinator, type TerminalGrid } from "./terminalGrid";
import { productionGridRuntime } from "./gridRuntimeAdapter";
import { createSelectionAutoCopy } from "./selectionAutoCopy";
import { confirmMultilinePaste, multilinePasteLines, quickCommandBytes, wrapBracketedPaste } from "./terminalPaste";
import { SelectionActionBar, isCoarsePointerDevice } from "./SelectionActionBar";
import { copyText } from "../../ui/clipboard";
import { pasteUploadFiles } from "./imagePaste";
import { attachTouchScroll } from "./touchScroll";
import { attachFollowerGestures } from "./followerGestures";
import { applyFollowerFont, type FollowerView } from "./followerFont";
import type { FollowerScale, FollowerZoom } from "./followerScale";

export { XTERM_DARK_THEME, XTERM_LIGHT_THEME, xtermThemeFor } from "./xtermTheme";

function accentColor(): string {
  const v = getComputedStyle(document.documentElement).getPropertyValue("--color-accent").trim();
  return v || "#516cd6";
}

// overflow: clip (not hidden) also forbids programmatic scrolling, so focusing the helper textarea cannot shift it.
const XTERM_HOST_STYLE = { overflow: "clip" } as const;

// A touch counts as a tap (focus the terminal) when it ends within this time and distance of where it started.
const TAP_MAX_MS = 500;
const TAP_SLOP_PX = 10;

// Repeated GPU context loss pins a pane to the DOM renderer; say so once per app run, not per pane.
let webglFallbackNoticeShown = false;
function showWebglFallbackNotice(): void {
  if (webglFallbackNoticeShown) return;
  webglFallbackNoticeShown = true;
  useUi
    .getState()
    .pushToast("info", "GPU 渲染多次失效，部分终端已改用兼容渲染。可在设置 > 终端 > GPU 加速中调整。");
}

function sameGrid(
  a: { cols: number; rows: number } | null,
  b: { cols: number; rows: number } | null,
): boolean {
  return a === b || (!!a && !!b && a.cols === b.cols && a.rows === b.rows);
}

export interface TerminalHandle {
  copyBlock: (index: number) => string;
  scrollToBlock: (index: number) => void;
  navigateBlock: (dir: "prev" | "next") => number | null;
  clearBlocks: () => void;
  clear: () => void;
  getSelection: () => string;
  focus: () => void;
  fit: () => boolean;
  /** Sends text as a paste (bracketed when the terminal enabled it), followed by Enter when submit is set. */
  paste: (text: string, submit?: boolean) => void;
  /** Whether the program in the terminal turned on bracketed paste (DECSET 2004), read live. */
  bracketedPasteMode: () => boolean;
  dimensions: () => { cols: number; rows: number };
  // The link target (URL or OSC 8 target) under the pointer, if any.
  hoveredLink: () => string | null;
  // Opens a link through the terminal link policy, without a modifier check.
  openLink: (uri: string) => void;
}

export type { TerminalLinkMenuRequest } from "./terminalLinks";

export interface XtermViewProps {
  sessionId: string;
  tabId: string;
  // The stable per-pane key the scrollback badge state is stored under. TerminalPane
  // passes its workspace storeTabId: the kernel tab id is unknown ("pending") until the
  // attach response, and that placeholder is shared by every attaching tab, so it must
  // never key store state. With a stable viewTabId there is no key switch at attach and
  // the current value carries over by construction. Falls back to tabId when absent.
  viewTabId?: string;
  winrm?: boolean;
  containerId?: string;
  resumeTabId?: string;
  forceRegular?: boolean;
  // requestRegular opens a fresh shell as a regular terminal (the durable helper was not ready when the tab opened);
  // unlike forceRegular it still resumes an existing attachment.
  requestRegular?: boolean;
  sessionConnected?: boolean;
  onAttachInfo?: (info: {
    tabId: string;
    sizeOwner: string | null;
    subscribers: number;
    viewers: number;
    exited: boolean;
    retainable?: boolean;
    durable?: boolean;
    fallbackNotice?: string;
    // durableRequested is set when this view created the shell: whether it asked for a durable terminal.
    durableRequested?: boolean;
    encoding?: string;
    title?: string;
    titleCustom?: boolean;
    titleChangedAt?: number;
    shell?: "powershell" | "cmd" | "posix" | "unknown";
  }) => void;
  canResize?: boolean;
  remoteGrid?: { cols: number; rows: number; revision: number };
  // While another device owns the PTY size (canResize false): show the whole grid or fit its rows only.
  followerZoom?: FollowerZoom;
  // How the PTY grid is scaled while another device owns its size; null while this view sizes the PTY.
  onFollowerScale?: (scale: FollowerScale | null) => void;
  onFollowerPinch?: (direction: "in" | "out") => void;
  visible?: boolean;
  onAttach?: (kernelTabId: string) => void;
  onAttachFailed?: (code: string) => void;
  registerSearch?: (api: { findNext: (t: string) => void; findPrevious: (t: string) => void }) => void;
  onData?: (data: string) => void;
  onBlocks?: (blocks: CommandBlock[]) => void;
  onHandle?: (h: TerminalHandle) => void;
  onTitle?: (title: string) => void;
  onClipboard?: (payload: string) => void;
  onStreamBacklog?: (backlog: TerminalStreamBacklog | null) => void;
  // Reports the output channel's ID, to match server events about it.
  onChannel?: (channelId: string | undefined) => void;
  onSelectionCopy?: (text: string, error: unknown | null) => void;
  onPasteFiles?: (files: File[], skipped: string[]) => void;
  onRequestPaste?: () => void;
  /**
   * A tap on the terminal of a touch screen. Returns true when the tap was handled (buffered input focuses its own
   * field); otherwise the terminal takes focus, which opens the soft keyboard.
   */
  onTouchTap?: () => boolean;
  onLinkMenu?: (request: TerminalLinkMenuRequest) => void;
}

// The scrollback badge subscribes to the per-tab view store with the narrow selector:
// a scroll burst on this tab re-renders only the badge, never the XtermView body above it.
function LinesBelowBadge({ tabId, onJump }: { tabId: string; onJump: () => void }) {
  const linesBelow = useTerminalLinesBelow(tabId);
  if (linesBelow <= 0) return null;
  return (
    <button
      type="button"
      className="nx-btn nx-btn-sm pointer-coarse:min-h-11 pointer-coarse:px-3 absolute bottom-1.5 right-3 z-10 border border-neutral-700/80 bg-neutral-800/95 text-neutral-100 shadow-lg"
      aria-label={`回到底部，下方还有 ${linesBelow} 行`}
      onClick={onJump}
    >
      <IconArrowDown size={12} />
      回到底部 · {linesBelow} 行
    </button>
  );
}

export function XtermView(props: XtermViewProps) {
  const { sessionId, containerId, resumeTabId } = props;
  const hostRef = useRef<HTMLDivElement>(null);
  const termRef = useRef<Terminal | null>(null);
  const gridRef = useRef<TerminalGridCoordinator | null>(null);
  const webglRef = useRef<WebglRenderer | null>(null);
  const outputRef = useRef<ScheduledTerminalOutput | null>(null);
  const onGeometryRef = useRef<(() => void) | null>(null);
  const kernelTabIdRef = useRef<string>("");
  const viewKey = props.viewTabId ?? props.tabId;
  const viewKeyRef = useRef(viewKey);
  viewKeyRef.current = viewKey;
  const [selectionBar, setSelectionBar] = useState<{ x: number; y: number } | null>(null);
  const reattachRef = useRef<(() => void) | null>(null);
  const previousSessionConnected = useRef(props.sessionConnected === true);
  const onBlocksRef = useRef(props.onBlocks);
  const onHandleRef = useRef(props.onHandle);
  const onAttachRef = useRef(props.onAttach);
  const onAttachInfoRef = useRef(props.onAttachInfo);
  const requestRegularRef = useRef(props.requestRegular === true);
  const onAttachFailedRef = useRef(props.onAttachFailed);
  const onDataRef = useRef(props.onData);
  const onTitleRef = useRef(props.onTitle);
  const onClipboardRef = useRef(props.onClipboard);
  const onStreamBacklogRef = useRef(props.onStreamBacklog);
  const onChannelRef = useRef(props.onChannel);
  const onSelectionCopyRef = useRef(props.onSelectionCopy);
  const onPasteFilesRef = useRef(props.onPasteFiles);
  const onRequestPasteRef = useRef(props.onRequestPaste);
  const onTouchTapRef = useRef(props.onTouchTap);
  const onLinkMenuRef = useRef(props.onLinkMenu);
  const registerSearchRef = useRef(props.registerSearch);
  // Mount-effect inputs that must not rebuild the terminal when they change:
  // visibility and resize updates flow through their dedicated effects, and
  // the WinRM mode is fixed for the tab's lifetime.
  const visibleRef = useRef(props.visible);
  const canResizeRef = useRef(props.canResize);
  const onFollowerScaleRef = useRef(props.onFollowerScale);
  const onFollowerPinchRef = useRef(props.onFollowerPinch);
  // The shown font differs from the user's only while another device owns the PTY size (followerFont.ts).
  const followerRef = useRef<FollowerView>({
    base: 0,
    following: false,
    zoom: "fit",
    scale: null,
    report: (scale) => onFollowerScaleRef.current?.(scale),
  });
  const winrmRef = useRef(props.winrm);
  const lastFitViewportRef = useRef<{ widthPx: number; heightPx: number } | null>(null);
  onBlocksRef.current = props.onBlocks;
  onHandleRef.current = props.onHandle;
  onAttachRef.current = props.onAttach;
  onAttachInfoRef.current = props.onAttachInfo;
  requestRegularRef.current = props.requestRegular === true;
  onAttachFailedRef.current = props.onAttachFailed;
  onDataRef.current = props.onData;
  onTitleRef.current = props.onTitle;
  onClipboardRef.current = props.onClipboard;
  onStreamBacklogRef.current = props.onStreamBacklog;
  onChannelRef.current = props.onChannel;
  onSelectionCopyRef.current = props.onSelectionCopy;
  onPasteFilesRef.current = props.onPasteFiles;
  onRequestPasteRef.current = props.onRequestPaste;
  onTouchTapRef.current = props.onTouchTap;
  onLinkMenuRef.current = props.onLinkMenu;
  registerSearchRef.current = props.registerSearch;
  visibleRef.current = props.visible;
  canResizeRef.current = props.canResize;
  followerRef.current.following = props.canResize === false;
  followerRef.current.zoom = props.followerZoom ?? "fit";
  onFollowerScaleRef.current = props.onFollowerScale;
  onFollowerPinchRef.current = props.onFollowerPinch;
  winrmRef.current = props.winrm;

  const fitIfSized = (final = false): boolean => {
    const host = hostRef.current;
    const term = termRef.current;
    const coordinator = gridRef.current;
    if (!host || !term || !coordinator) return false;
    const geometry = measureTerminalGeometry(term, host);
    if (!geometry) return false;
    // A soft keyboard only shrinks the visible height: keep the grid (no local or PTY resize) and let CSS bottom-align the
    // terminal above it. A width change (rotation) or a taller viewport still refits.
    const last = lastFitViewportRef.current;
    const keyboardOpen = document.documentElement.dataset.nxKeyboard === "open";
    if (keyboardOpen && last && last.widthPx === geometry.viewport.widthPx && geometry.viewport.heightPx < last.heightPx) {
      return false;
    }
    lastFitViewportRef.current = geometry.viewport;
    const snapshot = coordinator.snapshot();
    const localChanged = term.cols !== geometry.grid.cols || term.rows !== geometry.grid.rows;
    const remoteChanged =
      !sameGrid(snapshot.desired, geometry.grid) || !sameGrid(snapshot.committed, geometry.grid);
    coordinator.update(geometry.viewport, geometry.metrics);
    if (final) coordinator.flush();
    if (followerRef.current.following) applyFollowerFont(term, host, followerRef.current);
    onGeometryRef.current?.();
    return localChanged || remoteChanged;
  };

  useEffect(() => {
    if (!hostRef.current || termRef.current) return;
    const appearance = getAppearancePrefs();
    // A new terminal starts at the user's font; a follower view shrinks it again on its first fit.
    followerRef.current.base = appearance.terminalFontSize;
    followerRef.current.scale = null;
    hostRef.current.style.overflowX = "";
    const kittyKeyboard = kittyKeyboardSwitch();
    const linkHost = hostRef.current;
    // The host a remote session's shell reports through OSC 7 (file://host/cwd).
    // The first report is the login shell's; a nested ssh reporting another
    // host later must not redirect file links to the wrong machine. Local
    // sessions use the machine name from the backend instead.
    let sessionHost: string | null = null;
    const openLink = (uri: string) => {
      void openTerminalLink(uri, terminalLinkDeps(sessionId, containerId, () => sessionHost));
    };
    const links = createTerminalLinkController({
      tooltipHost: () => linkHost,
      cursorHost: linkHost,
      activation: () => getTerminalPrefs().linkActivation,
      mouseTracking: () => (termRef.current?.modes.mouseTrackingMode ?? "none") !== "none",
      open: openLink,
    });
    const term = new Terminal({
      scrollback: getTerminalPrefs().scrollback,
      allowProposedApi: true,
      scrollbar: { width: 10 },
      fontFamily: TERMINAL_FONT_STACKS[appearance.terminalFontFamily],
      fontSize: appearance.terminalFontSize,
      // Hidden panes do not blink: the blink timer would keep scheduling repaints.
      cursorBlink: visibleRef.current !== false,
      theme: { ...xtermThemeFor(getResolvedTerminalTheme(), appearance.terminalContrast), cursor: accentColor() },
      vtExtensions: { kittyKeyboard },
      linkHandler: links.linkHandler,
    });
    termRef.current = term;
    const osc7Disposable = term.parser.registerOscHandler(7, (data) => {
      if (sessionHost === null) {
        try {
          const url = new URL(data);
          if (url.protocol === "file:" && url.hostname) sessionHost = url.hostname;
        } catch {
          // Not a URL: nothing to learn.
        }
      }
      // Leave the sequence to any other handler.
      return false;
    });
    term.open(hostRef.current);
    // One-finger vertical swipes scroll like the wheel; a touch that stops a fling is not a tap.
    const touchScroll = attachTouchScroll(term, hostRef.current);
    const detachFollowerGestures = attachFollowerGestures(hostRef.current, {
      following: () => followerRef.current.following,
      panX: () => followerRef.current.scale?.panX === true,
      onPinch: (direction) => onFollowerPinchRef.current?.(direction),
    });
    // Keyboard, IME and app shortcuts share one precedence order (terminalInput/resolveKey).
    const detachTerminalInput = attachTerminalInput(term, hostRef.current, { kittyKeyboard });

    let kernelTabId = "";
    let disposed = false;
    const search = attachLazyAddons(
      term,
      { handler: links.webLinkHandler, options: links.webLinkOptions },
      () => disposed,
    );
    const webgl = scheduleWebglRenderer(term, () => disposed, {
      mode: getTerminalPrefs().gpuAcceleration,
      visible: visibleRef.current !== false,
      onFallback: showWebglFallbackNotice,
    });
    webglRef.current = webgl;
    const unsubscribeGpuPreference = subscribeTerminalPrefs(() => {
      const prefs = getTerminalPrefs();
      webgl.setMode(prefs.gpuAcceleration);
      if (term.options.scrollback !== prefs.scrollback) {
        term.options.scrollback = prefs.scrollback;
        scheduleUiFlush();
      }
    });
    const attachedAssetId = useUi.getState().sessions.find((session) => session.id === sessionId)?.assetId;
    const applyGrid = (grid: TerminalGrid) => {
      resizeTerminalToGridPreservingSelection(term, grid);
      if (followerRef.current.following) applyFollowerFont(term, hostRef.current, followerRef.current);
    };
    const coordinator = new TerminalGridCoordinator(
      productionGridRuntime,
      applyGrid,
      { visible: visibleRef.current !== false, canResize: canResizeRef.current !== false },
    );
    gridRef.current = coordinator;
    fitIfSized();

    const blocks = new CommandBlockManager(term, (list) => onBlocksRef.current?.(list));
    // Input goes over the channel socket when the server supports it (small ordered frames, bursts merged). The
    // channel is created later in this effect; it is only read when input is actually sent.
    const writeTabInput = (data: string) => {
      const tabId = kernelTabId;
      sendChannelInput(channel, tabId, new TextEncoder().encode(data), (bytes) =>
        terminalApi.write(tabId, bytes),
      );
    };
    const sendInput = (data: string, blockText = data) => {
      blocks.feedInput(blockText);
      if (!kernelTabId) return;
      const onData = onDataRef.current;
      if (onData) {
        onData(data);
      } else {
        writeTabInput(data);
      }
    };
    const updateLinesBelow = () => {
      const buffer = term.buffer.active;
      const below = Math.max(0, buffer.baseY - buffer.viewportY);
      // The root view store dedups unchanged values: only a real change notifies
      // the badge's precise selector.
      setTerminalLinesBelow(viewKeyRef.current, below);
      const normal = buffer.type === "normal";
      setTerminalHistoryView(viewKeyRef.current, {
        atTop: normal && buffer.viewportY === 0,
        // A full scrollback has trimmed lines after the view's first byte.
        trimmed: normal && buffer.length >= term.rows + (term.options.scrollback ?? 0),
      });
    };
    // Scroll and write-completion signals are high-frequency: they only mark
    // the UI dirty, and the buffer read plus store update runs once per
    // animation frame with the latest value. After disposal nothing may write
    // again: a late write-callback scheduling a flush must stay a no-op so no
    // stale value lands in the store for a dead tab.
    let uiFrame: number | null = null;
    let pendingTitle: string | null = null;
    let lastSentTitle: string | null = null;
    const flushUiUpdates = () => {
      if (disposed) return;
      updateLinesBelow();
      if (pendingTitle !== null && pendingTitle !== lastSentTitle) {
        lastSentTitle = pendingTitle;
        onTitleRef.current?.(pendingTitle);
      }
      pendingTitle = null;
    };
    const scheduleUiFlush = () => {
      if (disposed || uiFrame !== null) return;
      uiFrame = requestAnimationFrame(() => {
        uiFrame = null;
        flushUiUpdates();
      });
    };
    const scrollDisposable = term.onScroll(scheduleUiFlush);
    onHandleRef.current?.({
      copyBlock: (index) => blocks.getBlockText(index),
      scrollToBlock: (index) => blocks.scrollTo(index),
      navigateBlock: (dir) => blocks.navigate(dir),
      clearBlocks: () => blocks.clear(),
      clear: () => term.clear(),
      getSelection: () => term.getSelection(),
      focus: () => term.focus(),
      fit: () => fitIfSized(),
      paste: (text, submit = false) => {
        // Like typed input (xterm's scrollOnUserInput), text sent from the composer or key bar returns to the bottom.
        touchScroll.stop();
        term.scrollToBottom();
        sendInput(quickCommandBytes(text, submit, term.modes.bracketedPasteMode), submit ? `${text}\r` : text);
      },
      bracketedPasteMode: () => term.modes.bracketedPasteMode,
      dimensions: () => ({ cols: term.cols, rows: term.rows }),
      hoveredLink: () => links.hovered(),
      openLink,
    });

    let ephemeral: boolean | undefined;
    let attachmentId = 0;
    const reportAttachInfo = (info: {
      tabId: string;
      sizeOwner: string | null;
      subscribers: number;
      viewers: number;
      exited: boolean;
      ephemeral?: boolean;
      retainable?: boolean;
      durable?: boolean;
      fallbackNotice?: string;
      durableRequested?: boolean;
      encoding?: string;
      title?: string;
      titleCustom?: boolean;
      titleChangedAt?: number;
      shell?: "powershell" | "cmd" | "posix" | "unknown";
      attachmentId?: number;
    }) => {
      ephemeral = info.ephemeral;
      // Adopt the response's incarnation only when it is newer than the one
      // already learned from the ordered stream: a deferred stale response
      // must never override the current attachment identity.
      adoptAttachmentId(info.attachmentId);
      onAttachInfoRef.current?.(info);
    };
    // Producer backpressure: a paused writer asks the server to stop feeding
    // this channel (terminal_pause/resume) and the feed resumes when the
    // backlog drains — non-destructive for every session tab kind, including
    // ephemeral local/WinRM tabs whose PTY a detach would reap. Docker exec
    // streams pause through the existing hub backpressure: detaching routes
    // to DetachStream (the exec keeps running, output accumulates in the
    // bounded replay) and draining resumes via attachTab/ResumeExec.
    let pauseNeeded = false;
    let reattaching = false;
    let backpressureDetached = false;
    let backpressureDetach: Promise<void> | null = null;
    const reattachAfterDrain = () => {
      if (disposed || pauseNeeded || !backpressureDetached || backpressureDetach !== null) return;
      reattachOnReopen();
    };
    const detachForBackpressure = () => {
      if (disposed || !kernelTabId || backpressureDetached) return;
      backpressureDetached = true;
      const pending = terminalApi.detach(kernelTabId, channelIdOf(channel));
      backpressureDetach = pending;
      const settled = () => {
        if (disposed || backpressureDetach !== pending) return;
        backpressureDetach = null;
        // A failed RPC can still have detached on the server. Wait for its
        // result before replacing the subscription, including that case.
        reattachAfterDrain();
      };
      void pending.then(settled, settled);
    };
    let pauseRetryTimer: number | undefined;
    // The request generation of the current normal-pause chain: each pause
    // request carries a fresh generation, so a newer attempt invalidates the
    // previous chain's pending timer and callbacks — a successful pause ends
    // the chain and an old failure never continues it.
    let pauseGeneration = 0;
    // The episode generation the server issued for the current refusal
    // episode (from the pause or recover response), and the token the last
    // resume was sent for: the server rejects a resume carrying a previous
    // episode's token, and one episode needs at most one effective resume.
    let episodeToken = 0;
    let resumeSentToken = -1;
    // A token the server rejected as stale: the gate refreshes it
    // single-flight instead of resending a known-stale resume. The refresh
    // is one in-flight request at a time — a deferred refresh never piles
    // up behind another, and unmount or a new attachment cancels it.
    let staleToken = -1;
    let tokenRefreshInFlight = false;
    // Resume retry state, isolated by attachment and episode: the timer and
    // the consecutive attempt count for the current episode. An old
    // attempt's callbacks never touch a newer episode's dedup or timers.
    let resumeRetryTimer: number | undefined;
    let resumeAttempts = 0;
    const learnEpisodeToken = (next: number | undefined) => {
      if (next !== undefined && next > episodeToken) {
        episodeToken = next;
        resumeSentToken = -1;
        staleToken = -1;
        // A new episode is a fresh retry budget, and the old episode's
        // pending retry timer is cancelled: one timer, always for the
        // current episode.
        clearResumeRetry();
      }
      // The unified gate decides whether the learned token may resume now:
      // never while a recovery is in flight or pending, and only once the
      // accepted offset caught up to the recovery target.
      resumeIfReady();
    };
    const pauseUpstream = () => {
      if (!kernelTabId || backpressureDetached) return;
      if (containerId) {
        detachForBackpressure();
        return;
      }
      pauseWithRetry();
    };
    const pauseWithRetry = () => {
      if (disposed || !kernelTabId) return;
      const channelId = channelIdOf(channel);
      if (!channelId) return;
      // Each pause request carries a fresh generation: a newer attempt
      // invalidates the previous chain's pending timer and callbacks.
      pauseGeneration += 1;
      const firedGeneration = pauseGeneration;
      const firedAttachment = attachmentId;
      call("terminal_pause", { tabId: kernelTabId, channelId, clientId: clientId(), attachmentId: firedAttachment, acceptedBytes: acceptedOffsetNow() })
        .then((res) => {
          // The response belongs to the request that fired it: a stale
          // request (a newer one exists) or an old attachment never touches
          // the current chain or the new incarnation's episode space.
          if (disposed || firedAttachment !== attachmentId || firedGeneration !== pauseGeneration) return;
          // The current request succeeded: the chain ends here — cancel any
          // pending retry and learn the token.
          if (pauseRetryTimer !== undefined) {
            window.clearTimeout(pauseRetryTimer);
            pauseRetryTimer = undefined;
          }
          learnEpisodeToken((res as { episodeToken?: number } | null)?.episodeToken);
        })
        .catch(() => {
          // The pause could not be established yet: retry while the writer is
          // still over the high watermark — without it the producer keeps
          // advancing and the refused suffix can outgrow the recovery ring.
          // The failure belongs to the request that fired it: a stale
          // request never continues the chain, and one attachment keeps at
          // most one retry chain.
          if (disposed || firedAttachment !== attachmentId || firedGeneration !== pauseGeneration) return;
          if (!pauseNeeded) return;
          if (pauseRetryTimer !== undefined) {
            window.clearTimeout(pauseRetryTimer);
          }
          pauseRetryTimer = window.setTimeout(() => {
            pauseRetryTimer = undefined;
            if (disposed || firedAttachment !== attachmentId || firedGeneration !== pauseGeneration || !pauseNeeded) return;
            pauseWithRetry();
          }, 100);
        });
    };
    let lastRefuseToastAt = 0;
    // The server's flushed offset from the last recovery response: the
    // client resumes only once its accepted offset catches up to it.
    let recoverTarget: number | null = null;
    let recoverInFlight = false;
    let recoverPending = false;
    const acceptedOffsetNow = () =>
      streamAnchor ? streamAnchor.seq + Math.max(0, streamBytes - streamAnchor.control) : streamBytes;
    // Report the accepted offset through the ordered write path so the server
    // opens the recovery ring's retention floor without waiting for a
    // pause/recover RPC: a delayed RPC must never let the producer evict the
    // refused suffix first.
    let lastStreamAckOffset = 0;
    const sendStreamAck = () => {
      if (!kernelTabId || containerId) return;
      const channelId = channelIdOf(channel);
      if (!channelId) return;
      const offset = acceptedOffsetNow();
      if (offset === lastStreamAckOffset) return;
      lastStreamAckOffset = offset;
      void call("terminal_stream_ack", {
        tabId: kernelTabId,
        channelId,
        clientId: clientId(),
        attachmentId,
        acceptedBytes: offset,
      }).catch(() => undefined);
    };
    // Adopt a newer attachment incarnation learned from the attach response
    // or the stream anchor: the retention floor and the stream-control
    // commands are bound to the incarnation, so a new one must re-report the
    // current absolute accepted offset immediately instead of reusing the
    // previous incarnation's ACK state or waiting for the next 1 MiB
    // boundary. Attachments are allocated from a per-tab monotonic counter,
    // so a smaller id is a deferred stale response and is ignored.
    const adoptAttachmentId = (next: number | undefined) => {
      if (next === undefined || next <= attachmentId) return;
      attachmentId = next;
      lastStreamAckOffset = -1;
      // A new attachment is a new episode and recovery space: generations
      // restart, so the learned token, the resume dedup state, the stale
      // marker, any pending token refresh, any retry timer, and any
      // in-flight or pending recovery state for the previous attachment
      // must not carry over — a delayed callback from the old attachment is
      // dropped by its fired-attachment guard, never clearing the new one.
      episodeToken = 0;
      resumeSentToken = -1;
      staleToken = -1;
      tokenRefreshInFlight = false;
      recoverInFlight = false;
      recoverPending = false;
      recoverTarget = null;
      if (pauseRetryTimer !== undefined) {
        window.clearTimeout(pauseRetryTimer);
        pauseRetryTimer = undefined;
      }
      clearResumeRetry();
      sendStreamAck();
    };
    const resumeUpstream = () => {
      if (disposed || containerId || !kernelTabId) return;
      const channelId = channelIdOf(channel);
      if (!channelId) return;
      // A token-less resume never accomplishes anything — the server rejects
      // it while an episode is open — so it is skipped, not sent: the token
      // is learned from the pause or recover response, and the learn path
      // re-fires the resume through the gate once the writer is drained.
      if (episodeToken === 0) return;
      // One episode needs at most one effective resume: the sent marker is
      // recorded only when a resume actually fires, and a rejected resume
      // clears it so a later legitimate resume is never blocked.
      if (episodeToken === resumeSentToken) return;
      // The server rejected this token as stale: refresh it single-flight
      // instead of resending a known-stale resume.
      if (episodeToken === staleToken) {
        refreshEpisodeToken();
        return;
      }
      const firedAttachment = attachmentId;
      const firedToken = episodeToken;
      resumeSentToken = firedToken;
      void call("terminal_resume", { tabId: kernelTabId, channelId, clientId: clientId(), attachmentId: firedAttachment, episodeToken: firedToken })
        .then(() => {
          // The success belongs to the attachment and episode that fired it:
          // a late success from an old episode never clears the new
          // episode's retry budget or dedup state.
          if (firedAttachment !== attachmentId || firedToken !== episodeToken) return;
          clearResumeRetry();
        })
        .catch((e) => {
          // The outcome belongs to the attachment and episode that fired it:
          // never clear a newer episode's dedup state or retry budget.
          if (firedAttachment !== attachmentId) return;
          if (resumeSentToken === firedToken) resumeSentToken = -1;
          // The episode moved on while this attempt was in flight: its
          // failure is moot and must not schedule a retry.
          if (episodeToken !== firedToken) return;
          const code = (e as { code?: string } | null)?.code;
          if (code === "not_found" || code === "unsupported") {
            // No retry will help: surface the accurate terminal state.
            useUi.getState().pushToast("error", "终端输出流恢复失败，画面可能停滞，请重新连接");
            return;
          }
          if (code === "forbidden") {
            // The token is stale: refresh it single-flight — never resend a
            // known-stale resume, and never pile up refresh requests.
            staleToken = firedToken;
            refreshEpisodeToken();
            return;
          }
          scheduleResumeRetry();
        });
    };
    // The unified resume gate, the only path to resumeUpstream: a resume
    // fires only when no recovery is in flight or pending, the accepted
    // offset has caught up to the recovery target, the writer is drained,
    // and the episode token belongs to the current episode and has not been
    // resumed yet. A learned token, a drained writer, and a caught-up replay
    // all funnel through here — never around it.
    const resumeIfReady = () => {
      if (disposed || !kernelTabId) return;
      if (backpressureDetached || backpressureDetach !== null) {
        reattachAfterDrain();
        return;
      }
      if (containerId || reattaching) return;
      if (recoverInFlight || recoverPending) return;
      if (recoverTarget !== null && acceptedOffsetNow() < recoverTarget) return;
      if (pauseNeeded) return;
      recoverTarget = null;
      resumeUpstream();
    };
    const resumeRetryDelays = [100, 200, 400, 800, 1600];
    const clearResumeRetry = () => {
      if (resumeRetryTimer !== undefined) {
        window.clearTimeout(resumeRetryTimer);
        resumeRetryTimer = undefined;
      }
      resumeAttempts = 0;
    };
    const scheduleResumeRetry = () => {
      if (disposed || !kernelTabId) return;
      // One retry timer at any time: a new attempt always replaces the
      // previous chain instead of piling up, and the timer re-checks
      // disposal before re-entering the gate. Transient failures retry with
      // a bounded backoff; once it is exhausted the channel stays paused
      // until a retry lands, so the accurate state is surfaced instead of a
      // silent stall — recovery is the existing manual path (a transport
      // reopen re-attaches and unpauses) plus the slow retry, which keeps
      // trying while the view lives.
      if (resumeRetryTimer !== undefined) {
        window.clearTimeout(resumeRetryTimer);
        resumeRetryTimer = undefined;
      }
      const exhausted = resumeAttempts >= resumeRetryDelays.length;
      const delay = exhausted ? 5000 : resumeRetryDelays[resumeAttempts];
      resumeAttempts += 1;
      if (exhausted && resumeAttempts === resumeRetryDelays.length + 1) {
        useUi.getState().pushToast("error", "终端输出流恢复失败，画面可能停滞，正在尝试恢复");
      }
      resumeRetryTimer = window.setTimeout(() => {
        resumeRetryTimer = undefined;
        if (disposed) return;
        resumeIfReady();
      }, delay);
    };
    // The forbidden path's token refresh is single-flight: one in-flight
    // refresh at a time, bound to the attachment that started it. The gate
    // never fires a resume with a known-stale token while a refresh is
    // pending, and a failed refresh retries with the same bounded,
    // cancellable backoff — never by piling up concurrent requests.
    const refreshEpisodeToken = () => {
      if (disposed || !kernelTabId || tokenRefreshInFlight) return;
      const channelId = channelIdOf(channel);
      if (!channelId) return;
      const firedAttachment = attachmentId;
      tokenRefreshInFlight = true;
      call("terminal_pause", { tabId: kernelTabId, channelId, clientId: clientId(), attachmentId: firedAttachment, acceptedBytes: acceptedOffsetNow() })
        .then((res) => {
          // The callback belongs to the attachment that fired it: a delayed
          // response from an old attachment returns directly and never
          // clears the new attachment's in-flight marker — only the callback
          // that still owns the current attachment clears it.
          if (disposed || firedAttachment !== attachmentId) return;
          tokenRefreshInFlight = false;
          staleToken = -1;
          learnEpisodeToken((res as { episodeToken?: number } | null)?.episodeToken);
        })
        .catch(() => {
          if (disposed || firedAttachment !== attachmentId) return;
          tokenRefreshInFlight = false;
          // The refresh failed: bounded, cancellable backoff that re-enters
          // the gate — which re-attempts the refresh, never a stale resume.
          scheduleResumeRetry();
        });
    };
    const pumpRecover = () => {
      if (recoverInFlight || !kernelTabId || ephemeral !== true) return;
      const channelId = channelIdOf(channel);
      if (!channelId) return;
      const firedAttachment = attachmentId;
      recoverInFlight = true;
      void call("terminal_recover", {
        tabId: kernelTabId,
        channelId,
        clientId: clientId(),
        attachmentId: firedAttachment,
        acceptedBytes: acceptedOffsetNow(),
      })
        .then((res) => {
          // The response belongs to the attachment that fired it: a delayed
          // recover from an old attachment never clears the new attachment's
          // recovery state, never writes its flushed offset, and never feeds
          // its episode space.
          if (disposed || firedAttachment !== attachmentId) return;
          recoverInFlight = false;
          const payload = (res as { flushedOffset?: number; episodeToken?: number } | null) ?? null;
          recoverTarget = payload?.flushedOffset ?? null;
          learnEpisodeToken(payload?.episodeToken);
          if (recoverPending) {
            // A refusal arrived while this recover was in flight: serially
            // recover the newly refused suffix with the latest accepted
            // offset before any resume is considered.
            recoverPending = false;
            pumpRecover();
            return;
          }
          resumeIfReady();
        })
        .catch(() => {
          if (disposed || firedAttachment !== attachmentId) return;
          recoverInFlight = false;
          // The recovery could not redeliver the refused suffix whole (the
          // accepted offset fell out of the server's bounded recovery
          // window): the view is missing output and cannot silently
          // resync, so surface the accurate state instead of a false
          // "recovering" notice.
          useUi.getState().pushToast("error", "终端输出积压过长，早期输出无法恢复，画面可能不完整");
        });
    };
    const refuseStream = () => {
      if (kernelTabId && ephemeral === true) {
        // Ephemeral local/WinRM tabs: never detach (it would reap the PTY).
        // Redeliver the refused suffix from the tab's recovery ring instead;
        // the replay arrives with a synthetic anchor that ends the episode.
        // Report the accepted offset in absolute stream coordinates — the
        // last anchor's streamSeq plus the bytes accepted since it — so the
        // server's recovery ring replays exactly the refused suffix, not the
        // bytes a paused coalescer still holds for the resume flush.
        sendStreamAck();
        if (recoverInFlight) {
          // A recover is already in flight: record that another refusal
          // arrived so it serially re-recovers once the response lands.
          recoverPending = true;
        } else {
          recoverTarget = null;
          pumpRecover();
        }
      } else if (kernelTabId) {
        // Durable tabs: the resume anchor replays refused bytes in order.
        detachForBackpressure();
      }
      const nowMs = Date.now();
      if (nowMs - lastRefuseToastAt > 10_000) {
        lastRefuseToastAt = nowMs;
        const replayable = containerId || ephemeral !== true;
        useUi
          .getState()
          .pushToast(
            replayable ? "info" : "error",
            replayable
              ? "终端输出积压，正在重新同步，画面将自动恢复"
              : "终端输出积压，正在重新同步，画面将自动恢复",
          );
      }
    };
    // Output reaches xterm through the app-wide scheduler: a hidden pane's
    // bytes drain within the background budget, and the writer's completion
    // (watermarks, OSC events) still waits for xterm's own write callback.
    const output = terminalOutputScheduler.register(term, { visible: visibleRef.current !== false });
    outputRef.current = output;
    // The server classifies output (streamClasses.ts). Replayed output only
    // redraws this view: it never answers queries upstream, writes the
    // clipboard, prompts, or notifies. Answered output was already answered
    // by the server, so it gets no automatic replies. OSC 133 marks, titles,
    // cwd and modes are display state and always apply. parseMode is the
    // mode of the bytes xterm is parsing right now (set from write callbacks
    // in parse order); suppressed effects are dropped silently.
    let parseMode: StreamMode = "live";
    // With several devices on one tab, live clipboard writes apply only on
    // the device that most recently typed into it. Every anchor carries
    // whether this view is that device, and input-owner frames announce
    // later changes; both are applied through the writer in stream order,
    // so a clipboard write is judged by the state in effect where its bytes
    // arrived. Servers that never announce leave it active.
    let inputOwner = true;
    const streamWriter = createTerminalStreamWriter(
      output,
      (event, mode) => {
        if (event.kind === "command") blocks.feedOSC133(event.phase, event.exitCode);
        // OSC 9 is stripped from the screen; the backend reports it as an attention state.
        else if (mode === "replay" || event.kind === "notification") return;
        else if (inputOwner) onClipboardRef.current?.(event.payload);
      },
      {
        highWaterBytes: STREAM_HIGH_WATER_BYTES,
        lowWaterBytes: STREAM_LOW_WATER_BYTES,
        onPausedChange: (paused, stats) => {
          onStreamBacklogRef.current?.(
            paused
              ? {
                  paused: true,
                  pendingBytes: stats.pendingBytes,
                  oldestPendingAgeMs: stats.oldestPendingAgeMs,
                }
              : null,
          );
          if (paused) {
            pauseNeeded = true;
            sendStreamAck();
            pauseUpstream();
          } else {
            pauseNeeded = false;
            // The unified gate: resume only once the refused suffix is fully
            // accepted, no recovery is in flight or pending, the writer is
            // drained, and the episode token is current and unresent.
            resumeIfReady();
          }
        },
        onRefused: () => refuseStream(),
        onWritten: scheduleUiFlush,
        onReplayChange: (mode) => {
          parseMode = mode;
        },
      },
    );
    // Automatic query replies (DA/DSR/CPR reports and friends) are answered
    // by xterm itself through onData. They pass a byte-rate limiter so a query
    // flood from the host cannot flood the upstream channel; suppression is
    // logged for diagnostics, never shown to the user. Keystrokes and pastes
    // bypass the limiter.
    const responses = createTerminalResponseLimiter({
      send: (data) => {
        if (!kernelTabId) return;
        const onData = onDataRef.current;
        if (onData) {
          onData(data);
        } else {
          writeTabInput(data);
        }
      },
    });
    // Output stream sequence anchor: on every attach the server first delivers an anchor frame, after which this stream segment's bytes
    // map continuously to ring sequence streamSeq + max(0, bytes received - controlBytes). A reconnecting attach brings the
    // sequence and epoch back (fromSeq/fromEpoch), and the server resumes from the offset instead of clearing the screen for a full replay.
    // skipLeading handles in-flight frames that arrived between the reconnect decision and server processing: they were already rendered,
    // so the prefix the new stream resends from fromSeq must be skipped to avoid duplicate output.
    let streamAnchor: { epoch: string; seq: number; control: number } | null = null;
    let streamBytes = 0;
    let skipLeading = 0;
    // Output-class spans for the current anchored stream, by stream sequence.
    let streamClasses: StreamClassSpan[] = [];
    // Docker exec streams have no anchor: a resumed backlog is announced as
    // the next replayRemaining bytes.
    let replayRemaining = 0;
    // Splits accepted bytes at stream index `index` into runs by mode.
    const classifyRuns = (out: Uint8Array, index: number): StreamRun[] => {
      if (out.length === 0) return [];
      if (!streamAnchor) {
        // replayRemaining shrinks as runs are accepted, not here: refused
        // bytes come back through the resume.
        const replayed = Math.min(out.length, replayRemaining);
        const runs: StreamRun[] = [];
        if (replayed > 0) runs.push({ offset: 0, length: replayed, mode: "replay" });
        if (out.length > replayed) runs.push({ offset: replayed, length: out.length - replayed, mode: "live" });
        return runs;
      }
      // The clear-screen control bytes at the head carry no sequence.
      const control = Math.min(out.length, Math.max(0, streamAnchor.control - index));
      const runs: StreamRun[] = control > 0 ? [{ offset: 0, length: control, mode: "live" }] : [];
      const seq = streamAnchor.seq + Math.max(0, index + control - streamAnchor.control);
      for (const run of splitByClass(seq, out.length - control, streamClasses)) {
        runs.push({ ...run, offset: run.offset + control });
      }
      return runs;
    };
    let expiredNoticePending = false;
    const writeExpiredNotice = () => {
      streamWriter.writeLocalText("\x1b[33m[输出历史已过期，已刷新屏幕]\x1b[0m\n");
    };
    const channel = createBinaryChannel(
      (bytes) => {
        let out = bytes;
        let skipped = 0;
        if (skipLeading > 0) {
          if (out.length <= skipLeading) {
            skipped = out.length;
            skipLeading -= out.length;
            out = out.subarray(0, 0);
          } else {
            skipped = skipLeading;
            out = out.subarray(skipLeading);
            skipLeading = 0;
          }
        }
        // Each run of one output class goes to the writer separately so xterm
        // parses it with the right mode.
        let accepted = skipped;
        for (const run of classifyRuns(out, streamBytes + skipped)) {
          if (!streamWriter.pushBytes(out.subarray(run.offset, run.offset + run.length), run.mode)) {
            // Refused bytes are not counted in streamBytes: the resume anchor
            // must point at the last accepted byte so the replay resends them.
            // The refusal episode is sticky inside the writer until the next
            // attachment boundary, keeping the accepted prefix contiguous.
            streamBytes += accepted;
            return;
          }
          accepted += run.length;
          if (!streamAnchor && run.mode === "replay") replayRemaining -= run.length;
        }
        streamBytes += bytes.length;
        if (streamAnchor) streamClasses = pruneSpans(streamClasses, acceptedOffsetNow());
        if (recoverTarget !== null) resumeIfReady();
        // Keep the server's retention floor current with the accepted offset
        // through the ordered write path, without waiting for a pause/recover
        // RPC: report at most once per MiB accepted, in absolute stream
        // coordinates so a new anchor never stalls the periodic ACK.
        if (acceptedOffsetNow() - lastStreamAckOffset >= 1 << 20) {
          sendStreamAck();
        }
        if (expiredNoticePending && streamBytes >= (streamAnchor?.control ?? 0)) {
          expiredNoticePending = false;
          writeExpiredNotice();
        }
      },
      (control) => {
        // Every anchor opens a new attachment stream boundary: the refusal
        // episode ends here, not on the attach RPC response, so replay bytes
        // that arrive before the response is processed are still accepted.
        streamWriter.beginAttachment();
        streamWriter.mark(() => {
          inputOwner = control.inputPassive !== true;
        });
        // Every anchor carries the classes of all output from its sequence to
        // the latest fed byte, including bytes a paused coalescer still holds,
        // and later spans arrive as class frames: the list is replaced, so
        // spans from another stream (a fresh attach starts a new sequence
        // space without being marked expired) never leak into this one.
        streamClasses = [...(control.classes ?? [])];
        // A full replay reports where this view's durable history starts.
        // A resume is a non-expired anchor without control bytes (the server
        // sends the clear prelude only with a full replay); any other anchor
        // without a start leaves this view without durable history.
        const resumed = !control.expired && (control.controlBytes ?? 0) === 0;
        if (control.historyFrom !== undefined || control.expired || !resumed) {
          setTerminalHistoryView(viewKeyRef.current, { from: control.historyFrom ?? null });
        }
        if (control.expired) {
          term.clearSelection();
          streamWriter.resetStream();
          blocks.clear();
          // History is gone: write callbacks still queued from the previous
          // attachment must not dispatch their OSC events into the fresh
          // screen, so the stream moves to a new generation. A resumed
          // reattach (non-expired anchor) keeps its queued events: the server
          // does not resend the bytes they came from.
          responses.setGeneration(streamWriter.nextGeneration());
        }
        const previous = streamAnchor
          ? streamAnchor.seq + Math.max(0, streamBytes - streamAnchor.control)
          : 0;
        streamAnchor = {
          epoch: control.epoch,
          seq: control.streamSeq,
          control: control.controlBytes ?? 0,
        };
        streamBytes = 0;
        skipLeading = 0;
        if (!control.expired && streamAnchor.control === 0 && previous > control.streamSeq) {
          skipLeading = previous - control.streamSeq;
        }
        expiredNoticePending = control.expired === true;
        if (expiredNoticePending && streamAnchor.control === 0) {
          expiredNoticePending = false;
          writeExpiredNotice();
        }
        // The anchor carries the incarnation it was sent for: adopt it before
        // any replay bytes are accepted, so an ACK triggered by the replay
        // reports the new attachment even when the attach response has not
        // arrived yet.
        adoptAttachmentId(control.attachmentId);
      },
      (replay) => {
        replayRemaining = Math.max(0, replay.bytes);
      },
      (span) => {
        streamClasses.push(span);
      },
      (owner) => {
        streamWriter.mark(() => {
          inputOwner = owner.active;
        });
      },
    );
    onChannelRef.current?.(channelIdOf(channel));
    const applyRemoteDimensions = (cols: number, rows: number) => {
      applyGrid({ cols, rows });
    };
    let attaching = false;
    const doAttach = async () => {
      if (disposed || kernelTabId || attaching) return;
      const resume = props.forceRegular ? undefined : resumeTabId;
      const dimensions = coordinator.desiredGrid();
      if (!resume && !dimensions) return;
      attaching = true;
      if (!containerId) {
        useUi.getState().registerHelperOperationIntent(viewKey, resume ? "reattach" : "deploy", resume);
      }
      try {
        let initialGrid = dimensions;
        let id: string;
        let fallbackNotice: string | undefined;
        if (resume) {
          const info = await terminalApi.attachTab(resume, channel, -1, undefined, containerId ? undefined : sessionId);
          id = info.tabId;
          if (disposed) {
            void terminalApi.detach(id, channelIdOf(channel)).catch(() => undefined);
            return;
          }
          initialGrid = { cols: info.cols, rows: info.rows };
          applyRemoteDimensions(info.cols, info.rows);
          reportAttachInfo({
            tabId: info.tabId,
            sizeOwner: info.sizeOwner,
            subscribers: info.subscribers,
            viewers: info.viewers,
            exited: info.exited,
            encoding: info.encoding,
            title: info.title,
            titleCustom: info.titleCustom,
            titleChangedAt: info.titleChangedAt,
            shell: info.shell,
            retainable: info.retainable,
            durable: info.durable,
            ephemeral: info.ephemeral,
            attachmentId: info.attachmentId,
          });
        } else if (containerId) {
          if (!dimensions) return;
          id = await dockerApi.execAttach(
            sessionId,
            containerId,
            dimensions.cols,
            dimensions.rows,
            channel,
          );
          reportAttachInfo({
            tabId: id,
            sizeOwner: null,
            subscribers: 1,
            viewers: 1,
            exited: false,
            shell: "unknown",
            retainable: false,
          });
        } else if (winrmRef.current) {
          if (!dimensions) return;
          const info = await import("../../ipc/commands").then((m) =>
            m.sessionApi.openLineTab(sessionId, dimensions.cols, dimensions.rows, channel),
          );
          id = info.tabId;
          reportAttachInfo({
            tabId: info.tabId,
            sizeOwner: info.sizeOwner,
            subscribers: info.subscribers,
            viewers: info.viewers,
            exited: info.exited,
            shell: info.shell,
            retainable: info.retainable,
            durable: info.durable,
            attachmentId: info.attachmentId,
            // session_open_line_tab creates an Ephemeral tab: a cap refusal
            // must take the recovery path, never the ordinary detach that
            // reaps the line tab's process.
            ephemeral: true,
          });
        } else {
          if (!dimensions) return;
          let regular = props.forceRegular === true || requestRegularRef.current;
          const preferencesLoading = !regular && !WEB ? waitAccountPreferences() : null;
          if (preferencesLoading) {
            await preferencesLoading;
            if (disposed) return;
          }
          if (!regular && !WEB && getTerminalPrefs().defaultMode === "regular") {
            const current = useUi.getState().sessions.find((session) => session.id === sessionId);
            if (current?.kind === "ssh") {
              try {
                const asset = current.assetId ? await assetApi.get(current.assetId) : null;
                regular = asset?.options?.terminalMode !== "durable";
              } catch (error) {
                if (!disposed) useUi.getState().pushToast("error", `无法读取资产终端模式，本次按持久终端打开：${describeError(error)}`);
              }
            }
          }
          if (disposed) return;
          const info = regular
            ? await terminalApi.attach(sessionId, dimensions.cols, dimensions.rows, channel, false)
            : await terminalApi.attach(sessionId, dimensions.cols, dimensions.rows, channel);
          id = info.tabId;
          fallbackNotice = info.fallbackNotice;
          reportAttachInfo({
            tabId: info.tabId,
            sizeOwner: info.sizeOwner,
            subscribers: info.subscribers,
            viewers: info.viewers,
            exited: info.exited,
            encoding: info.encoding,
            title: info.title,
            titleCustom: info.titleCustom,
            titleChangedAt: info.titleChangedAt,
            shell: info.shell,
            retainable: info.retainable,
            durable: info.durable,
            fallbackNotice: info.fallbackNotice,
            durableRequested: !regular,
            ephemeral: info.ephemeral,
            attachmentId: info.attachmentId,
          });
        }
        if (disposed) {
          void terminalApi.detach(id, channelIdOf(channel)).catch(() => undefined);
          return;
        }
        if (containerId) {
          const current = useUi.getState().sessions.find((session) => session.id === sessionId);
          if ((attachedAssetId !== undefined && (!current || current.assetId !== attachedAssetId)) || (current && current.status !== "connected")) {
            void terminalApi.closeTab(id, "kill", sessionId).catch(() => undefined);
            onAttachFailedRef.current?.("interrupted");
            return;
          }
        }
        coordinator.attach(id, initialGrid, false);
        kernelTabId = id;
        kernelTabIdRef.current = id;
        // The attach adopted an attachment incarnation before the tab id was
        // known: deliver the re-report now so the new incarnation's retention
        // floor opens immediately, not at the next 1 MiB boundary.
        sendStreamAck();
        if (fallbackNotice) useUi.getState().pushToast("info", fallbackNotice);
        onAttachRef.current?.(id);
      } catch (e) {
        const code = (e as { code?: string } | null)?.code;
        if (code === "not_found") {
          if (!disposed) onAttachFailedRef.current?.(code);
          streamWriter.writeLocalText("\r\n\x1b[33m[连接已失效] 该终端所属的连接已在服务端删除\x1b[0m\n");
        } else {
          if (!disposed) onAttachFailedRef.current?.(code ?? "internal");
          streamWriter.writeLocalText(`\r\n\x1b[31m[连接失败] ${describeError(e)}\x1b[0m\n`);
        }
      } finally {
        attaching = false;
      }
    };
    onGeometryRef.current = () => void doAttach();

    const reattachOnReopen = () => {
      const id = kernelTabId;
      if (disposed || !id || reattaching) return;
      // Transport reopen and a drained FIFO share this entry point. An
      // outstanding detach must settle first, even if the transport has
      // already reopened, or it could remove the replacement subscription.
      if (backpressureDetach !== null || (backpressureDetached && pauseNeeded)) return;
      backpressureDetached = false;
      const resume = containerId
        ? undefined
        : streamAnchor
          ? {
              seq: streamAnchor.seq + Math.max(0, streamBytes - streamAnchor.control),
              epoch: streamAnchor.epoch,
            }
          : undefined;
      reattaching = true;
      void (async () => {
        try {
          if (containerId) {
            // Docker exec replays synchronously inside the attach RPC, before
            // its response: end the refusal episode up front so the replay is
            // accepted. Session tabs end the episode at the anchor frame
            // instead, which the server sends first. The resumed backlog is
            // announced in-band; nothing before it is replay.
            streamWriter.beginAttachment();
            replayRemaining = 0;
          }
          const info = resume
            ? await terminalApi.attachTab(id, channel, -1, resume, containerId ? undefined : sessionId)
            : await terminalApi.attachTab(id, channel, -1, undefined, containerId ? undefined : sessionId);
          if (disposed) {
            void terminalApi.detach(info.tabId, channelIdOf(channel)).catch(() => undefined);
            return;
          }
          kernelTabId = info.tabId;
          kernelTabIdRef.current = info.tabId;
          // Replay can overflow before this response arrives. Its refusal
          // ends only at the next stream boundary, never at the response.
          applyRemoteDimensions(info.cols, info.rows);
          coordinator.attach(info.tabId, { cols: info.cols, rows: info.rows }, true);
          reportAttachInfo({
            tabId: info.tabId,
            sizeOwner: info.sizeOwner,
            subscribers: info.subscribers,
            viewers: info.viewers,
            exited: info.exited,
            encoding: info.encoding,
            title: info.title,
            titleCustom: info.titleCustom,
            titleChangedAt: info.titleChangedAt,
            shell: info.shell,
            retainable: info.retainable,
            durable: info.durable,
            ephemeral: info.ephemeral,
            attachmentId: info.attachmentId,
          });
          onAttachRef.current?.(info.tabId);
        } catch (e) {
          if (disposed) return;
          if (containerId) {
            // Docker exec streams resume through attachTab/ResumeExec; when
            // that fails the stream is gone or completed, so surface the
            // accurate terminal state instead of a frozen view.
            kernelTabId = "";
            onAttachFailedRef.current?.("interrupted");
            return;
          }
          if ((e as { code?: string } | null)?.code === "not_found") {
            onAttachFailedRef.current?.("not_found");
          }
        } finally {
          reattaching = false;
          // Synchronous replay can fill the FIFO and detach again before
          // the attach response arrives. Its drain waits for this request
          // to finish instead of losing the next resubscription.
          reattachAfterDrain();
        }
      })();
    };
    reattachRef.current = reattachOnReopen;
    const offReopen = onChannelReopen(channel, reattachOnReopen);

    const attachTimer = window.setTimeout(() => {
      void doAttach();
    }, 0);

    // onData raised while a DOM input event is being dispatched comes from the
    // user (keys, paste, focus reports, mouse), never from parsing output,
    // even when it looks like a query reply (modified F3 matches CPR).
    const userInput = createUserInputGate(hostRef.current);
    const dataDisposable = term.onData((raw) => {
      // A Ctrl/Alt latched on the key bar applies to the next character typed into the focused terminal.
      const data = term.textarea && document.activeElement === term.textarea ? applyStickyModifiersToTyped(raw) : raw;
      // A reply xterm produced while parsing replayed output answers a query
      // the live shell is not asking now: drop it.
      if (parseMode !== "live" && !userInput.active() && isAutomaticTerminalReply(raw)) return;
      // Input in a visible pane switches hidden panes to the conservative drain tier.
      if (visibleRef.current !== false) terminalOutputScheduler.noteForegroundActivity();
      blocks.feedInput(data);
      responses.submit(data);
    });
    const titleDisposable = term.onTitleChange((t) => {
      pendingTitle = t;
      scheduleUiFlush();
    });
    const autoCopy = createSelectionAutoCopy({
      isEnabled: () => getInputPrefs().selectionAutoCopy,
      getSelection: () => term.getSelection(),
      onCopied: (text) => onSelectionCopyRef.current?.(text, null),
      onError: (error) => onSelectionCopyRef.current?.("", error),
    });
    // Clipboard images and image drag-drop: only pure image payloads are intercepted for the upload flow; mixed clipboards/drag-drops containing plain text
    // go through the text paste path, so image bytes never become key input. Multi-line text paste asks for confirmation first; single lines go back to default handling.
    const pasteHost = hostRef.current;
    let lastTouchX = 0;
    let lastTouchY = 0;
    let touchSeen = false;
    // Track finger position at the capture phase: xterm's touch selection is handled at the target phase, so coordinates are available even without bubbling.
    const trackTouch = (event: TouchEvent) => {
      const touch = event.touches[0] ?? event.changedTouches[0];
      if (!touch) return;
      lastTouchX = touch.clientX;
      lastTouchY = touch.clientY;
      touchSeen = true;
    };
    pasteHost.addEventListener("touchstart", trackTouch, true);
    pasteHost.addEventListener("touchmove", trackTouch, true);
    // xterm cancels the touch events on its screen (for touch scrolling), so a tap never becomes the mousedown that
    // focuses it and the soft keyboard stays closed. A short, still, single-finger tap focuses the terminal here,
    // inside the touchend that iOS and Android accept as the gesture opening the keyboard.
    let tap: { x: number | null; y: number | null; at: number; valid: boolean } | null = null;
    const touchPoint = (event: TouchEvent) => event.touches[0] ?? event.changedTouches[0];
    const onTapStart = (event: TouchEvent) => {
      const touch = touchPoint(event);
      tap = { x: touch?.clientX ?? null, y: touch?.clientY ?? null, at: performance.now(), valid: event.touches.length <= 1 };
    };
    const tapMoved = (start: NonNullable<typeof tap>, touch: Touch | undefined) =>
      !!touch && start.x !== null && start.y !== null &&
      Math.hypot(touch.clientX - start.x, touch.clientY - start.y) > TAP_SLOP_PX;
    const onTapMove = (event: TouchEvent) => {
      if (tap && (event.touches.length > 1 || tapMoved(tap, touchPoint(event)))) tap.valid = false;
    };
    const onTapEnd = (event: TouchEvent) => {
      const started = tap;
      tap = null;
      if (!started?.valid || event.touches.length > 0 || touchScroll.stoppedFling()) return;
      if (performance.now() - started.at > TAP_MAX_MS || tapMoved(started, event.changedTouches[0])) return;
      if (term.getSelection()) return;
      if (onTouchTapRef.current?.()) return;
      term.focus();
    };
    const onTapCancel = () => {
      tap = null;
    };
    pasteHost.addEventListener("touchstart", onTapStart, true);
    pasteHost.addEventListener("touchmove", onTapMove, true);
    pasteHost.addEventListener("touchend", onTapEnd, true);
    pasteHost.addEventListener("touchcancel", onTapCancel, true);
    // Touch has no modifier keys: a long press on a link offers it in the
    // terminal menu instead. xterm's link detection runs on mouse movement, so
    // a mousemove at the touch point asks it for the link under the finger.
    const detachLinkLongPress = attachLinkLongPress(pasteHost, {
      probe: (x, y) => probeTerminalLink(term.element?.querySelector(".xterm-screen"), links, x, y),
      onLinkMenu: (request) => onLinkMenuRef.current?.(request),
    });
    const updateSelectionBar = () => {
      if (!isCoarsePointerDevice() || !term.getSelection()) {
        touchSeen = false;
        setSelectionBar(null);
        return;
      }
      const rect = pasteHost.getBoundingClientRect();
      setSelectionBar({
        x: touchSeen ? lastTouchX : rect.left + rect.width / 2,
        y: touchSeen ? lastTouchY : rect.top + Math.min(96, rect.height / 3),
      });
    };
    const selectionDisposable = term.onSelectionChange(() => {
      autoCopy.notifySelectionChanged();
      updateSelectionBar();
    });
    const onPasteCapture = (event: ClipboardEvent) => {
      const { files, skipped } = pasteUploadFiles(event.clipboardData);
      if (files.length > 0 || skipped.length > 0) {
        event.preventDefault();
        event.stopPropagation();
        onPasteFilesRef.current?.(files, skipped);
        return;
      }
      const text = event.clipboardData?.getData("text/plain") ?? "";
      if (multilinePasteLines(text) === null) return;
      event.preventDefault();
      event.stopPropagation();
      void confirmMultilinePaste(text, viewKeyRef.current, term.modes.bracketedPasteMode).then((ok) => {
        if (ok) sendInput(wrapBracketedPaste(text, term.modes.bracketedPasteMode), text);
      });
    };
    const onDragOver = (event: DragEvent) => {
      if (!event.dataTransfer?.types.includes("Files")) return;
      event.preventDefault();
    };
    const onDropCapture = (event: DragEvent) => {
      const { files, skipped } = pasteUploadFiles(event.dataTransfer);
      if (files.length === 0 && skipped.length === 0) return;
      event.preventDefault();
      event.stopPropagation();
      onPasteFilesRef.current?.(files, skipped);
    };
    pasteHost.addEventListener("paste", onPasteCapture, true);
    pasteHost.addEventListener("dragover", onDragOver);
    pasteHost.addEventListener("drop", onDropCapture, true);
    registerSearchRef.current?.({
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
        fitIfSized(accurate);
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
      pauseNeeded = false;
      if (pauseRetryTimer !== undefined) {
        window.clearTimeout(pauseRetryTimer);
        pauseRetryTimer = undefined;
      }
      clearResumeRetry();
      coordinator.close();
      gridRef.current = null;
      onGeometryRef.current = null;
      if (reattachRef.current === reattachOnReopen) reattachRef.current = null;
      offReopen();
      window.clearTimeout(attachTimer);
      if (windowResizeTimer !== null) window.clearTimeout(windowResizeTimer);
      if (measureFrame !== null) cancelAnimationFrame(measureFrame);
      if (uiFrame !== null) cancelAnimationFrame(uiFrame);
      responses.dispose();
      window.removeEventListener(RESIZE_END_EVENT, onFinalResize);
      window.removeEventListener("resize", onWindowResize);
      window.visualViewport?.removeEventListener("resize", onWindowResize);
      ro.disconnect();
      pasteHost.removeEventListener("paste", onPasteCapture, true);
      pasteHost.removeEventListener("dragover", onDragOver);
      pasteHost.removeEventListener("drop", onDropCapture, true);
      userInput.dispose();
      pasteHost.removeEventListener("touchstart", trackTouch, true);
      pasteHost.removeEventListener("touchmove", trackTouch, true);
      pasteHost.removeEventListener("touchstart", onTapStart, true);
      pasteHost.removeEventListener("touchmove", onTapMove, true);
      pasteHost.removeEventListener("touchend", onTapEnd, true);
      pasteHost.removeEventListener("touchcancel", onTapCancel, true);
      detachLinkLongPress();
      touchScroll.dispose();
      detachFollowerGestures();
      osc7Disposable.dispose();
      links.dispose();
      dataDisposable.dispose();
      detachTerminalInput();
      scrollDisposable.dispose();
      titleDisposable.dispose();
      selectionDisposable.dispose();
      autoCopy.dispose();
      blocks.dispose();
      if (kernelTabId) {
        void terminalApi.detach(kernelTabId, channelIdOf(channel)).catch(() => undefined);
      }
      disposeChannel(channel);
      onChannelRef.current?.(undefined);
      kernelTabIdRef.current = "";
      unsubscribeGpuPreference();
      webgl.cancel();
      webglRef.current = null;
      output.dispose();
      outputRef.current = null;
      term.dispose();
      termRef.current = null;
    };
  }, [sessionId, containerId, resumeTabId, props.forceRegular, viewKey]);

  useEffect(() => {
    const connected = props.sessionConnected === true;
    const wasConnected = previousSessionConnected.current;
    previousSessionConnected.current = connected;
    if (!wasConnected && connected) reattachRef.current?.();
  }, [props.sessionConnected]);

  // The scrollback badge lives in the root view store: drop this pane's entry when the
  // view unmounts or its view key changes, so a closed tab never leaks a stale value.
  // Only linesBelow is cleared here — the tab's OSC title outlives a terminal remount.
  useEffect(() => () => resetTerminalLinesBelow(viewKey), [viewKey]);

  useEffect(() => {
    const applyAppearance = () => {
      const term = termRef.current;
      if (!term) return;
      const prefs = getAppearancePrefs();
      const fontFamily = TERMINAL_FONT_STACKS[prefs.terminalFontFamily];
      const fontMetricsChanged = followerRef.current.base !== prefs.terminalFontSize || term.options.fontFamily !== fontFamily;
      followerRef.current.base = prefs.terminalFontSize;
      // While another device owns the PTY size, fitIfSized derives the shown size from the new font.
      if (canResizeRef.current !== false) term.options.fontSize = prefs.terminalFontSize;
      term.options.fontFamily = fontFamily;
      if (fontMetricsChanged) fitIfSized(true);
      term.options.theme = { ...xtermThemeFor(getResolvedTerminalTheme(), prefs.terminalContrast), cursor: accentColor() };
    };
    return subscribeAppearancePrefs(applyAppearance);
  }, []);

  useEffect(() => {
    const visible = props.visible !== false;
    gridRef.current?.setVisible(visible);
    webglRef.current?.setVisible(visible);
    outputRef.current?.setVisible(visible);
    const term = termRef.current;
    if (term && term.options.cursorBlink !== visible) term.options.cursorBlink = visible;
    const tabId = kernelTabIdRef.current;
    if (tabId) void terminalApi.setVisible(tabId, visible).catch(() => undefined);
    if (!visible) return;
    const raf = requestAnimationFrame(() => fitIfSized());
    return () => cancelAnimationFrame(raf);
  }, [props.visible, props.tabId]);

  useEffect(() => {
    const canResize = props.canResize !== false;
    const coordinator = gridRef.current;
    if (canResize && coordinator && !coordinator.snapshot().canResize) {
      // Taking the size back: measure at the user's font before the coordinator sends this view's grid, so it is
      // not the grid of the shrunken font. With the soft keyboard open the last full height still counts.
      const host = hostRef.current;
      const term = termRef.current;
      applyFollowerFont(term, host, followerRef.current);
      const geometry = host && term ? measureTerminalGeometry(term, host) : null;
      if (geometry) {
        const keyboardOpen = document.documentElement.dataset.nxKeyboard === "open";
        const last = lastFitViewportRef.current;
        const viewport = keyboardOpen && last && last.widthPx === geometry.viewport.widthPx ? last : geometry.viewport;
        coordinator.update(viewport, geometry.metrics);
      }
    }
    coordinator?.setCanResize(canResize);
    if (!canResize) applyFollowerFont(termRef.current, hostRef.current, followerRef.current);
  }, [props.canResize]);

  useEffect(() => {
    if (followerRef.current.following) applyFollowerFont(termRef.current, hostRef.current, followerRef.current);
  }, [props.followerZoom]);

  useEffect(() => {
    const remote = props.remoteGrid;
    if (remote) gridRef.current?.observe(remote.revision, remote);
  }, [props.remoteGrid]);

  const copySelectionViaBar = () => {
    const term = termRef.current;
    if (!term) return;
    const text = term.getSelection();
    if (!text) return;
    copyText(text).then(
      () => {
        term.clearSelection();
        onSelectionCopyRef.current?.(text, null);
      },
      (error: unknown) => {
        onSelectionCopyRef.current?.("", error);
      },
    );
  };

  const pasteViaBar = () => {
    onRequestPasteRef.current?.();
    const term = termRef.current;
    term?.clearSelection();
    term?.focus();
  };

  const scrollToBottom = () => termRef.current?.scrollToBottom();

  return (
    <div className="relative h-full w-full min-h-0">
      {/* The IME composition view and helper textarea follow the cursor and can extend past the right edge; clipping
          keeps them from scrolling an ancestor and shifting the terminal sideways. */}
      <div ref={hostRef} className="nx-xterm-host relative h-full w-full min-h-0" style={XTERM_HOST_STYLE} />
      {selectionBar && (
        <SelectionActionBar
          x={selectionBar.x}
          y={selectionBar.y}
          onCopy={copySelectionViaBar}
          onPaste={pasteViaBar}
          onSelectAll={() => termRef.current?.selectAll()}
        />
      )}
      <LinesBelowBadge tabId={viewKey} onJump={scrollToBottom} />
    </div>
  );
}
