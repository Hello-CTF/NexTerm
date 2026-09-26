// 挂载面板（M1-T8）：远端目录 ↔ 本机盘符。
// 列表以本机真实状态为准（§5.5）：显示系统全部映射，含其他进程挂的。
import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ask } from "../../ui/dialogs";
import { mountApi } from "../../ipc/commands";
import { useUi } from "../../app/store";

export function MountPanel({ sessionId }: { sessionId?: string }) {
  const qc = useQueryClient();
  const { pushToast } = useUi();
  const [localPoint, setLocalPoint] = useState("Z:");
  const [remotePath, setRemotePath] = useState("");
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");

  const mounts = useQuery({
    queryKey: ["mounts"],
    queryFn: () => mountApi.list(true),
    refetchInterval: 15000,
  });

  const create = async () => {
    if (!sessionId) {
      pushToast("info", "先连接一台主机（凭据默认复用资产）");
      return;
    }
    if (!remotePath.trim() || !localPoint.trim()) {
      pushToast("error", "远端路径与挂载点不能为空");
      return;
    }
    try {
      await mountApi.create({
        sessionId,
        remotePath: remotePath.trim(),
        localPoint: localPoint.trim(),
        username: username || undefined,
        password: password || undefined,
      });
      pushToast("success", `已挂载 ${localPoint}`);
      void qc.invalidateQueries({ queryKey: ["mounts"] });
    } catch (e) {
      pushToast("error", `挂载失败: ${String(e)}`);
    }
  };

  const remove = async (point: string) => {
    if (!(await ask(`断开 ${point}？`))) return;
    try {
      await mountApi.remove(point);
      void qc.invalidateQueries({ queryKey: ["mounts"] });
      pushToast("success", "已断开");
    } catch (e) {
      pushToast("error", `断开失败: ${String(e)}`);
    }
  };

  return (
    <div className="flex h-full flex-col bg-neutral-900 text-sm text-neutral-300">
      <div className="flex items-center gap-2 border-b border-neutral-800 px-3 py-1.5">
        <span className="font-medium">磁盘挂载</span>
        <span className="text-xs text-neutral-500">列表 = 本机真实状态（15s 刷新）</span>
        <div className="flex-1" />
        <button
          className="rounded px-2 py-0.5 text-xs hover:bg-neutral-800"
          onClick={() => void qc.invalidateQueries({ queryKey: ["mounts"] })}
        >
          强制重扫
        </button>
      </div>

      <div className="min-h-0 flex-1 overflow-auto">
        <table className="w-full text-xs">
          <thead className="sticky top-0 bg-neutral-800 text-neutral-300">
            <tr>
              <th className="px-3 py-1.5 text-left">本地</th>
              <th className="px-2 py-1.5 text-left">远端</th>
              <th className="px-2 py-1.5 text-right">操作</th>
            </tr>
          </thead>
          <tbody>
            {(mounts.data ?? []).map((m) => (
              <tr key={`${m.localPoint}-${m.remote}`} className="border-t border-neutral-800/60">
                <td className="px-3 py-1.5 font-mono">{m.localPoint}</td>
                <td className="px-2 py-1.5 font-mono text-neutral-400">{m.remote}</td>
                <td className="px-2 py-1.5 text-right">
                  <button
                    className="rounded px-2 py-0.5 text-red-400 hover:bg-neutral-800"
                    onClick={() => void remove(m.localPoint)}
                  >
                    断开
                  </button>
                </td>
              </tr>
            ))}
            {(mounts.data ?? []).length === 0 && (
              <tr>
                <td colSpan={3} className="px-4 py-8 text-center text-neutral-600">
                  本机当前没有映射盘
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>

      <div className="border-t border-neutral-800 p-3">
        <div className="mb-2 text-xs text-neutral-500">
          新建映射 — Windows: `\\\\host\\share` → `Z:`；Linux: `user@host:/path` → `/mnt/point`
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <input
            className="w-24 rounded bg-neutral-800 px-2 py-1 font-mono text-xs outline-none"
            value={localPoint}
            onChange={(e) => setLocalPoint(e.target.value)}
            placeholder="Z:"
          />
          <span className="text-neutral-600">←</span>
          <input
            className="min-w-0 flex-1 rounded bg-neutral-800 px-2 py-1 font-mono text-xs outline-none"
            value={remotePath}
            onChange={(e) => setRemotePath(e.target.value)}
            placeholder="\\\\host\\share 或 user@host:/data"
          />
          <input
            className="w-32 rounded bg-neutral-800 px-2 py-1 text-xs outline-none"
            value={username}
            onChange={(e) => setUsername(e.target.value)}
            placeholder="用户名（可选）"
          />
          <input
            type="password"
            className="w-32 rounded bg-neutral-800 px-2 py-1 text-xs outline-none"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            placeholder="密码（可选）"
          />
          <button
            className="rounded bg-blue-600 px-3 py-1 text-xs text-white hover:bg-blue-500"
            onClick={() => void create()}
          >
            挂载
          </button>
        </div>
      </div>
    </div>
  );
}
