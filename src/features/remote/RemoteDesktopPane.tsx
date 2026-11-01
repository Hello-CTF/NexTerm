import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { requestCloseTab } from "../../app/store";
import { captureKeyboard, isReleaseChord, RELEASE_CHORD_LABEL, releaseKeyboard } from "../../app/inputCapture";
import { isTouchShell } from "../../app/shell/shellMode";
import { IconLoader } from "../../ui/icons";
import type { KeyCombo } from "./keys";
import { RemoteKeysBar } from "./RemoteKeysBar";
import { RemoteToolbar, type ResolutionChoice } from "./RemoteToolbar";
import { RemoteTouchLayer } from "./RemoteTouchLayer";
import { useRemoteSession } from "./useRemoteSession";
import { WAKE_DISCLAIMER, wakeAsset } from "../explorer/wakeAction";
import type { Failure } from "./connectFlow";
import type { ModifierKey, ScaleMode } from "./remoteTypes";
import type { TouchMode } from "./touchGestures";

const TOOLBAR_HIDE_MS = 2500;
const TOOLBAR_REVEAL_ZONE_PX = 56;
const RESIZE_DEBOUNCE_MS = 300;
const HINT_MS = 3500;

function failureTitle(failure: Failure): string {
  switch (failure.kind) {
    case "auth":
      return "认证失败";
    case "certificate":
      return "服务器证书未确认";
    case "permission":
      return "没有权限连接";
    case "ended":
      return "远程桌面已断开";
    case "retryable":
      return "连接已中断";
    default:
      return "无法连接远程桌面";
  }
}

