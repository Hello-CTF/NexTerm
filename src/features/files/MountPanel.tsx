// 挂载面板（M1-T8）：远端目录 ↔ 本机盘符。
// 列表以本机真实状态为准（§5.5）：显示系统全部映射，含其他进程挂的。
import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ask } from "../../ui/dialogs";
import { mountApi } from "../../ipc/commands";
import { describeError } from "../../ui/errorText";
import { useUi } from "../../app/store";
import { mountUnavailableReason } from "../../app/capabilities";
import { IconArrowLeft, IconDrive, IconRefresh, IconTrash } from "../../ui/icons";

/**
 * 暂不可用态。
 *
 * 这版 macOS 不做「点了才失败」：把理由和替代路径直接摆在面板里。
 * 理由文案来自内核（`mount_capability`），不在前端拼。
 */
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
            远程文件读写请走左侧 <span className="text-neutral-300">「文件树」</span>
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
  /**
   * 本机会话上挂载没有意义：要挂的那个「远端」就是这台机器自己。
   * 不给「点了才失败」的入口 —— 直接说明白并指出去哪儿看本机文件。
   */
  const localSession = sessions.find((s) => s.id === sessionId)?.kind === "local";
  const [localPoint, setLocalPoint] = useState("Z:");
  const [remotePath, setRemotePath] = useState("");
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);

  const mounts = useQuery({
    queryKey: ["mounts"],
    queryFn: () => mountApi.list(true),
    refetchInterval: 15000,
  });

  const refresh = () => void qc.invalidateQueries({ queryKey: ["mounts"] });

  const create = async () => {
    if (!sessionId) {
      pushToast("info", "先连接一台主机（凭据默认复用资产）");
      return;
    }
    if (localSession) {
      pushToast("info", "当前是「当前设备」会话 —— 本机文件直接在左栏文件树里看，不用挂载");
      return;
    }
    if (!remotePath.trim() || !localPoint.trim()) {
      pushToast("error", "远端路径与挂载点不能为空");
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
      refresh();
    } catch (e) {
      pushToast("error", `挂载失败: ${describeError(e)}`);
    } finally {
      setBusy(false);
    }
  };

  const remove = async (point: string) => {
    if (!(await ask(`断开 ${point}？`, { kind: "info" }))) return;
    try {
      await mountApi.remove(point, sessionId);
      refresh();
      pushToast("success", "已断开");
    } catch (e) {
      pushToast("error", `断开失败: ${describeError(e)}`);
    }
  };

  const list = mounts.data ?? [];

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
              <th style={{ width: 90 }}>本地盘符</th>
              <th>远端路径</th>
              <th style={{ width: 120 }}>挂载时间</th>
              <th style={{ width: 90 }} />
            </tr>
          </thead>
          <tbody>
            {list.map((m) => (
              <tr key={`${m.localPoint}-${m.remote}`}>
                <td className="nx-mono font-semibold text-neutral-100">{m.localPoint}</td>
                <td className="nx-mono">{m.remote}</td>
                <td className="text-neutral-500">
                  {m.createdAt ? new Date(m.createdAt).toLocaleDateString() : "—"}
                </td>
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
            ))}
            {list.length === 0 && (
              <tr>
                <td colSpan={4} className="nx-table-empty">
                  本机当前没有映射盘
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>

      <div className="shrink-0 border-t border-neutral-800/60 bg-neutral-950/40 p-3">
        <div className="mb-2.5 flex items-center gap-2 text-[11px] text-neutral-500">
          <IconArrowLeft size={12} />
          新建映射 · Windows 用 <span className="nx-code">\\host\share</span> →{' '}
          <span className="nx-code">Z:</span>；Linux 用{' '}
          <span className="nx-code">user@host:/path</span> →{' '}
          <span className="nx-code">/mnt/point</span>（需 sshfs）
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <input
            className="nx-input nx-input-sm w-[86px] font-mono"
            value={localPoint}
            onChange={(e) => setLocalPoint(e.target.value)}
            placeholder="Z:"
          />
          <span className="text-neutral-600">→</span>
          <input
            className="nx-input nx-input-sm min-w-[220px] flex-1 font-mono"
            value={remotePath}
            onChange={(e) => setRemotePath(e.target.value)}
            placeholder="\\10.0.0.8\share  或  user@host:/data"
            onKeyDown={(e) => e.key === "Enter" && void create()}
          />
          <input
            className="nx-input nx-input-sm w-[150px]"
            value={username}
            onChange={(e) => setUsername(e.target.value)}
            placeholder="用户名（可选）"
          />
          <input
            type="password"
            className="nx-input nx-input-sm w-[150px]"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            placeholder="密码（可选）"
          />
          <button
            className="nx-btn nx-btn-primary nx-btn-sm"
            disabled={busy || localSession}
            onClick={() => void create()}
          >
            {busy ? <IconRefresh size={13} className="animate-spin" /> : null}
            挂载
          </button>
        </div>
        <div className="nx-hint mt-2">
          {localSession ? (
            <>
              当前工作区是内置的「当前设备」—— 要挂的「远端」就是这台机器自己。
              本机目录请直接看左栏 <span className="text-neutral-300">文件树</span>；
              挂载是给 <span className="text-neutral-300">SSH 资产</span> 用的。
            </>
          ) : (
            <>凭据默认复用资产里保存的那份；这里填的只对本次挂载生效，不落盘。</>
          )}
        </div>
      </div>
    </div>
  );
}
