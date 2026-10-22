import { useState, type DragEvent, type KeyboardEvent } from "react";
import {
  ALL_PROJECTS,
  focusedWorkspaces,
  projectFocusLabel,
  type ProjectFocus,
  type ProjectInventory,
} from "./projectFocus";
import { sessionStatusText, type SessionRuntimeInfo, type Workspace } from "./store";
import type { AssetGroup } from "../ipc/commands";
import { IconDatabase, IconLayers, IconSettings, assetIcon } from "../ui/icons";

const WORKSPACE_DRAG_MIME = "application/x-nexterm-workspace";

function workspaceIcon(workspace: Workspace) {
  if (workspace.kind === "db") return workspace.dbKind === "redis" ? IconLayers : IconDatabase;
  if (workspace.kind === "tools") return IconSettings;
  return assetIcon(workspace.assetKind ?? "ssh");
}

export function ProjectWorkspaceStrip({
  workspaces,
  activeWorkspaceId,
  activeProject,
  projectInventory,
  groups,
  sessions,
  onSelect,
  onReorder,
}: {
  workspaces: Workspace[];
  activeWorkspaceId: string | null;
  activeProject: ProjectFocus;
  projectInventory: ProjectInventory | null;
  groups: readonly AssetGroup[];
  sessions: readonly SessionRuntimeInfo[];
  onSelect: (workspaceId: string) => void;
  onReorder?: (dragId: string, overId: string, before: boolean) => void;
}) {
  const [draggingId, setDraggingId] = useState<string | null>(null);
  const [dropHint, setDropHint] = useState<{ id: string; edge: "before" | "after" } | null>(null);
  if (activeProject === ALL_PROJECTS) return null;
  const visible = focusedWorkspaces({ workspaces, activeWorkspaceId, activeProject, projectInventory });
  const ids = visible.map((workspace) => workspace.id);
  const label = projectFocusLabel(activeProject, groups);

  const focusWorkspace = (event: KeyboardEvent<HTMLButtonElement>, workspaceId: string) => {
    event.currentTarget
      .closest("[role='tablist']")
      ?.querySelector<HTMLElement>(`[data-project-workspace-id="${CSS.escape(workspaceId)}"]`)
      ?.focus();
  };

  // Ctrl+Shift+Arrow mirrors the pane tab-strip move keys; plain arrows keep their navigation role.
  const onKeyDown = (event: KeyboardEvent<HTMLButtonElement>, workspaceId: string) => {
    const index = ids.indexOf(workspaceId);
    if (index < 0) return;
    if ((event.key === "ArrowLeft" || event.key === "ArrowRight") && event.ctrlKey && event.shiftKey) {
      if (!onReorder) return;
      const to = event.key === "ArrowLeft" ? index - 1 : index + 1;
      if (to < 0 || to >= ids.length) return;
      event.preventDefault();
      onReorder(workspaceId, ids[to], event.key === "ArrowLeft");
      focusWorkspace(event, workspaceId);
      return;
    }
    let next = -1;
    if (event.key === "ArrowRight") next = (index + 1) % ids.length;
    else if (event.key === "ArrowLeft") next = (index - 1 + ids.length) % ids.length;
    else if (event.key === "Home") next = 0;
    else if (event.key === "End") next = ids.length - 1;
    else return;
    event.preventDefault();
    const nextId = ids[next];
    if (!nextId) return;
    onSelect(nextId);
    const tabs = event.currentTarget.closest("[role='tablist']")
      ?.querySelectorAll<HTMLButtonElement>("[data-project-workspace-id]");
    tabs?.[next]?.focus();
  };

  const onDragStart = (event: DragEvent<HTMLButtonElement>, workspaceId: string) => {
    if (!onReorder) return;
    setDraggingId(workspaceId);
    event.dataTransfer.setData(WORKSPACE_DRAG_MIME, workspaceId);
    event.dataTransfer.effectAllowed = "move";
  };

  const onDragOver = (event: DragEvent<HTMLButtonElement>, workspace: Workspace) => {
    if (!onReorder || !event.dataTransfer.types.includes(WORKSPACE_DRAG_MIME)) return;
    if (draggingId === workspace.id) return;
    event.preventDefault();
    event.dataTransfer.dropEffect = "move";
    const rect = event.currentTarget.getBoundingClientRect();
    const edge = event.clientX < rect.left + rect.width / 2 ? "before" : "after";
    setDropHint((hint) => (hint?.id === workspace.id && hint.edge === edge ? hint : { id: workspace.id, edge }));
  };

  const onDrop = (event: DragEvent<HTMLButtonElement>, workspace: Workspace) => {
    if (!onReorder || !event.dataTransfer.types.includes(WORKSPACE_DRAG_MIME)) return;
    event.preventDefault();
    setDropHint(null);
    const dragId = event.dataTransfer.getData(WORKSPACE_DRAG_MIME);
    if (!dragId || dragId === workspace.id) return;
    const rect = event.currentTarget.getBoundingClientRect();
    onReorder(dragId, workspace.id, event.clientX < rect.left + rect.width / 2);
  };

  return (
    <div className="nx-tabstrip shrink-0" data-project-focus={activeProject}>
      <span className="shrink-0 px-2 text-xs font-medium text-neutral-500">{label}</span>
      <div className="nx-tabstrip-scroll">
        {visible.length > 0 ? (
          <div role="tablist" aria-label={`${label}工作区`} className="flex items-center gap-[3px]">
            {visible.map((workspace) => {
              const Icon = workspaceIcon(workspace);
              const selected = workspace.id === activeWorkspaceId;
              const session = sessions.find((item) => item.id === workspace.sessionId);
              const tabCount = workspace.panes.flatMap((pane) => pane.tabs).length;
              return (
                <button
                  key={workspace.id}
                  type="button"
                  role="tab"
                  data-project-workspace-id={workspace.id}
                  aria-selected={selected}
                  tabIndex={selected ? 0 : -1}
                  draggable={Boolean(onReorder)}
                  className={`nx-tab ${selected ? "is-active" : ""}${
                    dropHint?.id === workspace.id ? ` nx-drop-${dropHint.edge}` : ""
                  }${draggingId === workspace.id ? " nx-drag-source" : ""}`}
                  title={[session ? sessionStatusText(session.status) : null, `${tabCount} 个标签`]
                    .filter(Boolean)
                    .join(" · ")}
                  onClick={() => onSelect(workspace.id)}
                  onKeyDown={(event) => onKeyDown(event, workspace.id)}
                  onDragStart={(event) => onDragStart(event, workspace.id)}
                  onDragEnd={() => {
                    setDraggingId(null);
                    setDropHint(null);
                  }}
                  onDragOver={(event) => onDragOver(event, workspace)}
                  onDragLeave={() => {
                    setDropHint((hint) => (hint?.id === workspace.id ? null : hint));
                  }}
                  onDrop={(event) => onDrop(event, workspace)}
                >
                  <Icon size={13} />
                  <span className="truncate">{workspace.title}</span>
                </button>
              );
            })}
          </div>
        ) : (
          <span className="px-2 text-xs text-neutral-500">没有打开的工作区</span>
        )}
      </div>
    </div>
  );
}
