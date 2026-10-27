import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type ReactNode, type TouchEvent } from "react";
import { useUi, useWorkspaceList } from "../../app/store";
import { useAwarenessStore } from "../../app/sessionAwarenessStore";
import { aggregateRows, reloadSessionAggregate, useSessionAggregate } from "../../app/sessionAggregate";
import { freezeOrder } from "../../app/jump/jumpRanking";
import { TabAttentionBadge } from "../../app/workspace/tabStrip";
import { describeError } from "../../ui/errorText";
import { IconRefresh, IconServer } from "../../ui/icons";
import { openInboxTab } from "../inbox/inbox";
import { takeOverAggregateSession, unknownHostText } from "../terminal/backgroundSessionsAccess";
import { buildStatusWall, WALL_GROUP_LABELS, WALL_GROUPS, type WallCard, type WallGroup } from "./statusWall";

// A downward drag this far from the top of the list refreshes it.
export const PULL_REFRESH_PX = 64;
const CLOCK_MS = 30_000;

export interface StatusWallProps {
  // Called once a card's session is open, so the shell can show it.
  onOpened: () => void;
  // Opens the asset drawer, for the empty wall.
  onOpenAssets?: () => void;
  // Extra controls on a card, such as the choice buttons of a session that needs the user. Clicks in them do not
  // open the session; open opens it as a click on the card does.
  cardActions?: (card: WallCard, controls: { open: () => void; opening: boolean }) => ReactNode;
}

function ago(at: number, now: number): string {
  if (at <= 0) return "";
  const minutes = Math.max(0, Math.floor((now - at) / 60_000));
  if (minutes < 1) return "刚刚";
  if (minutes < 60) return `${minutes} 分钟前`;
  const hours = Math.floor(minutes / 60);
  return hours < 24 ? `${hours} 小时前` : `${Math.floor(hours / 24)} 天前`;
}

const STATE_TONE: Record<WallGroup, string> = {
  needs_me: "text-[var(--nx-fg-strong)] font-medium",
  running: "text-[var(--nx-fg)]",
  finished: "text-[var(--nx-fg-soft)]",
  unknown: "text-[var(--nx-fg-soft)]",
};

// A card names its session by title, host and state; the detail, the time and a stale listing describe it. A card
// this client cannot open is a plain item that says why.
function WallCardView({
  card,
  now,
  onOpen,
  opening,
  actions,
}: {
  card: WallCard;
  now: number;
  onOpen: () => void;
  opening: boolean;
  actions?: ReactNode;
}) {
  const reachable = card.uiTabId !== undefined || card.host !== undefined;
  const id = `nx-wall-${card.tabId.replace(/[^A-Za-z0-9_-]/g, "_")}`;
  const time = ago(card.at, now);
  const described = [card.detail ? `${id}-detail` : null, time ? `${id}-time` : null, card.stale ? `${id}-stale` : null]
    .filter(Boolean)
    .join(" ");
  const body = (
    <>
      <span className="mt-[4px] flex w-3 shrink-0 justify-center">
        {opening ? <IconRefresh size={11} className="animate-spin" /> : <TabAttentionBadge badge={card.badge} />}
      </span>
      <span className="min-w-0 flex-1">
        <span className="flex items-baseline gap-2">
          <span id={`${id}-title`} className="truncate text-[13.5px] font-medium text-[var(--nx-fg-strong)]" data-testid="wall-title">{card.title}</span>
          <span id={`${id}-time`} className="ml-auto shrink-0 text-[11.5px] text-[var(--nx-fg-soft)]" data-testid="wall-time">{time}</span>
        </span>
        <span className="flex items-baseline gap-2 text-[12px]">
          <span id={`${id}-host`} className="truncate text-[var(--nx-fg-soft)]" data-testid="wall-host">{card.hostName}</span>
          <span id={`${id}-state`} className={`shrink-0 ${STATE_TONE[card.group]}`} data-testid="wall-state">{card.stateLabel}</span>
        </span>
        {card.detail && <span id={`${id}-detail`} className="block truncate text-[12px] text-[var(--nx-fg)]" data-testid="wall-detail">{card.detail}</span>}
        {card.stale && <span id={`${id}-stale`} className="block text-[11.5px] text-[var(--nx-fg-soft)]" data-testid="wall-stale">这台主机的列表可能已过时</span>}
        {!reachable && <span className="block text-[11.5px] text-[var(--nx-fg-soft)]" data-testid="wall-unreachable">此端无法打开这个终端</span>}
      </span>
    </>
  );
  return (
    <li className="nx-wall-card rounded-md border border-[var(--nx-border-strong)]" data-wall-card={card.tabId} data-state={card.state}>
      {reachable ? (
        <button
          type="button"
          className="flex w-full min-w-0 items-start gap-2.5 px-3 py-2.5 text-left disabled:cursor-default"
          disabled={opening}
          aria-labelledby={`${id}-title ${id}-host ${id}-state`}
          aria-describedby={described || undefined}
          onClick={onOpen}
        >
          {body}
        </button>
      ) : (
        <div className="flex w-full min-w-0 items-start gap-2.5 px-3 py-2.5" data-wall-static="">
          {body}
        </div>
      )}
      {actions && (
        <div className="px-3 pb-2.5" data-wall-actions="">
          {actions}
        </div>
      )}
    </li>
  );
}


