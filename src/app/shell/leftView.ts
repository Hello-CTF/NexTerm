import { isHostWorkspace, useUi, type LeftMode, type Workspace } from "../store";

export type LeftView = LeftMode | "files";

// resolveLeftView is what the left sidebar shows: in a host workspace the file tree unless a temporary override is
// set; in the tools area (no host workspace) the user's asset/credentials choice.
export function resolveLeftView(ws: Workspace | null | undefined, leftMode: LeftMode, override: LeftMode | null): LeftView {
  return isHostWorkspace(ws) ? (override ?? "files") : leftMode;
}

export function useLeftView(ws: Workspace | null | undefined): LeftView {
  const leftMode = useUi((s) => s.leftMode);
  const override = useUi((s) => s.leftOverride);
  return resolveLeftView(ws, leftMode, override);
}
