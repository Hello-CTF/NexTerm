import { useEffect, useState, type ComponentType } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { type Asset } from "../../ipc/commands";
import { WEB } from "../../ipc/env";
import { PromptModal } from "../../ui/PromptModal";
import { DialogHost } from "../../ui/DialogHost";
import { MultilinePasteDialogHost } from "../../features/terminal/MultilinePasteDialog";
import { StateMarkDialogHost } from "../../features/session/StateMarkDialogHost";
import { Lazy } from "../../ui/LazyBoundary";
import { ContextMenu, type ContextMenuState } from "../../ui/ContextMenu";
import { AssetEditor, JumpPalette } from "../lazyPanels";
import type { JumpScope } from "../JumpPalette";

// LazyAuthGate loads the account gate only under WEB, so the desktop does not pull the auth store into env mocks of terminal tests.
function LazyAuthGate() {
  const [gate, setGate] = useState<ComponentType | null>(null);
  useEffect(() => {
    if (!WEB) return;
    let alive = true;
    void import("../../features/auth/AuthGate").then((m) => {
      if (alive) setGate(() => m.AuthGate);
    });
    return () => {
      alive = false;
    };
  }, []);
  if (!gate) return null;
  const Gate = gate;
  return <Gate />;
}

export interface AppDialogsProps {
  jumpScope: JumpScope | null;
  setJumpScope: (scope: JumpScope | null) => void;
  onOpenFiles: () => void;
  editingAsset: Asset | null;
  setEditingAsset: (asset: Asset | null) => void;
  workspaceMenu: ContextMenuState | null;
  onCloseWorkspaceMenu: () => void;
}

// AppDialogs hosts the app-level overlays: the jump palette, the palette's asset editor,
// the workspace switcher menu, the account gate and the prompt/dialog hosts.
export function AppDialogs({
  jumpScope,
  setJumpScope,
  onOpenFiles,
  editingAsset,
  setEditingAsset,
  workspaceMenu,
  onCloseWorkspaceMenu,
}: AppDialogsProps) {
  const queryClient = useQueryClient();
  return (
    <>
      {jumpScope && (
        <Lazy fallback="none" overlay>
          <JumpPalette
            key={jumpScope}
            scope={jumpScope}
            onClose={() => setJumpScope(null)}
            onOpenFiles={onOpenFiles}
            onEditAsset={(asset) => setEditingAsset(asset)}
          />
        </Lazy>
      )}
      {editingAsset && (
        <Lazy fallback="none" overlay>
          <AssetEditor
            kind="asset"
            initial={editingAsset}
            onClose={() => setEditingAsset(null)}
            onSaved={() => {
              setEditingAsset(null);
              void queryClient.invalidateQueries({ queryKey: ["assets"] });
              void queryClient.invalidateQueries({ queryKey: ["asset-search"] });
              void queryClient.invalidateQueries({ queryKey: ["credentials"] });
            }}
          />
        </Lazy>
      )}
      <ContextMenu state={workspaceMenu} onClose={onCloseWorkspaceMenu} />
      <LazyAuthGate />
      <PromptModal />
      <DialogHost />
      <MultilinePasteDialogHost />
      <StateMarkDialogHost />
    </>
  );
}
