import { useCallback, useEffect, useRef, useState } from "react";
import { diagnosticsApi, type DiagnosticsCapture, type DiagnosticsStatus } from "../../ipc/diagnosticsApi";
import { toAppError } from "../../ipc/commands";
import { describeError } from "../../ui/errorText";
import { ask } from "../../ui/dialogs";
import { copyText } from "../../ui/clipboard";
import { formatBytes } from "../../ui/format";
import { IconActivity, IconArchive, IconCopy, IconFolderOpen, IconLoader, IconTrash } from "../../ui/icons";

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

  const revealArchive = async (path: string) => {
    setError(null);
    try {
      await diagnosticsApi.revealArchive(path);
    } catch (e) {
      fail(e);
    }
  };

  const copyPath = async (path: string) => {
    setError(null);
    try {
      await copyText(path);
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

  const remove = async (capture: DiagnosticsCapture) => {
    if (!(await ask(`删除调试记录 ${capture.name}？\n\n文件夹和已打包的压缩包都会被删除，无法恢复。`, { kind: "warning" }))) return;
    setBusy(true);
    setError(null);
    try {
      const next = await diagnosticsApi.remove(capture.path);
      if (mounted.current) setStatus(next);
    } catch (e) {
      fail(e);
    } finally {
      if (mounted.current) setBusy(false);
    }
  };

  const clearAll = async () => {
    if (!(await ask("清空全部调试记录？\n\n除本次正在记录的以外，所有文件夹和压缩包都会被删除，无法恢复。", { kind: "warning" }))) return;
    setBusy(true);
    setError(null);
    try {
      const next = await diagnosticsApi.clear();
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

      <p className="nx-hint">
        仅对下次启动生效，最长 30 分钟、约 200 MB，期间运行会略慢。记录性能数据、调试日志和系统信息，不含终端输入输出；密码、令牌、私钥已脱敏，发送前请自行检查主机名、路径等信息。
      </p>
      <div className="mt-3 flex flex-wrap items-center gap-2">
        {status?.armed ? (
          <>
            <span className="text-[12.5px] text-neutral-200">已开启，重启应用后开始记录（仅一次）</span>
            <button type="button" className="nx-btn nx-btn-outline nx-btn-sm" disabled={busy} onClick={() => void toggle(false)}>
              取消
            </button>
          </>
        ) : (
          <button
            type="button"
            className="nx-btn nx-btn-primary nx-btn-sm"
            disabled={status === null || busy}
            onClick={() => void toggle(true)}
          >
            开启下次启动的调试模式
          </button>
        )}
        {canOpen && (
          <button type="button" className="nx-btn nx-btn-outline nx-btn-sm" onClick={() => void openFolder("")}>
            <IconFolderOpen size={12} />
            打开记录目录
          </button>
        )}
      </div>

      {error && (
        <div className="nx-alert nx-alert-danger mt-3" role="alert">
          操作失败：{error}
        </div>
      )}

      {captures.length > 0 && (
        <div className="mt-4">
          <div className="mb-1 flex flex-wrap items-center gap-x-2 gap-y-1.5">
            <span className="text-[12px] text-neutral-400">历史记录</span>
            <button
              type="button"
              className="nx-btn nx-btn-ghost nx-btn-sm ml-auto"
              disabled={busy || captures.every((c) => c.path === activeDir)}
              onClick={() => void clearAll()}
            >
              <IconTrash size={12} />
              清空全部记录
            </button>
          </div>
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
                  {capture.path !== activeDir && (
                    <button
                      type="button"
                      className="nx-btn nx-btn-outline nx-btn-sm is-danger"
                      aria-label={`删除 ${capture.name}`}
                      disabled={busy}
                      onClick={() => void remove(capture)}
                    >
                      <IconTrash size={12} />
                      删除
                    </button>
                  )}
                </div>
                {(packed[capture.name] || capture.archive) && (
                  <div className="mt-1 flex flex-wrap items-center gap-2">
                    <span className="nx-hint min-w-0 flex-1 break-all">压缩包：{packed[capture.name] || capture.archive}</span>
                    {canOpen && (
                      <button
                        type="button"
                        className="nx-btn nx-btn-outline nx-btn-sm"
                        aria-label={`在文件夹中显示 ${capture.name}`}
                        onClick={() => void revealArchive(packed[capture.name] || capture.archive || "")}
                      >
                        <IconFolderOpen size={12} />
                        在文件夹中显示
                      </button>
                    )}
                    <button
                      type="button"
                      className="nx-btn nx-btn-outline nx-btn-sm"
                      aria-label={`复制路径 ${capture.name}`}
                      onClick={() => void copyPath(packed[capture.name] || capture.archive || "")}
                    >
                      <IconCopy size={12} />
                      复制路径
                    </button>
                  </div>
                )}
              </li>
            ))}
          </ul>
        </div>
      )}
    </section>
  );
}
