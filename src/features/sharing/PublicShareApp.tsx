import { useCallback, useEffect, useRef, useState } from "react";
import {
  PublicShareConnection,
  type PublicShareFailure,
  type PublicShareReady,
} from "../../ipc/publicShareApi";
import { PublicShareTerminal, type PublicShareTerminalHandle } from "./PublicShareTerminal";

type SharePhase =
  | { kind: "connecting" }
  | { kind: "live"; ready: PublicShareReady }
  | { kind: "error"; title: string; detail: string; retryable: boolean }
  | { kind: "ended" }
  | { kind: "disconnected" };

interface ErrorCopy {
  title: string;
  detail: string;
  retryable: boolean;
}

const CONNECT_FAILED_COPY: ErrorCopy = {
  title: "无法连接到分享",
  detail: "链接无效、已过期或已被吊销，也可能是网络不通。请核对链接完整后重试。",
  retryable: true,
};

function errorCopy(failure: PublicShareFailure): ErrorCopy {
  switch (failure.code) {
    case "expired":
      return { title: "分享链接已过期", detail: "请让分享者重新生成链接后再打开。", retryable: false };
    case "not_found":
      return { title: "分享链接无效或已吊销", detail: "请核对链接是否完整，或向分享者确认分享状态。", retryable: false };
    case "disconnected":
      return { title: "设备当前离线", detail: "被分享的设备代理不在线，恢复后可重新连接。", retryable: true };
    case "read_only":
      return { title: "此分享为只读", detail: failure.message || "输入已被禁用，仅可查看输出。", retryable: false };
    case "forbidden":
      return { title: "没有访问权限", detail: failure.message || "请向分享者确认你的访问权限。", retryable: false };
    default:
      return {
        title: failure.message || "分享暂时不可用",
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

export function PublicShareApp({ token }: { token: string }) {
  const [phase, setPhase] = useState<SharePhase>({ kind: "connecting" });
  const [attempt, setAttempt] = useState(0);
  const [copyHint, setCopyHint] = useState<string | null>(null);
  const terminalRef = useRef<PublicShareTerminalHandle | null>(null);
  const connectionRef = useRef<PublicShareConnection | null>(null);
  const phaseRef = useRef<SharePhase>(phase);
  phaseRef.current = phase;
  const copyHintTimerRef = useRef<number | null>(null);

  useEffect(() => {
    let ready = false;
    let disposed = false;
    const connection = new PublicShareConnection(token, {
      onReady: (info) => {
        ready = true;
        setPhase({ kind: "live", ready: info });
      },
      onOutput: (bytes) => terminalRef.current?.write(bytes),
      onError: (failure) => setPhase({ kind: "error", ...errorCopy(failure) }),
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
  }, [token, attempt]);

  useEffect(() => {
    return () => {
      if (copyHintTimerRef.current !== null) window.clearTimeout(copyHintTimerRef.current);
    };
  }, []);

  const handleInput = useCallback((data: string) => {
    const connection = connectionRef.current;
    const current = phaseRef.current;
    if (!connection) return;
    if (current.kind !== "live" || current.ready.permission !== "read_write") return;
    connection.sendInput(data);
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
  const expiry = phase.kind === "live" ? formatExpiry(phase.ready.expiresAt) : null;

  return (
    <div className="flex h-dvh w-full flex-col overflow-hidden bg-[#101217] text-neutral-200">
      <header className="flex flex-wrap items-center gap-x-3 gap-y-1 border-b border-neutral-800 px-3 py-2">
        <span className="text-sm font-medium text-neutral-100">NexTerm 公开分享</span>
        {phase.kind === "live" && (
          <span
            className={
              writable
                ? "rounded-full bg-emerald-900/60 px-2 py-0.5 text-xs text-emerald-300"
                : "rounded-full bg-neutral-800 px-2 py-0.5 text-xs text-neutral-400"
            }
          >
            {writable ? "可读写" : "只读"}
          </span>
        )}
        {expiry && <span className="text-xs text-neutral-500">{expiry}</span>}
      </header>
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
                  <p className="text-sm text-neutral-400">正在连接分享终端…</p>
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
                      className="mt-4 rounded-md bg-indigo-600 px-4 py-2 text-sm text-white hover:bg-indigo-500"
                      onClick={retry}
                    >
                      重新连接
                    </button>
                  )}
                </>
              )}
              {phase.kind === "ended" && (
                <>
                  <p className="text-base font-medium text-neutral-100">分享已结束</p>
                  <p className="mt-2 text-sm text-neutral-400">会话已被分享者关闭或退出。如需继续查看，请让分享者重新发起分享。</p>
                </>
              )}
              {phase.kind === "disconnected" && (
                <>
                  <p className="text-base font-medium text-neutral-100">连接已断开</p>
                  <p className="mt-2 text-sm text-neutral-400">网络波动或设备离线导致连接中断，可尝试重新连接。</p>
                  <button
                    type="button"
                    className="mt-4 rounded-md bg-indigo-600 px-4 py-2 text-sm text-white hover:bg-indigo-500"
                    onClick={retry}
                  >
                    重新连接
                  </button>
                </>
              )}
            </div>
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
