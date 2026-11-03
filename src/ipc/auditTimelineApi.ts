import { call } from "./commands";

export type AuditTimelineOrigin = "user" | "ai" | "device";
export type AuditTimelineStream = "audit" | "command" | "device";

export interface AuditTimelineCursor {
  ts: number;
  stream: string;
  id: number;
}

export interface AuditTimelineRequest {
  assetId?: string | null;
  origins?: AuditTimelineOrigin[];
  /** Start in ms, inclusive. */
  from?: number | null;
  /** End in ms, exclusive. */
  to?: number | null;
  limit?: number;
  cursor?: AuditTimelineCursor | null;
}

export interface AuditTimelineItem {
  stream: AuditTimelineStream;
  id: number;
  ts: number;
  origin: AuditTimelineOrigin;
  source: string;
  kind: string;
  assetId: string | null;
  sessionId: string | null;
  command: string | null;
  payload: unknown;
  exitCode: number | null;
  durationMs: number | null;
}

export interface AuditTimelinePage {
  items: AuditTimelineItem[];
  nextCursor: AuditTimelineCursor | null;
}

// Hand-written until the IPC contract is regenerated with the audit_timeline command.
export const auditTimelineApi = {
  page: async (request: AuditTimelineRequest): Promise<AuditTimelinePage> => {
    const result = (await call("audit_timeline" as never, request as never)) as unknown as Partial<AuditTimelinePage> | null;
    return { items: result?.items ?? [], nextCursor: result?.nextCursor ?? null };
  },
};
