import { sessionApi, type DurableReadiness, type SessionInfo } from "../../ipc/commands";
import type { HelperProgressEvent } from "../../ipc/events";
import { WEB } from "../../ipc/env";
import { describeError } from "../../ui/errorText";
import { getTerminalPrefs, waitAccountPreferences } from "../preferences";

// Reasons shown on a terminal tab that opened as a regular terminal although durable terminals are preferred.
export const DURABLE_PREPARING_REASON = "持久组件准备中";
export const DURABLE_UNAVAILABLE_PREFIX = "持久组件不可用";
export const LOCAL_DURABLE_UNAVAILABLE_REASON = `${DURABLE_UNAVAILABLE_PREFIX}：本机终端暂不支持持久模式`;
export const DURABLE_FALLBACK_REASON = `${DURABLE_UNAVAILABLE_PREFIX}：创建持久终端失败，已改用普通终端`;
export const DURABLE_NOT_READY_AT_OPEN_REASON = "打开时持久组件未就绪";

// An explicit open never waits longer than this for the readiness answer; a slower answer counts as "preparing".
export const READINESS_QUERY_TIMEOUT_MS = 1000;

type ReadinessSession = Pick<SessionInfo, "id" | "kind">;

// The latest readiness known per session, from prepare replies and helper progress events.
const knownReadiness = new Map<string, DurableReadiness>();

export function knownDurableReadiness(sessionId: string): DurableReadiness | undefined {
  return knownReadiness.get(sessionId);
}

export function forgetDurableReadiness(): void {
  knownReadiness.clear();
}

// noteHelperProgress keeps the readiness cache current while a helper deploys, including deployments that started
// without any terminal tab.
export function noteHelperProgress(progress: HelperProgressEvent | null | undefined): void {
  if (!progress?.sessionId) return;
  if (progress.stage === "ready" && progress.done && !progress.error) {
    knownReadiness.set(progress.sessionId, { state: "ready" });
  } else if (progress.stage === "failed") {
    const reason = progress.cause || progress.error;
    knownReadiness.set(progress.sessionId, reason ? { state: "unavailable", reason } : { state: "unavailable" });
  } else if (!progress.done) {
    knownReadiness.set(progress.sessionId, { state: "preparing" });
  }
}

function queryReadiness(sessionId: string): Promise<DurableReadiness> {
  return sessionApi.prepareDurable(sessionId).then((readiness) => {
    knownReadiness.set(sessionId, readiness);
    return readiness;
  });
}

// The desktop keeps the default terminal mode on the device; the server applies the account preference itself and
// reports it as "disabled".
async function regularPreferred(): Promise<boolean> {
  if (WEB) return false;
  const loading = waitAccountPreferences();
  if (loading) await loading;
  return getTerminalPrefs().defaultMode === "regular";
}

// prepareDurableInBackground starts the durable helper deployment for an SSH session when a host workspace opens,
// without creating a terminal. Failures are cached and surface on the next explicitly opened terminal.
export function prepareDurableInBackground(session: ReadinessSession): void {
  if (session.kind !== "ssh") return;
  // A (re)connected session may sit on a new connection, so an older answer no longer applies.
  knownReadiness.delete(session.id);
  void (async () => {
    if (await regularPreferred()) return;
    await queryReadiness(session.id);
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

async function readinessWithin(sessionId: string, timeoutMs: number): Promise<DurableReadiness> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  const timeout = new Promise<DurableReadiness>((resolve) => {
    timer = setTimeout(() => resolve(knownReadiness.get(sessionId) ?? { state: "preparing" }), timeoutMs);
  });
  try {
    return await Promise.race([queryReadiness(sessionId), timeout]);
  } finally {
    clearTimeout(timer);
  }
}

// regularTerminalReason decides how an explicitly opened terminal starts. undefined keeps the default (durable when
// the helper is ready, or regular because the user chose regular terminals); a string opens a regular terminal now
// and is shown on the tab as the reason. A settled cached answer is used at once and refreshed in the background;
// otherwise the backend is asked, and an answer slower than READINESS_QUERY_TIMEOUT_MS counts as "preparing".
export async function regularTerminalReason(session: ReadinessSession): Promise<string | undefined> {
  if (session.kind !== "ssh" && session.kind !== "local") return undefined;
  if (await regularPreferred()) return undefined;
  if (session.kind === "local") return LOCAL_DURABLE_UNAVAILABLE_REASON;
  const cached = knownReadiness.get(session.id);
  if (cached && cached.state !== "preparing") {
    void queryReadiness(session.id).catch(() => undefined);
    return reasonFor(cached);
  }
  try {
    return reasonFor(await readinessWithin(session.id, READINESS_QUERY_TIMEOUT_MS));
  } catch (error) {
    return `${DURABLE_UNAVAILABLE_PREFIX}：${describeError(error)}`;
  }
}

// fallbackTerminalReason explains an SSH terminal that attached as regular although no regular terminal was chosen
// for it (a backend fallback, a terminal created elsewhere, or one opened before the helper was ready).
// durableRequested is what this client asked for when it created the shell, unknown for an attachment to an existing
// one. undefined means regular terminals are the effective choice for this host, so nothing is degraded.
export async function fallbackTerminalReason(
  sessionId: string,
  options: { fallbackNotice: boolean; durableRequested?: boolean },
): Promise<string | undefined> {
  if (options.durableRequested === false) return undefined;
  if (options.durableRequested === undefined && (await regularPreferred())) return undefined;
  const fallbackNotice = options.fallbackNotice;
  let readiness: DurableReadiness;
  try {
    readiness = await readinessWithin(sessionId, READINESS_QUERY_TIMEOUT_MS);
  } catch (error) {
    return `${DURABLE_UNAVAILABLE_PREFIX}：${describeError(error)}`;
  }
  if (readiness.state === "disabled") return undefined;
  if (readiness.state === "ready") return fallbackNotice ? DURABLE_FALLBACK_REASON : DURABLE_NOT_READY_AT_OPEN_REASON;
  return reasonFor(readiness);
}
