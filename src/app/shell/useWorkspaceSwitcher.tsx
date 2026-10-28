import { useCallback, useEffect, useRef, useState } from "react";
import {
  useUi,
  useWorkspaceList,
  restoreClosedTab,
  sessionStatusText,
  sessionReconnectStatusText,
  type Workspace,
} from "../store";
import { useClosedTabs } from "../closedTabs";
import { formatBinding, useKeybindings } from "../keybindings";
import { type ContextMenuState, type MenuItem } from "../../ui/ContextMenu";
import { IconClose, IconHistory } from "../../ui/icons";
import { workspaceIcon } from "./AppHeader";
import { hideMobileHome } from "./mobileHome";

// useWorkspaceSwitcher builds the header's workspace switcher menu.
export function useWorkspaceSwitcher(visibleWorkspaces: Workspace[], closeAllWorkspaces: () => Promise<void>) {
  const workspaces = useWorkspaceList();
  const setActiveWorkspace = useUi((s) => s.setActiveWorkspace);
  const closeWorkspace = useUi((s) => s.closeWorkspace);
  const closedTabEntries = useClosedTabs((s) => s.stack.entries);
  const restoreBinding = useKeybindings().restoreClosedTab;
  const [wsMenu, setWsMenu] = useState<ContextMenuState | null>(null);

  const buildWorkspaceMenuItems = useCallback(
    (): MenuItem[] => {
      // Session status is read when the menu is built, so status changes do not re-render the app shell.
      const sessions = useUi.getState().sessions;
      const items: MenuItem[] = visibleWorkspaces.map((w) => {
        const Icon = workspaceIcon(w);
        const session = sessions.find((s) => s.id === w.sessionId);
        const tabCount = w.panes.flatMap((p) => p.tabs).length;
        return {
          kind: "item",
          label: w.title,
          icon: <Icon size={12} />,
          hint: [
            w.kind === "tools" ? "工具" : null,
            w.panes.length > 1 ? "已分屏" : null,
            sessionReconnectStatusText(session) || (session ? sessionStatusText(session.status, session.liveness) : null),
            `${tabCount} 个标签页`,
          ]
            .filter(Boolean)
            .join(" · "),
          onSelect: () => {
            hideMobileHome();
            setActiveWorkspace(w.id);
          },
          trailingAction: {
            label: `关闭工作区 ${w.title}`,
            icon: <IconClose size={12} />,
            disabled: !w.closable,
            keepOpen: true,
            onSelect: () => void closeWorkspace(w.id),
          },
        };
      });
      if (workspaces.length > 0) items.push({ kind: "separator" });
      if (workspaces.length > 0) {
        items.push({
          kind: "item",
          label: "关闭所有工作区",
          icon: <IconClose size={12} />,
          hint: `${workspaces.filter((workspace) => workspace.closable).length} 个可关闭`,
          danger: true,
          disabled: !workspaces.some((workspace) => workspace.closable),
          onSelect: () => void closeAllWorkspaces(),
        });
      }
      items.push({ kind: "separator" });
      const latestClosed = closedTabEntries[0];
      items.push({
        kind: "item",
        label: "恢复关闭的标签页",
        icon: <IconHistory size={12} />,
        hint: latestClosed?.title,
        accel: restoreBinding === null ? undefined : formatBinding(restoreBinding),
        disabled: !latestClosed,
        onSelect: () => void restoreClosedTab(),
      });
      return items;
    },
    [visibleWorkspaces, workspaces, setActiveWorkspace, closeWorkspace, closeAllWorkspaces, closedTabEntries, restoreBinding],
  );
  const buildWorkspaceMenuItemsRef = useRef(buildWorkspaceMenuItems);
  buildWorkspaceMenuItemsRef.current = buildWorkspaceMenuItems;
  const openWorkspaceSwitcher = useCallback(
    (anchor: HTMLElement) => {
      const rect = anchor.getBoundingClientRect();
      setWsMenu({ x: rect.left, y: rect.bottom + 4, title: "工作区", items: buildWorkspaceMenuItems() });
    },
    [buildWorkspaceMenuItems],
  );
  // Closing a workspace from its row keeps the switcher open, so the open menu follows the workspace list.
  useEffect(() => {
    setWsMenu((menu) => menu && { ...menu, items: buildWorkspaceMenuItemsRef.current() });
  }, [visibleWorkspaces, workspaces]);

  const closeWorkspaceSwitcher = useCallback(() => setWsMenu(null), []);
  return { wsMenu, openWorkspaceSwitcher, closeWorkspaceSwitcher };
}
