import { useEffect, useRef, useState } from "react";
import { Terminal } from "@xterm/xterm";
import { WebglAddon } from "@xterm/addon-webgl";
import { SearchAddon } from "@xterm/addon-search";
import { WebLinksAddon } from "@xterm/addon-web-links";
import "@xterm/xterm/css/xterm.css";

import { channelIdOf, createBinaryChannel, disposeChannel, onChannelReopen } from "../../ipc/events";
import { WEB, clientId } from "../../ipc/env";
import { openExternalURL } from "../../ipc/wails";
import { call, dockerApi, terminalApi } from "../../ipc/commands";
import { describeError } from "../../ui/errorText";
import { IconArrowDown } from "../../ui/icons";
import { RESIZE_END_EVENT } from "../../ui/ResizeHandle";
import { matchAppKeybinding } from "../../app/keybindings";
import { useUi } from "../../app/store";
import {
  getAppearancePrefs,
  getInputPrefs,
  getResolvedTerminalTheme,
  subscribeAppearancePrefs,
} from "../../app/preferences";
import { xtermThemeFor } from "./xtermTheme";
import { CommandBlockManager, type CommandBlock } from "./commandBlocks";
import {
  createTerminalStreamWriter,
  STREAM_HIGH_WATER_BYTES,
  STREAM_LOW_WATER_BYTES,
  type TerminalStreamBacklog,
} from "./terminalStream";
import { createTerminalResponseCoalescer } from "./terminalResponses";
import { measureTerminalGeometry, resizeTerminalToGridPreservingSelection } from "./terminalGeometry";
import { TerminalGridCoordinator, type TerminalGrid } from "./terminalGrid";
import { productionGridRuntime } from "./gridRuntimeAdapter";
import { createSelectionAutoCopy } from "./selectionAutoCopy";
import { confirmMultilinePaste, multilinePasteLines, wrapBracketedPaste } from "./terminalPaste";
import { SelectionActionBar, isCoarsePointerDevice } from "./SelectionActionBar";
import { copyText } from "../../ui/clipboard";
import { clipboardHasPlainText, pureImageFiles } from "./imagePaste";

export { XTERM_DARK_THEME, XTERM_LIGHT_THEME, xtermThemeFor } from "./xtermTheme";

function accentColor(): string {
  const v = getComputedStyle(document.documentElement).getPropertyValue("--color-accent").trim();
  return v || "#516cd6";
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
  paste: (text: string) => void;
  dimensions: () => { cols: number; rows: number };
}

export interface XtermViewProps {
  sessionId: string;
  tabId: string;
  winrm?: boolean;
  containerId?: string;
  resumeTabId?: string;
  sessionConnected?: boolean;
  onAttachInfo?: (info: {
    tabId: string;
    controller: string | null;
    subscribers: number;
    viewers: number;
    exited: boolean;
    retainable?: boolean;
    encoding?: string;
    title?: string;
    shell?: "powershell" | "cmd" | "posix" | "unknown";
  }) => void;
  canResize?: boolean;
  remoteGrid?: { cols: number; rows: number; revision: number };
  visible?: boolean;
  onAttach?: (kernelTabId: string) => void;
  onAttachFailed?: (code: string) => void;
  registerSearch?: (api: { findNext: (t: string) => void; findPrevious: (t: string) => void }) => void;
  onData?: (data: string) => void;
  onBlocks?: (blocks: CommandBlock[]) => void;
  onHandle?: (h: TerminalHandle) => void;
  onTitle?: (title: string) => void;
  onNotification?: (body: string) => void;
  onClipboard?: (payload: string) => void;
  onStreamBacklog?: (backlog: TerminalStreamBacklog | null) => void;
  onSelectionCopy?: (text: string, error: unknown | null) => void;
  onPasteImages?: (files: File[]) => void;
  onRequestPaste?: () => void;
}

