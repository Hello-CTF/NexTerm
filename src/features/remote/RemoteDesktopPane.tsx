import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { requestCloseTab } from "../../app/store";
import { captureKeyboard, isReleaseChord, RELEASE_CHORD_LABEL, releaseKeyboard } from "../../app/inputCapture";
import { isTouchShell } from "../../app/shell/shellMode";
import { isMac } from "../../app/platform";
import { promptText } from "../../ui/dialogs";
import { IconLoader } from "../../ui/icons";
import { parseResolutionInput } from "./resolution";
import type { KeyCombo } from "./keys";
import { RemoteKeysBar } from "./RemoteKeysBar";
import { dismissSoftKeyboard } from "../../ui/softKeyboard";
import { RemoteToolbar, type ResolutionChoice } from "./RemoteToolbar";
import { RemoteTouchLayer } from "./RemoteTouchLayer";
import { useRemoteSession } from "./useRemoteSession";
import { RemoteKeyboard, type PasteSource } from "./remoteKeyboard";
import { filesTransfer, readNativeDrop } from "./nativeRemoteDrop";
import { asNativeDropEvent, html5DropIsRecent, noteHtml5Drop } from "../files/nativeDrop";
import { EVENTS, listenEvent } from "../../ipc/events";
import { DESKTOP } from "../../ipc/env";
import { WAKE_DISCLAIMER, wakeAsset } from "../explorer/wakeAction";
import { AUTO_RECONNECT_WINDOW_MS, MAX_AUTO_RECONNECTS, type Failure } from "./connectFlow";
import type { ClipboardOutcome, ModifierKey, ScaleMode } from "./remoteTypes";
import type { TouchMode } from "./touchGestures";
import { t } from "../../i18n";

const TOOLBAR_HIDE_MS = 2500;
const TOOLBAR_REVEAL_ZONE_PX = 56;
const RESIZE_DEBOUNCE_MS = 300;
const HINT_MS = 3500;
const NOTICE_MS = 4000;

// What a paste tells the user. A paste that worked says nothing (the remote side shows the result); an empty local
// clipboard on a key paste is not an error either, the remote side then pastes its own clipboard.
export function pasteNotice(outcome: ClipboardOutcome, source: PasteSource): string | null {
  if (outcome.note) return outcome.note;
  if (outcome.status === "refused") return source === "drop" ? t("拖入的内容未能发送") : t("无法读取本机剪贴板，远程桌面粘贴的是它自己的剪贴板内容");
  if (outcome.status === "empty" && source === "drop") return t("拖入的内容中没有可发送的项目");
  return null;
}

export function sendNotice(outcome: ClipboardOutcome): string {
  if (outcome.status === "done") return outcome.note ?? t("已发送本机剪贴板");
  if (outcome.status === "empty") return t("本机剪贴板为空");
  return outcome.note ?? t("无法读取本机剪贴板");
}

export function fetchNotice(outcome: ClipboardOutcome): string {
  if (outcome.status === "done") return outcome.note ?? t("已复制远程剪贴板");
  if (outcome.status === "empty") return t("远程剪贴板暂无内容");
  return outcome.note ?? t("无法写入本机剪贴板");
}
const PENDING_RETRY_MS = 600;
const PENDING_RETRY_MAX = 10;
const NOTICE_RECONNECT = t("可重新连接，按所选大小启动。");
const NOTICE_UNCONFIRMED = t("远程主机尚未确认这次分辨率调整，所选分辨率尚未生效。{NOTICE_RECONNECT}", { NOTICE_RECONNECT });
const NOTICE_UNSUPPORTED = t("这台远程主机不支持在线调整分辨率，所选分辨率尚未生效。{NOTICE_RECONNECT}", { NOTICE_RECONNECT });
const NOTICE_REFUSED = t("远程主机拒绝了这次分辨率调整，所选分辨率尚未生效。{NOTICE_RECONNECT}", { NOTICE_RECONNECT });

function failureTitle(failure: Failure): string {
  switch (failure.kind) {
    case "auth":
      return t("认证失败");
    case "certificate":
      return t("服务器证书未确认");
    case "permission":
      return t("没有权限连接");
    case "ended":
      return t("远程桌面已断开");
    case "retryable":
      return t("连接已中断");
    default:
      return t("无法连接远程桌面");
  }
}

