import { call } from "./commands";

export interface DiagnosticsCapture {
  name: string;
  path: string;
  sizeBytes: number;
  timeMs: number;
  archive?: string;
}

export interface DiagnosticsStatus {
  armed: boolean;
  active: boolean;
  activeDir?: string;
  root: string;
  canOpenFolder: boolean;
  captures: DiagnosticsCapture[];
}

// One-shot debug capture: arming applies to the next launch only and disarms itself.
export const diagnosticsApi = {
  status: () => call("diagnostics_status") as Promise<DiagnosticsStatus>,
  arm: (armed: boolean) => call("diagnostics_arm", { armed }) as Promise<DiagnosticsStatus>,
  openFolder: (path = "") => call("diagnostics_open_folder", { path }) as Promise<unknown>,
  pack: (path: string) => call("diagnostics_pack", { path }) as Promise<{ path: string }>,
};
