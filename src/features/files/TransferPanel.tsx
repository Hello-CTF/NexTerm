import { useState } from "react";
import { useUi } from "../../app/store";
import { describeTarget } from "../../ui/dialogs";
import { IconClose, IconDownload, IconUpload } from "../../ui/icons";
import { progressPercent } from "./fileProgress";
import { baseName } from "./pathUtils";
import { TRANSFER_STATUS_LABEL } from "./transferQueue";
import {
  cancelTransferTask,
  canRestartTransfer,
  canResumeTransfer,
  clearFinishedTransfers,
  restartTransferTask,
  resumeTransferTask,
  transferMatchesScope,
  useTransferStore,
  type ScopedTransferTask,
} from "./transferStore";

function TransferRow({ task, showTarget }: { task: ScopedTransferTask; showTarget: boolean }) {
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
      <div className="flex flex-wrap items-center gap-x-2 gap-y-0.5">
        <span
          className={task.direction === "upload" ? "shrink-0 text-blue-300" : "shrink-0 text-green-300"}
          title={task.direction === "upload" ? "上传" : "下载"}
        >
          {task.direction === "upload" ? <IconUpload size={12} /> : <IconDownload size={12} />}
        </span>
        <span className="min-w-0 flex-[1_1_6rem] truncate font-mono text-[11px] text-neutral-300" title={pathText}>
          {name}
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
          {task.status === "running"
            ? task.cancelling
              ? "取消中"
              : `${percent}%`
            : TRANSFER_STATUS_LABEL[task.status]}
        </span>
        {(canRestartTransfer(task) || task.restartPreparing) && (
          <button
            type="button"
            className="nx-link shrink-0"
            title={task.restartPreparing ? "正在准备全量重传" : "全量重传这个传输任务"}
            aria-label={`全量重传 ${name}`}
            disabled={task.restartPreparing}
            onClick={() => void restartTransferTask(task.id)}
          >
            {task.restartPreparing ? "准备中" : "全量重传"}
          </button>
        )}
        {canResumeTransfer(task) && (
          <button
            type="button"
            className="nx-link shrink-0"
            title="续传这个传输任务"
            aria-label={`续传 ${name}`}
            onClick={() => resumeTransferTask(task.id)}
          >
            续传
          </button>
        )}
        {(task.status === "queued" || task.status === "running") && (
          <button
            type="button"
            className="nx-icon-btn nx-icon-btn-sm"
            title={
              task.status === "queued"
                ? "取消这个排队任务"
                : task.cancelling
                  ? "正在取消这个传输任务"
                  : "取消这个传输任务"
            }
            aria-label={`取消 ${name}`}
            disabled={task.cancelling}
            onClick={() => cancelTransferTask(task.id)}
          >
            <IconClose size={11} />
          </button>
        )}
      </div>
      {showTarget && (
        <div
          className="truncate pl-5 text-[10px] text-neutral-500"
          title={`${task.targetName} · 会话 ${task.sessionId}`}
        >
          {task.targetName}
        </div>
      )}
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
      className="nx-xfer-panel shrink-0 border-t border-neutral-800/60 bg-neutral-950/60"
      data-testid="transfer-panel"
    >
      <div className="flex items-center gap-2 px-3 pt-1.5 text-[11px] text-neutral-500">
        <span className="shrink-0 font-semibold text-neutral-300">传输</span>
        <span className="min-w-0 truncate">
          {tasks.length === 0 ? "无任务" : active > 0 ? `${active} 进行中` : "已结束"}
        </span>
        <span className="nx-spacer" />
        <button
          type="button"
          className="nx-link shrink-0"
          aria-pressed={!showAll}
          title={showAll ? "显示当前主机的传输任务" : "显示全部主机的传输任务"}
          onClick={() => setShowAll((current) => !current)}
        >
          {showAll ? "全部" : "本机"}
        </button>
        {active < tasks.length && (
          <button
            type="button"
            className="nx-link shrink-0"
            title="清除已结束"
            onClick={() => clearFinishedTransfers(showAll ? "global" : { sessionId, hostId })}
          >
            清除
          </button>
        )}
      </div>
      <div className="max-h-36 overflow-y-auto pb-1.5">
        {tasks.length === 0 ? (
          <div className="px-3 py-2 text-[11px] text-neutral-500">没有传输任务</div>
        ) : (
          tasks.map((t) => <TransferRow key={t.id} task={t} showTarget={showAll} />)
        )}
      </div>
    </div>
  );
}