// StatusWall is the mobile home: sessions grouped by needs me, running, finished and unknown. It renders at once from
// what is already known (this client's tabs and the last cross-host listing) and fills in as hosts answer. Cards keep
// their place while the wall is shown; a refresh or a return to the app re-sorts them. A card that changes state
// moves to its new group at once.
export function StatusWall({ onOpened, onOpenAssets, cardActions }: StatusWallProps) {
  const byTabId = useAwarenessStore((state) => state.byTabId);
  const workspaces = useWorkspaceList();
  const hosts = useSessionAggregate((state) => state.hosts);
  const sessionsByHost = useSessionAggregate((state) => state.sessionsByHost);
  const loaded = useSessionAggregate((state) => state.loaded);
  const [now, setNow] = useState(() => Date.now());
  const [orderToken, setOrderToken] = useState(0);
  const [refreshing, setRefreshing] = useState(false);
  const [opening, setOpening] = useState<string | null>(null);
  const [pull, setPull] = useState(0);
  const pullStart = useRef<number | null>(null);
  const pullRef = useRef(0);
  const scrollRef = useRef<HTMLDivElement>(null);
  const orderRef = useRef<{ token: number; keys: Record<WallGroup, string[]> } | null>(null);
  // While a finger or button is down the cards hold still, so a state change cannot move another card under it.
  const [held, setHeld] = useState<Record<WallGroup, WallCard[]> | null>(null);

  const model = useMemo(
    () => buildStatusWall(byTabId, workspaces, aggregateRows({ hosts, sessionsByHost }), hosts),
    [byTabId, workspaces, hosts, sessionsByHost],
  );
  const groups = useMemo(() => {
    const previous = orderRef.current?.token === orderToken ? orderRef.current.keys : null;
    const out = {} as Record<WallGroup, WallCard[]>;
    for (const group of WALL_GROUPS) {
      const byKey = new Map(model.groups[group].map((entry) => [entry.tabId, entry]));
      out[group] = freezeOrder(previous?.[group] ?? null, model.groups[group].map((entry) => entry.tabId)).map((key) => byKey.get(key)!);
    }
    return out;
  }, [model, orderToken]);
  useLayoutEffect(() => {
    const keys = {} as Record<WallGroup, string[]>;
    for (const group of WALL_GROUPS) keys[group] = groups[group].map((entry) => entry.tabId);
    orderRef.current = { token: orderToken, keys };
  }, [groups, orderToken]);

  const refresh = useCallback(async () => {
    setRefreshing(true);
    try {
      await reloadSessionAggregate();
    } finally {
      setRefreshing(false);
      setNow(Date.now());
      setOrderToken((token) => token + 1);
    }
  }, []);

  // The listing normally loads at startup; a wall shown before it did asks for it. Returning to the app re-sorts
  // (the listing itself reloads on resume).
  useEffect(() => {
    if (!useSessionAggregate.getState().loaded) void reloadSessionAggregate();
    const onVisible = () => {
      if (document.hidden) return;
      setNow(Date.now());
      setOrderToken((token) => token + 1);
    };
    document.addEventListener("visibilitychange", onVisible);
    const timer = setInterval(() => setNow(Date.now()), CLOCK_MS);
    return () => {
      document.removeEventListener("visibilitychange", onVisible);
      clearInterval(timer);
    };
  }, []);

  const open = (card: WallCard) => {
    if (openInboxTab(card.tabId)) {
      onOpened();
      return;
    }
    if (!card.host) return;
    setOpening(card.tabId);
    // A canceled takeover (the user dismissed a device connect) leaves the wall as it was.
    void takeOverAggregateSession(card.host, card.tabId, card.title)
      .then((result) => {
        if (result.ok) onOpened();
        else if ("error" in result) useUi.getState().pushToast("error", `无法打开终端：${result.error}`);
      })
      .catch((error: unknown) => useUi.getState().pushToast("error", `无法打开终端：${describeError(error)}`))
      .finally(() => setOpening(null));
  };

  // The drag distance lives in a ref: touch events of one gesture can arrive before React renders the last one.
  const onTouchStart = (event: TouchEvent) => {
    pullStart.current = (scrollRef.current?.scrollTop ?? 0) <= 0 ? (event.touches[0]?.clientY ?? null) : null;
    pullRef.current = 0;
  };
  const onTouchMove = (event: TouchEvent) => {
    if (pullStart.current === null) return;
    const y = event.touches[0]?.clientY;
    if (y === undefined) return;
    pullRef.current = Math.max(0, y - pullStart.current);
    setPull(pullRef.current);
  };
  const onTouchEnd = () => {
    const pulled = pullRef.current;
    pullStart.current = null;
    pullRef.current = 0;
    setPull(0);
    if (pulled >= PULL_REFRESH_PX && !refreshing) void refresh();
  };

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
  const shown = held ?? groups;

  const total = WALL_GROUPS.reduce((sum, group) => sum + shown[group].length, 0);
  const pending = model.hosts.filter((host) => host.status === "pending");
  const unreachable = model.hosts.filter((host) => host.status !== "pending");

  return (
    <section className="nx-status-wall flex min-h-0 flex-1 flex-col bg-neutral-900" aria-labelledby="nx-wall-heading" data-status-wall="">
      <div className="nx-toolbar">
        <h2 id="nx-wall-heading" className="nx-toolbar-title">会话状态</h2>
        <span className="nx-hint" role="status" aria-live="polite" data-testid="wall-progress">
          {refreshing ? "正在刷新…" : pending.length > 0 ? `正在查询 ${pending.length} 台主机…` : !loaded ? "正在读取各主机的会话…" : ""}
        </span>
        <div className="nx-spacer" />
        <button type="button" className="nx-btn nx-btn-ghost nx-btn-sm" disabled={refreshing} onClick={() => void refresh()}>
          <IconRefresh size={13} className={refreshing ? "animate-spin" : ""} />
          刷新
        </button>
      </div>
      <div
        ref={scrollRef}
        className="min-h-0 flex-1 overflow-y-auto overscroll-contain px-3 pb-6"
        onTouchStart={onTouchStart}
        onTouchMove={onTouchMove}
        onTouchEnd={onTouchEnd}
        onTouchCancel={() => { pullStart.current = null; pullRef.current = 0; setPull(0); }}
        onPointerDownCapture={() => setHeld(groups)}
      >
        {pull > 0 && (
          <div className="flex items-center justify-center text-[12px] text-[var(--nx-fg-soft)]" style={{ height: Math.min(pull, PULL_REFRESH_PX) }} data-testid="wall-pull">
            {pull >= PULL_REFRESH_PX ? "松开刷新" : "下拉刷新"}
          </div>
        )}
        {WALL_GROUPS.map((group) => shown[group].length > 0 && (
          <div key={group} className="pt-3" data-wall-group={group}>
            <h3 className="mb-1.5 text-[12px] font-semibold text-[var(--nx-fg-soft)]">
              {WALL_GROUP_LABELS[group]} <span className="font-normal">{shown[group].length}</span>
            </h3>
            <ul className="flex flex-col gap-2">
              {shown[group].map((entry) => (
                <WallCardView
                  key={entry.tabId}
                  card={entry}
                  now={now}
                  opening={opening === entry.tabId}
                  onOpen={() => open(entry)}
                  actions={cardActions?.(entry, { open: () => open(entry), opening: opening === entry.tabId })}
                />
              ))}
            </ul>
          </div>
        ))}
        {unreachable.length > 0 && (
          <div className="pt-3" data-wall-group="hosts">
            <h3 className="mb-1.5 text-[12px] font-semibold text-[var(--nx-fg-soft)]">无法查看的主机</h3>
            <ul className="flex flex-col gap-1">
              {unreachable.map((host) => (
                <li key={host.key} className="flex items-baseline gap-2 text-[12px]" data-wall-host={host.key} data-host-status={host.status}>
                  <IconServer size={12} className="shrink-0 self-center text-[var(--nx-fg-soft)]" />
                  <span className="truncate text-[var(--nx-fg)]">{host.name}</span>
                  <span className="shrink-0 text-[var(--nx-fg-soft)]">
                    {unknownHostText(host)}
                  </span>
                </li>
              ))}
            </ul>
          </div>
        )}
        {total === 0 && (
          <div className="flex flex-col items-center gap-2 px-6 pt-12 text-center text-[13px] text-[var(--nx-fg-soft)]" data-testid="wall-empty">
            {!loaded || pending.length > 0 ? (
              <span>正在读取各主机的会话…</span>
            ) : (
              <>
                <span>还没有会话。连接一台主机后，它的终端会显示在这里。</span>
                {onOpenAssets && (
                  <button type="button" className="nx-btn nx-btn-primary nx-btn-sm" onClick={onOpenAssets}>
                    打开资产
                  </button>
                )}
              </>
            )}
          </div>
        )}
      </div>
    </section>
  );
}
