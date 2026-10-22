import { useUi, type BroadcastTargetStatus } from "../../app/store";
import { IconClose, IconZap } from "../../ui/icons";
import { collectBroadcastCandidates } from "./broadcast";

const STATUS_LABEL: Record<BroadcastTargetStatus, string> = {
  pending: "发送中",
  sent: "已发送",
  skipped: "已跳过",
  failed: "失败",
};

export function BroadcastStrip(props: {
  workspaceId: string;
  myStoreTabId: string;
  onManage: () => void;
}) {
  const broadcast = useUi((state) => state.broadcast);
  const workspaces = useUi((state) => state.workspaces);
  const sessions = useUi((state) => state.sessions);
  const setBroadcast = useUi((state) => state.setBroadcast);
  const pushToast = useUi((state) => state.pushToast);
  if (
    !broadcast ||
    broadcast.origin.workspaceId !== props.workspaceId ||
    broadcast.origin.storeTabId !== props.myStoreTabId
  ) {
    return null;
  }
  const candidates = collectBroadcastCandidates(workspaces, sessions);
  const candidateById = new Map(candidates.map((candidate) => [candidate.storeTabId, candidate]));
  const writable = broadcast.targetIds.filter((targetId) => candidateById.get(targetId)?.eligible).length;
  return (
    <div
      role="alert"
      className="shrink-0 border-b border-red-500/40 bg-red-500/10 px-2.5 py-1.5 text-[12px] text-red-300"
    >
      <div className="flex flex-wrap items-center gap-2">
        <IconZap size={13} className="shrink-0" />
        <span>
          广播已开启：此来源终端的输入将发送到 {broadcast.targetIds.length} 个目标
          {writable < broadcast.targetIds.length
            ? `（${broadcast.targetIds.length - writable} 个当前不可写）`
            : ""}
        </span>
        <div className="nx-spacer" />
        <button type="button" className="nx-btn nx-btn-ghost nx-btn-sm" onClick={props.onManage}>
          管理目标
        </button>
        <button
          type="button"
          className="nx-btn nx-btn-ghost nx-btn-sm"
          onClick={() => {
            setBroadcast(null);
            pushToast("info", "广播已关闭，输入只发送到当前终端");
          }}
        >
          <IconClose size={12} />
          关闭广播
        </button>
      </div>
      <div className="mt-1.5 flex max-h-24 flex-wrap gap-1 overflow-y-auto" aria-label="广播目标状态">
        {broadcast.targetIds.map((targetId) => {
          const candidate = candidateById.get(targetId);
          const report = broadcast.results[targetId];
          const label = candidate
            ? `${candidate.hostName} / ${candidate.workspaceTitle} / ${candidate.title}`
            : targetId;
          return (
            <span
              key={targetId}
              className="inline-flex max-w-full items-center gap-1 rounded border border-red-500/20 bg-neutral-950/25 px-1.5 py-0.5 text-[11px]"
              data-status={report?.status ?? "idle"}
              title={report?.reason ?? candidate?.ineligibleReason ?? label}
            >
              <span className="min-w-0 truncate">{label}</span>
              <span className="shrink-0 font-medium">
                {report ? STATUS_LABEL[report.status] : "等待输入"}
              </span>
            </span>
          );
        })}
      </div>
    </div>
  );
}
