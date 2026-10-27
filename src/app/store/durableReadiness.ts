import { useSyncExternalStore } from "react";
import { sessionApi, type DurableReadiness, type SessionInfo } from "../../ipc/commands";
import type { HelperProgressEvent } from "../../ipc/events";
import { WEB } from "../../ipc/env";
import { describeError } from "../../ui/errorText";
import { getTerminalPrefs, waitAccountPreferences } from "../preferences";

// Reasons shown on a terminal tab that opened as a regular terminal although durable terminals are preferred.
export const DURABLE_PREPARING_REASON = "持久组件准备中";
export const DURABLE_UNAVAILABLE_PREFIX = "持久组件不可用";
export const DURABLE_FALLBACK_REASON = `${DURABLE_UNAVAILABLE_PREFIX}：创建持久终端失败，已改用普通终端`;
export const DURABLE_NOT_READY_AT_OPEN_REASON = "打开时持久组件未就绪";

// An explicit open never waits longer than this for the readiness answer; a slower answer counts as "preparing".
export const READINESS_QUERY_TIMEOUT_MS = 1000;

type ReadinessSession = Pick<SessionInfo, "id" | "kind">;

function durableKind(kind: string | undefined): boolean {
  return kind === "ssh" || kind === "local";
}

// The latest readiness known per session, from prepare replies and helper progress events. Components read it
// through useKnownDurableReadiness, which re-renders only when that session's answer changes.
const knownReadiness = new Map<string, DurableReadiness>();
// Sessions known to be local; their answers depend on the local helper rather than a remote host.
const localSessions = new Set<string>();
const readinessListeners = new Set<() => void>();

function emitReadiness(): void {
  for (const listener of readinessListeners) listener();
}

function subscribeReadiness(listener: () => void): () => void {
  readinessListeners.add(listener);
  return () => {
    readinessListeners.delete(listener);
  };
}

// setReadiness keeps the stored object when nothing changed, so readers see a stable snapshot.
function setReadiness(sessionId: string, readiness: DurableReadiness): void {
  const previous = knownReadiness.get(sessionId);
  if (previous && previous.state === readiness.state && previous.reason === readiness.reason) return;
  knownReadiness.set(sessionId, readiness);
  emitReadiness();
}

function deleteReadiness(sessionId: string): void {
  if (knownReadiness.delete(sessionId)) emitReadiness();
}

function noteKind(sessionId: string, kind: string | undefined): void {
  if (kind === "local") localSessions.add(sessionId);
}

export function knownDurableReadiness(sessionId: string): DurableReadiness | undefined {
  return knownReadiness.get(sessionId);
}

export function useKnownDurableReadiness(sessionId: string): DurableReadiness | undefined {
  return useSyncExternalStore(subscribeReadiness, () => knownReadiness.get(sessionId));
}

export function forgetDurableReadiness(): void {
  localSessions.clear();
  if (knownReadiness.size === 0) return;
  knownReadiness.clear();
  emitReadiness();
}

// forgetLocalDurableReadiness drops only the answers for local sessions, after the local helper changed state or
// mode; remote hosts' answers stay valid.
export function forgetLocalDurableReadiness(): void {
  let changed = false;
  for (const sessionId of localSessions) changed = knownReadiness.delete(sessionId) || changed;
  if (changed) emitReadiness();
}

// noteHelperProgress keeps the readiness cache current while a helper deploys, including deployments that started
// without any terminal tab.
export function noteHelperProgress(progress: HelperProgressEvent | null | undefined): void {
  if (!progress?.sessionId) return;
  if (progress.stage === "ready" && progress.done && !progress.error) {
    setReadiness(progress.sessionId, { state: "ready" });
  } else if (progress.stage === "failed") {
    const reason = progress.cause || progress.error;
    setReadiness(progress.sessionId, reason ? { state: "unavailable", reason } : { state: "unavailable" });
  } else if (!progress.done) {
    setReadiness(progress.sessionId, { state: "preparing" });
  }
}

function queryReadiness(sessionId: string, kind?: string): Promise<DurableReadiness> {
  noteKind(sessionId, kind);
  return sessionApi.prepareDurable(sessionId).then((readiness) => {
    setReadiness(sessionId, readiness);
    return knownReadiness.get(sessionId) ?? readiness;
  });
}

