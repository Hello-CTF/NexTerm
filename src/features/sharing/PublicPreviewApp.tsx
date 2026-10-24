import { useCallback, useEffect, useRef, useState } from "react";
import {
  PreviewConnection,
  type PreviewControlState,
  type PreviewFailure,
  type PreviewReady,
  type PreviewSnapshot,
} from "./previewApi";
import { PublicShareTerminal, type PublicShareTerminalHandle } from "./PublicShareTerminal";

type PreviewPhase =
  | { kind: "connecting" }
  | { kind: "live"; ready: PreviewReady }
  | { kind: "error"; title: string; detail: string; retryable: boolean }
  | { kind: "ended" }
  | { kind: "disconnected" };

interface ErrorCopy {
  title: string;
  detail: string;
  retryable: boolean;
}

const CONNECT_FAILED_COPY: ErrorCopy = {
  title: "无法连接预览终端",
  detail: "链接可能无效、已过期或已吊销，也可能是网络异常。请核对链接是否完整后重试。",
  retryable: true,
};

function errorCopy(failure: PreviewFailure): ErrorCopy {
  switch (failure.code) {
    case "expired":
      return { title: "预览链接已过期", detail: "请让分享者重新生成链接后再打开。", retryable: false };
    case "revoked":
      return { title: "预览链接已吊销", detail: "分享者已停止本次分享，可向其确认是否重新分享。", retryable: false };
    case "not_found":
      return { title: "预览链接无效", detail: "请核对链接是否完整，或向分享者确认预览状态。", retryable: false };
    case "ended":
      return { title: "预览已结束", detail: "会话已被分享者关闭或退出。", retryable: false };
    default:
      return {
        title: failure.message || "预览暂时不可用",
        detail: "请稍后刷新页面重试。",
        retryable: false,
      };
  }
}

function formatExpiry(expiresAt: number): string | null {
  if (!Number.isFinite(expiresAt) || expiresAt <= 0) return null;
  const date = new Date(expiresAt);
  if (Number.isNaN(date.getTime())) return null;
  return `有效期至 ${date.toLocaleString()}`;
}

// snapshotEscape renders the current screen snapshot into a blank terminal: text lines spread from the top-left, then
// the cursor moves to the snapshot position. The snapshot carries no character attributes (the server screen model stores text only).
function snapshotEscape(snapshot: PreviewSnapshot): string {
  const text = snapshot.lines.join("\r\n");
  const row = Math.max(0, Math.trunc(snapshot.cursorRow)) + 1;
  const col = Math.max(0, Math.trunc(snapshot.cursorCol)) + 1;
  return `${text}\x1b[${row};${col}H`;
}

function controlBadge(writable: boolean, controlState: PreviewControlState): { text: string; tone: string } {
  if (!writable) return { text: "只读", tone: "bg-sky-900/60 text-sky-300" };
  if (controlState === "guest") return { text: "可控制", tone: "bg-emerald-900/60 text-emerald-300" };
  if (controlState === "owner") return { text: "分享者控制中", tone: "bg-amber-900/60 text-amber-300" };
  return { text: "另一访客控制中", tone: "bg-amber-900/60 text-amber-300" };
}

