import type { Asset } from "../../ipc/commands";
import type { LogTailSourceKind } from "../../ipc/logTailApi";
import { t } from "../../i18n";

export const SOURCE_KIND_LABELS: Record<LogTailSourceKind, string> = {
  file: t("文件"),
  journal: "journalctl",
  docker: t("Docker 容器"),
  winevent: t("Windows 事件日志"),
};

export const SOURCE_PLACEHOLDERS: Record<LogTailSourceKind, string> = {
  file: "/var/log/app.log",
  journal: "nginx.service",
  docker: "container-name",
  winevent: "System",
};

export interface SourceDraft {
  kind: LogTailSourceKind;
  value: string;
}

const WINDOWS_SHELL = /(^|[\\/])(powershell|pwsh)(\.exe)?$/i;

// isWindowsAsset recognizes the hosts the backend also treats as Windows from their asset record: WinRM assets and
// SSH assets configured with a PowerShell shell. Windows OpenSSH hosts left on the default shell cannot be told apart.
export function isWindowsAsset(asset: Asset | undefined): boolean {
  if (!asset) return false;
  if (asset.kind === "winrm") return true;
  const shell = asset.options?.shell;
  return typeof shell === "string" && WINDOWS_SHELL.test(shell.trim());
}

export function defaultSourceDraft(windows: boolean): SourceDraft {
  return windows ? { kind: "winevent", value: "System" } : { kind: "file", value: "" };
}

// sourceKindOptions lists the kinds in the order shown; the Windows event log leads for Windows hosts and stays
// selectable elsewhere so a Windows host the client cannot recognize is not locked out.
export function sourceKindOptions(windows: boolean): { value: LogTailSourceKind; label: string }[] {
  const order: LogTailSourceKind[] = windows ? ["winevent", "file", "docker"] : ["file", "journal", "docker", "winevent"];
  return order.map((value) => ({ value, label: SOURCE_KIND_LABELS[value] }));
}

// changeSourceKind switches the kind and supplies the default channel when moving to the event log with no value.
export function changeSourceKind(draft: SourceDraft, kind: LogTailSourceKind): SourceDraft {
  if (kind === "winevent" && !draft.value.trim()) return { kind, value: "System" };
  return { ...draft, kind };
}
