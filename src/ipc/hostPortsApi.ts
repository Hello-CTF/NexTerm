import { call } from "./commands";

export interface HostListener {
  proto: "tcp" | "tcp6";
  address: string;
  port: number;
  pid?: number;
  process?: string;
}

export interface HostPortsResult {
  listeners: HostListener[];
  format: string;
  platform: "posix" | "windows";
  truncated?: boolean;
  // Set when the terminal printed recently and the backend did not run the command.
  skipped?: boolean;
}

export interface ConnectivityRequest {
  sessionId: string;
  method: "tcp" | "http";
  host?: string;
  port?: number;
  url?: string;
  dryRun?: boolean;
}

export interface ConnectivityResult {
  command: string;
  ran: boolean;
  ok: boolean;
  exitCode?: number;
  output: string;
  durationMs: number;
}

type UntypedCall = (cmd: string, args: Record<string, unknown>) => Promise<unknown>;

// These commands are not in the generated manifest until the contract is regenerated, so they use the untyped path.
const untyped: UntypedCall = (cmd, args) => (call as unknown as UntypedCall)(cmd, args);

export const hostPortsApi = {
  list: (args: { sessionId: string; processes?: boolean; quietMs?: number }) =>
    untyped("host_ports_list", args) as Promise<HostPortsResult>,
  connectivityTest: (args: ConnectivityRequest) =>
    untyped("host_connectivity_test", { ...args }) as Promise<ConnectivityResult>,
};
