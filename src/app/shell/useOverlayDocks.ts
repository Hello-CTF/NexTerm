import { useCallback, useEffect, useRef, useState } from "react";
import { useUi } from "../store";
import { useEdgeGestures } from "../edgeGestures";
import { showSessionPanel, useSessionPanel } from "../../features/session-panel/sessionPanelStore";
import { sessionPanelFits } from "../../features/session-panel/sessionPanelFit";

export type OverlayDock = "left" | "right" | "session" | null;

export interface OverlayDocks {
  overlayDock: OverlayDock;
  setOverlayDock: (dock: OverlayDock) => void;
  leftDockOpen: boolean;
  rightDockOpen: boolean;
  // sessionDockOpen is the session side panel: docked beside the AI sidebar, or a full-screen sheet (sessionSheet) in
  // overlay layouts and wherever docking it would squeeze the workspace below its minimum width.
  sessionDockOpen: boolean;
  sessionSheet: boolean;
  setLeftOpen: (open: boolean) => void;
  setRightOpen: (open: boolean) => void;
  // setSessionOpen is the user's open/close: opening moves focus into the panel.
  setSessionOpen: (open: boolean) => void;
}

// useOverlayDocks resolves which sidebar is shown. Docked layouts follow the persisted leftOpen/rightOpen;
// overlay layouts show at most one drawer, tracked per session in overlayDock.
export function useOverlayDocks(overlaySidebars: boolean, viewportWidth: number): OverlayDocks {
  const leftOpen = useUi((s) => s.leftOpen);
  const setLeftOpenStore = useUi((s) => s.setLeftOpen);
  const rightOpen = useUi((s) => s.rightOpen);
  const setRightOpenStore = useUi((s) => s.setRightOpen);
  const sessionOpen = useSessionPanel((s) => s.open);
  const sessionWidth = useSessionPanel((s) => s.width);
  const leftWidth = useUi((s) => s.leftWidth);
  const rightWidth = useUi((s) => s.rightWidth);
  const connectFocusRevision = useUi((s) => s.connectFocusRevision);
  const [overlayDock, setOverlayDock] = useState<OverlayDock>(null);
  const leftDockOpen = overlaySidebars ? overlayDock === "left" && leftOpen : leftOpen;
  const rightDockOpen = overlaySidebars ? overlayDock === "right" && rightOpen : rightOpen;
  const sessionSheet =
    overlaySidebars ||
    !sessionPanelFits({
      viewportWidth,
      leftOpen: leftDockOpen,
      leftWidth,
      rightOpen: rightDockOpen,
      rightWidth,
      panelWidth: sessionWidth,
    });
  // A sheet shows only after an open in this run, so a remembered open never covers the terminal at startup.
  const sessionDockOpen = sessionSheet ? overlayDock === "session" && sessionOpen : sessionOpen;

  const setLeftOpen = useCallback(
    (open: boolean) => {
      setLeftOpenStore(open);
      setOverlayDock(overlaySidebars && open ? "left" : null);
    },
    [setLeftOpenStore, overlaySidebars],
  );
  const setRightOpen = useCallback(
    (open: boolean) => {
      setRightOpenStore(open);
      setOverlayDock(overlaySidebars && open ? "right" : null);
    },
    [setRightOpenStore, overlaySidebars],
  );
  const setSessionOpen = useCallback(
    (open: boolean) => {
      showSessionPanel(open);
      setOverlayDock(sessionSheet && open ? "session" : null);
    },
    [sessionSheet],
  );

  useEffect(() => {
    if (!overlaySidebars) return;
    setOverlayDock(null);
  }, [connectFocusRevision, overlaySidebars]);

  useEdgeGestures({
    overlayDock,
    openLeft: () => setLeftOpen(true),
    openRight: () => setRightOpen(true),
    closeOverlay: () => setOverlayDock(null),
  });

  return {
    overlayDock,
    setOverlayDock,
    leftDockOpen,
    rightDockOpen,
    sessionDockOpen,
    sessionSheet,
    setLeftOpen,
    setRightOpen,
    setSessionOpen,
  };
}

// useMobileTerminalDockCollapse: on phones, entering a terminal tab auto-collapses the overlay sidebar and switching back
// to a non-terminal tab restores it. Only the session-level overlayDock is touched: leftOpen/rightOpen are user-persisted
// and synced across devices; writing them would lose preferences and propagate to the other end.
export function useMobileTerminalDockCollapse(
  docks: Pick<OverlayDocks, "overlayDock" | "setOverlayDock">,
  overlaySidebars: boolean,
  activeTabKind: string | null,
): void {
  const { overlayDock, setOverlayDock } = docks;
  const rememberedOverlayDock = useRef<OverlayDock>(null);
  const prevActiveKind = useRef<string | null>(null);
  const connectFocusRevision = useUi((s) => s.connectFocusRevision);
  const prevConnectFocusRevision = useRef(connectFocusRevision);
  const isTerminalPaneKind = activeTabKind === "terminal";
  useEffect(() => {
    if (!overlaySidebars) return;
    const prev = prevActiveKind.current;
    prevActiveKind.current = activeTabKind;
    if (prevConnectFocusRevision.current !== connectFocusRevision) {
      // Opening a host shows its workspace, which may have no terminal yet: never bring a drawer back over it.
      prevConnectFocusRevision.current = connectFocusRevision;
      rememberedOverlayDock.current = null;
      return;
    }
    if (activeTabKind === prev) return;
    if (isTerminalPaneKind) {
      rememberedOverlayDock.current = overlayDock;
      if (overlayDock) setOverlayDock(null);
    } else if (rememberedOverlayDock.current) {
      const mem = rememberedOverlayDock.current;
      rememberedOverlayDock.current = null;
      setOverlayDock(mem);
    }
  }, [activeTabKind, connectFocusRevision, isTerminalPaneKind, overlaySidebars, overlayDock, setOverlayDock]);
}

// useSessionSheetDismiss closes the session sheet when the active tab changes underneath it (a file opened from the
// sheet, a tab picked elsewhere), so the sheet never covers the tab the user just opened.
export function useSessionSheetDismiss(
  docks: Pick<OverlayDocks, "overlayDock" | "setOverlayDock" | "sessionSheet">,
  activeTabKey: string,
): void {
  const { overlayDock, setOverlayDock, sessionSheet } = docks;
  const previous = useRef(activeTabKey);
  useEffect(() => {
    if (previous.current === activeTabKey) return;
    previous.current = activeTabKey;
    if (sessionSheet && overlayDock === "session") setOverlayDock(null);
  }, [activeTabKey, overlayDock, sessionSheet, setOverlayDock]);
}
