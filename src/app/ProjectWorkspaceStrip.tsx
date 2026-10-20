import type { KeyboardEvent } from "react";
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
}: {
  workspaces: Workspace[];
  activeWorkspaceId: string | null;
  activeProject: ProjectFocus;
  projectInventory: ProjectInventory | null;
  groups: readonly AssetGroup[];
  sessions: readonly SessionRuntimeInfo[];
  onSelect: (workspaceId: string) => void;
}) {
  if (activeProject === ALL_PROJECTS) return null;
  const visible = focusedWorkspaces({ workspaces, activeWorkspaceId, activeProject, projectInventory });
  const ids = visible.map((workspace) => workspace.id);
  const label = projectFocusLabel(activeProject, groups);

  const onKeyDown = (event: KeyboardEvent<HTMLButtonElement>, workspaceId: string) => {
    const index = ids.indexOf(workspaceId);
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
                  className={`nx-tab ${selected ? "is-active" : ""}`}
                  title={[session ? sessionStatusText(session.status) : null, `${tabCount} 个标签`]
                    .filter(Boolean)
                    .join(" · ")}
                  onClick={() => onSelect(workspace.id)}
                  onKeyDown={(event) => onKeyDown(event, workspace.id)}
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
