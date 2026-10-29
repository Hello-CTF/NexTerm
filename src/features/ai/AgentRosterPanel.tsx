import { useCallback, useEffect, useRef, useState } from "react";
import { useUi } from "../../app/store";
import { ask } from "../../ui/dialogs";
import { describeError } from "../../ui/errorText";
import {
  IconAlert,
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

// Mirrors the backend alias bound (roster.go): a rename fails at the contract
// boundary with a visible reason instead of deep in a backend.
const MAX_ALIAS_RUNES = 191;

// Presences that may be terminally over. They only count as dead when the
// evidence view confirms them; without evidence the row stays unknown.
const DEAD_PRESENCES: ReadonlySet<AgentPresence> = new Set<AgentPresence>([
  "gone",
  "replaced",
  "exited",
]);

function deadReason(presence: AgentPresence): string {
  switch (presence) {
    case "gone":
      return "子代理已不存在，无法打开";
    case "replaced":
      return "子代理已被新会话替换，无法打开";
    default:
      return "终端已退出，无法打开";
  }
}

function PresenceIcon({ presence, unknown }: { presence: AgentPresence; unknown: boolean }) {
  if (unknown) return <IconAlert size={12} className="shrink-0 text-amber-300" />;
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
  onChanged,
}: {
  api: AgentRosterApi;
  onChanged?: () => void;
  onMention?: (token: string, entry: AgentRosterEntry) => void;
  onOpened?: (tabId: string, entry: AgentRosterEntry) => void;
}) {
  const pushToast = useUi((s) => s.pushToast);
  const [entries, setEntries] = useState<AgentRosterEntry[] | null>(null);
  const [refreshing, setRefreshing] = useState(false);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [busy, setBusy] = useState<Record<string, boolean>>({});
  const [renaming, setRenaming] = useState<string | null>(null);
  const [renameDraft, setRenameDraft] = useState("");
  const [renameError, setRenameError] = useState<string | null>(null);

  const onChangedRef = useRef(onChanged);
  onChangedRef.current = onChanged;
  const aliveRef = useRef(true);
  const loadGenRef = useRef(0);
  const invalidatePendingLoads = useCallback(() => {
    aliveRef.current = false;
    loadGenRef.current++;
  }, []);
  useEffect(() => {
    aliveRef.current = true;
    return invalidatePendingLoads;
  }, [invalidatePendingLoads]);

  // Load generation + liveness guard (same pattern as CronPanel): an older
  // list response must never roll back a newer roster or local mutation
  // state such as a submitted release-pending flag.
  const reload = useCallback(async () => {
    const gen = ++loadGenRef.current;
    setRefreshing(true);
    try {
      const rows = await api.list();
      if (!aliveRef.current || gen !== loadGenRef.current) return;
      setEntries(rows);
      setLoadError(null);
      onChangedRef.current?.();
    } catch (e) {
      if (!aliveRef.current || gen !== loadGenRef.current) return;
      setLoadError(describeError(e));
    } finally {
      if (aliveRef.current && gen === loadGenRef.current) setRefreshing(false);
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
        pushToast("error", `未能打开子代理：${describeError(e)}`);
      }
    });

  const attachEntry = (entry: AgentRosterEntry) =>
    withBusy(entry.handle, async () => {
      try {
        const { tabId } = await api.attach(entry.handle);
        onOpened?.(tabId, entry);
      } catch (e) {
        pushToast("error", `未能附加子代理：${describeError(e)}`);
      }
    });

  const startRename = (entry: AgentRosterEntry) => {
    setRenaming(entry.handle);
    setRenameDraft(entry.alias);
    setRenameError(null);
  };

  // Rename edits only the display alias (the real backend contract): the
  // handle is permanent, so the draft starts from the current alias and the
  // validation mirrors the backend's alias rules (trimmed non-empty, bounded
  // runes) instead of the mention-safe handle pattern.
  const commitRename = (entry: AgentRosterEntry) => {
    const next = renameDraft.trim();
    if (next === entry.alias) {
      setRenaming(null);
      return;
    }
    if (next === "") {
      setRenameError("请输入别名");
      return;
    }
    if ([...next].length > MAX_ALIAS_RUNES) {
      setRenameError(`别名最多 ${MAX_ALIAS_RUNES} 个字`);
      return;
    }
    void withBusy(entry.handle, async () => {
      try {
        const updated = await api.rename(entry.handle, next);
        // Invalidate any in-flight list response so it cannot resurrect the
        // pre-rename roster over this local mutation.
        loadGenRef.current++;
        setEntries((prev) =>
          (prev ?? []).map((row) => (row.handle === entry.handle ? updated : row)),
        );
        setRenaming(null);
        pushToast("success", `子代理 ${entry.handle} 的别名已更新为 ${updated.alias}`);
      } catch (e) {
        pushToast("error", `未能重命名：${describeError(e)}`);
      }
    });
  };

  const releaseEntry = (entry: AgentRosterEntry) =>
    withBusy(entry.handle, async () => {
      const productionNote = entry.production
        ? "\n\n该子代理位于生产环境，释放会影响正在运行的生产会话，请确认后再继续。"
        : "";
      const confirmed = await ask(
        `确定要释放子代理「${handleAtHostCwd(entry)}」吗？\n\n释放后将回收该子代理，它所在终端中未保存的状态可能丢失。${productionNote}`,
        { kind: "warning" },
      );
      if (!confirmed) return;
      try {
        await api.release(entry.handle);
        // Invalidate any in-flight list response so a stale roster cannot
        // clear the release-pending flag and re-enable dangerous actions.
        loadGenRef.current++;
        // The roster stays authoritative: until a reload shows the handle
        // gone, the row remains visible in its release-pending state instead
        // of claiming completion on the client's own say-so.
        setEntries((prev) =>
          (prev ?? []).map((row) =>
            row.handle === entry.handle ? { ...row, releasePending: true } : row,
          ),
        );
        onChangedRef.current?.();
        pushToast("success", `已提交释放子代理 ${entry.handle}`);
      } catch (e) {
        pushToast("error", `未能释放：${describeError(e)}`);
      }
    });

  return (
    <div className="max-h-[55%] shrink-0 overflow-y-auto border-b border-neutral-800/60 bg-neutral-900/60 p-2.5">
      <div className="mb-1.5 flex items-center gap-1.5 text-[11.5px] font-medium text-neutral-200">
        <IconTerminal size={12} />
        子代理与终端会话
        <span className="nx-count">{entries ? `${entries.length} 个` : "加载中"}</span>
        <span className="nx-spacer" />
        <button
          className="nx-icon-btn nx-icon-btn-sm"
          title="刷新子代理列表"
          aria-label="刷新子代理列表"
          aria-busy={refreshing}
          onClick={() => void reload()}
        >
          {refreshing ? <IconLoader size={11} className="animate-spin" /> : <IconRefresh size={11} />}
        </button>
      </div>

      {loadError && (
        <div className="mb-1.5 flex items-center gap-1.5 text-[11px] text-red-400">
          <span className="min-w-0 flex-1 truncate">未能加载子代理列表：{loadError}</span>
          <button className="nx-btn nx-btn-outline nx-btn-xs" onClick={() => void reload()}>
            重试
          </button>
        </div>
      )}

      {!loadError && entries === null && (
        <div className="flex items-center gap-1.5 py-1 text-[11px] text-neutral-500">
          <IconLoader size={12} className="animate-spin" />
          加载子代理列表…
        </div>
      )}

      {entries?.map((entry) => {
        const presence = presenceView(entry.presence, entry.evidence);
        const pending = busy[entry.handle] === true;
        // Terminal presences only count as dead when evidence backs them;
        // without evidence the row stays "unknown" and the actions must not
        // claim a definitive reason.
        const terminal = DEAD_PRESENCES.has(entry.presence);
        const confirmedDead = terminal && !presence.unknown;
        const unknownTerminal = terminal && presence.unknown;
        const run = entry.lastRun ? runStateView(entry.lastRun.state, entry.lastRun.evidence) : null;
        return (
          <div
            key={entry.handle}
            className="mb-1 rounded-lg border border-neutral-800/60 px-2 py-1.5"
            data-handle={entry.handle}
            data-presence={entry.presence}
          >
            <div className="flex items-center gap-1.5">
              <PresenceIcon presence={entry.presence} unknown={presence.unknown} />
              <span className="min-w-0 flex-1 truncate font-mono text-[11.5px] text-neutral-200" title={handleAtHostCwd(entry)}>
                {handleAtHostCwd(entry)}
              </span>
              <span
                className={`shrink-0 ${TONE_BADGE[presence.tone]}`}
                title={presence.evidence ? formatEvidence(presence.evidence) : "暂无依据"}
              >
                {presence.label}
              </span>
              {run ? (
                <span
                  className={`shrink-0 ${TONE_BADGE[run.tone]}`}
                  title={run.evidence ? formatEvidence(run.evidence) : "暂无依据"}
                >
                  {run.label}
                </span>
              ) : null}
            </div>

            <div className="mt-0.5 flex flex-wrap items-center gap-1 text-[10.5px]">
              {entry.alias !== entry.handle && (
                <span className="nx-count" title={`子代理编号 ${entry.handle} 不会改变，此处仅显示别名`}>
                  别名 {entry.alias}
                </span>
              )}
              {!entry.isMine && (
                <span className="nx-badge nx-badge-amber" title="仅所有者可重命名和释放">
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
              <div className="mt-1">
                <div className="flex items-center gap-1">
                  <input
                    className="nx-input min-w-32 flex-1 py-0.5 font-mono text-[11px]"
                    aria-label="新别名"
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
                <div className="mt-0.5 text-[10.5px] text-neutral-500">
                  仅修改显示别名，子代理编号 {entry.handle} 不会改变
                </div>
              </div>
            ) : (
              <div className="mt-1 flex flex-wrap items-center gap-1">
                <button
                  className="nx-btn nx-btn-outline nx-btn-xs"
                  disabled={pending || terminal || entry.releasePending}
                  title={
                    confirmedDead
                      ? deadReason(entry.presence)
                      : unknownTerminal
                        ? "子代理状态未知，暂时不能打开"
                        : "在终端标签页中打开"
                  }
                  onClick={() => void openEntry(entry)}
                >
                  打开
                </button>
                <button
                  className="nx-btn nx-btn-outline nx-btn-xs"
                  disabled={pending || terminal || entry.releasePending}
                  title={
                    confirmedDead
                      ? deadReason(entry.presence)
                      : unknownTerminal
                        ? "子代理状态未知，暂时不能附加"
                        : entry.releasePending
                          ? "正在释放，无法附加"
                          : "附加到现有终端会话"
                  }
                  onClick={() => void attachEntry(entry)}
                >
                  附加
                </button>
                <button
                  className="nx-btn nx-btn-outline nx-btn-xs"
                  disabled={pending || !entry.isMine || entry.releasePending}
                  title={entry.isMine ? "重命名子代理" : "仅所有者可重命名"}
                  onClick={() => startRename(entry)}
                >
                  重命名
                </button>
                <button
                  className="nx-btn nx-btn-outline nx-btn-xs"
                  title="在对话中 @ 该子代理"
                  onClick={() => onMention?.(mentionToken(entry), entry)}
                >
                  提及
                </button>
                <button
                  className="nx-btn nx-btn-outline nx-btn-xs"
                  disabled={pending || !entry.isMine || entry.releasePending}
                  title={entry.isMine ? "释放子代理" : "仅所有者可释放"}
                  onClick={() => void releaseEntry(entry)}
                >
                  {pending ? "处理中…" : "释放"}
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
        <div className="py-1 text-[11px] text-neutral-500">尚无子代理。AI 创建子代理后将显示在此处。</div>
      )}
    </div>
  );
}
