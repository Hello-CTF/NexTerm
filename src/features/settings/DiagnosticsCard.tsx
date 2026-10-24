import { useCallback, useEffect, useRef, useState } from "react";
import { diagnosticsApi, type DiagnosticsCapture, type DiagnosticsStatus } from "../../ipc/diagnosticsApi";
import { toAppError } from "../../ipc/commands";
import { describeError } from "../../ui/errorText";
import { formatBytes } from "../../ui/format";
import { IconActivity, IconArchive, IconFolderOpen, IconLoader } from "../../ui/icons";

function formatTime(timeMs: number): string {
  return timeMs > 0 ? new Date(timeMs).toLocaleString() : "";
}

export function DiagnosticsCard() {
  const [status, setStatus] = useState<DiagnosticsStatus | null>(null);
  const [forbidden, setForbidden] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [packed, setPacked] = useState<Record<string, string>>({});
  const mounted = useRef(true);

  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);

  const fail = useCallback((e: unknown) => {
    if (!mounted.current) return;
    if (toAppError(e).code === "forbidden") {
      setForbidden(true);
      return;
    }
    setError(describeError(e));
  }, []);

  useEffect(() => {
    void (async () => {
      try {
        const next = await diagnosticsApi.status();
        if (mounted.current) setStatus(next);
      } catch (e) {
        fail(e);
      }
    })();
  }, [fail]);

  const toggle = async (armed: boolean) => {
    setBusy(true);
    setError(null);
    try {
      const next = await diagnosticsApi.arm(armed);
      if (mounted.current) setStatus(next);
    } catch (e) {
      fail(e);
    } finally {
      if (mounted.current) setBusy(false);
    }
  };

  const openFolder = async (path: string) => {
    setError(null);
    try {
      await diagnosticsApi.openFolder(path);
    } catch (e) {
      fail(e);
    }
  };

  const pack = async (capture: DiagnosticsCapture) => {
    setBusy(true);
    setError(null);
    try {
      const result = await diagnosticsApi.pack(capture.path);
      if (mounted.current) setPacked((prev) => ({ ...prev, [capture.name]: result.path }));
      const next = await diagnosticsApi.status();
      if (mounted.current) setStatus(next);
    } catch (e) {
      fail(e);
    } finally {
      if (mounted.current) setBusy(false);
    }
  };

  // Non-admin accounts on a server cannot use these commands; keep the navigation target but say why it is empty.
  if (forbidden) {
    return (
      <section className="nx-card" aria-label="诊断与调试">
        <div className="mb-1 flex flex-wrap items-center gap-2">
          <IconActivity size={15} className="text-neutral-400" />
          <span className="nx-card-title">诊断与调试</span>
        </div>
        <div className="nx-hint">仅管理员可以使用调试模式。</div>
      </section>
    );
  }

  const captures = status?.captures ?? [];
  const canOpen = status?.canOpenFolder ?? false;
  const activeDir = status?.active ? status.activeDir ?? "" : "";

  return (
    <section className="nx-card" aria-label="诊断与调试">
      <div className="mb-1 flex flex-wrap items-center gap-2">
        <IconActivity size={15} className="text-neutral-400" />
        <span className="nx-card-title">诊断与调试</span>
      </div>

      {status?.active && (
        <div className="nx-alert mb-3" role="status">
          <div className="font-medium">本次启动正在记录调试信息</div>
          <div className="mt-1 break-all text-[12px]">{activeDir}</div>
          <div className="mt-2 flex flex-wrap gap-2">
            {canOpen && (
              <button type="button" className="nx-btn nx-btn-outline nx-btn-sm" onClick={() => void openFolder(activeDir)}>
                <IconFolderOpen size={12} />
                打开文件夹
              </button>
            )}
            <button
              type="button"
              className="nx-btn nx-btn-outline nx-btn-sm"
              disabled={busy}
              onClick={() => {
                const current = captures.find((c) => c.path === activeDir);
                if (current) void pack(current);
              }}
            >
              <IconArchive size={12} />
              打包
            </button>
          </div>
        </div>
      )}

      <label className="flex cursor-pointer items-start gap-3">
        <input
          type="checkbox"
          className="mt-0.5 h-4 w-4 shrink-0"
          checked={status?.armed ?? false}
          disabled={status === null || busy}
          onChange={(e) => void toggle(e.target.checked)}
        />
        <span className="min-w-0 flex-1">
          <span className="block text-[13px] text-neutral-100">下次启动时开启调试模式（仅一次）</span>
          <span className="nx-hint mt-1 block">
            开启后，下次启动应用时会把性能剖析数据、调试日志和系统信息（版本、系统、CPU 核数等）保存到本机文件夹，
            方便打包发给我们排查问题。日志已对密码、令牌、私钥做脱敏，不记录终端输出和输入的命令；但仍可能包含主机名或地址、
            用户名、文件路径和资产/会话 ID，发送前请自行检查。记录期间应用会略有变慢，最长 30 分钟，文件夹约 200 MB 封顶。只对下一次启动生效，
            启动后开关会自动关闭。
          </span>
        </span>
      </label>
      {status?.armed && <p className="nx-hint mt-2">已开启：请重启应用以开始记录。</p>}

      {error && (
        <div className="nx-alert nx-alert-danger mt-3" role="alert">
          操作失败：{error}
        </div>
      )}

      {captures.length > 0 && (
        <div className="mt-4">
          <div className="mb-1 text-[12px] text-neutral-400">历史记录</div>
          <ul className="flex flex-col gap-2">
            {captures.map((capture) => (
              <li key={capture.name} className="rounded border border-neutral-800 px-2 py-1.5 text-[12.5px]">
                <div className="flex flex-wrap items-center gap-2">
                  <span className="min-w-0 flex-1 break-all text-neutral-200">
                    {capture.name} · {formatBytes(capture.sizeBytes)}
                    {capture.timeMs > 0 ? ` · ${formatTime(capture.timeMs)}` : ""}
                  </span>
                  {canOpen && (
                    <button
                      type="button"
                      className="nx-btn nx-btn-outline nx-btn-sm"
                      aria-label={`打开 ${capture.name}`}
                      onClick={() => void openFolder(capture.path)}
                    >
                      <IconFolderOpen size={12} />
                      打开
                    </button>
                  )}
                  <button
                    type="button"
                    className="nx-btn nx-btn-outline nx-btn-sm"
                    aria-label={`打包 ${capture.name}`}
                    disabled={busy}
                    onClick={() => void pack(capture)}
                  >
                    {busy ? <IconLoader size={12} className="animate-spin" /> : <IconArchive size={12} />}
                    打包
                  </button>
                </div>
                {(packed[capture.name] || capture.archive) && (
                  <div className="nx-hint mt-1 break-all">压缩包：{packed[capture.name] || capture.archive}</div>
                )}
              </li>
            ))}
          </ul>
        </div>
      )}
    </section>
  );
}
