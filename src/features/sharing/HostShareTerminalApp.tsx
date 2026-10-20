import { useCallback, useEffect, useRef, useState } from "react";
import {
  createHostShareConnection,
  type HostShareConnection,
  type HostShareFailure,
  type HostShareReady,
} from "../../ipc/hostShareApi";
import { describeError } from "../../ui/errorText";
import { PublicShareTerminal, type PublicShareTerminalHandle } from "./PublicShareTerminal";

type HostSharePhase =
  | { kind: "connecting" }
  | { kind: "live"; ready: HostShareReady }
  | { kind: "error"; title: string; detail: string; retryable: boolean }
  | { kind: "ended" }
  | { kind: "disconnected" };

interface ErrorCopy {
  title: string;
  detail: string;
  retryable: boolean;
}

const CONNECT_FAILED: ErrorCopy = {
  title: "无法连接共享终端",
  detail: "分享可能已过期或已吊销，也可能是设备离线或登录状态已失效。请稍后重试。",
  retryable: true,
};

function errorCopy(failure: HostShareFailure): ErrorCopy {
  switch (failure.code) {
    case "expired":
      return { title: "主机分享已过期", detail: "请让分享者重新发起分享。", retryable: false };
    case "forbidden":
    case "not_found":
      return { title: "主机分享不可用", detail: failure.message || "分享已吊销，或当前账号没有访问权限。", retryable: false };
    case "disconnected":
      return { title: "共享终端连接已断开", detail: failure.message || "设备可能离线，请恢复连接后重试。", retryable: true };
    case "read_only":
      return { title: "此分享为只读", detail: failure.message || "输入已被禁用，仅可查看输出。", retryable: false };
    default:
      return { title: "共享终端暂时不可用", detail: failure.message || "请稍后重试。", retryable: false };
  }
}

export interface HostShareTerminalAppProps {
  deviceId: string;
  deviceName: string;
  ownerUsername: string;
  onClose: () => void;
}

export function HostShareTerminalApp({ deviceId, deviceName, ownerUsername, onClose }: HostShareTerminalAppProps) {
  const [phase, setPhase] = useState<HostSharePhase>({ kind: "connecting" });
  const [attempt, setAttempt] = useState(0);
  const phaseRef = useRef<HostSharePhase>(phase);
  phaseRef.current = phase;
  const terminalRef = useRef<PublicShareTerminalHandle | null>(null);
  const connectionRef = useRef<HostShareConnection | null>(null);

  useEffect(() => {
    let disposed = false;
    let ready = false;
    try {
      const connection = createHostShareConnection(deviceId, {
        onReady: (info) => {
          if (disposed) return;
          ready = true;
          setPhase({ kind: "live", ready: info });
        },
        onOutput: (bytes) => {
          if (!disposed) terminalRef.current?.write(bytes);
        },
        onError: (failure) => {
          if (!disposed) setPhase({ kind: "error", ...errorCopy(failure) });
        },
        onClose: (info) => {
          if (disposed) return;
          if (ready) {
            setPhase((current) => {
              if (current.kind === "error" || current.kind === "ended") return current;
              return info.wasClean && info.code === 1000 ? { kind: "ended" } : { kind: "disconnected" };
            });
          } else {
            setPhase((current) => current.kind === "error" ? current : { kind: "error", ...CONNECT_FAILED });
          }
        },
      });
      connectionRef.current = connection;
    } catch (error) {
      setPhase({
        kind: "error",
        title: "无法连接共享终端",
        detail: describeError(error),
        retryable: true,
      });
    }
    return () => {
      disposed = true;
      connectionRef.current?.close();
      connectionRef.current = null;
    };
  }, [deviceId, attempt]);

  const handleInput = useCallback((data: string) => {
    const current = phaseRef.current;
    if (current.kind !== "live" || current.ready.permission !== "read_write") return;
    connectionRef.current?.sendInput(data);
  }, []);

  const handleTerminal = useCallback((handle: PublicShareTerminalHandle) => {
    terminalRef.current = handle;
  }, []);

  const retry = useCallback(() => {
    terminalRef.current?.reset();
    setPhase({ kind: "connecting" });
    setAttempt((value) => value + 1);
  }, []);

  const writable = phase.kind === "live" && phase.ready.permission === "read_write";

  return (
    <div className="nx-fullscreen-safe-bottom fixed inset-0 z-50 flex flex-col bg-[#101217] text-neutral-200" role="dialog" aria-modal="true" aria-label="共享主机终端">
      <header className="flex flex-wrap items-center gap-x-3 gap-y-1 border-b border-neutral-800 px-3 py-2">
        <span className="min-w-0 truncate text-sm font-medium text-neutral-100">共享终端 · {deviceName}</span>
        <span className="nx-hint text-xs">来自 {ownerUsername}</span>
        {phase.kind === "live" && (
          <span className={`nx-badge shrink-0 ${writable ? "nx-badge-green" : ""}`}>{writable ? "可读写" : "只读"}</span>
        )}
        <div className="nx-spacer" />
        <button type="button" className="nx-btn nx-btn-outline nx-btn-sm shrink-0" onClick={onClose}>关闭</button>
      </header>
      <main className="relative min-h-0 flex-1">
        <PublicShareTerminal onInput={handleInput} onHandle={handleTerminal} readOnly={!writable} />
        {phase.kind !== "live" && (
          <div className="absolute inset-0 flex items-center justify-center bg-[#101217]/95 p-4">
            <div className="w-full max-w-sm text-center">
              {phase.kind === "connecting" && <p className="text-sm text-neutral-400">正在连接共享终端…</p>}
              {phase.kind === "error" && (
                <>
                  <p className="text-base font-medium text-neutral-100">{phase.title}</p>
                  <p className="mt-2 text-sm text-neutral-400">{phase.detail}</p>
                  {phase.retryable && <button type="button" className="nx-btn nx-btn-primary mt-4" onClick={retry}>重新连接</button>}
                </>
              )}
              {phase.kind === "ended" && (
                <>
                  <p className="text-base font-medium text-neutral-100">共享终端已结束</p>
                  <p className="mt-2 text-sm text-neutral-400">终端已关闭。需要继续使用时，可以重新打开。</p>
                </>
              )}
              {phase.kind === "disconnected" && (
                <>
                  <p className="text-base font-medium text-neutral-100">连接已断开</p>
                  <p className="mt-2 text-sm text-neutral-400">网络波动、设备离线或账号状态变化导致连接中断。</p>
                  <button type="button" className="nx-btn nx-btn-primary mt-4" onClick={retry}>重新连接</button>
                </>
              )}
            </div>
          </div>
        )}
      </main>
    </div>
  );
}