// The engine's own text, folded away: useful in a bug report, wrong as a headline.
function FailureDetail({ text }: { text: string }) {
  const [copied, setCopied] = useState(false);
  const copy = () => {
    void navigator.clipboard
      ?.writeText(text)
      .then(() => setCopied(true))
      .catch(() => undefined);
  };
  return (
    <details className="max-w-md text-left text-[11.5px] text-neutral-400" data-testid="remote-failure-detail">
      <summary className="cursor-pointer text-center">{t("详情")}</summary>
      <pre className="mt-1 max-h-40 overflow-auto rounded bg-black/50 p-2 break-words whitespace-pre-wrap select-text">{text}</pre>
      <button type="button" className="nx-btn nx-btn-outline mt-1" onClick={copy}>
        {copied ? t("已复制") : t("复制详情")}
      </button>
    </details>
  );
}

function DisconnectOverlay({
  failure,
  retryIn,
  attempt,
  reconnectPaused,
  onReconnect,
  onClose,
  onWake,
}: {
  failure: Failure;
  retryIn: number | null;
  attempt: number;
  reconnectPaused: boolean;
  onReconnect: () => void;
  onClose: () => void;
  // Present when the asset has a Wake-on-LAN MAC.
  onWake?: () => Promise<boolean>;
}) {
  const never = failure.kind === "auth" || failure.kind === "certificate" || failure.kind === "permission";
  const [waking, setWaking] = useState(false);
  // Authentication, certificate and permission failures prove the host answered, so waking it would not help.
  const showWake = onWake !== undefined && !never;
  return (
    <div
      role="alertdialog"
      aria-modal="false"
      aria-labelledby="nx-remote-overlay-title"
      className="absolute inset-0 z-20 flex flex-col items-center justify-center gap-3 bg-black/60 px-6 text-center"
      data-testid="remote-disconnect-overlay"
    >
      <div id="nx-remote-overlay-title" className="text-[15px] font-semibold text-neutral-100">
        {failure.title ?? failureTitle(failure)}
      </div>
      <div className="max-w-md text-[12.5px] break-words text-neutral-300">{failure.message}</div>
      {failure.detail && <FailureDetail text={failure.detail} />}
      {retryIn !== null && (
        <div className="text-[12.5px] text-neutral-300" role="status" data-testid="remote-retry-countdown">
          {t("{retryIn} 秒后自动重新连接（第 {value} 次）", { retryIn, value: attempt + 1 })}
        </div>
      )}
      {reconnectPaused && (
        <div className="max-w-md text-[12.5px] text-amber-300" role="status" data-testid="remote-reconnect-paused">
          {t("{value} 分钟内已自动重新连接 {MAX_AUTO_RECONNECTS} 次，已暂停自动重连。可能有另一台设备正在使用同一个远程桌面，确认后请手动重新连接。", { value: AUTO_RECONNECT_WINDOW_MS / 60_000, MAX_AUTO_RECONNECTS })}
        </div>
      )}
      {never && <div className="text-[11.5px] text-neutral-400">{t("这类失败不会自动重试，请处理后手动重新连接。")}</div>}
      <div className="flex items-center gap-2">
        <button type="button" className="nx-btn nx-btn-primary" onClick={onReconnect}>
          {t("重新连接")}
        </button>
        {showWake && (
          <button
            type="button"
            className="nx-btn nx-btn-outline"
            disabled={waking}
            onClick={() => {
              setWaking(true);
              void onWake().finally(() => setWaking(false));
            }}
          >
            {t("唤醒")}
          </button>
        )}
        <button type="button" className="nx-btn nx-btn-outline" onClick={onClose}>
          {t("关闭")}
        </button>
      </div>
      {showWake && <div className="max-w-md text-[11.5px] text-neutral-400">{WAKE_DISCLAIMER}</div>}
    </div>
  );
}

function clamp(value: number, min: number, max: number): number {
  return Math.min(max, Math.max(min, value));
}

