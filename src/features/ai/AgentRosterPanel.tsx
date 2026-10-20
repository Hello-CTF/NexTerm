import { useCallback, useEffect, useState } from "react";
import { useUi } from "../../app/store";
import { ask } from "../../ui/dialogs";
import { describeError } from "../../ui/errorText";
import {
  IconBot,
  IconCheck,
  IconLoader,
  IconRefresh,
  IconShield,
  IconTerminal,
  IconXCircle,
} from "../../ui/icons";
import type { AgentRosterApi } from "./agentApi";
import {
  autoWriteAllowed,
  formatEvidence,
  handleAtHostCwd,
  isValidHandleName,
  mentionToken,
  presenceView,
  runStateView,
  type AgentStateTone,
} from "./agentState";
import type { AgentPresence, AgentRosterEntry } from "./agentTypes";

const TONE_BADGE: Record<AgentStateTone, string> = {
  ok: "nx-badge nx-badge-green",
  warn: "nx-badge nx-badge-amber",
  error: "nx-badge nx-badge-red",
  muted: "nx-count",
  accent: "nx-badge nx-badge-blue",
};

// Presences whose tab/process is authoritatively over: there is nothing left
// to open or attach to.
const DEAD_PRESENCES: ReadonlySet<AgentPresence> = new Set<AgentPresence>([
  "gone",
  "replaced",
  "exited",
]);

function deadReason(presence: AgentPresence): string {
  switch (presence) {
    case "gone":
      return "句柄已消失，无法打开";
    case "replaced":
      return "句柄已被新化身替换，无法打开";
    default:
      return "终端已退出，无法打开";
  }
}

function PresenceIcon({ presence }: { presence: AgentPresence }) {
  if (presence === "attached") return <IconCheck size={12} className="shrink-0 text-green-300" />;
  if (presence === "detached") return <IconBot size={12} className="shrink-0 text-neutral-400" />;
  if (presence === "disconnected" || presence === "unverifiable")
    return <IconShield size={12} className="shrink-0 text-amber-300" />;
  return <IconXCircle size={12} className="shrink-0 text-neutral-500" />;
}

