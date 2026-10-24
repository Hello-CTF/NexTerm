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

export interface DiagnosticsNamedValue {
  name: string;
  value: number;
}

// One performance window (or the start line) from the web view; numbers and short names only.
export interface FrontendPerfReport {
  kind: "start" | "window";
  agent?: string;
  viewWidth?: number;
  viewHeight?: number;
  windowMs?: number;
  lagMaxMs?: number;
  lagP95Ms?: number;
  fps?: number;
  maxFrameGapMs?: number;
  mutations?: number;
  domChurn?: DiagnosticsNamedValue[];
  animations?: number;
  ipc?: DiagnosticsNamedValue[];
  events?: DiagnosticsNamedValue[];
  storeSets?: number;
  storeKeys?: DiagnosticsNamedValue[];
  domNodes?: number;
  xterms?: number;
  hidden?: boolean;
  longTasks?: number;
  longTaskMsMax?: number;
  heapUsedMB?: number;
}

// One-shot debug capture: arming applies to the next launch only and disarms itself.
export const diagnosticsApi = {
  status: () => call("diagnostics_status") as Promise<DiagnosticsStatus>,
  arm: (armed: boolean) => call("diagnostics_arm", { armed }) as Promise<DiagnosticsStatus>,
  openFolder: (path = "") => call("diagnostics_open_folder", { path }) as Promise<unknown>,
  pack: (path: string) => call("diagnostics_pack", { path }) as Promise<{ path: string }>,
  remove: (path: string) => call("diagnostics_delete", { path }) as Promise<DiagnosticsStatus>,
  clear: () => call("diagnostics_clear") as Promise<DiagnosticsStatus>,
  reportFrontend: (report: FrontendPerfReport) => call("diagnostics_report_frontend", report) as Promise<unknown>,
};
