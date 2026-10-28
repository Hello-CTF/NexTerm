import { useEffect, useState, useSyncExternalStore } from "react";
import { coarsePointerMedia, isCoarsePointer, isMobile, useLandscapeFullScreen } from "../platform";
import { workspaceViewport, type WorkspaceViewport } from "../../features/terminal/workspaceLayout";
import { createFrameCoalescer } from "../../ui/ResizeHandle";
import { overlaySidebarsFor, readShellMode, shellModeFor, type ShellMode } from "./shellMode";

function subscribeShell(callback: () => void): () => void {
  const coarse = coarsePointerMedia();
  window.addEventListener("resize", callback);
  window.addEventListener("orientationchange", callback);
  window.visualViewport?.addEventListener("resize", callback);
  coarse.addEventListener("change", callback);
  return () => {
    window.removeEventListener("resize", callback);
    window.removeEventListener("orientationchange", callback);
    window.visualViewport?.removeEventListener("resize", callback);
    coarse.removeEventListener("change", callback);
  };
}

// useShell re-renders only when the mode itself changes, not on every resize.
export function useShell(): ShellMode {
  return useSyncExternalStore(subscribeShell, readShellMode);
}

export interface ShellLayout {
  mode: ShellMode;
  viewport: WorkspaceViewport;
  landscapeFull: boolean;
  overlaySidebars: boolean;
}

function readWorkspaceViewport(): WorkspaceViewport {
  return workspaceViewport(window.innerWidth, isCoarsePointer(), window.innerHeight);
}

function sameViewport(a: WorkspaceViewport, b: WorkspaceViewport): boolean {
  return a.width === b.width && a.height === b.height && a.coarsePointer === b.coarsePointer;
}

function useWorkspaceViewport(): WorkspaceViewport {
  const [viewport, setViewport] = useState(readWorkspaceViewport);

  useEffect(() => {
    const frames = createFrameCoalescer<WorkspaceViewport>((next) =>
      setViewport((prev) => (sameViewport(prev, next) ? prev : next)),
    );
    const update = () => frames.schedule(readWorkspaceViewport());
    const coarseQuery = coarsePointerMedia();
    window.addEventListener("resize", update);
    window.visualViewport?.addEventListener("resize", update);
    coarseQuery.addEventListener("change", update);
    update();
    return () => {
      frames.cancel();
      window.removeEventListener("resize", update);
      window.visualViewport?.removeEventListener("resize", update);
      coarseQuery.removeEventListener("change", update);
    };
  }, []);

  return viewport;
}

// useShellLayout is the app frame's view of the shell: the frame-coalesced workspace viewport plus
// landscape fullscreen, which forces the sidebars into overlays.
export function useShellLayout(): ShellLayout {
  const viewport = useWorkspaceViewport();
  const landscapeFull = useLandscapeFullScreen();
  const overlaySidebars = overlaySidebarsFor(viewport.overlaySidebars, landscapeFull);
  return { mode: shellModeFor(isMobile(), overlaySidebars), viewport, landscapeFull, overlaySidebars };
}