export function AgentRosterPanel({
  api,
  onMention,
  onOpened,
}: {
  api: AgentRosterApi;
  onMention?: (token: string, entry: AgentRosterEntry) => void;
  onOpened?: (tabId: string, entry: AgentRosterEntry) => void;
}) {
  const pushToast = useUi((s) => s.pushToast);
  const [entries, setEntries] = useState<AgentRosterEntry[] | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [busy, setBusy] = useState<Record<string, boolean>>({});
  const [renaming, setRenaming] = useState<string | null>(null);
  const [renameDraft, setRenameDraft] = useState("");
  const [renameError, setRenameError] = useState<string | null>(null);

  const reload = useCallback(async () => {
    try {
      const rows = await api.list();
      setEntries(rows);
      setLoadError(null);
    } catch (e) {
      setLoadError(describeError(e));
    }
  }, [api]);

  useEffect(() => {
    void reload();
  }, [reload]);

  const withBusy = async (handle: string, action: () => Promise<void>) => {
    setBusy((prev) => ({ ...prev, [handle]: true }));
    try {
      await action();
    } finally {
      setBusy((prev) => ({ ...prev, [handle]: false }));
    }
  };

  const openEntry = (entry: AgentRosterEntry) =>
    withBusy(entry.handle, async () => {
      try {
        const { tabId } = await api.open(entry.handle);
        onOpened?.(tabId, entry);
      } catch (e) {
        pushToast("error", `打开句柄失败：${describeError(e)}`);
      }
    });

  const attachEntry = (entry: AgentRosterEntry) =>
    withBusy(entry.handle, async () => {
      try {
        const { tabId } = await api.attach(entry.handle);
        onOpened?.(tabId, entry);
      } catch (e) {
        pushToast("error", `附加句柄失败：${describeError(e)}`);
      }
    });

  const startRename = (entry: AgentRosterEntry) => {
    setRenaming(entry.handle);
    setRenameDraft(entry.handle);
    setRenameError(null);
  };

  const commitRename = (entry: AgentRosterEntry) => {
    const next = renameDraft.trim();
    if (next === entry.handle) {
      setRenaming(null);
      return;
    }
    if (!isValidHandleName(next)) {
      setRenameError("句柄只能包含字母、数字、下划线和连字符，且不能以符号开头");
      return;
    }
    void withBusy(entry.handle, async () => {
      try {
        const updated = await api.rename(entry.handle, next);
        setEntries((prev) =>
          (prev ?? []).map((row) => (row.handle === entry.handle ? updated : row)),
        );
        setRenaming(null);
        pushToast("success", `句柄已重命名为 ${updated.handle}`);
      } catch (e) {
        pushToast("error", `重命名失败：${describeError(e)}`);
      }
    });
  };

  const releaseEntry = (entry: AgentRosterEntry) =>
    withBusy(entry.handle, async () => {
      const productionNote = entry.production
        ? "\n\n目标位于生产环境：释放会影响生产会话，请确认已评估影响。"
        : "";
      const confirmed = await ask(
        `释放句柄「${handleAtHostCwd(entry)}」？\n\n释放后句柄将被回收，关联终端状态可能丢失。${productionNote}`,
        { kind: "warning" },
      );
      if (!confirmed) return;
      try {
        await api.release(entry.handle);
        // The roster stays authoritative: until a reload shows the handle
        // gone, the row remains visible in its release-pending state instead
        // of claiming completion on the client's own say-so.
        setEntries((prev) =>
          (prev ?? []).map((row) =>
            row.handle === entry.handle ? { ...row, releasePending: true } : row,
          ),
        );
        pushToast("success", `句柄 ${entry.handle} 释放已提交`);
      } catch (e) {
        pushToast("error", `释放失败：${describeError(e)}`);
      }
    });

  return (
    <div className="max-h-[55%] shrink-0 overflow-y-auto border-b border-neutral-800/60 bg-neutral-900/60 p-2.5">
      <div className="mb-1.5 flex items-center gap-1.5 text-[11.5px] font-medium text-neutral-200">
        <IconTerminal size={12} />
        Agent 句柄
        <span className="nx-count">{entries ? `${entries.length} 个` : "加载中"}</span>
        <span className="nx-spacer" />
        <button
          className="nx-icon-btn nx-icon-btn-sm"
          title="刷新句柄列表"
          aria-label="刷新句柄列表"
          onClick={() => void reload()}
        >
          <IconRefresh size={11} />
        </button>
      </div>

      {loadError && (
        <div className="mb-1.5 flex items-center gap-1.5 text-[11px] text-red-400">
          <span className="min-w-0 flex-1 truncate">加载失败：{loadError}</span>
          <button className="nx-btn nx-btn-outline nx-btn-xs" onClick={() => void reload()}>
            重试
          </button>
        </div>
      )}

      {!loadError && entries === null && (
        <div className="flex items-center gap-1.5 py-1 text-[11px] text-neutral-500">
          <IconLoader size={12} className="animate-spin" />
          加载句柄列表…
        </div>
      )}

      {entries?.map((entry) => {
        const presence = presenceView(entry.presence, entry.evidence);
        const pending = busy[entry.handle] === true;
        const dead = DEAD_PRESENCES.has(entry.presence);
        const run = entry.lastRun ? runStateView(entry.lastRun.state, entry.lastRun.evidence) : null;
        return (
          <div
            key={entry.handle}
            className="mb-1 rounded-lg border border-neutral-800/60 px-2 py-1.5"
            data-handle={entry.handle}
            data-presence={entry.presence}
          >
            <div className="flex items-center gap-1.5">
              <PresenceIcon presence={entry.presence} />
              <span className="min-w-0 flex-1 truncate font-mono text-[11.5px] text-neutral-200" title={handleAtHostCwd(entry)}>
                {handleAtHostCwd(entry)}
              </span>
              <span
                className={`shrink-0 ${TONE_BADGE[presence.tone]}`}
                title={presence.evidence ? formatEvidence(presence.evidence) : "无证据"}
              >
                {presence.label}
              </span>
              {run ? (
                <span
                  className={`shrink-0 ${TONE_BADGE[run.tone]}`}
                  title={run.evidence ? formatEvidence(run.evidence) : "无证据"}
                >
                  {run.label}
                </span>
              ) : null}
            </div>

            <div className="mt-0.5 flex flex-wrap items-center gap-1 text-[10.5px]">
              {!entry.isMine && (
                <span className="nx-badge nx-badge-amber" title="仅所有者可执行重命名和释放">
                  所有者：{entry.ownerName}
                </span>
              )}
              {entry.production && <span className="nx-badge nx-badge-red">生产环境</span>}
              {entry.releasePending && <span className="nx-count">释放待确认</span>}
              {!autoWriteAllowed(entry.presence) && (
                <span className="text-[var(--nx-fg-warning)]">自动写入已禁止</span>
              )}
            </div>

            {renaming === entry.handle ? (
              <div className="mt-1 flex items-center gap-1">
                <input
                  className="nx-input min-w-32 flex-1 py-0.5 font-mono text-[11px]"
                  aria-label="新句柄名称"
                  value={renameDraft}
                  disabled={pending}
                  onChange={(event) => {
                    setRenameDraft(event.target.value);
                    setRenameError(null);
                  }}
                  onKeyDown={(event) => {
                    if (event.key === "Enter") commitRename(entry);
                    if (event.key === "Escape") setRenaming(null);
                  }}
                />
                <button
                  className="nx-btn nx-btn-outline nx-btn-xs"
                  disabled={pending}
                  onClick={() => commitRename(entry)}
                >
                  {pending ? "保存中…" : "保存"}
                </button>
                <button
                  className="nx-btn nx-btn-outline nx-btn-xs"
                  disabled={pending}
                  onClick={() => setRenaming(null)}
                >
                  取消
                </button>
              </div>
            ) : (
              <div className="mt-1 flex flex-wrap items-center gap-1">
                <button
                  className="nx-btn nx-btn-outline nx-btn-xs"
                  disabled={pending || dead || entry.releasePending}
                  title={dead ? deadReason(entry.presence) : "在终端标签页中打开"}
                  onClick={() => void openEntry(entry)}
                >
                  打开
                </button>
                <button
                  className="nx-btn nx-btn-outline nx-btn-xs"
                  disabled={pending || dead || entry.releasePending}
                  title={
                    dead
                      ? deadReason(entry.presence)
                      : entry.releasePending
                        ? "释放待确认，无法附加"
                        : "附加到现有会话"
                  }
                  onClick={() => void attachEntry(entry)}
                >
                  附加
                </button>
                <button
                  className="nx-btn nx-btn-outline nx-btn-xs"
                  disabled={pending || !entry.isMine || entry.releasePending}
                  title={entry.isMine ? "重命名句柄" : "仅所有者可重命名"}
                  onClick={() => startRename(entry)}
                >
                  重命名
                </button>
                <button
                  className="nx-btn nx-btn-outline nx-btn-xs"
                  title="在对话中提及该句柄"
                  onClick={() => onMention?.(mentionToken(entry), entry)}
                >
                  提及
                </button>
                <button
                  className="nx-btn nx-btn-outline nx-btn-xs"
                  disabled={pending || !entry.isMine || entry.releasePending}
                  title={entry.isMine ? "释放句柄" : "仅所有者可释放"}
                  onClick={() => void releaseEntry(entry)}
                >
                  {pending ? "操作中…" : "释放"}
                </button>
              </div>
            )}

            {renaming === entry.handle && renameError && (
              <div className="mt-0.5 text-[10.5px] text-red-300">{renameError}</div>
            )}
          </div>
        );
      })}

      {!loadError && entries !== null && entries.length === 0 && (
        <div className="py-1 text-[11px] text-neutral-500">暂无句柄</div>
      )}
    </div>
  );
}
