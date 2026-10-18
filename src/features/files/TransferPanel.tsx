import { useState } from "react";
import { useUi } from "../../app/store";
import { describeTarget } from "../../ui/dialogs";
import { IconClose, IconDownload, IconUpload } from "../../ui/icons";
import { progressPercent } from "./fileProgress";
import { baseName } from "./pathUtils";
import { TRANSFER_STATUS_LABEL } from "./transferQueue";
import {
  cancelTransferTask,
  clearFinishedTransfers,
  transferMatchesScope,
  useTransferStore,
  type ScopedTransferTask,
} from "./transferStore";

function TransferRow({ task }: { task: ScopedTransferTask }) {
  const name = baseName(task.remotePath);
  const localPath = task.file ? task.file.name : describeTarget(task.localPath, name);
  const pathText =
    task.direction === "upload"
      ? `${localPath} -> ${task.remotePath}`
      : `${task.remotePath} -> ${localPath}`;
  const percent = progressPercent(task);
  const active = task.status === "queued" || task.status === "running";
  return (
    <div className="px-3 py-1" data-task-key={task.id}>
      <div className="flex items-center gap-2">
        <span
          className={task.direction === "upload" ? "shrink-0 text-blue-300" : "shrink-0 text-green-300"}
          title={task.direction === "upload" ? "上传" : "下载"}
        >
          {task.direction === "upload" ? <IconUpload size={12} /> : <IconDownload size={12} />}
        </span>
        <span className="min-w-0 flex-1 truncate font-mono text-[11px] text-neutral-300" title={pathText}>
          {pathText}
        </span>
        {active && (
          <span
            className="nx-progress w-16 shrink-0"
            role="progressbar"
            aria-valuemin={0}
            aria-valuemax={100}
            aria-valuenow={percent}
          >
            <span className="nx-progress-bar" style={{ width: `${percent}%` }} />
          </span>
        )}
        <span
          className={`w-12 shrink-0 text-right text-[11px] ${
            task.status === "failed" ? "text-red-300" : "text-neutral-500"
          }`}
        >
          {task.status === "running" ? `${percent}%` : TRANSFER_STATUS_LABEL[task.status]}
        </span>
        {task.status === "queued" && (
          <button
            type="button"
            className="nx-icon-btn nx-icon-btn-sm"
            title="取消这个排队任务"
            aria-label={`取消 ${name}`}
            onClick={() => cancelTransferTask(task.id)}
          >
            <IconClose size={11} />
          </button>
        )}
      </div>
      <div
        className="truncate pl-5 text-[10px] text-neutral-500"
        title={`目标：${task.targetName} · 会话 ${task.sessionId}`}
      >
        目标：{task.targetName} · 会话 {task.sessionId.slice(0, 8)}
      </div>
      {task.error && (
        <div className="truncate pl-5 text-[11px] text-red-300" title={task.error}>
          {task.error}
        </div>
      )}
    </div>
  );
}

export function TransferPanel({ sessionId }: { sessionId: string }) {
  const [showAll, setShowAll] = useState(false);
  const hostId = useUi(
    (s) =>
      s.sessions.find((item) => item.id === sessionId)?.assetId ??
      s.workspaces.find((item) => item.sessionId === sessionId)?.assetId ??
      null,
  );
  const allTasks = useTransferStore((s) => s.tasks);
  if (allTasks.length === 0) return null;
  const tasks = showAll
    ? allTasks
    : allTasks.filter((task) => transferMatchesScope(task, sessionId, hostId));
  const active = tasks.filter((t) => t.status === "queued" || t.status === "running").length;
  return (
    <div
      className="shrink-0 border-t border-neutral-800/60 bg-neutral-950/60"
      data-testid="transfer-panel"
    >
      <div className="flex items-center gap-2 px-3 pt-1.5 text-[11px] text-neutral-500">
        <span className="font-semibold text-neutral-300">传输队列</span>
        <span>
          {tasks.length === 0
            ? "当前主机无任务"
            : active > 0
              ? `${active} 个未结束`
              : "已全部结束"}
        </span>
        <span className="nx-spacer" />
        <button
          type="button"
          className="nx-link"
          aria-pressed={!showAll}
          title={showAll ? "显示当前主机的传输任务" : "显示全部主机的传输任务"}
          onClick={() => setShowAll((current) => !current)}
        >
          {showAll ? "全部主机" : "当前主机"}
        </button>
        {active < tasks.length && (
          <button
            type="button"
            className="nx-link"
            onClick={() => clearFinishedTransfers(showAll ? "global" : { sessionId, hostId })}
          >
            清除已结束
          </button>
        )}
      </div>
      <div className="max-h-36 overflow-y-auto pb-1.5">
        {tasks.length === 0 ? (
          <div className="px-3 py-2 text-[11px] text-neutral-500">当前主机没有传输任务</div>
        ) : (
          tasks.map((t) => <TransferRow key={t.id} task={t} />)
        )}
      </div>
    </div>
  );
}