export function XtermView(props: XtermViewProps) {
  const { sessionId, containerId, resumeTabId } = props;
  const hostRef = useRef<HTMLDivElement>(null);
  const termRef = useRef<Terminal | null>(null);
  const gridRef = useRef<TerminalGridCoordinator | null>(null);
  const onGeometryRef = useRef<(() => void) | null>(null);
  const kernelTabIdRef = useRef<string>("");
  const [linesBelow, setLinesBelow] = useState(0);
  const [selectionBar, setSelectionBar] = useState<{ x: number; y: number } | null>(null);
  const reattachRef = useRef<(() => void) | null>(null);
  const previousSessionConnected = useRef(props.sessionConnected === true);
  const onBlocksRef = useRef(props.onBlocks);
  const onHandleRef = useRef(props.onHandle);
  const onAttachRef = useRef(props.onAttach);
  const onAttachInfoRef = useRef(props.onAttachInfo);
  const onAttachFailedRef = useRef(props.onAttachFailed);
  const onDataRef = useRef(props.onData);
  const onTitleRef = useRef(props.onTitle);
  const onNotificationRef = useRef(props.onNotification);
  const onClipboardRef = useRef(props.onClipboard);
  const onStreamBacklogRef = useRef(props.onStreamBacklog);
  const onSelectionCopyRef = useRef(props.onSelectionCopy);
  const onPasteImagesRef = useRef(props.onPasteImages);
  const onRequestPasteRef = useRef(props.onRequestPaste);
  const registerSearchRef = useRef(props.registerSearch);
  // Mount-effect inputs that must not rebuild the terminal when they change:
  // visibility and resize updates flow through their dedicated effects, and
  // the WinRM mode is fixed for the tab's lifetime.
  const visibleRef = useRef(props.visible);
  const canResizeRef = useRef(props.canResize);
  const winrmRef = useRef(props.winrm);
  onBlocksRef.current = props.onBlocks;
  onHandleRef.current = props.onHandle;
  onAttachRef.current = props.onAttach;
  onAttachInfoRef.current = props.onAttachInfo;
  onAttachFailedRef.current = props.onAttachFailed;
  onDataRef.current = props.onData;
  onTitleRef.current = props.onTitle;
  onNotificationRef.current = props.onNotification;
  onClipboardRef.current = props.onClipboard;
  onStreamBacklogRef.current = props.onStreamBacklog;
  onSelectionCopyRef.current = props.onSelectionCopy;
  onPasteImagesRef.current = props.onPasteImages;
  onRequestPasteRef.current = props.onRequestPaste;
  registerSearchRef.current = props.registerSearch;
  visibleRef.current = props.visible;
  canResizeRef.current = props.canResize;
  winrmRef.current = props.winrm;

  const fitIfSized = (afterClaim = false, final = false): boolean => {
    const host = hostRef.current;
    const term = termRef.current;
    const coordinator = gridRef.current;
    if (!host || !term || !coordinator) return false;
    if (afterClaim) coordinator.setCanResize(true);
    const geometry = measureTerminalGeometry(term, host);
    if (!geometry) return false;
    const snapshot = coordinator.snapshot();
    const localChanged = term.cols !== geometry.grid.cols || term.rows !== geometry.grid.rows;
    const remoteChanged =
      !sameGrid(snapshot.desired, geometry.grid) || !sameGrid(snapshot.committed, geometry.grid);
    coordinator.update(geometry.viewport, geometry.metrics);
    if (final) coordinator.flush();
    onGeometryRef.current?.();
    return localChanged || remoteChanged;
  };

  useEffect(() => {
    if (!hostRef.current || termRef.current) return;
    const term = new Terminal({
      scrollback: 100_000,
      allowProposedApi: true,
      overviewRuler: { width: 10 },
      fontFamily: "'Cascadia Mono', 'Cascadia Code', Consolas, 'Courier New', monospace",
      fontSize: getAppearancePrefs().terminalFontSize,
      cursorBlink: true,
      theme: { ...xtermThemeFor(getResolvedTerminalTheme()), cursor: accentColor() },
    });
    const search = new SearchAddon();
    term.loadAddon(search);
    term.loadAddon(
      new WebLinksAddon((event, uri) => {
        event.preventDefault();
        if (!/^https?:\/\//i.test(uri)) return;
        if (WEB) {
          window.open(uri, "_blank", "noopener,noreferrer");
        } else {
          void openExternalURL(uri);
        }
      }),
    );
    termRef.current = term;
    term.open(hostRef.current);
    // xterm always preventDefault + stopPropagation on keys it handles, so window-level listeners
    // receive no keys while the terminal is focused; keys matching app-level keybindings are let through here,
    // bubbling to window for App to handle.
    term.attachCustomKeyEventHandler((event) => {
      if (event.repeat || event.isComposing || event.keyCode === 229) return true;
      return matchAppKeybinding(event) === null;
    });
    try {
      term.loadAddon(new WebglAddon());
    } catch {
    }

    let kernelTabId = "";
    let disposed = false;
    const attachedAssetId = useUi.getState().sessions.find((session) => session.id === sessionId)?.assetId;
    const applyGrid = (grid: TerminalGrid) => resizeTerminalToGridPreservingSelection(term, grid);
    const coordinator = new TerminalGridCoordinator(
      productionGridRuntime,
      applyGrid,
      { visible: visibleRef.current !== false, canResize: canResizeRef.current !== false },
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
    // Scroll and write-completion signals are high-frequency: they only mark
    // the UI dirty, and the buffer read plus state update runs once per
    // animation frame with the latest value.
    let uiFrame: number | null = null;
    let pendingTitle: string | null = null;
    let lastSentTitle: string | null = null;
    const flushUiUpdates = () => {
      updateLinesBelow();
      if (pendingTitle !== null && pendingTitle !== lastSentTitle) {
        lastSentTitle = pendingTitle;
        onTitleRef.current?.(pendingTitle);
      }
      pendingTitle = null;
    };
    const scheduleUiFlush = () => {
      if (uiFrame !== null) return;
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
      fit: () => fitIfSized(true),
      paste: (text) =>
        sendInput(wrapBracketedPaste(text, term.modes.bracketedPasteMode), text),
      dimensions: () => ({ cols: term.cols, rows: term.rows }),
    });

    let ephemeral: boolean | undefined;
    let attachmentId = 0;
    const reportAttachInfo = (info: {
      tabId: string;
      controller: string | null;
      subscribers: number;
      viewers: number;
      exited: boolean;
      ephemeral?: boolean;
      retainable?: boolean;
      encoding?: string;
      title?: string;
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
    // bounded replay) and the reopen resumes via attachTab/ResumeExec.
    let pauseNeeded = false;
    let pauseRetryTimer: number | undefined;
    const pauseUpstream = () => {
      if (!kernelTabId) return;
      if (containerId) {
        void terminalApi.detach(kernelTabId, channelIdOf(channel)).catch(() => undefined);
        return;
      }
      pauseWithRetry();
    };
    const pauseWithRetry = () => {
      if (disposed || !kernelTabId) return;
      const channelId = channelIdOf(channel);
      if (!channelId) return;
      call("terminal_pause", { tabId: kernelTabId, channelId, clientId: clientId(), attachmentId, acceptedBytes: acceptedOffsetNow() }).catch(() => {
        // The pause could not be established yet: retry while the writer is
        // still over the high watermark — without it the producer keeps
        // advancing and the refused suffix can outgrow the recovery ring.
        if (pauseNeeded && !disposed) {
          pauseRetryTimer = window.setTimeout(() => pauseWithRetry(), 100);
        }
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
      sendStreamAck();
    };
    const resumeUpstream = () => {
      if (containerId || !kernelTabId) return;
      const channelId = channelIdOf(channel);
      if (!channelId) return;
      void call("terminal_resume", { tabId: kernelTabId, channelId, clientId: clientId(), attachmentId }).catch(() => undefined);
    };
    const maybeResume = () => {
      if (recoverInFlight || recoverPending) return;
      if (recoverTarget !== null && acceptedOffsetNow() >= recoverTarget) {
        recoverTarget = null;
        resumeUpstream();
      }
    };
    const pumpRecover = () => {
      if (recoverInFlight || !kernelTabId || ephemeral !== true) return;
      const channelId = channelIdOf(channel);
      if (!channelId) return;
      recoverInFlight = true;
      void call("terminal_recover", {
        tabId: kernelTabId,
        channelId,
        clientId: clientId(),
        attachmentId,
        acceptedBytes: acceptedOffsetNow(),
      })
        .then((res) => {
          recoverInFlight = false;
          recoverTarget = (res as { flushedOffset?: number } | null)?.flushedOffset ?? null;
          if (recoverPending) {
            // A refusal arrived while this recover was in flight: serially
            // recover the newly refused suffix with the latest accepted
            // offset before any resume is considered.
            recoverPending = false;
            pumpRecover();
            return;
          }
          maybeResume();
        })
        .catch(() => {
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
        void terminalApi.detach(kernelTabId, channelIdOf(channel)).catch(() => undefined);
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
    const streamWriter = createTerminalStreamWriter(
      term,
      (event) => {
        if (event.kind === "notification") onNotificationRef.current?.(event.body);
        else if (event.kind === "command") blocks.feedOSC133(event.phase, event.exitCode);
        else onClipboardRef.current?.(event.payload);
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
            // Lift a refusal episode for tab kinds without an anchor-driven
            // recovery (docker exec); durable tabs end the episode at the
            // next anchor, and ephemeral recovery ends it at the recovery
            // anchor — never here, where a later old frame would be
            // accepted into a non-contiguous prefix.
            if (containerId) streamWriter.beginAttachment();
            // Resume only once the refused suffix is fully accepted and no
            // recovery is in flight or pending; a pending replay is awaited,
            // not re-requested here.
            maybeResume();
            if (recoverTarget === null && !recoverInFlight && !recoverPending) {
              resumeUpstream();
            }
          }
        },
        onRefused: () => refuseStream(),
        onWritten: scheduleUiFlush,
      },
    );
    // Automatic query responses (DA/DSR/CPR reports and friends) are answered
    // by xterm itself through onData; floods of them collapse per type with a
    // budget instead of one upstream write each. Keystrokes and pastes never
    // enter the coalescer.
    const overflowNotified = new Set<string>();
    const responses = createTerminalResponseCoalescer({
      send: (data) => {
        if (!kernelTabId) return;
        const onData = onDataRef.current;
        if (onData) {
          onData(data);
        } else {
          void terminalApi
            .write(kernelTabId, new TextEncoder().encode(data))
            .catch(() => undefined);
        }
      },
      onOverflow: (info) => {
        const key = `${info.generation}:${info.type}`;
        if (overflowNotified.has(key)) return;
        overflowNotified.add(key);
        useUi
          .getState()
          .pushToast(
            "info",
            `终端自动响应（${info.type}）超出预算，已抑制 ${info.dropped} 条相同类型响应`,
          );
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
        if (out.length > 0 && !streamWriter.pushBytes(out)) {
          // Refused bytes are not counted in streamBytes: the resume anchor
          // must point at the last accepted byte so the replay resends them.
          // The refusal episode is sticky inside the writer until the next
          // attachment boundary, keeping the accepted prefix contiguous.
          streamBytes += skipped;
          return;
        }
        streamBytes += bytes.length;
        if (recoverTarget !== null) maybeResume();
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
    );
    const applyRemoteDimensions = (cols: number, rows: number) => {
      applyGrid({ cols, rows });
    };
    let attaching = false;
    const doAttach = async () => {
      if (disposed || kernelTabId || attaching) return;
      const resume = resumeTabId;
      const dimensions = coordinator.desiredGrid();
      if (!resume && !dimensions) return;
      attaching = true;
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
            controller: info.controller,
            subscribers: info.subscribers,
            viewers: info.viewers,
            exited: info.exited,
            encoding: info.encoding,
            title: info.title,
            shell: info.shell,
            retainable: info.retainable,
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
            controller: null,
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
            controller: info.controller,
            subscribers: info.subscribers,
            viewers: info.viewers,
            exited: info.exited,
            shell: info.shell,
            retainable: info.retainable,
            attachmentId: info.attachmentId,
            // session_open_line_tab creates an Ephemeral tab: a cap refusal
            // must take the recovery path, never the ordinary detach that
            // reaps the line tab's process.
            ephemeral: true,
          });
        } else {
          if (!dimensions) return;
          const info = await terminalApi.attach(sessionId, dimensions.cols, dimensions.rows, channel);
          id = info.tabId;
          fallbackNotice = info.fallbackNotice;
          reportAttachInfo({
            tabId: info.tabId,
            controller: info.controller,
            subscribers: info.subscribers,
            viewers: info.viewers,
            exited: info.exited,
            encoding: info.encoding,
            title: info.title,
            shell: info.shell,
            retainable: info.retainable,
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
          streamWriter.writeLocalText(`\r\n\x1b[31m[连接失败] ${describeError(e)}\x1b[0m\n`);
        }
      } finally {
        attaching = false;
      }
    };
    onGeometryRef.current = () => void doAttach();

    let reattaching = false;
    const reattachOnReopen = () => {
      const id = kernelTabId;
      if (!id || reattaching) return;
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
            // instead, which the server sends first.
            streamWriter.beginAttachment();
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
          // The backpressure episode ends at the attachment boundary: the
          // resume replay (anchor for session tabs, bounded replay for docker
          // exec) redelivers everything the saturated FIFO refused.
          streamWriter.beginAttachment();
          applyRemoteDimensions(info.cols, info.rows);
          coordinator.attach(info.tabId, { cols: info.cols, rows: info.rows }, true);
          reportAttachInfo({
            tabId: info.tabId,
            controller: info.controller,
            subscribers: info.subscribers,
            viewers: info.viewers,
            exited: info.exited,
            encoding: info.encoding,
            title: info.title,
            shell: info.shell,
            retainable: info.retainable,
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
        }
      })();
    };
    reattachRef.current = reattachOnReopen;
    const offReopen = onChannelReopen(channel, reattachOnReopen);

    const attachTimer = window.setTimeout(() => {
      void doAttach();
    }, 0);

    const dataDisposable = term.onData((data) => {
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
      const files = pureImageFiles(event.clipboardData);
      if (files.length > 0 && !clipboardHasPlainText(event.clipboardData)) {
        event.preventDefault();
        event.stopPropagation();
        onPasteImagesRef.current?.(files);
        return;
      }
      const text = event.clipboardData?.getData("text/plain") ?? "";
      if (multilinePasteLines(text) === null) return;
      event.preventDefault();
      event.stopPropagation();
      void confirmMultilinePaste(text).then((ok) => {
        if (ok) sendInput(wrapBracketedPaste(text, term.modes.bracketedPasteMode), text);
      });
    };
    const onDragOver = (event: DragEvent) => {
      if (!event.dataTransfer?.types.includes("Files")) return;
      event.preventDefault();
    };
    const onDropCapture = (event: DragEvent) => {
      const files = pureImageFiles(event.dataTransfer);
      if (files.length === 0 || clipboardHasPlainText(event.dataTransfer)) return;
      event.preventDefault();
      event.stopPropagation();
      onPasteImagesRef.current?.(files);
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
      pauseNeeded = false;
      if (pauseRetryTimer !== undefined) {
        window.clearTimeout(pauseRetryTimer);
        pauseRetryTimer = undefined;
      }
      coordinator.close();
      gridRef.current = null;
      onGeometryRef.current = null;
      if (reattachRef.current === reattachOnReopen) reattachRef.current = null;
      offReopen();
      window.clearTimeout(attachTimer);
      if (windowResizeTimer !== null) window.clearTimeout(windowResizeTimer);
      if (measureFrame !== null) cancelAnimationFrame(measureFrame);
      if (uiFrame !== null) cancelAnimationFrame(uiFrame);
      responses.flush();
      window.removeEventListener(RESIZE_END_EVENT, onFinalResize);
      window.removeEventListener("resize", onWindowResize);
      window.visualViewport?.removeEventListener("resize", onWindowResize);
      ro.disconnect();
      pasteHost.removeEventListener("paste", onPasteCapture, true);
      pasteHost.removeEventListener("dragover", onDragOver);
      pasteHost.removeEventListener("drop", onDropCapture, true);
      pasteHost.removeEventListener("touchstart", trackTouch, true);
      pasteHost.removeEventListener("touchmove", trackTouch, true);
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
  }, [sessionId, containerId, resumeTabId]);

  useEffect(() => {
    const connected = props.sessionConnected === true;
    const wasConnected = previousSessionConnected.current;
    previousSessionConnected.current = connected;
    if (!wasConnected && connected) reattachRef.current?.();
  }, [props.sessionConnected]);

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

  return (
    <div className="relative h-full w-full min-h-0">
      <div ref={hostRef} className="h-full w-full min-h-0" />
      {selectionBar && (
        <SelectionActionBar
          x={selectionBar.x}
          y={selectionBar.y}
          onCopy={copySelectionViaBar}
          onPaste={pasteViaBar}
          onSelectAll={() => termRef.current?.selectAll()}
        />
      )}
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