export function PublicPreviewApp({ token }: { token: string }) {
  const [phase, setPhase] = useState<PreviewPhase>({ kind: "connecting" });
  const [attempt, setAttempt] = useState(0);
  const [copyHint, setCopyHint] = useState<string | null>(null);
  const [controlState, setControlState] = useState<PreviewControlState>("guest");
  const [readOnlyHint, setReadOnlyHint] = useState<string | null>(null);
  const terminalRef = useRef<PublicShareTerminalHandle | null>(null);
  const connectionRef = useRef<PreviewConnection | null>(null);
  const phaseRef = useRef<PreviewPhase>(phase);
  phaseRef.current = phase;
  const copyHintTimerRef = useRef<number | null>(null);
  const readOnlyHintedRef = useRef(false);
  const readOnlyHintTimerRef = useRef<number | null>(null);

  const showReadOnlyHint = useCallback((message: string) => {
    if (readOnlyHintedRef.current) return;
    readOnlyHintedRef.current = true;
    setReadOnlyHint(message || "输入未被接受：当前没有控制权");
    readOnlyHintTimerRef.current = window.setTimeout(() => {
      readOnlyHintTimerRef.current = null;
      setReadOnlyHint(null);
    }, 3000);
  }, []);

  useEffect(() => {
    let ready = false;
    let disposed = false;
    const connection = new PreviewConnection(token, {
      onReady: (info) => {
        ready = true;
        readOnlyHintedRef.current = false;
        setControlState("guest");
        setPhase({ kind: "live", ready: info });
      },
      onSnapshot: (snapshot) => {
        terminalRef.current?.reset();
        terminalRef.current?.write(new TextEncoder().encode(snapshotEscape(snapshot)));
      },
      onOutput: (bytes) => terminalRef.current?.write(bytes),
      onControl: (control) => {
        if (control.state === "guest") readOnlyHintedRef.current = false;
        setControlState(control.state);
      },
      onError: (failure) => {
        if (failure.code === "read_only") {
          showReadOnlyHint(failure.message);
          return;
        }
        setPhase({ kind: "error", ...errorCopy(failure) });
      },
      onClose: (info) => {
        if (disposed) return;
        if (ready) {
          setPhase((current) => {
            if (current.kind === "error" || current.kind === "ended") return current;
            return info.wasClean && info.code === 1000 ? { kind: "ended" } : { kind: "disconnected" };
          });
        } else {
          setPhase((current) => (current.kind === "error" ? current : { kind: "error", ...CONNECT_FAILED_COPY }));
        }
      },
    });
    connectionRef.current = connection;
    return () => {
      disposed = true;
      connectionRef.current = null;
      connection.close();
    };
  }, [token, attempt, showReadOnlyHint]);

  useEffect(() => {
    return () => {
      if (copyHintTimerRef.current !== null) window.clearTimeout(copyHintTimerRef.current);
      if (readOnlyHintTimerRef.current !== null) window.clearTimeout(readOnlyHintTimerRef.current);
    };
  }, []);

  // Input is gated purely by permission: when live and read_write, keys always go out via sendInput as binary frames,
  // arbitrated frame by frame by the server, with control frames refreshing badges and hints; keys under read permission
  // are dropped on the spot and never enter any send path.
  const handleInput = useCallback((data: string) => {
    const connection = connectionRef.current;
    const current = phaseRef.current;
    if (!connection) return;
    if (current.kind !== "live" || current.ready.permission !== "read_write") return;
    connection.sendInput(new TextEncoder().encode(data));
  }, []);

  const handleSelectionCopy = useCallback((text: string, error: unknown | null) => {
    if (copyHintTimerRef.current !== null) window.clearTimeout(copyHintTimerRef.current);
    setCopyHint(error ? "复制失败，请长按或右键手动复制" : `已复制 ${text.length} 个字符`);
    copyHintTimerRef.current = window.setTimeout(() => {
      copyHintTimerRef.current = null;
      setCopyHint(null);
    }, 1500);
  }, []);

  const handleTerminal = useCallback((handle: PublicShareTerminalHandle) => {
    terminalRef.current = handle;
  }, []);

  const retry = useCallback(() => {
    setPhase({ kind: "connecting" });
    setAttempt((value) => value + 1);
  }, []);

  const writable = phase.kind === "live" && phase.ready.permission === "read_write";
  const badge = phase.kind === "live" ? controlBadge(writable, controlState) : null;
  const expiry = phase.kind === "live" ? formatExpiry(phase.ready.expiresAt) : null;

  return (
    <div className="flex h-dvh w-full flex-col overflow-hidden bg-[#101217] text-neutral-200">
      <header className="flex flex-wrap items-center gap-x-3 gap-y-1 border-b border-neutral-800 px-3 py-2">
        <span className="text-sm font-medium text-neutral-100">NexTerm 预览</span>
        {badge && <span className={`rounded-full px-2 py-0.5 text-xs ${badge.tone}`}>{badge.text}</span>}
        {expiry && <span className="text-xs text-neutral-500">{expiry}</span>}
      </header>
      {phase.kind === "live" && writable && controlState !== "guest" && (
        <div className="border-b border-amber-900/50 bg-amber-950/70 px-3 py-1.5 text-xs text-amber-200">
          {controlState === "owner"
            ? "分享者正在控制终端，控制权归还前你只能观看"
            : "另一位访客正在控制终端，控制权归还前你只能观看"}
        </div>
      )}
      <main className="relative min-h-0 flex-1">
        <PublicShareTerminal
          onInput={handleInput}
          onSelectionCopy={handleSelectionCopy}
          onHandle={handleTerminal}
        />
        {phase.kind !== "live" && (
          <div className="absolute inset-0 flex items-center justify-center bg-[#101217]/95 p-4">
            <div className="w-full max-w-sm text-center">
              {phase.kind === "connecting" && (
                <>
                  <p className="text-sm text-neutral-400">正在连接预览终端…</p>
                  <p className="mt-2 text-xs text-neutral-600">首次连接可能需要几秒</p>
                </>
              )}
              {phase.kind === "error" && (
                <>
                  <p className="text-base font-medium text-neutral-100">{phase.title}</p>
                  <p className="mt-2 text-sm text-neutral-400">{phase.detail}</p>
                  {phase.retryable && (
                    <button
                      type="button"
                      className="mt-4 rounded-md bg-indigo-600 px-4 py-2 text-sm min-h-10 text-white hover:bg-indigo-500"
                      onClick={retry}
                    >
                      重新连接
                    </button>
                  )}
                </>
              )}
              {phase.kind === "ended" && (
                <>
                  <p className="text-base font-medium text-neutral-100">预览已结束</p>
                  <p className="mt-2 text-sm text-neutral-400">会话已被分享者关闭或退出。如需继续查看，请让分享者重新发起预览。</p>
                </>
              )}
              {phase.kind === "disconnected" && (
                <>
                  <p className="text-base font-medium text-neutral-100">连接已断开</p>
                  <p className="mt-2 text-sm text-neutral-400">网络波动导致连接中断，可尝试重新连接。</p>
                  <button
                    type="button"
                    className="mt-4 rounded-md bg-indigo-600 px-4 py-2 text-sm min-h-10 text-white hover:bg-indigo-500"
                    onClick={retry}
                  >
                    重新连接
                  </button>
                </>
              )}
            </div>
          </div>
        )}
        {readOnlyHint && (
          <div className="pointer-events-none absolute bottom-10 left-1/2 -translate-x-1/2 rounded-full bg-amber-900/95 px-3 py-1 text-xs text-amber-100 shadow-lg">
            {readOnlyHint}
          </div>
        )}
        {copyHint && (
          <div className="pointer-events-none absolute bottom-3 left-1/2 -translate-x-1/2 rounded-full bg-neutral-800/95 px-3 py-1 text-xs text-neutral-200 shadow-lg">
            {copyHint}
          </div>
        )}
      </main>
    </div>
  );
}
