import { useCallback, useEffect, useRef, useState } from "react";
import type { WatchApi, WatchStatus, WatchSummary, WatchTargetSummary } from "./watchTypes";
import { IconLoader, IconRefresh } from "../../ui/icons";

const statusLabels: Record<WatchStatus, string> = {
  armed: "等待中",
  matched: "已触发",
  cancelled: "已取消",
  expired: "已过期",
  unverifiable: "无法确认",
};

const statusClasses: Record<WatchStatus, string> = {
  armed: "nx-badge-green",
  matched: "nx-badge-blue",
  cancelled: "",
  expired: "",
  unverifiable: "nx-badge-red",
};

const conditionLabels = { contains: "出现", regex: "匹配" } as const;

// WatchPanel lists the watches the AI armed on terminals (wait until output
// matches, then wake the AI) and lets the user cancel one. It remounts by
// owner, so the first committed frame of a switch can never show the
// previous owner's rows.
export function WatchPanel({
  api,
  owner,
  onChanged,
}: {
  api: WatchApi;
  owner: string;
  onChanged?: () => void;
}) {
  return <WatchPanelContent key={owner} api={api} owner={owner} onChanged={onChanged} />;
}

function WatchPanelContent({
  api,
  owner,
  onChanged,
}: {
  api: WatchApi;
  owner: string;
  onChanged?: () => void;
}) {
  const [watches, setWatches] = useState<WatchSummary[] | null>(null);
  const [targets, setTargets] = useState<WatchTargetSummary[]>([]);
  const [loading, setLoading] = useState(false);
  const [cancelling, setCancelling] = useState<Set<string>>(new Set());
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<{ tone: "ok" | "error"; text: string } | null>(null);
  const [noticeHeld, setNoticeHeld] = useState(false);
  const loadGenRef = useRef(0);
  const aliveRef = useRef(true);
  const onChangedRef = useRef(onChanged);
  onChangedRef.current = onChanged;

  const load = useCallback(async () => {
    const gen = ++loadGenRef.current;
    setLoading(true);
    try {
      const [nextWatches, nextTargets] = await Promise.all([
        api.listWatches(owner),
        api.listTargets(owner).catch(() => [] as WatchTargetSummary[]),
      ]);
      if (!aliveRef.current || gen !== loadGenRef.current) return;
      setWatches(nextWatches);
      setTargets(nextTargets);
      setError(null);
      onChangedRef.current?.();
    } catch (err) {
      if (!aliveRef.current || gen !== loadGenRef.current) return;
      setError(`读取监视失败：${String(err)}`);
    } finally {
      if (aliveRef.current && gen === loadGenRef.current) setLoading(false);
    }
  }, [api, owner]);

  useEffect(() => {
    aliveRef.current = true;
    void load();
    const loadGen = loadGenRef;
    return () => {
      aliveRef.current = false;
      loadGen.current++;
    };
  }, [load]);

  // A success notice clears itself, but never while the user is pointing at
  // or focused inside it. Errors stay until acknowledged.
  useEffect(() => {
    if (!notice || noticeHeld || notice.tone === "error") return;
    const timer = window.setTimeout(() => setNotice(null), 5000);
    return () => window.clearTimeout(timer);
  }, [notice, noticeHeld]);

  const cancel = useCallback(
    async (watch: WatchSummary) => {
      if (cancelling.has(watch.id)) return;
      setNotice(null);
      setCancelling((prev) => new Set(prev).add(watch.id));
      try {
        await api.cancelWatch(owner, watch.id, watch.revision);
        if (!aliveRef.current) return;
        // The row moves to the ended section at once; the reload reconciles.
        setWatches((prev) => (prev ?? []).map((w) => (w.id === watch.id ? { ...w, status: "cancelled" } : w)));
        setNotice({ tone: "ok", text: `已取消监视「${watch.name}」` });
        void load();
      } catch (err) {
        if (!aliveRef.current) return;
        setNotice({
          tone: "error",
          text: `取消失败：${err instanceof Error ? err.message : String(err)}`,
        });
      } finally {
        if (aliveRef.current) {
          setCancelling((prev) => {
            const next = new Set(prev);
            next.delete(watch.id);
            return next;
          });
        }
      }
    },
    [api, owner, load, cancelling],
  );

  const targetLabel = (targetId: string) => {
    const t = targets.find((x) => x.targetId === targetId);
    return t?.identity ? `终端 ${t.identity}` : "终端";
  };

  const active = (watches ?? []).filter((w) => w.status === "armed" || w.status === "matched");
  const ended = (watches ?? []).filter((w) => !(w.status === "armed" || w.status === "matched"));

  const row = (watch: WatchSummary, canCancel: boolean) => (
    <div
      key={watch.id}
      className="flex flex-wrap items-center gap-1.5 rounded-lg border border-neutral-800/60 px-2 py-1.5 text-[11.5px]"
    >
      <span className="font-medium">{watch.name}</span>
      <span className="min-w-0 flex-1 truncate text-neutral-500" title={watch.condition.pattern}>
        {targetLabel(watch.targetId)}
        {conditionLabels[watch.condition.type]}「{watch.condition.pattern}」时叫醒 AI
      </span>
      <span className={`nx-badge ${statusClasses[watch.status]}`}>{statusLabels[watch.status]}</span>
      {watch.repeat && <span className="nx-badge">重复</span>}
      {watch.lastError && <span className="text-neutral-500">{watch.lastError}</span>}
      {canCancel && (
        <button
          type="button"
          className="nx-btn nx-btn-outline nx-btn-xs"
          aria-label={`取消监视「${watch.name}」`}
          disabled={cancelling.has(watch.id)}
          aria-busy={cancelling.has(watch.id)}
          onClick={() => void cancel(watch)}
        >
          {cancelling.has(watch.id) ? "取消中…" : "取消监视"}
        </button>
      )}
    </div>
  );

  return (
    <div className="flex flex-col gap-1.5 p-2.5">
      <header className="flex items-center gap-1.5 text-[11.5px] font-medium text-neutral-200">
        监视
        {watches && <span className="nx-badge">{active.length} 个进行中</span>}
        <span className="nx-spacer" />
        <button
          type="button"
          className="nx-icon-btn nx-icon-btn-sm"
          title="刷新"
          aria-label="刷新监视列表"
          disabled={loading}
          aria-busy={loading}
          onClick={() => void load()}
        >
          {loading ? <IconLoader size={11} className="animate-spin" /> : <IconRefresh size={11} />}
        </button>
      </header>
      <p className="nx-hint text-[11px]">监视：AI 盯着某个终端，出现指定输出或进程结束时叫醒它。不再需要时可以取消。</p>
      {error && (
        <div role="alert" className="nx-badge nx-badge-red">
          {error}
        </div>
      )}
      {notice && (
        <div
          role={notice.tone === "error" ? "alert" : "status"}
          className={`nx-badge ${notice.tone === "error" ? "nx-badge-red" : "nx-badge-green"}`}
          onMouseEnter={() => setNoticeHeld(true)}
          onMouseLeave={() => setNoticeHeld(false)}
          onFocus={() => setNoticeHeld(true)}
          onBlur={() => setNoticeHeld(false)}
        >
          {notice.text}
          {notice.tone === "error" && (
            <button type="button" className="ml-1.5 underline" onClick={() => setNotice(null)}>
              知道了
            </button>
          )}
        </div>
      )}
      {watches !== null && active.length === 0 && !error && (
        <div className="py-1 text-[11px] text-neutral-500">现在没有进行中的监视</div>
      )}
      {active.map((w) => row(w, true))}
      {ended.length > 0 && (
        <details className="text-[11px] text-neutral-500">
          <summary className="cursor-pointer py-0.5">已结束（{ended.length}）</summary>
          <div className="mt-1 flex flex-col gap-1">{ended.map((w) => row(w, false))}</div>
        </details>
      )}
    </div>
  );
}
