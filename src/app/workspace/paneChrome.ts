import type { Pane, Workspace } from "../store";

// A remote desktop workspace that shows only its desktop needs no tab strip: the desktop's own toolbar disconnects
// and reconnects it, and the workspace strip closes it. Another tab or pane brings the strip back.
export function paneShowsTabStrip(workspace: Pick<Workspace, "kind" | "panes"> | undefined, pane: Pane): boolean {
  if (workspace?.kind !== "remote" || workspace.panes.length !== 1) return true;
  return !(pane.tabs.length === 1 && pane.tabs[0].kind === "remote");
}
