import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { freezeOrder } from "../../app/jump/jumpRanking";
import { useUi } from "../../app/store";
import { wailsNoDragRegionStyle } from "../../app/platform";
import { TabAttentionBadge } from "../../app/workspace/tabStrip";
import { describeError } from "../../ui/errorText";
import { IconBell, IconCheck } from "../../ui/icons";
import { InboxGuidance } from "./InboxGuidance";
import { isInboxGuidanceDismissed, useInboxGuidanceDismissed } from "./firstRunGuidance";
import { markInboxEntrySeen, openAggregateEntry, openInboxTab, useInboxEntries, type InboxEntry } from "./inbox";

const CUE_MS = 8000;

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
  const [opening, setOpening] = useState(false);
  const open = () => {
    if (openInboxTab(entry.tabId)) {
      onOpen();
      return;
    }
    if (!entry.host) return;
    // A session on a host this client holds no tab for is taken over through that host.
    setOpening(true);
    void openAggregateEntry(entry)
      .then((problem) => {
        if (problem) useUi.getState().pushToast("error", `无法打开终端：${problem}`);
        else onOpen();
      })
      .catch((error: unknown) => useUi.getState().pushToast("error", `无法打开终端：${describeError(error)}`))
      .finally(() => setOpening(false));
  };
  const markSeen = () => {
    setMarking(true);
    markInboxEntrySeen(entry)
      .catch((error: unknown) => useUi.getState().pushToast("error", `标记已读失败：${describeError(error)}`))
      .finally(() => setMarking(false));
  };
  const shown = entry.uiTabId !== undefined;
  const reachable = shown || entry.host !== undefined;
  // The seen mark goes through a tab this client holds; a session known only from the listing is marked once opened.
  const markable = shown || !entry.listedOnly;
  return (
    <li className="nx-inbox-row rounded-md px-2 py-1.5 hover:bg-neutral-800/60" data-inbox-tab={entry.tabId}>
      <div className="flex items-start gap-2">
        <button
          type="button"
          className="flex min-w-0 flex-1 items-start gap-2 text-left disabled:cursor-default"
          style={wailsNoDragRegionStyle}
          disabled={!reachable || opening}
          title={shown ? "转到该终端" : reachable ? `在此端打开（${entry.place}）` : "此端没有打开这个终端"}
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
          style={wailsNoDragRegionStyle}
          aria-label={`标记 ${entry.title} 为已读`}
          title={markable ? "标记为已读（所有设备）" : "打开这个终端后才能标记为已读"}
          disabled={marking || !markable}
          onClick={markSeen}
        >
          <IconCheck size={14} />
        </button>
      </div>
      {(!reachable || !markable) && (
        <div className="pl-[18px] pt-0.5 text-[11px] text-[var(--nx-fg-soft)]" data-inbox-reason="">
          {!reachable && <p>此端没有打开这个终端，无法转到它。</p>}
          {!markable && <p>打开这个终端后才能标记为已读。</p>}
        </div>
      )}
    </li>
  );
}

