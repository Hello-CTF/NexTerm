import { useEffect, useMemo, useState } from "react";
import { useUi, type WorkspaceRestoreStatus } from "../../app/store";
import { IconClose } from "../../ui/icons";
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

// A fully restored banner stays just long enough to be read, then goes away on its own.
const RESTORED_LINGER_MS = 3000;

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

  const hosts = items.filter((item) => item.kind === "session");
  const isOpen = (item: RestoreItem) => item.eligible && item.status !== "success" && item.status !== "skipped";
  const pendingCount = hosts.filter(isOpen).length;
  const failedCount = hosts.filter((item) => item.status === "failed").length;
  const successCount = hosts.filter((item) => item.status === "success").length;
  const skippedCount = items.filter((item) => item.status === "skipped").length;
  const eligibleIds = candidates.filter((candidate) => candidate.eligible).map((candidate) => candidate.workspaceId);
  const selectedIds = [...selected].filter((id) => eligibleIds.includes(id));
  const restored = pendingCount === 0 && successCount > 0;
  // Nothing to reconnect and nothing reconnected: skipped-only workspaces are not worth a banner.
  const hidden = !ready || dismissed || (pendingCount === 0 && successCount === 0);

  useEffect(() => {
    if (hidden || !restored || running) return;
    const timer = window.setTimeout(dismiss, RESTORED_LINGER_MS);
    return () => window.clearTimeout(timer);
  }, [hidden, restored, running, dismiss]);

  if (hidden) return null;

  const openSelection = () => {
    if (!expanded) setSelected(new Set(eligibleIds));
    setExpanded(!expanded);
  };
  const run = async (ids?: string[]) => {
    if (running) return;
    setRunning(true);
    try {
      await reconnectRestoredWorkspaces(ids);
    } finally {
      setRunning(false);
    }
    // Failures need their reason on screen; a clean run does not.
    if (Object.values(useUi.getState().workspaceRestoreResults).some((result) => result.status === "failed")) {
      setExpanded(true);
    }
  };
  const skippedNote = skippedCount > 0 ? `，${skippedCount} 个已跳过` : "";
  const summary = running
    ? "正在重连工作区…"
    : restored
      ? `已恢复 ${successCount} 个工作区${skippedNote}`
      : failedCount > 0
        ? `${failedCount} 个主机工作区连接失败`
        : `${pendingCount} 个主机工作区需要重新连接`;
  const listable = expanded && !restored;

  return (
    <section
      aria-label="工作区恢复"
      className="relative shrink-0 border-b border-neutral-800/60 bg-neutral-900 text-xs text-neutral-300"
      onKeyDown={(event) => {
        if (event.key === "Escape" && expanded) setExpanded(false);
      }}
    >
      <div className="flex flex-wrap items-center gap-2 px-4 py-1.5">
        <span className={failedCount > 0 && !running ? "text-red-300" : undefined}>{summary}</span>
        {!restored && (
          <>
            <button
              type="button"
              className="nx-btn nx-btn-success nx-btn-sm"
              disabled={running || eligibleIds.length === 0}
              onClick={() => void run()}
            >
              {running ? "正在重连…" : "全部重连"}
            </button>
            <button
              type="button"
              className="nx-btn nx-btn-ghost nx-btn-sm"
              aria-expanded={expanded}
              onClick={openSelection}
            >
              选择…
            </button>
          </>
        )}
        <button
          type="button"
          className="nx-icon-btn nx-icon-btn-sm ml-auto"
          aria-label="关闭工作区恢复提示"
          title="关闭"
          onClick={dismiss}
        >
          <IconClose size={12} />
        </button>
      </div>
      {listable && (
        <div className="absolute left-4 top-full z-30 mt-1 w-[min(440px,calc(100%-2rem))] rounded-lg border border-neutral-700 bg-neutral-900 p-2 shadow-lg">
          <ul aria-label="可恢复工作区" className="flex flex-col gap-1.5">
            {items.map((item) => {
              const selectable = isOpen(item);
              return (
                <li key={item.workspaceId} className="flex flex-wrap items-center gap-x-3 gap-y-1">
                  {selectable ? (
                    <label className="flex min-w-0 items-center gap-2">
                      <input
                        type="checkbox"
                        aria-label={`选择 ${item.title}`}
                        checked={selectedIds.includes(item.workspaceId)}
                        disabled={running}
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
                  ) : (
                    <span className="min-w-0 truncate pl-5 text-neutral-400">{item.title}</span>
                  )}
                  <span className={item.status === "failed" ? "text-red-300" : "text-neutral-400"}>
                    {statusText(item)}
                  </span>
                  {item.detail && <span className="text-neutral-500">{item.detail}</span>}
                </li>
              );
            })}
          </ul>
          <button
            type="button"
            className="nx-btn nx-btn-outline nx-btn-sm mt-2"
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
