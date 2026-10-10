import { useEffect, useId, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ask } from "../../ui/dialogs";
import { mountApi, systemApi } from "../../ipc/commands";
import { isImeKeyEvent } from "../../ui/DialogHost";
import { describeError } from "../../ui/errorText";
import { useUi } from "../../app/store";
import { mountUnavailableReason } from "../../app/capabilities";
import { IconArrowLeft, IconChevronRight, IconDrive, IconRefresh, IconTrash } from "../../ui/icons";

const FIELD_IDLE_VALIDATION_MS = 500;

function validateRequired(label: string, value: string): string | null {
  if (!value.trim()) return `${label}不能为空`;
  return null;
}

function useMountPanelVisible(sessionId?: string): boolean {
  return useUi((s) => {
    const exists = s.workspaces.some((w) =>
      w.panes.some((p) => p.tabs.some((t) => t.kind === "mount" && t.sessionId === sessionId)),
    );
    if (!exists) return true;
    const workspace =
      s.workspaces.find((w) => w.id === s.activeWorkspaceId) ?? s.workspaces[s.workspaces.length - 1];
    if (!workspace) return true;
    return workspace.panes.some((p) => {
      const active = p.tabs.find((t) => t.id === p.activeTabId) ?? p.tabs[p.tabs.length - 1];
      return active?.kind === "mount" && active.sessionId === sessionId;
    });
  });
}

function MountUnavailable({ reason }: { reason: string }) {
  return (
    <div className="nx-pane">
      <div className="nx-toolbar">
        <IconDrive size={14} className="text-neutral-500" />
        <span className="nx-toolbar-title">磁盘挂载</span>
        <span className="nx-badge nx-badge-amber">暂不可用</span>
      </div>
      <div className="flex min-h-0 flex-1 items-center justify-center px-6">
        <div className="max-w-[560px] text-center">
          <IconDrive size={30} className="mx-auto mb-3 text-neutral-600" />
          <div className="text-[13px] font-semibold text-neutral-200">本平台暂不支持磁盘挂载</div>
          <p className="mt-2 text-[12px] leading-relaxed text-neutral-500">{reason}</p>
          <p className="mt-2.5 text-[12px] leading-relaxed text-neutral-500">
            远程文件读写请走左侧<span className="text-neutral-300">「文件树」</span>
            （SFTP 通道，不需要任何本机依赖）。
          </p>
        </div>
      </div>
    </div>
  );
}

export function MountPanel({ sessionId }: { sessionId?: string }) {
  const reason = mountUnavailableReason();
  if (reason) return <MountUnavailable reason={reason} />;
  return <MountPanelInner sessionId={sessionId} />;
}

