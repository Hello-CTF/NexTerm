import { create } from "zustand";
import { hostHistoryTab, useUi } from "../../app/store";
import { t } from "../../i18n";

export interface TranscriptFocusRequest {
  assetId: string;
  transcriptId: string;
  ts: number;
  seq?: number;
}

interface TranscriptFocusState {
  request: (TranscriptFocusRequest & { nonce: number }) | null;
  set: (request: TranscriptFocusRequest) => void;
  clear: (nonce: number) => void;
}

let nonceCounter = 0;

// The store only carries a pending request; the history panel consumes and clears it.
export const useTranscriptFocus = create<TranscriptFocusState>((set, get) => ({
  request: null,
  set: (request) => set({ request: { ...request, nonce: ++nonceCounter } }),
  clear: (nonce) => {
    if (get().request?.nonce === nonce) set({ request: null });
  },
}));

export function requestTranscriptFocus(request: TranscriptFocusRequest): void {
  useTranscriptFocus.getState().set(request);
}

// openHostHistoryTab opens the terminal history of one host: the host workspace's own tab when it is the active
// workspace, the shared history tab otherwise.
export function openHostHistoryTab(assetId: string): void {
  const ui = useUi.getState();
  const active =
    ui.workspaces.find((item) => item.id === ui.activeWorkspaceId) ??
    ui.workspaces[ui.workspaces.length - 1];
  const hostTab = active?.assetId === assetId ? hostHistoryTab(active) : null;
  ui.addTab(hostTab ?? { id: "history", kind: "history", title: t("终端历史"), closable: true, assetId });
}

// openTranscriptFocus asks the history panel to land on a transcript position and makes sure a history tab is open.
export function openTranscriptFocus(request: TranscriptFocusRequest): void {
  requestTranscriptFocus(request);
  openHostHistoryTab(request.assetId);
}
