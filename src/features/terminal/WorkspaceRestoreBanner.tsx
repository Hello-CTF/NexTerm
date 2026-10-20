import { useMemo, useState } from "react";
import { useUi, type WorkspaceRestoreStatus } from "../../app/store";
import {
  classifyRestoredWorkspace,
  collectWorkspaceRestoreCandidates,
  reconnectRestoredWorkspaces,
  type WorkspaceRestoreCandidate,
} from "./workspaceRestore";

interface RestoreItem extends WorkspaceRestoreCandidate {
  status: WorkspaceRestoreStatus;
  detail?: string;
}

function statusText(item: RestoreItem): string {
  if (item.status === "waiting") return "等待重连";
  if (item.status === "connecting") return "连接中…";
  if (item.status === "success") return "已连接";
  if (item.status === "failed") return "连接失败";
  if (item.status === "canceled") return "已取消";
  return "已跳过";
}

export function WorkspaceRestoreBanner() {
  const ready = useUi((state) => state.layoutSyncCompleted && state.sessionResyncCompleted);
  const dismissed = useUi((state) => state.workspaceRestoreDismissed);
  const workspaces = useUi((state) => state.workspaces);
  const sessions = useUi((state) => state.sessions);
  const results = useUi((state) => state.workspaceRestoreResults);
  const dismiss = useUi((state) => state.dismissWorkspaceRestoreBanner);
  const [expanded, setExpanded] = useState(false);
  const [running, setRunning] = useState(false);
  const [selected, setSelected] = useState<Set<string>>(new Set());

  const candidates = useMemo(
    () => collectWorkspaceRestoreCandidates(workspaces, sessions),
    [workspaces, sessions],
  );
  const items = useMemo(() => {
    const byId = new Map<string, WorkspaceRestoreCandidate>(
      candidates.map((candidate) => [candidate.workspaceId, candidate]),
    );
    for (const workspace of workspaces) {
      if (results[workspace.id] && !byId.has(workspace.id)) {
        byId.set(workspace.id, classifyRestoredWorkspace(workspace));
      }
    }
    return [...byId.values()].map((candidate): RestoreItem => {
      const result = results[candidate.workspaceId];
      if (result) return { ...candidate, status: result.status, detail: result.detail };
      return candidate.eligible
        ? { ...candidate, status: "waiting" }
        : { ...candidate, status: "skipped", detail: candidate.skipReason };
    });
  }, [candidates, results, workspaces]);

  const hasHost = items.some((item) => item.kind === "session");
  const visibleItems = hasHost ? items : items.filter((item) => results[item.workspaceId]);
  const pendingCount = visibleItems.filter(
    (item) => item.kind === "session" && item.eligible && item.status !== "success" && item.status !== "skipped",
  ).length;
  const successCount = visibleItems.filter((item) => item.kind === "session" && item.status === "success").length;
  const eligibleIds = candidates.filter((candidate) => candidate.eligible).map((candidate) => candidate.workspaceId);
  const selectedIds = [...selected].filter((id) => eligibleIds.includes(id));

  if (!ready || dismissed || visibleItems.length === 0) return null;

  const openSelection = () => {
    if (!expanded) setSelected(new Set(eligibleIds));
    setExpanded(!expanded);
  };
  const run = async (ids?: string[]) => {
    if (running) return;
    setRunning(true);
    setExpanded(true);
    try {
      await reconnectRestoredWorkspaces(ids);
    } finally {
      setRunning(false);
    }
  };
  const summary = pendingCount > 0
    ? `${pendingCount} 个主机工作区需要重新连接`
    : successCount > 0
      ? `工作区恢复完成：${successCount} 个主机工作区已连接`
      : "没有可自动重连的主机工作区";

  return (
    <section
      aria-label="工作区恢复"
      className="shrink-0 border-b border-amber-500/30 bg-amber-950/30 text-xs text-amber-100"
    >
      <div className="flex flex-wrap items-center gap-2 px-4 py-2">
        <span>{summary}</span>
        <button
          type="button"
          className="nx-btn-ghost"
          disabled={running || eligibleIds.length === 0}
          onClick={() => void run()}
        >
          {running ? "正在重连…" : "全部重连"}
        </button>
        <button type="button" className="nx-btn-ghost" aria-expanded={expanded} onClick={openSelection}>
          选择…
        </button>
        <button
          type="button"
          className="nx-btn-ghost ml-auto"
          aria-label="关闭工作区恢复提示"
          onClick={dismiss}
        >
          关闭
        </button>
      </div>
      {expanded && (
        <div className="border-t border-amber-500/20 px-4 py-2">
          <ul aria-label="可恢复工作区" className="flex flex-col gap-1.5">
            {visibleItems.map((item) => {
              const selectable = item.eligible && item.status !== "success" && item.status !== "skipped";
              return (
                <li key={item.workspaceId} className="flex flex-wrap items-center gap-x-3 gap-y-1">
                  <label className="flex min-w-0 items-center gap-2">
                    <input
                      type="checkbox"
                      aria-label={`选择 ${item.title}`}
                      checked={selectedIds.includes(item.workspaceId)}
                      disabled={!selectable || running}
                      onChange={(event) => {
                        setSelected((current) => {
                          const next = new Set(current);
                          if (event.target.checked) next.add(item.workspaceId);
                          else next.delete(item.workspaceId);
                          return next;
                        });
                      }}
                    />
                    <span className="truncate">{item.title}</span>
                  </label>
                  <span className={item.status === "failed" ? "text-red-300" : "text-amber-300"}>
                    {statusText(item)}
                  </span>
                  {item.detail && <span className="text-amber-200/70">{item.detail}</span>}
                </li>
              );
            })}
          </ul>
          <button
            type="button"
            className="nx-btn-ghost mt-2"
            disabled={running || selectedIds.length === 0}
            onClick={() => void run(selectedIds)}
          >
            重连所选
          </button>
        </div>
      )}
    </section>
  );
}
