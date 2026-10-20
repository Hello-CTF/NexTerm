import { call } from "./commands";
import { bytesToBase64 } from "../ui/base64";

export type LogTailSourceKind = "file" | "journal" | "docker";
export type LogTailSourceStatus = "queued" | "starting" | "running" | "exited" | "failed" | "canceled";

export interface LogTailSource {
  sourceId: string;
  sessionId: string;
  sessionName?: string;
  kind: LogTailSourceKind;
  value: string;
}

export interface LogTailStartRequest {
  runId: string;
  sources: LogTailSource[];
  maxConcurrent?: number;
}

export interface LogTailEvent {
  runId: string;
  sequence: number;
  type: "status" | "line" | "done";
  sourceId?: string;
  sessionId?: string;
  sessionName?: string;
  status?: LogTailSourceStatus;
  text?: string;
  stderr?: boolean;
  error?: string;
  timeMs: number;
}

export interface LogTailStartResult {
  runId: string;
}

export interface LogTailCancelResult {
  runId: string;
  canceled: boolean;
}

export interface LogTailSaveResult {
  path: string;
  bytes: number;
}

export const logTailApi = {
  start: (request: LogTailStartRequest, channel: unknown) =>
    call<LogTailStartResult>("log_tail_start", {
      runId: request.runId,
      sources: request.sources,
      maxConcurrent: request.maxConcurrent,
      channel,
    }),
  cancel: (runId: string) => call<LogTailCancelResult>("log_tail_cancel", { runId }),
  save: (path: string, content: string) =>
    call<LogTailSaveResult>("log_tail_save", {
      path,
      contentBase64: bytesToBase64(new TextEncoder().encode(content)),
    }),
};

export function createLogTailRunId(): string {
  return globalThis.crypto.randomUUID();
}
