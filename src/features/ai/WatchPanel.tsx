import { useCallback, useEffect, useRef, useState } from "react";
import type {
  WatchApi,
  WatchEvent,
  WatchStatus,
  WatchSummary,
  WatchTargetStateKind,
  WatchTargetSummary,
  WatchToolData,
} from "./watchTypes";
import { IconClose, IconRefresh } from "../../ui/icons";

const statusLabels: Record<WatchStatus, string> = {
  armed: "等待中",
  matched: "已触发",
  cancelled: "已取消",
  expired: "已过期",
  unverifiable: "不可验证",
};

const statusClasses: Record<WatchStatus, string> = {
  armed: "nx-badge-green",
  matched: "nx-badge-blue",
  cancelled: "",
  expired: "",
  unverifiable: "nx-badge-red",
};

const targetStateLabels: Record<WatchTargetStateKind, string> = {
  attached: "已连接",
  disconnected: "已断连",
  gone: "已消失",
  replaced: "已被替换",
  exited: "已退出",
  unverifiable: "不可验证",
  nonpersistent: "非持久",
};

// evidenceBearing states are only shown as definitive when the runtime
// supplies evidence; without it they render as unknown.
const evidenceBearing: Record<WatchTargetStateKind, boolean> = {
  attached: false,
  disconnected: false,
  gone: true,
  replaced: true,
  exited: true,
  unverifiable: true,
  nonpersistent: true,
};

interface EventEntry {
  event: WatchEvent;
  duplicate: boolean;
}

// WatchPanel is the watch surface. The panel remounts its content by owner,
// so the first committed frame of a switch can never show the previous
// owner's rows, events or unread count, and every operation, subscription
// and load generation is scoped to its own incarnation.
export function WatchPanel({
  api,
  owner,
  onClose,
}: {
  api: WatchApi;
  owner: string;
  onClose?: () => void;
}) {
  return <WatchPanelContent key={owner} api={api} owner={owner} onClose={onClose} />;
}

