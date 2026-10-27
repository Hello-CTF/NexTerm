import { type ReactNode } from "react";
import { hasCustomTitle, useUi, type AppTab } from "../store";
import { useTabUnread } from "../unreadStore";
import { useTerminalTabTitle } from "../terminalViewStore";
import { fileVisual } from "../../features/files/fileTypes";
import { useAssetProductionClassification } from "../../features/production/productionSettings";
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

export function tabTooltip(t: AppTab, unread: boolean): string {
  const notes: string[] = [];
  if (t.kind === "terminal" && t.durable === true) notes.push("持久终端：断开后可在「后台会话」接管");
  else if (t.kind === "terminal" && t.regularReason) notes.push(`普通终端：${t.regularReason}`);
  if (unread) notes.push("有新输出");
  if (t.closeError) notes.push(`上次关闭失败：${t.closeError}`);
  return notes.length > 0 ? `${t.title}（${notes.join(" · ")}）` : t.title;
}

// A durable terminal tab carries a small persistence mark before its title; regular tabs carry none.
export function TabPersistenceMark({ tab }: { tab: AppTab }) {
  if (tab.kind !== "terminal" || tab.durable !== true) return null;
  return (
    <span className="flex shrink-0 items-center" role="img" aria-label="持久终端" data-testid="tab-persistence-mark">
      <IconPersist size={12} />
    </span>
  );
}

export function TabStripItem({
  tab,
  children,
}: {
  tab: AppTab;
  children: (unread: boolean) => ReactNode;
}) {
  const unread = useTabUnread(tab.id);
  return children(unread);
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