function MountPanelInner({ sessionId }: { sessionId?: string }) {
  const qc = useQueryClient();
  const { pushToast } = useUi();
  const sessions = useUi((s) => s.sessions);
  const visible = useMountPanelVisible(sessionId);
  const localSession = sessions.find((s) => s.id === sessionId)?.kind === "local";
  const platform = useQuery({
    queryKey: ["system-platform"],
    queryFn: () => systemApi.platform(),
    staleTime: Infinity,
    refetchOnWindowFocus: false,
  });
  const windows = platform.data === "windows";
  const [localPoint, setLocalPoint] = useState("");
  const localPointEdited = useRef(false);
  const [remotePath, setRemotePath] = useState("");
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [credsOpen, setCredsOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const localPointErrorId = useId();
  const remotePathErrorId = useId();
  const credsPanelId = useId();
  const localPointRef = useRef<HTMLInputElement>(null);
  const remotePathRef = useRef<HTMLInputElement>(null);
  const [localPointError, setLocalPointError] = useState<string | null>(null);
  const [remotePathError, setRemotePathError] = useState<string | null>(null);
  const localPointTimer = useRef<number | null>(null);
  const remotePathTimer = useRef<number | null>(null);
  const localPointValueRef = useRef(localPoint);
  localPointValueRef.current = localPoint;
  const remotePathValueRef = useRef(remotePath);
  remotePathValueRef.current = remotePath;

  useEffect(() => {
    if (!localPointEdited.current) setLocalPoint(windows ? "Z:" : "");
  }, [windows]);

  useEffect(
    () => () => {
      if (localPointTimer.current !== null) window.clearTimeout(localPointTimer.current);
      if (remotePathTimer.current !== null) window.clearTimeout(remotePathTimer.current);
    },
    [],
  );

  const onLocalPointChange = (value: string) => {
    localPointEdited.current = true;
    setLocalPoint(value);
    if (localPointTimer.current !== null) window.clearTimeout(localPointTimer.current);
    localPointTimer.current = window.setTimeout(() => {
      localPointTimer.current = null;
      setLocalPointError(validateRequired("挂载点", value));
    }, FIELD_IDLE_VALIDATION_MS);
  };
  const onLocalPointBlur = () => {
    if (localPointTimer.current !== null) {
      window.clearTimeout(localPointTimer.current);
      localPointTimer.current = null;
    }
    setLocalPointError(validateRequired("挂载点", localPointValueRef.current));
  };
  const onRemotePathChange = (value: string) => {
    setRemotePath(value);
    if (remotePathTimer.current !== null) window.clearTimeout(remotePathTimer.current);
    remotePathTimer.current = window.setTimeout(() => {
      remotePathTimer.current = null;
      setRemotePathError(validateRequired("远端路径", value));
    }, FIELD_IDLE_VALIDATION_MS);
  };
  const onRemotePathBlur = () => {
    if (remotePathTimer.current !== null) {
      window.clearTimeout(remotePathTimer.current);
      remotePathTimer.current = null;
    }
    setRemotePathError(validateRequired("远端路径", remotePathValueRef.current));
  };

  const mounts = useQuery({
    queryKey: ["mounts"],
    queryFn: () => mountApi.list(true),
    enabled: visible,
    refetchInterval: visible ? 15000 : false,
  });

  const refresh = () => void qc.invalidateQueries({ queryKey: ["mounts"] });

  const create = async () => {
    if (!sessionId || localSession) {
      pushToast("info", "请先连接 SSH 资产后再挂载");
      return;
    }
    const pointErr = validateRequired("挂载点", localPoint);
    const pathErr = validateRequired("远端路径", remotePath);
    setLocalPointError(pointErr);
    setRemotePathError(pathErr);
    if (pointErr || pathErr) {
      if (pointErr) localPointRef.current?.focus();
      else remotePathRef.current?.focus();
      return;
    }
    setBusy(true);
    try {
      await mountApi.create({
        sessionId,
        remotePath: remotePath.trim(),
        localPoint: localPoint.trim(),
        username: username || undefined,
        password: password || undefined,
      });
      pushToast("success", `已挂载 ${localPoint}`);
      setRemotePath("");
      setUsername("");
      setPassword("");
      setCredsOpen(false);
      refresh();
    } catch (e) {
      pushToast("error", `挂载失败：${describeError(e)}`);
    } finally {
      setBusy(false);
    }
  };

  const remove = async (point: string) => {
    if (!(await ask(`断开 ${point}？`, { kind: "warning" }))) return;
    try {
      await mountApi.remove(point, sessionId);
      refresh();
      pushToast("success", "已断开");
    } catch (e) {
      pushToast("error", `断开失败：${describeError(e)}`);
    }
  };

  const list = mounts.data ?? [];
  const mountsFailed = mounts.isError && !mounts.data;

  return (
    <div className="nx-pane">
      <div className="nx-toolbar">
        <IconDrive size={14} className="text-neutral-500" />
        <span className="nx-toolbar-title">磁盘挂载</span>
        <span className="nx-hint">列表 = 本机真实状态，15s 自动刷新</span>
        <div className="nx-spacer" />
        <button className="nx-btn nx-btn-ghost nx-btn-sm" onClick={refresh}>
          <IconRefresh size={13} />
          强制重扫
        </button>
      </div>

      <div className="min-h-0 flex-1 overflow-auto">
        <table className="nx-table">
          <thead>
            <tr>
              <th style={{ width: 110 }}>本地挂载点</th>
              <th>远端路径</th>
              <th style={{ width: 90 }} />
            </tr>
          </thead>
          <tbody>
            {mounts.isPending ? (
              <tr>
                <td colSpan={3} className="nx-table-empty">
                  挂载列表加载中…
                </td>
              </tr>
            ) : mountsFailed ? (
              <tr>
                <td colSpan={3} className="nx-table-empty">
                  <span className="text-red-300">挂载列表加载失败 · {describeError(mounts.error)}</span>
                  <button
                    className="nx-btn nx-btn-ghost nx-btn-sm ml-2"
                    onClick={() => void mounts.refetch()}
                  >
                    <IconRefresh size={12} />
                    重试
                  </button>
                </td>
              </tr>
            ) : list.length === 0 ? (
              <tr>
                <td colSpan={3} className="nx-table-empty">
                  本机当前没有挂载点
                </td>
              </tr>
            ) : (
              list.map((m) => (
              <tr key={`${m.localPoint}-${m.remote}`}>
                <td className="nx-mono font-semibold text-neutral-100">{m.localPoint}</td>
                <td className="nx-mono">{m.remote}</td>
                <td className="nx-right">
                  <button
                    className="nx-btn nx-btn-danger nx-btn-xs"
                    onClick={() => void remove(m.localPoint)}
                  >
                    <IconTrash size={11} />
                    断开
                  </button>
                </td>
              </tr>
              ))
            )}
          </tbody>
        </table>
      </div>

      <div className="shrink-0 border-t border-neutral-800/60 bg-neutral-950/40 p-3">
        <div className="mb-2.5 flex flex-wrap items-center gap-2 text-[11px] text-neutral-500">
          <IconArrowLeft size={12} />
          新建挂载 · Windows 用 <span className="nx-code">\\host\share</span> →{' '}
          <span className="nx-code">Z:</span>；Linux 用{' '}
          <span className="nx-code">user@host:/path</span> →{' '}
          <span className="nx-code">/mnt/point</span>（需 sshfs）
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <div className="w-[110px] shrink-0">
            <input
              ref={localPointRef}
              className="nx-input nx-input-sm font-mono"
              value={localPoint}
              onChange={(e) => onLocalPointChange(e.target.value)}
              onBlur={onLocalPointBlur}
              placeholder={windows ? "Z:" : "/mnt/point"}
              aria-label="本地挂载点"
              autoComplete="off"
              aria-invalid={localPointError ? true : undefined}
              aria-describedby={localPointError ? localPointErrorId : undefined}
            />
            {localPointError && (
              <p id={localPointErrorId} role="alert" className="mt-1 break-words text-[11px] text-red-400">
                {localPointError}
              </p>
            )}
          </div>
          <span className="text-neutral-600">→</span>
          <div className="min-w-[220px] flex-1 max-[560px]:min-w-[140px]">
            <input
              ref={remotePathRef}
              className="nx-input nx-input-sm font-mono"
              value={remotePath}
              onChange={(e) => onRemotePathChange(e.target.value)}
              onBlur={onRemotePathBlur}
              placeholder={windows ? "\\\\10.0.0.8\\share" : "user@host:/data"}
              aria-label="远端路径"
              autoComplete="off"
              aria-invalid={remotePathError ? true : undefined}
              aria-describedby={remotePathError ? remotePathErrorId : undefined}
              onKeyDown={(e) => e.key === "Enter" && !isImeKeyEvent(e) && void create()}
            />
            {remotePathError && (
              <p id={remotePathErrorId} role="alert" className="mt-1 break-words text-[11px] text-red-400">
                {remotePathError}
              </p>
            )}
          </div>
          <button
            className="nx-btn nx-btn-primary nx-btn-sm"
            disabled={busy || !sessionId || localSession}
            title={!sessionId || localSession ? "请先连接 SSH 资产" : undefined}
            onClick={() => void create()}
          >
            {busy ? <IconRefresh size={13} className="animate-spin" /> : null}
            挂载
          </button>
        </div>
        <button
          type="button"
          className="nx-btn nx-btn-ghost nx-btn-xs mt-2"
          aria-expanded={credsOpen}
          aria-controls={credsPanelId}
          onClick={() => setCredsOpen((v) => !v)}
        >
          <IconChevronRight size={11} className={credsOpen ? "rotate-90" : ""} />
          使用其他凭据（可选）
          {!credsOpen && (username !== "" || password !== "") && (
            <span className="nx-badge nx-badge-green">已配置</span>
          )}
        </button>
        {credsOpen && (
          <div id={credsPanelId} className="mt-2 flex flex-wrap items-center gap-2">
            <input
              className="nx-input nx-input-sm w-[150px]"
              value={username}
              onChange={(e) => setUsername(e.target.value)}
              placeholder="用户名（可选）"
              aria-label="挂载用户名（可选）"
              autoComplete="off"
            />
            <input
              type="password"
              className="nx-input nx-input-sm w-[150px]"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              placeholder="密码（可选）"
              aria-label="挂载密码（可选）"
              autoComplete="off"
            />
          </div>
        )}
        <div className="nx-hint mt-2">
          {!sessionId ? (
            "当前没有可用的 SSH 会话，请先连接 SSH 资产后再挂载。"
          ) : localSession ? (
            "当前是「当前设备」会话，不能建立 SSH 挂载；请先连接 SSH 资产。"
          ) : (
            <>凭据默认复用资产里保存的那份；这里填的只对本次挂载生效，不落盘。</>
          )}
        </div>
      </div>
    </div>
  );
}
