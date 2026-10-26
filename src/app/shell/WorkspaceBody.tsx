import {
  useUi,
  useHasSessions,
  useWorkspaceList,
  LEFT_WIDTH_RANGE,
  RIGHT_WIDTH_RANGE,
  type AppTab,
  type Workspace,
} from "../store";
import { ResizeHandle } from "../../ui/ResizeHandle";
import { Lazy } from "../../ui/LazyBoundary";
import { AiSidebar, AssetTree, CredentialsSidebar, SessionPanel } from "../lazyPanels";
import { SESSION_PANEL_WIDTH, useSessionPanel } from "../../features/session-panel/sessionPanelStore";
import { PaneGroup, PaneTabPanel, type PaneMoveTarget } from "../workspace/PaneGroup";
import { SplitView } from "../workspace/SplitView";
import { workspaceLayoutTree } from "../paneLayout";
import { focusNeighbor, paneIdsInOrder, type FocusDirection } from "../splitTree";
import { EmptyState, WorkspaceEmpty } from "../workspace/EmptyStates";
import { type OverlayDocks } from "./useOverlayDocks";

export interface WorkspaceBodyProps {
  ws: Workspace | null | undefined;
  visibleWorkspaces: Workspace[];
  activeTab: AppTab | null;
  activeSessionId: string | undefined;
  overlaySidebars: boolean;
  splitAllowed: boolean;
  // Show only the active pane of a split workspace (mobile shell and compact widths).
  singlePane: boolean;
  docks: OverlayDocks;
  onNewTerminal: () => void;
  onLocalTerminal: () => void;
  onOpenPalette: () => void;
}

// WorkspaceBody is the row under the header: the left dock, the workspace panes, the session side panel and the right
// (AI) dock. The session panel sits left of the AI sidebar so both can stay open. In overlay layouts the docks float
// above the panes behind a tap-to-close backdrop, and the session panel is a full-screen sheet.
export function WorkspaceBody({
  ws,
  visibleWorkspaces,
  activeTab,
  activeSessionId,
  overlaySidebars,
  splitAllowed,
  singlePane,
  docks,
  onNewTerminal,
  onLocalTerminal,
  onOpenPalette,
}: WorkspaceBodyProps) {
  const workspaces = useWorkspaceList();
  const hasSessions = useHasSessions();
  const leftMode = useUi((s) => s.leftMode);
  const leftWidth = useUi((s) => s.leftWidth);
  const setLeftWidth = useUi((s) => s.setLeftWidth);
  const rightWidth = useUi((s) => s.rightWidth);
  const setRightWidth = useUi((s) => s.setRightWidth);
  const sessionWidth = useSessionPanel((s) => s.width);
  const setSessionWidth = useSessionPanel((s) => s.setWidth);
  const accountEpoch = useUi((s) => s.accountEpoch);
  const aiAccountEpoch = useUi((s) => s.aiAccountEpoch);
  const splitPane = useUi((s) => s.splitPane);
  const closePane = useUi((s) => s.closePane);
  const moveTabToPane = useUi((s) => s.moveTabToPane);
  const splitPaneWithTab = useUi((s) => s.splitPaneWithTab);
  const setActivePane = useUi((s) => s.setActivePane);
  const setSplitRatio = useUi((s) => s.setSplitRatio);
  const { leftDockOpen, rightDockOpen, sessionDockOpen, sessionSheet, setLeftOpen, setOverlayDock, setSessionOpen } = docks;

  return (
    <div
      className={`nx-workspace-body flex min-h-0 flex-1 bg-neutral-900 ${
        sessionDockOpen && !sessionSheet ? "has-session-dock" : ""
      }`}
    >
      {((overlaySidebars && (leftDockOpen || rightDockOpen)) || (sessionSheet && sessionDockOpen)) && (
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
          hasSessions ? (
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
              const tree = workspaceLayoutTree(w);
              const order = tree ? paneIdsInOrder(tree) : w.panes.map((p) => p.id);
              const cyclePane = (paneId: string, step: 1 | -1) => {
                const index = order.indexOf(paneId);
                const next = order[(index + step + order.length) % order.length];
                if (next) setActivePane(next, w.id);
              };
              return (
                <div
                  key={w.id}
                  id={`nx-ws-panel-${w.id}`}
                  className={wsActive ? "h-full min-h-0" : "hidden"}
                >
                  <SplitView
                    workspace={w}
                    singlePane={singlePane}
                    onRatio={(splitId, ratio) => setSplitRatio(splitId, ratio, w.id)}
                    onActivatePane={(paneId) => setActivePane(paneId, w.id)}
                    onTabDrop={(tabId, paneId, region) => {
                      if (region === "center") moveTabToPane(tabId, paneId);
                      else splitPaneWithTab(tabId, paneId, region, w.id);
                    }}
                    renderTab={(p, t, visible) => (
                      <PaneTabPanel
                        tab={t}
                        idPrefix={`${w.id}-${p.id}`}
                        workspaceId={w.id}
                        visible={wsActive && visible}
                      />
                    )}
                    renderPane={(p, view, stripRef) => (
                      <PaneGroup
                        pane={p}
                        idPrefix={`${w.id}-${p.id}`}
                        active={wsActive && view.focused}
                        stripRef={stripRef}
                        split={split}
                        singlePane={singlePane}
                        paneNumber={order.indexOf(p.id) + 1}
                        paneCount={order.length}
                        splitAllowed={splitAllowed}
                        moveTargets={tree ? paneMoveTargets(tree, p.id) : []}
                        onSplit={(direction) => splitPane(direction, p.id, w.id)}
                        onClosePane={() => closePane(p.id, w.id)}
                        onCyclePane={(step) => cyclePane(p.id, step)}
                        onActivate={() => setActivePane(p.id, w.id)}
                        onNewTerminal={onNewTerminal}
                        leftOpen={leftDockOpen}
                        onToggleLeft={() => setLeftOpen(!leftDockOpen)}
                      />
                    )}
                  />
                </div>
              );
            })}
          </>
        )}
      </main>

      {sessionDockOpen && (
        <div className={`nx-session-dock ${sessionSheet ? "is-overlay" : ""}`}>
          {!sessionSheet && (
            <ResizeHandle
              side="right"
              label="调整会话侧栏宽度"
              width={sessionWidth}
              min={SESSION_PANEL_WIDTH.min}
              max={SESSION_PANEL_WIDTH.max}
              defaultWidth={SESSION_PANEL_WIDTH.default}
              onChange={setSessionWidth}
            />
          )}
          <div className="nx-session-dock-body" style={sessionSheet ? undefined : { width: sessionWidth }}>
            <Lazy>
              <SessionPanel overlay={sessionSheet} onClose={() => setSessionOpen(false)} />
            </Lazy>
          </div>
        </div>
      )}

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

const MOVE_DIRECTIONS: { direction: FocusDirection; label: string }[] = [
  { direction: "left", label: "左侧窗格" },
  { direction: "right", label: "右侧窗格" },
  { direction: "up", label: "上方窗格" },
  { direction: "down", label: "下方窗格" },
];

// paneMoveTargets lists the panes a tab can move to: the geometric neighbours of its pane.
function paneMoveTargets(tree: Parameters<typeof focusNeighbor>[0], paneId: string): PaneMoveTarget[] {
  const out: PaneMoveTarget[] = [];
  for (const { direction, label } of MOVE_DIRECTIONS) {
    const target = focusNeighbor(tree, paneId, direction);
    if (target && !out.some((entry) => entry.paneId === target)) out.push({ paneId: target, label });
  }
  return out;
}
