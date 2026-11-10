import { isHostWorkspace, useUi, type LeftMode, type Workspace } from "../store";
import { hostToolsSupported } from "../../features/explorer/assetKindFields";

export type LeftView = LeftMode | "files";

// resolveLeftView is what the left sidebar shows: in a host workspace the file tree unless a temporary override is
// set; in the tools area (no host workspace) the user's asset/credentials choice. A host without a file service
// (Telnet) has no file tree, so it shows the override or the user's choice.
export function resolveLeftView(ws: Workspace | null | undefined, leftMode: LeftMode, override: LeftMode | null): LeftView {
  if (!isHostWorkspace(ws)) return leftMode;
  if (!hostToolsSupported(ws?.assetKind)) return override ?? leftMode;
  return override ?? "files";
}

export function useLeftView(ws: Workspace | null | undefined): LeftView {
  const leftMode = useUi((s) => s.leftMode);
  const override = useUi((s) => s.leftOverride);
  return resolveLeftView(ws, leftMode, override);
}
