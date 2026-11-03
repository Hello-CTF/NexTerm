import { call } from "./commands";

export interface HostProcess {
  pid: number;
  ppid: number;
  user?: string;
  cpu: number;
  mem: number;
  rssKiB: number;
  // -1 when the host does not report it.
  elapsedSec: number;
  elapsed?: string;
  stat?: string;
  // Display name (comm on POSIX).
  name: string;
  // POSIX only: basename of the first args token.
  argName?: string;
  // Opaque start-time identifier, echoed back on kill to guard against PID reuse.
  started?: string;
  command: string;
  zombie?: boolean;
}

export interface HostProcessesResult {
  processes: HostProcess[];
  platform: "posix" | "windows";
  format: string;
  truncated?: boolean;
  // Columns the host could not provide.
  partial?: string[];
  skippedRows?: number;
}

export interface KillRequest {
  sessionId: string;
  pid: number;
  ppid: number;
  // POSIX: argName ?? name; Windows: name.
  name: string;
  started?: string;
  force?: boolean;
  dryRun?: boolean;
}

export interface KillResult {
  command: string;
  signal: "TERM" | "KILL";
  ran: boolean;
  ok: boolean;
  exitCode?: number;
  output: string;
  durationMs: number;
  auditError?: string;
}

export const hostProcessesApi = {
  list: (args: { sessionId: string }) => call("host_processes_list", args) as Promise<HostProcessesResult>,
  kill: (args: KillRequest) => call("host_process_kill", { ...args }) as Promise<KillResult>,
};
