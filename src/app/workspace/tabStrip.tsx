import { useEffect, useState, type ReactNode } from "react";
import { hasCustomTitle, useUi, type AppTab } from "../store";
import { useTabUnread } from "../unreadStore";
import { awarenessBadge, useTabAwareness, type AwarenessBadge } from "../sessionAwarenessStore";
import { useTerminalTabTitle } from "../terminalViewStore";
import { fileVisual } from "../../features/files/fileTypes";
import { useAssetProductionClassification } from "../../features/production/productionSettings";
import {
  reshowPersistenceHint,
  usePersistenceFailureMark,
  usePersistenceTabNote,
  type PersistenceTabInfo,
} from "../../features/terminal/persistenceHintStore";
import {
  IconActivity,
  IconBox,
  IconCheck,
  IconClose,
  IconClock,
  IconCommand,
  IconDatabase,
  IconDrive,
  IconFolderOpen,
  IconHistory,
  IconKey,
  IconList,
  IconMonitor,
  IconNetwork,
  IconEphemeral,
  IconServer,
  IconSettings,
  IconTerminal,
} from "../../ui/icons";

const TAB_ICON = {
  terminal: IconTerminal,
  files: IconFolderOpen,
  log: IconList,
  mount: IconDrive,
  forward: IconNetwork,
  docker: IconBox,
  db: IconDatabase,
  credentials: IconKey,
  settings: IconSettings,
  audit: IconHistory,
  devices: IconMonitor,
  background: IconActivity,
  history: IconClock,
  execBatch: IconCommand,
  host: IconServer,
} as const;

export function tabIcon(t: AppTab) {
  if ((t.kind === "files" || t.kind === "log") && t.path) return fileVisual(t.path, "file").Icon;
  return TAB_ICON[t.kind] ?? IconTerminal;
}

// tabTooltip prefers what the tab's pane reports about persistence (live) over the reason recorded when the tab
// opened, which only applies until a pane has reported.
export function tabTooltip(
  t: AppTab,
  unread: boolean,
  live: PersistenceTabInfo = {},
  attention: AwarenessBadge | null = null,
): string {
  const notes: string[] = [];
  if (t.kind === "terminal" && attention) notes.push(attention.label);
  const persistence = t.kind === "terminal" ? live.failureMark ?? live.note : undefined;
  if (persistence) notes.push(persistence);
  else if (t.kind === "terminal" && t.durable === true) notes.push("持久终端：断开后可在「后台会话」接管");
  else if (t.kind === "terminal" && t.regularReason) notes.push(`临时终端：${t.regularReason}`);
  else if (t.kind === "terminal" && t.durable === false) notes.push(EPHEMERAL_NOTE);
  if (unread) notes.push("有新输出");
  if (t.closeError) notes.push(`上次关闭失败：${t.closeError}`);
  return notes.length > 0 ? `${t.title}（${notes.join(" · ")}）` : t.title;
}

const EPHEMERAL_NOTE = "临时终端：关闭标签页或断开连接后进程会结束";

// Durable terminals (the common case) show no mark, only a tooltip note. A terminal known to be non-durable
// (durable === false, still alive) carries a small dashed-ring mark before its title; unknown durability, a tab
// that is still connecting and non-terminal tabs carry none. A failure
// hint the user dismissed while it still applies leaves a red dot that names it and shows the hint again; its hit
// area extends past the dot without taking layout space. The tab bar renders the icon before the title and the
// dot on the trailing side (part), away from the attention badge; the plain dot is reserved for this mark.
export function TabPersistenceMark({ tab, part = "all" }: { tab: AppTab; part?: "all" | "icon" | "failure" }) {
  const failure = usePersistenceFailureMark(tab.id);
  if (tab.kind !== "terminal") return null;
  return (
    <>
      {part !== "failure" && tab.durable === false && !tab.dead && !tab.exited && (
        <span className="flex shrink-0 items-center text-[var(--nx-fg-tertiary)]" role="img" aria-label={EPHEMERAL_NOTE} title={EPHEMERAL_NOTE}>
          <IconEphemeral size={12} />
        </span>
      )}
      {part !== "icon" && failure && (
        <button
          type="button"
          className="relative h-1.5 w-1.5 shrink-0 rounded-full bg-red-500 p-0 before:absolute before:-inset-2 before:content-['']"
          aria-label={`重新显示提示：${failure}`}
          title={failure}
          onClick={() => reshowPersistenceHint(tab.id)}
        />
      )}
    </>
  );
}