export function RemoteDesktopPane({ tabId, assetId, visible }: { tabId: string; assetId: string; visible: boolean }) {
  const paneRef = useRef<HTMLDivElement>(null);
  const wrapRef = useRef<HTMLDivElement>(null);
  const viewportRef = useRef<HTMLDivElement>(null);
  const pasteInputRef = useRef<HTMLTextAreaElement>(null);
  const keyboardInputRef = useRef<HTMLInputElement>(null);
  const session = useRemoteSession(tabId, assetId, viewportRef);
  const { clientRef, protocol, options, desktopSize, setResolution: setSessionResolution } = session;
  const touch = isTouchShell();

  const [captured, setCaptured] = useState(false);
  const [hint, setHint] = useState(false);
  const [toolbarShown, setToolbarShown] = useState(true);
  const [pinned, setPinned] = useState(false);
  const [scale, setScale] = useState<ScaleMode>("fit");
  const [viewOnly, setViewOnly] = useState<boolean | null>(null);
  const [resolution, setResolution] = useState<ResolutionChoice | null>(null);
  // A fixed resolution the server could not apply while connected; offers a reconnect that starts at that size.
  const [resolutionNotice, setResolutionNotice] = useState<string | null>(null);
  const [held, setHeld] = useState<ReadonlySet<ModifierKey>>(new Set());
  const [latched, setLatched] = useState<ReadonlySet<ModifierKey>>(new Set());
  const [touchMode, setTouchMode] = useState<TouchMode>("direct");
  const [fullscreen, setFullscreen] = useState(false);
  const [keyboardOpen, setKeyboardOpen] = useState(false);
  const hideTimer = useRef<number | null>(null);
  const hintTimer = useRef<number | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const noticeTimer = useRef<number | null>(null);
  const showNotice = useCallback((message: string | null) => {
    if (!message) return;
    setNotice(message);
    if (noticeTimer.current !== null) window.clearTimeout(noticeTimer.current);
    noticeTimer.current = window.setTimeout(() => setNotice(null), NOTICE_MS);
  }, []);
  const showNoticeRef = useRef(showNotice);
  showNoticeRef.current = showNotice;
  useEffect(() => {
    if (session.clipboardNotice) showNotice(session.clipboardNotice.message);
  }, [session.clipboardNotice, showNotice]);

  const effectiveViewOnly = viewOnly ?? options.viewOnly;
  const effectiveResolution = useMemo<ResolutionChoice>(
    () => resolution ?? (options.resolution === "fixed" ? { width: options.width, height: options.height } : "follow"),
    [resolution, options.resolution, options.width, options.height],
  );
  const connected = session.status === "connected";
  // RDP takes a new clipboard on its own channel, and a VNC server with the extended clipboard asks for the text
  // after it was announced, so the paste keys wait briefly in both.
  const keyboardOptionsRef = useRef({ commandAsCtrl: false, syncPaste: false, pasteDelayMs: 0 });
  keyboardOptionsRef.current = {
    commandAsCtrl: options.commandAsCtrl && isMac(),
    syncPaste: options.clipboard && options.clipboardAutoSync,
    pasteDelayMs: protocol === "rdp" ? 150 : 100,
  };
  const allowNativePasteRef = useRef(false);
  const keyboard = useMemo(
    () =>
      new RemoteKeyboard(
        () => clientRef.current,
        () => keyboardOptionsRef.current,
        (event) => {
          const nativeChord = isMac() ? event.metaKey && !event.ctrlKey : event.ctrlKey && !event.metaKey;
          const input = pasteInputRef.current;
          if (!nativeChord || !input) return false;
          allowNativePasteRef.current = true;
          input.value = "";
          input.focus({ preventScroll: true });
          return true;
        },
        (outcome, source) => showNoticeRef.current(pasteNotice(outcome, source)),
      ),
    [clientRef],
  );

  // The client is created after the first render, so a setting made before it exists is applied once it does.
  useEffect(() => {
    if (!connected) return;
    const client = clientRef.current;
    client?.setViewOnly(effectiveViewOnly);
    client?.setScale(scale);
  }, [connected, effectiveViewOnly, scale, clientRef]);

  // Toolbar: shown on arrival, hides after a pause, comes back when the pointer reaches the top edge.
  const armHide = useCallback(() => {
    if (hideTimer.current !== null) window.clearTimeout(hideTimer.current);
    hideTimer.current = window.setTimeout(() => setToolbarShown(false), TOOLBAR_HIDE_MS);
  }, []);
  useEffect(() => {
    armHide();
    return () => {
      if (hideTimer.current !== null) window.clearTimeout(hideTimer.current);
      if (hintTimer.current !== null) window.clearTimeout(hintTimer.current);
      if (noticeTimer.current !== null) window.clearTimeout(noticeTimer.current);
    };
  }, [armHide]);
  useEffect(() => {
    if (pinned) {
      if (hideTimer.current !== null) window.clearTimeout(hideTimer.current);
      setToolbarShown(true);
    } else armHide();
  }, [pinned, armHide]);
  const reveal = useCallback(() => {
    setToolbarShown(true);
    if (!pinned) armHide();
  }, [pinned, armHide]);

  const releaseHeld = useCallback(() => {
    keyboard.reset();
    clientRef.current?.releaseAllKeys();
    setHeld(new Set());
    setLatched(new Set());
  }, [clientRef, keyboard]);

  const releaseKeyboardNow = useCallback(() => {
    releaseHeld();
    clientRef.current?.blur();
    const active = document.activeElement;
    if (active instanceof HTMLElement && wrapRef.current?.contains(active)) active.blur();
    paneRef.current?.focus({ preventScroll: true });
    releaseKeyboard(tabId);
    setCaptured(false);
  }, [clientRef, releaseHeld, tabId]);

  // Keyboard capture follows focus inside the remote screen, never the toolbar or the rest of the pane.
  useEffect(() => {
    const wrap = wrapRef.current;
    if (!wrap) return;
    const inside = (node: EventTarget | null) =>
      node instanceof Node && (viewportRef.current?.contains(node) === true || node === pasteInputRef.current);
    const onFocusIn = (event: FocusEvent) => {
      if (!inside(event.target) || effectiveViewOnly || !connected) return;
      if (event.target === pasteInputRef.current) return;
      captureKeyboard(tabId);
      setCaptured(true);
      setHint(true);
      if (hintTimer.current !== null) window.clearTimeout(hintTimer.current);
      hintTimer.current = window.setTimeout(() => setHint(false), HINT_MS);
    };
    const onFocusOut = (event: FocusEvent) => {
      if (!inside(event.target) || inside(event.relatedTarget)) return;
      keyboard.reset();
      clientRef.current?.releaseAllKeys();
      setHeld(new Set());
      releaseKeyboard(tabId);
      setCaptured(false);
      setHint(false);
    };
    const onKey = (event: KeyboardEvent) => {
      if (event.type === "keydown" && isReleaseChord(event)) {
        event.preventDefault();
        event.stopPropagation();
        releaseKeyboardNow();
        return;
      }
      if (!inside(event.target) || effectiveViewOnly || !connected) return;
      allowNativePasteRef.current = false;
      if (!keyboard.handle(event)) return;
      if (!allowNativePasteRef.current) event.preventDefault();
      event.stopPropagation();
    };
    const onPaste = (event: ClipboardEvent) => {
      if (!inside(event.target) || effectiveViewOnly || !connected) return;
      if (!keyboard.handlePaste(event.clipboardData)) return;
      event.preventDefault();
      event.stopPropagation();
      if (pasteInputRef.current) pasteInputRef.current.value = "";
      clientRef.current?.focus();
    };
    const onDragOver = (event: DragEvent) => {
      if (effectiveViewOnly || !connected || !options.clipboard) return;
      if (!event.dataTransfer?.types.includes("Files")) return;
      event.preventDefault();
    };
    const onDrop = (event: DragEvent) => {
      if (effectiveViewOnly || !connected || !options.clipboard) return;
      if (!event.dataTransfer?.types.includes("Files")) return;
      event.preventDefault();
      event.stopPropagation();
      // WebView2 also reports this drop as a native one; that copy must not be sent again.
      noteHtml5Drop();
      clientRef.current?.focus();
      if (!keyboard.handlePaste(event.dataTransfer, "drop")) showNoticeRef.current(t("「自动同步剪贴板」已关闭，拖入的文件未发送"));
    };
    wrap.addEventListener("focusin", onFocusIn);
    wrap.addEventListener("focusout", onFocusOut);
    // Capture phase: the chord, the Command mapping and paste must be seen before the remote client's own key handling.
    wrap.addEventListener("keydown", onKey, true);
    wrap.addEventListener("keyup", onKey, true);
    wrap.addEventListener("paste", onPaste, true);
    wrap.addEventListener("dragover", onDragOver);
    wrap.addEventListener("drop", onDrop, true);
    return () => {
      wrap.removeEventListener("focusin", onFocusIn);
      wrap.removeEventListener("focusout", onFocusOut);
      wrap.removeEventListener("keydown", onKey, true);
      wrap.removeEventListener("keyup", onKey, true);
      wrap.removeEventListener("paste", onPaste, true);
      wrap.removeEventListener("dragover", onDragOver);
      wrap.removeEventListener("drop", onDrop, true);
    };
  }, [tabId, effectiveViewOnly, connected, options.clipboard, clientRef, releaseKeyboardNow, keyboard]);

  // macOS and Linux hand file drops to the desktop shell instead of the page; the shell reports the local paths.
  useEffect(() => {
    if (!DESKTOP || effectiveViewOnly || !connected || !options.clipboard) return;
    let off: (() => void) | null = null;
    let disposed = false;
    void listenEvent<unknown>(EVENTS.filesDropped, (raw) => {
      const event = asNativeDropEvent(raw);
      const wrap = wrapRef.current;
      if (!event || !wrap || html5DropIsRecent()) return;
      const point = document.elementFromPoint?.(event.x, event.y) ?? null;
      if (!point || !viewportRef.current?.contains(point)) return;
      // VNC carries text only, so the files are not read at all.
      if (protocol !== "rdp") {
        showNoticeRef.current(t("VNC 只能同步文本，图片和文件未发送"));
        return;
      }
      clientRef.current?.focus();
      void readNativeDrop(event.paths).then(({ files, skipped }) => {
        if (disposed) return;
        const note = skipped.length > 0 ? t("未发送：{value}", { value: skipped.join(t("、")) }) : undefined;
        if (files.length === 0) {
          showNoticeRef.current(note ?? t("拖入的内容中没有可发送的项目"));
          return;
        }
        if (!keyboard.handlePaste(filesTransfer(files), "drop", note)) showNoticeRef.current(t("「自动同步剪贴板」已关闭，拖入的文件未发送"));
      });
    }).then((unlisten) => {
      if (disposed) unlisten();
      else off = unlisten;
    });
    return () => {
      disposed = true;
      off?.();
    };
  }, [effectiveViewOnly, connected, options.clipboard, clientRef, keyboard, protocol]);

  useEffect(() => {
    const onBlur = () => releaseHeld();
    window.addEventListener("blur", onBlur);
    return () => window.removeEventListener("blur", onBlur);
  }, [releaseHeld]);

  useEffect(() => {
    clientRef.current?.setForeground(visible);
  }, [visible, connected, clientRef]);

  // A tab that is not shown, or whose connection is gone, cannot own the keyboard: the app shortcuts come back.
  useEffect(() => {
    if (visible && connected) return;
    releaseKeyboard(tabId);
    setCaptured(false);
    setHeld(new Set());
    setLatched(new Set());
  }, [visible, connected, tabId]);

  // Click to focus: the screen takes the keyboard on a click, not when the pointer merely passes over it.
  const focusScreen = useCallback(() => {
    if (connected) clientRef.current?.focus();
  }, [connected, clientRef]);

  // Resolution (RDP): the desktop follows the pane, or is held at the chosen size. A size the connection already
  // started with needs no request; a request before the Display Control channel opens is retried shortly.
  useEffect(() => {
    if (!connected) setResolutionNotice(null);
  }, [connected]);
  const fixedResolution = effectiveResolution !== "follow";
  // A desktop size change that answers the outstanding fixed-size request (even late, or rounded) clears the notice;
  // a server-initiated resize to some other size does not.
  useEffect(() => {
    if (!desktopSize || !resolutionNotice) return;
    if (effectiveResolution === "follow") {
      setResolutionNotice(null);
      return;
    }
    if (desktopSize.width === effectiveResolution.width && desktopSize.height === effectiveResolution.height) {
      setResolutionNotice(null);
      return;
    }
    const outcome = clientRef.current?.resize(effectiveResolution.width, effectiveResolution.height);
    if (outcome === "applied") setResolutionNotice(null);
    // eslint-disable-next-line react-hooks/exhaustive-deps -- reacts to a new desktop size only
  }, [desktopSize]);
  useEffect(() => {
    const viewport = viewportRef.current;
    if (!viewport || protocol !== "rdp" || !connected) return;
    let timer: number | null = null;
    let retryTimer: number | null = null;
    let retries = 0;
    let cancelled = false;
    const apply = () => {
      if (cancelled) return;
      if (retryTimer !== null) {
        window.clearTimeout(retryTimer);
        retryTimer = null;
      }
      const client = clientRef.current;
      if (!client || !client.supportsResize) return;
      let target: { width: number; height: number };
      if (effectiveResolution === "follow") {
        const zoom = typeof scale === "number" ? scale : 1;
        if (viewport.clientWidth <= 1 || viewport.clientHeight <= 1) return;
        target = { width: viewport.clientWidth / zoom, height: viewport.clientHeight / zoom };
      } else {
        target = effectiveResolution;
        const now = client.desktopSize();
        if (now.width === target.width && now.height === target.height) {
          setResolutionNotice(null);
          return;
        }
      }
      const outcome = client.resize(target.width, target.height);
      if (outcome === "applied") {
        setResolutionNotice(null);
      } else if (outcome === "pending" || outcome === "awaiting") {
        // "pending" re-sends the request on each retry; "awaiting" only looks again, the request is already in flight.
        if (retries < PENDING_RETRY_MAX) {
          retries += 1;
          retryTimer = window.setTimeout(apply, PENDING_RETRY_MS);
        } else if (fixedResolution) {
          setResolutionNotice(NOTICE_UNCONFIRMED);
        }
      } else if (fixedResolution) {
        setResolutionNotice(outcome === "unavailable" ? NOTICE_UNSUPPORTED : NOTICE_REFUSED);
      }
    };
    const observer = new ResizeObserver(() => {
      if (timer !== null) window.clearTimeout(timer);
      timer = window.setTimeout(() => {
        retries = 0;
        apply();
      }, RESIZE_DEBOUNCE_MS);
    });
    observer.observe(viewport);
    apply();
    return () => {
      cancelled = true;
      observer.disconnect();
      if (timer !== null) window.clearTimeout(timer);
      if (retryTimer !== null) window.clearTimeout(retryTimer);
    };
  }, [protocol, connected, effectiveResolution, scale, clientRef, fixedResolution]);

  const chooseResolution = useCallback(
    (choice: ResolutionChoice) => {
      setResolutionNotice(null);
      setResolution(choice);
      setSessionResolution(choice);
    },
    [setSessionResolution],
  );

  const chooseCustomResolution = useCallback(async () => {
    let message = t("输入分辨率（宽 x 高），例如 1600 x 900。\n仅对本次连接生效，不会保存到资产。");
    for (;;) {
      const text = await promptText(message, "");
      if (text === null) return;
      const parsed = parseResolutionInput(text);
      if (parsed.ok) {
        chooseResolution({ width: parsed.width, height: parsed.height });
        return;
      }
      message = t("{error}\n\n输入分辨率（宽 x 高），例如 1600 x 900。", { error: parsed.error });
    }
  }, [chooseResolution]);

  useEffect(() => {
    const onChange = () => setFullscreen(document.fullscreenElement === paneRef.current);
    document.addEventListener("fullscreenchange", onChange);
    return () => document.removeEventListener("fullscreenchange", onChange);
  }, []);

  const toggleFullscreen = () => {
    const pane = paneRef.current;
    if (!pane) return;
    const keyboard = (navigator as Navigator & { keyboard?: { lock?: (keys?: string[]) => Promise<void>; unlock?: () => void } }).keyboard;
    if (document.fullscreenElement) {
      keyboard?.unlock?.();
      void document.exitFullscreen();
      return;
    }
    void pane.requestFullscreen().then(() => keyboard?.lock?.(["Escape"]).catch(() => undefined)).catch(() => undefined);
  };

  const toggleHeld = (key: ModifierKey) => {
    const client = clientRef.current;
    if (!client) return;
    // The key goes out here, not inside the state updater: updaters can run twice.
    const next = new Set(held);
    if (next.delete(key)) client.sendKey(key, false);
    else {
      next.add(key);
      client.sendKey(key, true);
    }
    setHeld(next);
  };

  const sendCombo = (combo: KeyCombo) => {
    void clientRef.current?.sendCombo(combo.keys);
  };

  const toggleLatch = (key: ModifierKey) => {
    setLatched((current) => {
      const next = new Set(current);
      if (!next.delete(key)) next.add(key);
      return next;
    });
  };

  // One-shot latches: the next key from the bar or the soft keyboard goes out with them, then they release.
  const withLatches = async (send: () => void | Promise<void>) => {
    const client = clientRef.current;
    if (!client) return;
    const mods = [...latched];
    setLatched(new Set());
    for (const mod of mods) client.sendKey(mod, true);
    try {
      await send();
    } finally {
      for (const mod of mods.reverse()) client.sendKey(mod, false);
    }
  };

  const sendBarKey = (code: string) => {
    const client = clientRef.current;
    if (!client) return;
    void withLatches(() => {
      client.sendKey(code, true);
      client.sendKey(code, false);
    });
  };

  const typeText = (text: string) => {
    const client = clientRef.current;
    if (!client || !text) return;
    const single = [...text].length === 1 ? text : "";
    const code = /^[a-z]$/i.test(single) ? `Key${single.toUpperCase()}` : /^[0-9]$/.test(single) ? `Digit${single}` : "";
    if (latched.size > 0 && code) {
      sendBarKey(code);
      return;
    }
    void withLatches(() => {
      const skipped = client.sendText(text);
      if (skipped > 0) showNotice(t("{count} 个字符无法发送到远程桌面，已跳过", { count: skipped }));
    });
  };

  const openSoftKeyboard = () => {
    const input = keyboardInputRef.current;
    if (!input) return;
    // Focus inside the tap itself: browsers only open the soft keyboard for focus that a gesture caused.
    if (document.activeElement === input) {
      dismissSoftKeyboard();
      setKeyboardOpen(false);
      return;
    }
    input.value = "  ";
    input.focus();
    setKeyboardOpen(true);
  };

  const onSoftKeyboardInput = (event: React.FormEvent<HTMLInputElement>) => {
    const native = event.nativeEvent as InputEvent;
    const input = event.currentTarget;
    if (native.inputType === "deleteContentBackward") sendBarKey("Backspace");
    else if (native.inputType === "insertLineBreak" || native.inputType === "insertParagraph") sendBarKey("Enter");
    else if (native.data) typeText(native.data);
    input.value = "  ";
  };

  // Keys that arrive as real key events (hardware keyboards, iOS) go out as keys; a single-line input does not
  // reliably report Enter as a line break. IME and Android soft keyboards report key "Unidentified" and are handled
  // by the input event instead, so nothing is sent twice.
  const onSoftKeyboardKeyDown = (event: React.KeyboardEvent<HTMLInputElement>) => {
    if (event.nativeEvent.isComposing || event.key === "Unidentified" || event.key === "Process") return;
    if (event.key === "Enter" || event.key === "Backspace" || event.key === "Tab") {
      event.preventDefault();
      sendBarKey(event.key);
    }
  };

  const onPinch = (factor: number) => {
    const client = clientRef.current;
    const viewport = viewportRef.current;
    if (!client || !viewport) return;
    if (protocol === "vnc") {
      if (factor > 1.02) setScale(1);
      else if (factor < 0.98) setScale("fit");
      return;
    }
    const size = client.desktopSize();
    const fit = size.width > 0 ? Math.min(viewport.clientWidth / size.width, viewport.clientHeight / size.height) : 1;
    setScale((current) => clamp((current === "fit" ? fit : current) * factor, 0.25, 4));
  };

  const onPan = (dx: number, dy: number) => {
    const stage = viewportRef.current?.firstElementChild;
    if (!(stage instanceof HTMLElement)) return;
    // RDP scrolls the stage itself; noVNC scrolls its own screen element inside it.
    const inner = stage.firstElementChild;
    const scroller = inner instanceof HTMLElement && inner.scrollHeight + inner.scrollWidth > inner.clientHeight + inner.clientWidth ? inner : stage;
    scroller.scrollBy(-dx, -dy);
  };

  const onPaneMove = (event: React.PointerEvent) => {
    const top = paneRef.current?.getBoundingClientRect().top ?? 0;
    if (event.clientY - top < TOOLBAR_REVEAL_ZONE_PX) reveal();
  };

  const closeTab = () => void requestCloseTab(tabId);

  return (
    <div
      ref={paneRef}
      tabIndex={-1}
      className="relative flex h-full min-h-0 flex-col bg-black outline-none"
      onPointerMove={onPaneMove}
      data-testid="remote-desktop-pane"
      data-protocol={protocol}
      data-status={session.status}
    >
      <div
        ref={wrapRef}
        className="relative min-h-0 flex-1 overflow-hidden"
        data-captured={captured ? "true" : "false"}
      >
        <div ref={viewportRef} className="absolute inset-0" onPointerDownCapture={focusScreen} />
        <textarea
          ref={pasteInputRef}
          tabIndex={-1}
          aria-hidden="true"
          className="pointer-events-none fixed bottom-0 left-0 h-px w-px opacity-0"
          autoCapitalize="off"
          autoComplete="off"
          autoCorrect="off"
          spellCheck={false}
          data-testid="remote-paste-target"
        />
        {touch && connected && !effectiveViewOnly && (
          <RemoteTouchLayer
            mode={touchMode}
            getTarget={() => clientRef.current?.inputElement() ?? null}
            onPinch={onPinch}
            onPan={onPan}
            onFocusRequest={focusScreen}
          />
        )}
        <button
          type="button"
          className="absolute top-0 left-1/2 z-20 h-3 w-16 -translate-x-1/2 rounded-b-md bg-neutral-700/70 hover:bg-neutral-600"
          aria-label={t("显示工具栏")}
          title={t("显示工具栏")}
          onClick={reveal}
          onMouseDown={(event) => event.preventDefault()}
        />
        <RemoteToolbar
          protocol={protocol}
          visible={toolbarShown}
          scale={scale}
          onScale={setScale}
          held={held}
          onToggleHeld={toggleHeld}
          onCombo={sendCombo}
          clipboardEnabled={options.clipboard}
          onSendClipboard={() => void clientRef.current?.sendClipboard().then((outcome) => showNotice(sendNotice(outcome)))}
          onFetchClipboard={() => void clientRef.current?.fetchClipboard().then((outcome) => showNotice(fetchNotice(outcome)))}
          resolution={effectiveResolution}
          onResolution={chooseResolution}
          onCustomResolution={() => void chooseCustomResolution()}
          desktopSize={protocol === "vnc" && connected ? clientRef.current?.desktopSize() : undefined}
          viewOnly={effectiveViewOnly}
          onViewOnly={setViewOnly}
          captured={captured}
          onReleaseKeyboard={releaseKeyboardNow}
          fullscreen={fullscreen}
          canFullscreen={typeof document !== "undefined" && document.fullscreenEnabled === true}
          onFullscreen={toggleFullscreen}
          touch={touch}
          touchMode={touchMode}
          onTouchMode={setTouchMode}
          onDisconnect={session.disconnect}
          onPinnedChange={setPinned}
          onReveal={reveal}
        />
        {resolutionNotice && connected && (
          <div
            role="alert"
            className="absolute bottom-3 left-1/2 z-20 flex max-w-[calc(100%-1.5rem)] -translate-x-1/2 items-center gap-2 rounded-lg bg-neutral-800/95 px-3 py-2 text-[12px] text-neutral-100"
            data-testid="remote-resolution-notice"
          >
            <span>{resolutionNotice}</span>
            <button type="button" className="nx-btn nx-btn-primary shrink-0" onClick={session.connect}>
              {t("重新连接")}
            </button>
            <button type="button" className="nx-btn nx-btn-outline shrink-0" onClick={() => setResolutionNotice(null)}>
              {t("忽略")}
            </button>
          </div>
        )}
        {notice && connected && (
          <div
            role="status"
            className="absolute bottom-12 left-1/2 z-20 max-w-[calc(100%-1.5rem)] -translate-x-1/2 rounded-lg bg-neutral-800/95 px-3 py-1.5 text-[12px] text-neutral-100"
            data-testid="remote-clipboard-notice"
          >
            {notice}
          </div>
        )}
        {hint && (
          <div
            role="status"
            className="pointer-events-none absolute bottom-3 left-1/2 z-20 -translate-x-1/2 rounded-full bg-neutral-800/90 px-3 py-1 text-[12px] text-neutral-100"
            data-testid="remote-capture-hint"
          >
            {t("键盘已交给远程桌面，按 {RELEASE_CHORD_LABEL} 释放", { RELEASE_CHORD_LABEL })}
          </div>
        )}
        {effectiveViewOnly && connected && (
          <div className="pointer-events-none absolute top-2 right-2 z-20 rounded bg-neutral-800/90 px-2 py-0.5 text-[11.5px] text-neutral-200">{t("只读")}</div>
        )}
        {session.status === "connecting" && (
          <div className="absolute inset-0 z-20 flex flex-col items-center justify-center gap-2 text-[13px] text-neutral-300" role="status" data-testid="remote-connecting">
            <IconLoader size={18} className="animate-spin" />
            {t("正在连接远程桌面…")}
          </div>
        )}
        {session.status === "disconnected" && session.failure && (
          <DisconnectOverlay failure={session.failure} retryIn={session.retryIn} attempt={session.attempt} reconnectPaused={session.reconnectPaused} onReconnect={session.connect} onClose={closeTab} onWake={session.wake.available ? () => wakeAsset(assetId, session.wake.name) : undefined} />
        )}
      </div>
      {touch && connected && !effectiveViewOnly && (
        <RemoteKeysBar latched={latched} onToggleLatch={toggleLatch} onKey={sendBarKey} onKeyboard={openSoftKeyboard} keyboardActive={keyboardOpen} />
      )}
      {touch && (
        <input
          ref={keyboardInputRef}
          aria-label={t("远程键盘输入")}
          className="pointer-events-none fixed bottom-0 left-0 h-px w-px opacity-0"
          autoCapitalize="off"
          autoComplete="off"
          autoCorrect="off"
          spellCheck={false}
          onInput={onSoftKeyboardInput}
          onKeyDown={onSoftKeyboardKeyDown}
          onBlur={() => setKeyboardOpen(false)}
        />
      )}
    </div>
  );
}