function WatchPanelContent({
  api,
  owner,
  onClose,
}: {
  api: WatchApi;
  owner: string;
  onClose?: () => void;
}) {
  const [watches, setWatches] = useState<WatchSummary[] | null>(null);
  const [targets, setTargets] = useState<WatchTargetSummary[] | null>(null);
  const [events, setEvents] = useState<EventEntry[]>([]);
  const [unread, setUnread] = useState(0);
  const [error, setError] = useState<string | null>(null);
  const [actionError, setActionError] = useState<string | null>(null);
  const loadGenRef = useRef(0);
  const seenDeliveriesRef = useRef<Set<string>>(new Set());
  const aliveRef = useRef(true);

  const load = useCallback(async () => {
    const gen = ++loadGenRef.current;
    try {
      const [nextWatches, nextTargets] = await Promise.all([api.listWatches(owner), api.listTargets(owner)]);
      if (!aliveRef.current || gen !== loadGenRef.current) return;
      setWatches(nextWatches);
      setTargets(nextTargets);
      setError(null);
    } catch (err) {
      if (!aliveRef.current || gen !== loadGenRef.current) return;
      setError(String(err));
    }
  }, [api, owner]);

  useEffect(() => {
    aliveRef.current = true;
    setError(null);
    setActionError(null);
    void load();
    // The subscription callback is scoped to this effect's incarnation: a
    // queued delivery from a previous owner is rejected by the alive guard
    // even after unsubscribe, because the old content is unmounted.
    const unsubscribe = api.subscribeWatchToolData(owner, (data: WatchToolData) => {
      if (!aliveRef.current) return;
      const duplicate = seenDeliveriesRef.current.has(data.event.deliveryId);
      seenDeliveriesRef.current.add(data.event.deliveryId);
      setEvents((prev) => [{ event: data.event, duplicate }, ...prev].slice(0, 50));
      if (!duplicate) {
        setUnread((prev) => prev + 1);
      }
    });
    const loadGen = loadGenRef;
    return () => {
      aliveRef.current = false;
      loadGen.current++;
      unsubscribe();
    };
  }, [api, owner, load]);

  const cancel = useCallback(
    async (watch: WatchSummary) => {
      setActionError(null);
      try {
        await api.cancelWatch(owner, watch.id, watch.revision);
      } catch (err) {
        if (!aliveRef.current) return;
        setActionError(err instanceof Error ? err.message : String(err));
        return;
      }
      void load();
    },
    [api, owner, load],
  );

  return (
    <div className="flex h-full flex-col gap-3 p-3">
      <header className="flex items-center gap-2">
        <h3 className="text-sm font-semibold">Watch</h3>
        <span className="nx-badge">{owner}</span>
        {unread > 0 && <span className="nx-badge nx-badge-blue">未读 {unread}</span>}
        <button type="button" className="nx-button ml-auto" onClick={() => void load()}>
          <IconRefresh className="h-3.5 w-3.5" />
          刷新
        </button>
        {onClose && (
          <button type="button" className="nx-button" onClick={onClose}>
            <IconClose className="h-3.5 w-3.5" />
          </button>
        )}
      </header>
      {error && (
        <div role="alert" className="nx-badge nx-badge-red">
          {error}
        </div>
      )}
      {actionError && (
        <div role="alert" className="nx-badge nx-badge-red">
          {actionError}
        </div>
      )}
      <section>
        <h4 className="mb-1 text-xs font-semibold text-neutral-500">目标</h4>
        <div className="flex flex-col gap-1">
          {(targets ?? []).map((target) => (
            <div key={target.targetId} className="flex items-center gap-2 text-xs">
              <span className="font-medium">{target.targetId}</span>
              <span className="text-neutral-500">{target.identity}</span>
              {evidenceBearing[target.state] && !target.evidence ? (
                <span className="nx-badge">未知</span>
              ) : (
                <>
                  <span className="nx-badge">{targetStateLabels[target.state]}</span>
                  {target.evidence ? (
                    <span className="text-neutral-500">{target.evidence}</span>
                  ) : (
                    <span className="nx-badge">无证据</span>
                  )}
                </>
              )}
            </div>
          ))}
        </div>
      </section>
      <section>
        <h4 className="mb-1 text-xs font-semibold text-neutral-500">监视</h4>
        <div className="flex flex-col gap-1">
          {(watches ?? []).map((watch) => (
            <div key={watch.id} className="flex items-center gap-2 text-xs">
              <span className="font-medium">{watch.name}</span>
              <span className="text-neutral-500">
                {watch.condition.type}: {watch.condition.pattern}
              </span>
              <span className={`nx-badge ${statusClasses[watch.status]}`}>{statusLabels[watch.status]}</span>
              <span className="text-neutral-500">锚点 {watch.anchorSeq}</span>
              {watch.repeat && <span className="nx-badge">重复</span>}
              {watch.lastError && <span className="text-neutral-500">{watch.lastError}</span>}
              {(watch.status === "armed" || watch.status === "matched") && (
                <button type="button" className="nx-button" onClick={() => void cancel(watch)}>
                  取消
                </button>
              )}
            </div>
          ))}
        </div>
      </section>
      <section>
        <h4 className="mb-1 text-xs font-semibold text-neutral-500">事件</h4>
        <div className="flex flex-col gap-1">
          {events.map(({ event, duplicate }, index) => (
            <div
              key={`${event.deliveryId}-${index}`}
              data-delivery={event.deliveryId}
              className="flex items-center gap-2 text-xs"
            >
              <span className="nx-badge nx-badge-purple">工具数据</span>
              <span className="nx-badge">不可信</span>
              {duplicate && <span className="nx-badge nx-badge-amber">重复投递</span>}
              <span className="font-medium">{event.watchId}</span>
              <span className="text-neutral-500">{event.deliveryId}</span>
              <span className="text-neutral-500">
                {event.matchedFrom}..{event.matchedTo}
              </span>
              <span className="text-neutral-500">{event.excerpt}</span>
            </div>
          ))}
        </div>
      </section>
    </div>
  );
}
