import {
  useUi,
  LEFT_WIDTH_RANGE,
  RIGHT_WIDTH_RANGE,
  type AppTab,
  type Workspace,
} from "../store";
import { ResizeHandle } from "../../ui/ResizeHandle";
import { Lazy } from "../../ui/LazyBoundary";
import { AiSidebar, AssetTree, CredentialsSidebar, FileTree } from "../lazyPanels";
import { PaneGroup } from "../workspace/PaneGroup";
import { SplitStack } from "../workspace/SplitStack";
import { EmptyState, WorkspaceEmpty } from "../workspace/EmptyStates";
import { type OverlayDocks } from "./useOverlayDocks";

export interface WorkspaceBodyProps {
  ws: Workspace | null | undefined;
  visibleWorkspaces: Workspace[];
  activeTab: AppTab | null;
  activeSessionId: string | undefined;
  overlaySidebars: boolean;
  splitAllowed: boolean;
  docks: OverlayDocks;
  onNewTerminal: () => void;
  onLocalTerminal: () => void;
  onOpenPalette: () => void;
}

// WorkspaceBody is the row under the header: the left dock, the workspace panes and the right (AI) dock.
// In overlay layouts the docks float above the panes behind a tap-to-close backdrop.
export function WorkspaceBody({
  ws,
  visibleWorkspaces,
  activeTab,
  activeSessionId,
  overlaySidebars,
  splitAllowed,
  docks,
  onNewTerminal,
  onLocalTerminal,
  onOpenPalette,
}: WorkspaceBodyProps) {
  const workspaces = useUi((s) => s.workspaces);
  const sessions = useUi((s) => s.sessions);
  const leftMode = useUi((s) => s.leftMode);
  const leftWidth = useUi((s) => s.leftWidth);
  const setLeftWidth = useUi((s) => s.setLeftWidth);
  const rightWidth = useUi((s) => s.rightWidth);
  const setRightWidth = useUi((s) => s.setRightWidth);
  const accountEpoch = useUi((s) => s.accountEpoch);
  const aiAccountEpoch = useUi((s) => s.aiAccountEpoch);
  const splitWorkspace = useUi((s) => s.splitWorkspace);
  const unsplitWorkspace = useUi((s) => s.unsplitWorkspace);
  const setActivePane = useUi((s) => s.setActivePane);
  const setSplitRatio = useUi((s) => s.setSplitRatio);
  const { leftDockOpen, rightDockOpen, setLeftOpen, setOverlayDock } = docks;

  return (
    <div className="nx-workspace-body flex min-h-0 flex-1 bg-neutral-900">
      {overlaySidebars && (leftDockOpen || rightDockOpen) && (
        <button
          type="button"
          className="nx-dock-backdrop"
          aria-label="收起侧栏"
          onClick={() => setOverlayDock(null)}
        />
      )}
      {leftDockOpen && (
        <div className="nx-left-dock" role="navigation" aria-label="左栏">
          {leftMode === "credentials" ? (
            <Lazy><CredentialsSidebar /></Lazy>
          ) : leftMode === "files" && ws?.sessionId ? (
            <Lazy><FileTree key={ws.sessionId} sessionId={ws.sessionId} /></Lazy>
          ) : (
            // Keyed by account epoch: on account switch the tree remounts so template, runbook,
            // inventory discovery/watch, and bulk panels never show the previous account's state.
            <Lazy><AssetTree key={accountEpoch} /></Lazy>
          )}
          <ResizeHandle
            side="left"
            width={leftWidth}
            min={LEFT_WIDTH_RANGE.min}
            max={LEFT_WIDTH_RANGE.max}
            defaultWidth={LEFT_WIDTH_RANGE.default}
            onChange={setLeftWidth}
          />
        </div>
      )}

      <main id="nx-main" tabIndex={-1} className="nx-workspace-main min-w-0 flex-1">
        {workspaces.length === 0 ? (
          sessions.length > 0 ? (
            <WorkspaceEmpty onNew={onNewTerminal} />
          ) : (
            <EmptyState onLocal={onLocalTerminal} onPalette={onOpenPalette} />
          )
        ) : (
          <>
            {visibleWorkspaces.length === 0 && (
              <div role="status" className="flex h-full items-center justify-center px-6 text-center text-sm text-neutral-500">
                当前项目没有打开的工作区。切换项目不会断开其他工作区。
              </div>
            )}
            {workspaces.map((w) => {
              const wsActive = w.id === ws?.id;
              const split = w.panes.length > 1;
              const groups = w.panes.map((p) => (
                <PaneGroup
                  key={p.id}
                  pane={p}
                  idPrefix={`${w.id}-${p.id}`}
                  workspaceId={w.id}
                  active={wsActive && p.id === w.activePaneId}
                  visible={wsActive}
                  split={split}
                  canSplit={!split}
                  splitAllowed={splitAllowed}
                  onToggleSplit={() => {
                    if (split) void unsplitWorkspace(w.panes[1].id, w.id);
                    else if (splitAllowed) splitWorkspace(w.id);
                  }}
                  onActivate={() => setActivePane(p.id, w.id)}
                  onNewTerminal={onNewTerminal}
                  leftOpen={leftDockOpen}
                  onToggleLeft={() => setLeftOpen(!leftDockOpen)}
                />
              ));
              return (
                <div
                  key={w.id}
                  id={`nx-ws-panel-${w.id}`}
                  className={wsActive ? "h-full min-h-0" : "hidden"}
                >
                  <SplitStack
                    ratio={w.splitRatio}
                    onRatio={(r) => setSplitRatio(r, w.id)}
                    top={groups[0]}
                    bottom={groups[1]}
                  />
                </div>
              );
            })}
          </>
        )}
      </main>

      <div className={`nx-right-dock ${rightDockOpen ? "" : "is-hidden"}`}>
        {rightDockOpen && (
          <ResizeHandle
            side="right"
            width={rightWidth}
            min={RIGHT_WIDTH_RANGE.min}
            max={RIGHT_WIDTH_RANGE.max}
            defaultWidth={RIGHT_WIDTH_RANGE.default}
            onChange={setRightWidth}
          />
        )}
        <Lazy><AiSidebar key={aiAccountEpoch} sessionId={activeSessionId} tabId={activeTab?.kind === "terminal" ? activeTab.tabId : undefined} /></Lazy>
      </div>
    </div>
  );
}
