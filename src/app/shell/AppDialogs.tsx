import { useEffect, useState, type ComponentType } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { type Asset } from "../../ipc/commands";
import { WEB } from "../../ipc/env";
import { PromptModal } from "../../ui/PromptModal";
import { DialogHost } from "../../ui/DialogHost";
import { Lazy } from "../../ui/LazyBoundary";
import { ContextMenu, type ContextMenuState } from "../../ui/ContextMenu";
import { AssetEditor, CommandPalette, QuickConnect } from "../lazyPanels";

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

export type QuickConnectMode = "all" | "database" | null;

export interface AppDialogsProps {
  paletteOpen: boolean;
  onClosePalette: () => void;
  onOpenFiles: () => void;
  quickConnectMode: QuickConnectMode;
  setQuickConnectMode: (mode: QuickConnectMode) => void;
  editingAsset: Asset | null;
  setEditingAsset: (asset: Asset | null) => void;
  workspaceMenu: ContextMenuState | null;
  onCloseWorkspaceMenu: () => void;
}

// AppDialogs hosts the app-level overlays: command palette, quick connect, the palette's asset editor,
// the workspace switcher menu, the account gate and the prompt/dialog hosts.
export function AppDialogs({
  paletteOpen,
  onClosePalette,
  onOpenFiles,
  quickConnectMode,
  setQuickConnectMode,
  editingAsset,
  setEditingAsset,
  workspaceMenu,
  onCloseWorkspaceMenu,
}: AppDialogsProps) {
  const queryClient = useQueryClient();
  return (
    <>
      {paletteOpen && (
        <Lazy fallback="none" overlay>
          <CommandPalette
            onClose={onClosePalette}
            onOpenFiles={onOpenFiles}
            onEditAsset={(asset) => setEditingAsset(asset)}
            onQuickConnect={() => setQuickConnectMode("all")}
          />
        </Lazy>
      )}
      {quickConnectMode && (
        <Lazy fallback="none" overlay>
          <QuickConnect
            kindFilter={quickConnectMode === "database" ? "database" : undefined}
            onClose={() => setQuickConnectMode(null)}
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
    </>
  );
}
