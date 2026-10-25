import { lazy } from "react";

// Feature panels split out of the entry chunk. Each loader is created once so early preloads share the request.
const loadAssetTree = () => import("../features/explorer/AssetTree");

// xterm is the heaviest dependency; it is requested at boot (see the end of this file) but kept out of the entry chunk.
const loadTerminalPane = () => import("../features/terminal/TerminalPane");

export const TerminalPane = lazy(() => loadTerminalPane().then((m) => ({ default: m.TerminalPane })));
export const AssetTree = lazy(() => loadAssetTree().then((m) => ({ default: m.AssetTree })));
export const AssetEditor = lazy(() => loadAssetTree().then((m) => ({ default: m.AssetEditor })));
export const BackgroundSessions = lazy(() =>
  import("../features/terminal/BackgroundSessions").then((m) => ({ default: m.BackgroundSessions })),
);
export const TranscriptHistoryPanel = lazy(() =>
  import("../features/terminal/TranscriptHistoryPanel").then((m) => ({ default: m.TranscriptHistoryPanel })),
);
export const FileBrowser = lazy(() => import("../features/files/FileBrowser").then((m) => ({ default: m.FileBrowser })));
export const FileTree = lazy(() => import("../features/files/FileTree").then((m) => ({ default: m.FileTree })));
export const MountPanel = lazy(() => import("../features/files/MountPanel").then((m) => ({ default: m.MountPanel })));
export const FileEditor = lazy(() => import("../features/files/FileEditor").then((m) => ({ default: m.FileEditor })));
export const LogViewer = lazy(() => import("../features/files/LogViewer").then((m) => ({ default: m.LogViewer })));
export const ForwardPanel = lazy(() => import("../features/forward/ForwardPanel").then((m) => ({ default: m.ForwardPanel })));
export const ForwardStartupRecovery = lazy(() =>
  import("../features/forward/ForwardStartupRecovery").then((m) => ({ default: m.ForwardStartupRecovery })),
);
export const MultiHostLogTail = lazy(() =>
  import("../features/logs/MultiHostLogTail").then((m) => ({ default: m.MultiHostLogTail })),
);
export const FleetHealthPanel = lazy(() =>
  import("../features/fleet/FleetHealthPanel").then((m) => ({ default: m.FleetHealthPanel })),
);
export const DockerPanel = lazy(() => import("../features/docker/DockerPanel").then((m) => ({ default: m.DockerPanel })));
export const DbPanel = lazy(() => import("../features/db/DbPanel").then((m) => ({ default: m.DbPanel })));
export const ExecBatchPanel = lazy(() =>
  import("../features/execbatch/ExecBatchPanel").then((m) => ({ default: m.ExecBatchPanel })),
);
export const AiSidebar = lazy(() => import("../features/ai/AiSidebar").then((m) => ({ default: m.AiSidebar })));
export const SettingsView = lazy(() => import("../features/settings/SettingsView").then((m) => ({ default: m.SettingsView })));
export const AuditView = lazy(() => import("../features/settings/AuditView").then((m) => ({ default: m.AuditView })));
export const CredentialsPanel = lazy(() =>
  import("../features/credentials/CredentialsPanel").then((m) => ({ default: m.CredentialsPanel })),
);
export const CredentialsSidebar = lazy(() =>
  import("../features/credentials/CredentialsSidebar").then((m) => ({ default: m.CredentialsSidebar })),
);
const loadCommandPalette = () => import("./CommandPalette");
const loadQuickConnect = () => import("./QuickConnect");
export const CommandPalette = lazy(() => loadCommandPalette().then((m) => ({ default: m.CommandPalette })));
export const QuickConnect = lazy(() => loadQuickConnect().then((m) => ({ default: m.QuickConnect })));
export const AutoSync = lazy(() => import("./AutoSync"));

// The asset tree is on the first screen: start downloading it in parallel with the rest of boot.
void loadAssetTree().catch(() => undefined);
void loadTerminalPane().catch(() => undefined);

interface NetworkInformationLike {
  saveData?: boolean;
  effectiveType?: string;
}

export function shouldPrefetch(connection: NetworkInformationLike | undefined): boolean {
  if (!connection) return true;
  if (connection.saveData) return false;
  return connection.effectiveType !== "slow-2g" && connection.effectiveType !== "2g";
}

// Keyboard-opened overlays (Ctrl+Shift+O, Alt+Q) must not lose keystrokes to a chunk download:
// fetch them once the browser is idle after first paint, unless the user asked to save data.
export function prefetchOverlaysWhenIdle(): void {
  if (typeof window === "undefined") return;
  const connection = (navigator as Navigator & { connection?: NetworkInformationLike }).connection;
  if (!shouldPrefetch(connection)) return;
  const run = () => {
    void loadCommandPalette().catch(() => undefined);
    void loadQuickConnect().catch(() => undefined);
  };
  const schedule = () => {
    if (typeof window.requestIdleCallback === "function") window.requestIdleCallback(run, { timeout: 5000 });
    else window.setTimeout(run, 2000);
  };
  if (document.readyState === "complete") schedule();
  else window.addEventListener("load", schedule, { once: true });
}

prefetchOverlaysWhenIdle();