// The attention badge sits before the title at the persistence icon's scale, and each state has its own shape so
// it never depends on colour: "!" in a disc for needs_me, a check for a just_finished command that succeeded (a slate cross when it failed; red stays for the persistence failure dot), a turning ring while a
// background tab runs (no longer shown) and "?" when the state cannot be known. A tab the user is on shows no running badge, and a needs_me or
// just_finished state seen on any device shows nothing.
export function TabAttentionBadge({ badge }: { badge: AwarenessBadge | null }) {
  if (!badge) return null;
  const common = {
    role: "img",
    "aria-label": badge.label,
    title: badge.label,
    "data-attention": badge.kind,
  } as const;
  switch (badge.kind) {
    case "needs_me":
      return (
        <span
          {...common}
          className="flex h-3 w-3 shrink-0 items-center justify-center rounded-full bg-amber-400 text-[9px] font-bold leading-none text-slate-900"
        >
          !
        </span>
      );
    case "just_finished":
      return (
        <span
          {...common}
          data-outcome={badge.outcome}
          className={`flex shrink-0 items-center ${badge.outcome === "failed" ? "text-slate-400" : "text-green-500"}`}
        >
          {badge.outcome === "failed" ? <IconClose size={11} /> : <IconCheck size={11} />}
        </span>
      );
    case "running":
      // Static: only list views (never the tab strip, see useCalmAttention) draw it, and it never animates.
      return <span {...common} className="h-2 w-2 shrink-0 rounded-full border border-slate-400" />;
    case "unknown":
      return (
        <span {...common} className="shrink-0 text-[10px] font-semibold leading-none text-slate-500">
          ?
        </span>
      );
  }
}

// ATTENTION_HOLD_MS: a needs_me or just_finished badge appears only after the state held this long, so a state that
// flips during normal output never flashes. Hiding is immediate.
export const ATTENTION_HOLD_MS = 1800;

// useCalmAttention decides the badge a tab strip item shows. Running never shows (it flaps with output and is not
// actionable). The tab the user is on shows no needs_me / just_finished badge either: it is already seen, and the
// round trip that marks it seen would flash the badge. Other tabs show it, after the hold, without animation.
export function useCalmAttention(badge: AwarenessBadge | null, active: boolean): AwarenessBadge | null {
  const actionable = !active && badge && (badge.kind === "needs_me" || badge.kind === "just_finished");
  const candidate = actionable ? badge : badge?.kind === "unknown" && !active ? badge : null;
  const key = candidate ? `${candidate.kind}|${candidate.outcome ?? ""}` : "";
  const [shownKey, setShownKey] = useState("");
  useEffect(() => {
    if (!key) {
      setShownKey("");
      return;
    }
    if (key === shownKey) return;
    const timer = setTimeout(() => setShownKey(key), ATTENTION_HOLD_MS);
    return () => clearTimeout(timer);
  }, [key, shownKey]);
  return candidate && shownKey === key ? candidate : null;
}

export function TabStripItem({
  tab,
  active = false,
  children,
}: {
  tab: AppTab;
  active?: boolean;
  children: (unread: boolean, persistence: PersistenceTabInfo, attention: AwarenessBadge | null) => ReactNode;
}) {
  const unread = useTabUnread(tab.id);
  const note = usePersistenceTabNote(tab.id);
  const failureMark = usePersistenceFailureMark(tab.id);
  const awareness = useTabAwareness(tab.kind === "terminal" && !tab.exited ? tab.tabId : undefined);
  const attention = useCalmAttention(awarenessBadge(awareness), active);
  return children(unread, { note, failureMark }, attention);
}

// A terminal tab's label prefers the OSC-derived remote title from the per-tab view
// store (narrow subscription: only this tab's label re-renders on a title change);
// renames clear that entry, so the authoritative workspace tab title shows instead.
export function TabStripTitle({ tab }: { tab: AppTab }) {
  const remoteTitle = useTerminalTabTitle(tab.id);
  return (
    // A custom title always rules the strip, even when an OSC view title is still cached
    // (a rename synced from another device, a restored layout, or a reattach).
    <span className="truncate">
      {tab.kind === "terminal" && !hasCustomTitle(tab) && remoteTitle ? remoteTitle : tab.title}
    </span>
  );
}

// R-01 workspace-scope warning: a terminal tab whose backend-classified asset
// environment is production carries an amber badge; the environment name is in
// the tooltip, the tab strip stays compact.
export function TabProductionBadge({ sessionId }: { sessionId?: string }) {
  const assetId = useUi((s) => (sessionId ? s.sessions.find((x) => x.id === sessionId)?.assetId : undefined));
  const classification = useAssetProductionClassification(assetId);
  if (!classification?.production) return null;
  return (
    <span className="nx-badge nx-badge-amber shrink-0" title={`生产环境：${classification.environment}`}>
      生产
    </span>
  );
}