// InboxButton is the header bell: the number of sessions that need the user or just finished, across every
// workspace this client knows, and a list to jump to them or mark them read on every device.
export function InboxButton() {
  const listed = useInboxEntries();
  const [open, setOpen] = useState(false);
  // While the list is open its rows keep their order (new ones join at the end), and while a finger or button is
  // down they hold still entirely: a row moving under a press would answer another session's prompt.
  const orderRef = useRef<string[] | null>(null);
  const ordered = useMemo(() => {
    if (!open) return listed;
    const byTabId = new Map(listed.map((entry) => [entry.tabId, entry]));
    return freezeOrder(orderRef.current, listed.map((entry) => entry.tabId)).map((tabId) => byTabId.get(tabId)!);
  }, [listed, open]);
  useLayoutEffect(() => {
    orderRef.current = open ? ordered.map((entry) => entry.tabId) : null;
  }, [ordered, open]);
  const [held, setHeld] = useState<InboxEntry[] | null>(null);
  useEffect(() => {
    if (!held) return;
    const release = () => setHeld(null);
    window.addEventListener("pointerup", release);
    window.addEventListener("pointercancel", release);
    return () => {
      window.removeEventListener("pointerup", release);
      window.removeEventListener("pointercancel", release);
    };
  }, [held]);
  const entries = held ?? ordered;
  const [now, setNow] = useState(() => Date.now());
  const rootRef = useRef<HTMLDivElement>(null);
  const bellRef = useRef<HTMLButtonElement>(null);
  const panelRef = useRef<HTMLDivElement>(null);
  const count = entries.length;
  const guidanceDismissed = useInboxGuidanceDismissed();
  const guidanceOpened = useRef(false);
  const [cue, setCue] = useState(false);
  const needsMe = entries.some((entry) => entry.awareness.state === "needs_me");

  // The first time the bell gets a count on a device, show a short cue next to it. It never takes focus, so
  // typing into a terminal is not interrupted; it fades after a few seconds or when the bell is used.
  useEffect(() => {
    if (count === 0 || guidanceOpened.current || isInboxGuidanceDismissed()) return;
    guidanceOpened.current = true;
    setCue(true);
    const timer = setTimeout(() => setCue(false), CUE_MS);
    return () => clearTimeout(timer);
  }, [count]);

  // While the list is open, focus starts in it and the "minutes ago" labels keep up.
  useEffect(() => {
    if (!open) return;
    setNow(Date.now());
    const panel = panelRef.current;
    // The first session row comes before the first-run guidance, so the keyboard lands on what needs attention.
    (
      panel?.querySelector<HTMLElement>("[data-inbox-tab] button:not([disabled])") ??
      panel?.querySelector<HTMLElement>("button:not([disabled])") ??
      panel
    )?.focus();
    const timer = setInterval(() => setNow(Date.now()), 30_000);
    return () => clearInterval(timer);
  }, [open]);

  useEffect(() => {
    if (!open) return;
    const onDown = (event: PointerEvent) => {
      if (!rootRef.current?.contains(event.target as Node)) setOpen(false);
    };
    const onKey = (event: KeyboardEvent) => {
      if (event.key !== "Escape") return;
      // Escape belongs to whatever has focus elsewhere (a terminal running vim); only the panel and bell take it.
      if (!rootRef.current?.contains(document.activeElement)) return;
      setOpen(false);
      bellRef.current?.focus();
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
    <div
      className="relative"
      ref={rootRef}
      style={wailsNoDragRegionStyle}
      onBlur={(event) => {
        // Tabbing out of the open panel closes it; a null target (the window losing focus) keeps it.
        const next = event.relatedTarget as Node | null;
        if (open && next && !event.currentTarget.contains(next)) setOpen(false);
      }}
    >
      <button
        ref={bellRef}
        type="button"
        className={`nx-icon-btn relative ${open ? "is-active" : ""}`}
        title={label}
        aria-label={label}
        style={wailsNoDragRegionStyle}
        aria-haspopup="dialog"
        aria-expanded={open}
        onClick={() => {
          setCue(false);
          setOpen((value) => !value);
        }}
      >
        <IconBell size={15} />
        {count > 0 && (
          <span
            className={`absolute -right-0.5 -top-0.5 min-w-[14px] rounded-full px-[3px] text-center text-[9.5px] font-semibold leading-[14px] text-neutral-950 ${
              needsMe ? "bg-amber-400" : "bg-green-400"
            }`}
            data-inbox-count={count}
            style={wailsNoDragRegionStyle}
          >
            {count > 99 ? "99+" : count}
          </span>
        )}
      </button>
      <span role="status" aria-live="polite" style={wailsNoDragRegionStyle} className={
          cue && !open
            ? "pointer-events-none absolute right-0 top-[30px] z-40 whitespace-nowrap rounded-md border border-neutral-800 bg-neutral-900 px-2 py-1 text-[11.5px] text-neutral-300 shadow-lg"
            : "sr-only"
        } data-inbox-cue={cue && !open ? "" : undefined}>
        {cue && !open ? "新：有会话需要关注，点铃铛查看" : ""}
      </span>
      {open && (
        <div
          ref={panelRef}
          role="dialog"
          aria-label="收件箱"
          tabIndex={-1}
          onPointerDownCapture={() => setHeld(ordered)}
          style={wailsNoDragRegionStyle}
          className="nx-inbox-panel fixed inset-x-2 top-[calc(var(--nx-inset-top,0px)+42px)] z-50 max-h-[70vh] overflow-y-auto rounded-lg border border-neutral-800 bg-neutral-900 p-1.5 shadow-xl sm:absolute sm:inset-x-auto sm:right-0 sm:top-[30px] sm:w-[360px]"
        >
          {!guidanceDismissed && <InboxGuidance onNavigate={() => setOpen(false)} />}
          <div className="px-2 pb-1 pt-0.5 text-[11.5px] font-semibold text-neutral-400">需要关注的会话</div>
          {count === 0 ? (
            <p className="px-2 py-3 text-[12px] text-neutral-500">没有需要你处理或刚结束的会话。</p>
          ) : (
            <ul className="flex flex-col">
              {entries.map((entry) => (
                <InboxRow key={entry.tabId} entry={entry} now={now} onOpen={() => setOpen(false)} />
              ))}
            </ul>
          )}
        </div>
      )}
    </div>
  );
}
