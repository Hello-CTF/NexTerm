import { useEffect, useRef, useState } from "react";
import { useUi } from "../../app/store";
import { wailsNoDragRegionStyle } from "../../app/platform";
import { TabAttentionBadge } from "../../app/workspace/tabStrip";
import { describeError } from "../../ui/errorText";
import { IconBell, IconCheck } from "../../ui/icons";
import { markInboxEntrySeen, openInboxTab, useInboxEntries, type InboxEntry } from "./inbox";

function sinceLabel(since: string | undefined, now: number): string {
  const at = since ? Date.parse(since) : Number.NaN;
  if (Number.isNaN(at)) return "";
  const minutes = Math.max(0, Math.floor((now - at) / 60_000));
  if (minutes < 1) return "刚刚";
  if (minutes < 60) return `${minutes} 分钟前`;
  const hours = Math.floor(minutes / 60);
  return hours < 24 ? `${hours} 小时前` : `${Math.floor(hours / 24)} 天前`;
}

function InboxRow({ entry, now, onOpen }: { entry: InboxEntry; now: number; onOpen: () => void }) {
  const [marking, setMarking] = useState(false);
  const open = () => {
    if (!openInboxTab(entry.tabId)) return;
    onOpen();
  };
  const markSeen = () => {
    setMarking(true);
    markInboxEntrySeen(entry)
      .catch((error: unknown) => useUi.getState().pushToast("error", `标记已读失败：${describeError(error)}`))
      .finally(() => setMarking(false));
  };
  const reachable = entry.uiTabId !== undefined;
  return (
    <li className="nx-inbox-row flex items-start gap-2 rounded-md px-2 py-1.5 hover:bg-neutral-800/60" data-inbox-tab={entry.tabId}>
      <button
        type="button"
        className="flex min-w-0 flex-1 items-start gap-2 text-left disabled:cursor-default"
        disabled={!reachable}
        title={reachable ? "转到该终端" : "此端没有打开这个终端"}
        onClick={open}
      >
        <span className="mt-[5px] flex w-2.5 shrink-0 justify-center">
          <TabAttentionBadge badge={entry.badge} />
        </span>
        <span className="min-w-0 flex-1">
          <span className="flex items-baseline gap-1.5">
            <span className="truncate text-[12.5px] font-medium text-neutral-100">{entry.title}</span>
            <span className="shrink-0 text-[11px] text-neutral-500">{sinceLabel(entry.awareness.since, now)}</span>
          </span>
          <span className="block truncate text-[11.5px] text-neutral-300">{entry.detail}</span>
          <span className="block truncate text-[11px] text-neutral-500">{entry.place}</span>
        </span>
      </button>
      <button
        type="button"
        className="nx-icon-btn shrink-0"
        aria-label={`标记 ${entry.title} 为已读`}
        title="标记为已读（所有设备）"
        disabled={marking}
        onClick={markSeen}
      >
        <IconCheck size={14} />
      </button>
    </li>
  );
}

// InboxButton is the header bell: the number of sessions that need the user or just finished, across every
// workspace this client knows, and a list to jump to them or mark them read on every device.
export function InboxButton() {
  const entries = useInboxEntries();
  const [open, setOpen] = useState(false);
  const rootRef = useRef<HTMLDivElement>(null);
  const count = entries.length;
  const needsMe = entries.some((entry) => entry.awareness.state === "needs_me");

  useEffect(() => {
    if (!open) return;
    const onDown = (event: PointerEvent) => {
      if (!rootRef.current?.contains(event.target as Node)) setOpen(false);
    };
    const onKey = (event: KeyboardEvent) => {
      if (event.key === "Escape") setOpen(false);
    };
    window.addEventListener("pointerdown", onDown, true);
    window.addEventListener("keydown", onKey);
    return () => {
      window.removeEventListener("pointerdown", onDown, true);
      window.removeEventListener("keydown", onKey);
    };
  }, [open]);

  const label = count > 0 ? `收件箱：${count} 个会话需要关注` : "收件箱：没有需要关注的会话";
  return (
    <div className="relative" ref={rootRef} style={wailsNoDragRegionStyle}>
      <button
        type="button"
        className={`nx-icon-btn relative ${open ? "is-active" : ""}`}
        title={label}
        aria-label={label}
        aria-haspopup="dialog"
        aria-expanded={open}
        onClick={() => setOpen((value) => !value)}
      >
        <IconBell size={15} />
        {count > 0 && (
          <span
            className={`absolute -right-0.5 -top-0.5 min-w-[14px] rounded-full px-[3px] text-center text-[9.5px] font-semibold leading-[14px] text-neutral-950 ${
              needsMe ? "bg-amber-400" : "bg-emerald-400"
            }`}
            data-inbox-count={count}
          >
            {count > 99 ? "99+" : count}
          </span>
        )}
      </button>
      {open && (
        <div
          role="dialog"
          aria-label="收件箱"
          className="nx-inbox-panel fixed inset-x-2 top-[calc(var(--nx-inset-top,0px)+42px)] z-50 max-h-[70vh] overflow-y-auto rounded-lg border border-neutral-800 bg-neutral-900 p-1.5 shadow-xl sm:absolute sm:inset-x-auto sm:right-0 sm:top-[30px] sm:w-[360px]"
        >
          <div className="px-2 pb-1 pt-0.5 text-[11.5px] font-semibold text-neutral-400">需要关注的会话</div>
          {count === 0 ? (
            <p className="px-2 py-3 text-[12px] text-neutral-500">没有需要你处理或刚结束的会话。</p>
          ) : (
            <ul className="flex flex-col">
              {entries.map((entry) => (
                <InboxRow key={entry.tabId} entry={entry} now={Date.now()} onOpen={() => setOpen(false)} />
              ))}
            </ul>
          )}
        </div>
      )}
    </div>
  );
}