function DisconnectOverlay({
  failure,
  retryIn,
  attempt,
  onReconnect,
  onClose,
  onWake,
}: {
  failure: Failure;
  retryIn: number | null;
  attempt: number;
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
        {failureTitle(failure)}
      </div>
      <div className="max-w-md text-[12.5px] break-words text-neutral-300">{failure.message}</div>
      {retryIn !== null && (
        <div className="text-[12.5px] text-neutral-300" role="status" data-testid="remote-retry-countdown">
          {retryIn} 秒后自动重新连接（第 {attempt + 1} 次）
        </div>
      )}
      {never && <div className="text-[11.5px] text-neutral-400">这类失败不会自动重试，请处理后手动重新连接。</div>}
      <div className="flex items-center gap-2">
        <button type="button" className="nx-btn nx-btn-primary" onClick={onReconnect}>
          重新连接
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
            唤醒
          </button>
        )}
        <button type="button" className="nx-btn nx-btn-outline" onClick={onClose}>
          关闭
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
  const keyboardInputRef = useRef<HTMLInputElement>(null);
  const session = useRemoteSession(tabId, assetId, viewportRef);
  const { clientRef, protocol, options } = session;
  const touch = isTouchShell();

  const [captured, setCaptured] = useState(false);
  const [hint, setHint] = useState(false);
  const [toolbarShown, setToolbarShown] = useState(true);
  const [pinned, setPinned] = useState(false);
  const [scale, setScale] = useState<ScaleMode>("fit");
  const [viewOnly, setViewOnly] = useState<boolean | null>(null);
  const [resolution, setResolution] = useState<ResolutionChoice | null>(null);
  const [held, setHeld] = useState<ReadonlySet<ModifierKey>>(new Set());
  const [latched, setLatched] = useState<ReadonlySet<ModifierKey>>(new Set());
  const [touchMode, setTouchMode] = useState<TouchMode>("direct");
  const [fullscreen, setFullscreen] = useState(false);
  const [keyboardOpen, setKeyboardOpen] = useState(false);
  const hideTimer = useRef<number | null>(null);
  const hintTimer = useRef<number | null>(null);

  const effectiveViewOnly = viewOnly ?? options.viewOnly;
  const effectiveResolution = useMemo<ResolutionChoice>(
    () => resolution ?? (options.resolution === "fixed" ? { width: options.width, height: options.height } : "follow"),
    [resolution, options.resolution, options.width, options.height],
  );
  const connected = session.status === "connected";

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
    clientRef.current?.releaseAllKeys();
    setHeld(new Set());
    setLatched(new Set());
  }, [clientRef]);

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
    const inside = (node: EventTarget | null) => node instanceof Node && viewportRef.current?.contains(node) === true;
    const onFocusIn = (event: FocusEvent) => {
      if (!inside(event.target) || effectiveViewOnly || !connected) return;
      captureKeyboard(tabId);
      setCaptured(true);
      setHint(true);
      if (hintTimer.current !== null) window.clearTimeout(hintTimer.current);
      hintTimer.current = window.setTimeout(() => setHint(false), HINT_MS);
    };
    const onFocusOut = (event: FocusEvent) => {
      if (!inside(event.target) || inside(event.relatedTarget)) return;
      clientRef.current?.releaseAllKeys();
      setHeld(new Set());
      releaseKeyboard(tabId);
      setCaptured(false);
      setHint(false);
    };
    const onKeyDown = (event: KeyboardEvent) => {
      if (!isReleaseChord(event)) return;
      event.preventDefault();
      event.stopPropagation();
      releaseKeyboardNow();
    };
    wrap.addEventListener("focusin", onFocusIn);
    wrap.addEventListener("focusout", onFocusOut);
    // Capture phase: the chord must be seen before the remote client's own key handling.
    wrap.addEventListener("keydown", onKeyDown, true);
    return () => {
      wrap.removeEventListener("focusin", onFocusIn);
      wrap.removeEventListener("focusout", onFocusOut);
      wrap.removeEventListener("keydown", onKeyDown, true);
    };
  }, [tabId, effectiveViewOnly, connected, clientRef, releaseKeyboardNow]);

  useEffect(() => {
    const onBlur = () => releaseHeld();
    window.addEventListener("blur", onBlur);
    return () => window.removeEventListener("blur", onBlur);
  }, [releaseHeld]);

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

  // Resolution follows the pane (RDP): after the pane settles, ask the server for a matching desktop.
  useEffect(() => {
    const viewport = viewportRef.current;
    if (!viewport || protocol !== "rdp" || !connected) return;
    let timer: number | null = null;
    const apply = () => {
      const client = clientRef.current;
      if (!client || !client.supportsResize) return;
      if (effectiveResolution === "follow") {
        const zoom = typeof scale === "number" ? scale : 1;
        if (viewport.clientWidth > 1 && viewport.clientHeight > 1) client.resize(viewport.clientWidth / zoom, viewport.clientHeight / zoom);
      } else {
        client.resize(effectiveResolution.width, effectiveResolution.height);
      }
    };
    const observer = new ResizeObserver(() => {
      if (timer !== null) window.clearTimeout(timer);
      timer = window.setTimeout(apply, RESIZE_DEBOUNCE_MS);
    });
    observer.observe(viewport);
    apply();
    return () => {
      observer.disconnect();
      if (timer !== null) window.clearTimeout(timer);
    };
  }, [protocol, connected, effectiveResolution, scale, clientRef]);

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
    void withLatches(() => client.sendText(text));
  };

  const openSoftKeyboard = () => {
    const input = keyboardInputRef.current;
    if (!input) return;
    // Focus inside the tap itself: browsers only open the soft keyboard for focus that a gesture caused.
    if (document.activeElement === input) {
      input.blur();
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
    if (stage instanceof HTMLElement) stage.scrollBy(-dx, -dy);
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
        className={`relative min-h-0 flex-1 overflow-hidden border-2 ${captured ? "border-[var(--color-accent)]" : "border-transparent"}`}
        data-captured={captured ? "true" : "false"}
      >
        <div ref={viewportRef} className="absolute inset-0" onPointerDownCapture={focusScreen} />
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
          aria-label="显示工具栏"
          title="显示工具栏"
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
          onSendClipboard={() => void clientRef.current?.sendClipboard()}
          onFetchClipboard={() => void clientRef.current?.fetchClipboard()}
          resolution={effectiveResolution}
          onResolution={setResolution}
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
        {hint && (
          <div
            role="status"
            className="pointer-events-none absolute bottom-3 left-1/2 z-20 -translate-x-1/2 rounded-full bg-neutral-800/90 px-3 py-1 text-[12px] text-neutral-100"
            data-testid="remote-capture-hint"
          >
            键盘已交给远程桌面，按 {RELEASE_CHORD_LABEL} 释放
          </div>
        )}
        {effectiveViewOnly && connected && (
          <div className="pointer-events-none absolute top-2 right-2 z-20 rounded bg-neutral-800/90 px-2 py-0.5 text-[11.5px] text-neutral-200">只读</div>
        )}
        {session.status === "connecting" && (
          <div className="absolute inset-0 z-20 flex flex-col items-center justify-center gap-2 text-[13px] text-neutral-300" role="status" data-testid="remote-connecting">
            <IconLoader size={18} className="animate-spin" />
            正在连接远程桌面…
          </div>
        )}
        {session.status === "disconnected" && session.failure && (
          <DisconnectOverlay failure={session.failure} retryIn={session.retryIn} attempt={session.attempt} onReconnect={session.connect} onClose={closeTab} onWake={session.wake.available ? () => wakeAsset(assetId, session.wake.name) : undefined} />
        )}
      </div>
      {touch && connected && !effectiveViewOnly && (
        <RemoteKeysBar latched={latched} onToggleLatch={toggleLatch} onKey={sendBarKey} onKeyboard={openSoftKeyboard} keyboardActive={keyboardOpen} />
      )}
      {touch && (
        <input
          ref={keyboardInputRef}
          aria-label="远程键盘输入"
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
