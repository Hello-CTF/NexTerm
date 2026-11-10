import { call } from "./commands";
import type { TranscriptSummary } from "./commands";

export interface CommandSearchRequest {
  text: string;
  assetId?: string | null;
  // Exact directory the command started in, from OSC 7; null searches every directory.
  cwd?: string | null;
  startedFrom?: number | null;
  startedTo?: number | null;
  limit?: number;
  offset?: number;
}

export interface CommandSearchItem {
  id: number;
  sessionId: string;
  tabId: string;
  assetId: string;
  command: string;
  // Directory the command started in; empty when the shell never reported one.
  cwd: string;
  source: string;
  exitCode: number | null;
  startedAt: number;
  finishedAt: number;
  transcriptId: string | null;
}

export interface CommandSearchResult {
  items: CommandSearchItem[];
  hasMore: boolean;
}

export interface TranscriptHostSearchRequest {
  assetId: string;
  query: string;
  startedFrom?: number | null;
  startedTo?: number | null;
}

export interface TranscriptHostMatch {
  seq: number;
  ts: number;
  preview: string;
}

export interface TranscriptHostSession {
  transcript: TranscriptSummary;
  matches: TranscriptHostMatch[];
  moreMatches: boolean;
}

export interface TranscriptHostSearchResult {
  sessions: TranscriptHostSession[];
  scannedSessions: number;
  scannedBytes: number;
  truncated: boolean;
  // Sessions skipped because their recorded output cannot be decoded.
  unreadableSessions: number;
}

export const commandSearchApi = {
  search: async (request: CommandSearchRequest): Promise<CommandSearchResult> => {
    const result = (await call("command_search", {
      ...request,
      assetId: request.assetId ?? null,
      cwd: request.cwd ?? null,
      startedFrom: request.startedFrom ?? null,
      startedTo: request.startedTo ?? null,
      limit: request.limit ?? 0,
      offset: request.offset ?? 0,
    })) as CommandSearchResult;
    return {
      items: (result.items ?? []).map((item) => ({
        id: item.id,
        sessionId: item.sessionId,
        tabId: item.tabId,
        assetId: item.assetId,
        command: item.command,
        cwd: item.cwd ?? "",
        source: item.source,
        exitCode: item.exitCode ?? null,
        startedAt: item.startedAt,
        finishedAt: item.finishedAt,
        transcriptId: item.transcriptId ?? null,
      })),
      hasMore: Boolean(result.hasMore),
    };
  },
};

export const transcriptHostSearchApi = {
  search: async (request: TranscriptHostSearchRequest): Promise<TranscriptHostSearchResult> => {
    const result = (await call("transcript_search_host", {
      ...request,
      startedFrom: request.startedFrom ?? null,
      startedTo: request.startedTo ?? null,
    })) as TranscriptHostSearchResult;
    return {
      sessions: (result.sessions ?? []).map((session) => ({
        transcript: {
          id: session.transcript.id,
          sessionId: session.transcript.sessionId,
          assetId: session.transcript.assetId,
          assetName: session.transcript.assetName,
          assetKind: session.transcript.assetKind,
          assetDeleted: session.transcript.assetDeleted,
          startedAt: session.transcript.startedAt,
          endedAt: session.transcript.endedAt,
          bytes: session.transcript.bytes,
          storedBytes: session.transcript.storedBytes,
          chunks: session.transcript.chunks,
          truncated: session.transcript.truncated,
          active: session.transcript.active,
          syncOptIn: session.transcript.syncOptIn,
          contentOmitted: session.transcript.contentOmitted,
        },
        matches: (session.matches ?? []).map((match) => ({
          seq: match.seq,
          ts: match.ts,
          preview: match.preview,
        })),
        moreMatches: Boolean(session.moreMatches),
      })),
      scannedSessions: result.scannedSessions,
      scannedBytes: result.scannedBytes,
      truncated: Boolean(result.truncated),
      unreadableSessions: result.unreadableSessions ?? 0,
    };
  },
};
