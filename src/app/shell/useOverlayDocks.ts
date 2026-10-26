import { useCallback, useEffect, useRef, useState } from "react";
import { useUi } from "../store";
import { useEdgeGestures } from "../edgeGestures";

export type OverlayDock = "left" | "right" | null;

export interface OverlayDocks {
  overlayDock: OverlayDock;
  setOverlayDock: (dock: OverlayDock) => void;
  leftDockOpen: boolean;
  rightDockOpen: boolean;
  setLeftOpen: (open: boolean) => void;
  setRightOpen: (open: boolean) => void;
}

// useOverlayDocks resolves which sidebar is shown. Docked layouts follow the persisted leftOpen/rightOpen;
// overlay layouts show at most one drawer, tracked per session in overlayDock.
export function useOverlayDocks(overlaySidebars: boolean): OverlayDocks {
  const leftOpen = useUi((s) => s.leftOpen);
  const setLeftOpenStore = useUi((s) => s.setLeftOpen);
  const rightOpen = useUi((s) => s.rightOpen);
  const setRightOpenStore = useUi((s) => s.setRightOpen);
  const connectFocusRevision = useUi((s) => s.connectFocusRevision);
  const [overlayDock, setOverlayDock] = useState<OverlayDock>(null);
  const leftDockOpen = overlaySidebars ? overlayDock === "left" && leftOpen : leftOpen;
  const rightDockOpen = overlaySidebars ? overlayDock === "right" && rightOpen : rightOpen;

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

  return { overlayDock, setOverlayDock, leftDockOpen, rightDockOpen, setLeftOpen, setRightOpen };
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
  const isTerminalPaneKind = activeTabKind === "terminal";
  useEffect(() => {
    if (!overlaySidebars) return;
    const prev = prevActiveKind.current;
    prevActiveKind.current = activeTabKind;
    if (activeTabKind === prev) return;
    if (isTerminalPaneKind) {
      rememberedOverlayDock.current = overlayDock;
      if (overlayDock) setOverlayDock(null);
    } else if (rememberedOverlayDock.current) {
      const mem = rememberedOverlayDock.current;
      rememberedOverlayDock.current = null;
      setOverlayDock(mem);
    }
  }, [activeTabKind, isTerminalPaneKind, overlaySidebars, overlayDock, setOverlayDock]);
}