// The desktop keeps the default terminal mode on the device; the server applies the account preference itself and
// reports it as "disabled". Local terminals follow this device's local terminal setting, which the backend applies
// and reports as "disabled" too.
async function regularPreferred(kind?: string): Promise<boolean> {
  if (kind === "local") return false;
  if (WEB) return false;
  const loading = waitAccountPreferences();
  if (loading) await loading;
  return getTerminalPrefs().defaultMode === "regular";
}

// prepareDurableInBackground starts the durable helper deployment for an SSH session when a host workspace opens,
// without creating a terminal; for a local session it reads the local helper state the app started at launch.
// Failures are cached and surface on the next explicitly opened terminal.
export function prepareDurableInBackground(session: ReadinessSession): void {
  if (!durableKind(session.kind)) return;
  // A (re)connected session may sit on a new connection, so an older answer no longer applies.
  deleteReadiness(session.id);
  void (async () => {
    if (await regularPreferred(session.kind)) return;
    await queryReadiness(session.id, session.kind);
  })().catch(() => undefined);
}

function reasonFor(readiness: DurableReadiness): string | undefined {
  switch (readiness.state) {
    case "ready":
    case "disabled":
      return undefined;
    case "preparing":
      return DURABLE_PREPARING_REASON;
    default:
      return `${DURABLE_UNAVAILABLE_PREFIX}：${readiness.reason || "原因未知"}`;
  }
}

async function readinessWithin(sessionId: string, timeoutMs: number, kind?: string): Promise<DurableReadiness> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  const timeout = new Promise<DurableReadiness>((resolve) => {
    timer = setTimeout(() => resolve(knownReadiness.get(sessionId) ?? { state: "preparing" }), timeoutMs);
  });
  try {
    return await Promise.race([queryReadiness(sessionId, kind), timeout]);
  } finally {
    clearTimeout(timer);
  }
}

// regularTerminalReason decides how an explicitly opened terminal starts. undefined keeps the default (durable when
// the helper is ready, or regular because the user chose regular terminals); a string opens a regular terminal now
// and is shown on the tab as the reason. A settled cached answer is used at once and refreshed in the background;
// otherwise the backend is asked, and an answer slower than READINESS_QUERY_TIMEOUT_MS counts as "preparing".
export async function regularTerminalReason(session: ReadinessSession): Promise<string | undefined> {
  if (!durableKind(session.kind)) return undefined;
  if (await regularPreferred(session.kind)) return undefined;
  const cached = knownReadiness.get(session.id);
  if (cached && cached.state !== "preparing") {
    void queryReadiness(session.id, session.kind).catch(() => undefined);
    return reasonFor(cached);
  }
  try {
    return reasonFor(await readinessWithin(session.id, READINESS_QUERY_TIMEOUT_MS, session.kind));
  } catch (error) {
    return `${DURABLE_UNAVAILABLE_PREFIX}：${describeError(error)}`;
  }
}

// fallbackTerminalReason explains an SSH or local terminal that attached as regular although no regular terminal was chosen
// for it (a backend fallback, a terminal created elsewhere, or one opened before the helper was ready).
// durableRequested is what this client asked for when it created the shell, unknown for an attachment to an existing
// one. undefined means regular terminals are the effective choice for this host, so nothing is degraded.
export async function fallbackTerminalReason(
  sessionId: string,
  options: { fallbackNotice: boolean; durableRequested?: boolean; kind?: string },
): Promise<string | undefined> {
  if (options.durableRequested === false) return undefined;
  if (options.durableRequested === undefined && (await regularPreferred(options.kind))) return undefined;
  const fallbackNotice = options.fallbackNotice;
  let readiness: DurableReadiness;
  try {
    readiness = await readinessWithin(sessionId, READINESS_QUERY_TIMEOUT_MS, options.kind);
  } catch (error) {
    return `${DURABLE_UNAVAILABLE_PREFIX}：${describeError(error)}`;
  }
  if (readiness.state === "disabled") return undefined;
  if (readiness.state === "ready") return fallbackNotice ? DURABLE_FALLBACK_REASON : DURABLE_NOT_READY_AT_OPEN_REASON;
  return reasonFor(readiness);
}
