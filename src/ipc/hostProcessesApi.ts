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
  name: string;
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
}

export interface KillRequest {
  sessionId: string;
  pid: number;
  name: string;
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

type UntypedCall = (cmd: string, args: Record<string, unknown>) => Promise<unknown>;

// These commands are not in the generated manifest until the contract is regenerated, so they use the untyped path.
const untyped: UntypedCall = (cmd, args) => (call as unknown as UntypedCall)(cmd, args);

export const hostProcessesApi = {
  list: (args: { sessionId: string }) => untyped("host_processes_list", args) as Promise<HostProcessesResult>,
  kill: (args: KillRequest) => untyped("host_process_kill", { ...args }) as Promise<KillResult>,
};
