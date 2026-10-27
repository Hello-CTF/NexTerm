import { type ReactNode } from "react";
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
  IconPersist,
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
  else if (t.kind === "terminal" && t.regularReason) notes.push(`普通终端：${t.regularReason}`);
  if (unread) notes.push("有新输出");
  if (t.closeError) notes.push(`上次关闭失败：${t.closeError}`);
  return notes.length > 0 ? `${t.title}（${notes.join(" · ")}）` : t.title;
}

// A durable terminal tab carries a small persistence mark before its title; regular tabs carry none. A failure
// hint the user dismissed while it still applies leaves a red dot that names it and shows the hint again; its hit
// area extends past the dot without taking layout space.
export function TabPersistenceMark({ tab }: { tab: AppTab }) {
  const failure = usePersistenceFailureMark(tab.id);
  if (tab.kind !== "terminal") return null;
  return (
    <>
      {tab.durable === true && (
        <span className="flex shrink-0 items-center" role="img" aria-label="持久终端">
          <IconPersist size={12} />
        </span>
      )}
      {failure && (
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

// The attention badge sits with the persistence marks and keeps their scale: a dot for signalled states, a hollow
// ring for states inferred from the screen shape, and a grey question mark when the state cannot be known. A tab
// the user is on shows no running badge, and a needs_me or just_finished state seen on any device shows nothing.
export function TabAttentionBadge({ badge }: { badge: AwarenessBadge | null }) {
  if (!badge) return null;
  if (badge.kind === "unknown") {
    return (
      <span className="shrink-0 text-[10px] font-semibold leading-none text-slate-500" role="img" aria-label={badge.label} title={badge.label}>
        ?
      </span>
    );
  }
  const tone = {
    needs_me: badge.inferred ? "border border-amber-400/80" : "bg-amber-400 ring-2 ring-amber-400/30",
    just_finished: badge.inferred ? "border border-emerald-400/80" : "bg-emerald-400",
    running: "bg-slate-400 motion-safe:animate-pulse",
  }[badge.kind];
  const size = badge.kind === "running" ? "h-1.5 w-1.5" : "h-2 w-2";
  return (
    <span
      className={`${size} shrink-0 rounded-full ${tone}`}
      role="img"
      aria-label={badge.label}
      title={badge.label}
      data-attention={badge.kind}
      data-inferred={badge.inferred ? "true" : undefined}
    />
  );
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
  let attention = awarenessBadge(awareness);
  if (attention?.kind === "running" && active) attention = null;
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
