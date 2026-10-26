import { sessionApi, type SessionInfo } from "../../ipc/commands";
import { WEB } from "../../ipc/env";
import { describeError } from "../../ui/errorText";
import { getTerminalPrefs, waitAccountPreferences } from "../preferences";

// Reasons shown on a terminal tab that opened as a regular terminal although durable terminals are preferred.
export const DURABLE_PREPARING_REASON = "持久组件准备中";
export const DURABLE_UNAVAILABLE_PREFIX = "持久组件不可用";
export const LOCAL_DURABLE_UNAVAILABLE_REASON = `${DURABLE_UNAVAILABLE_PREFIX}：本机终端暂不支持持久模式`;
export const DURABLE_FALLBACK_REASON = `${DURABLE_UNAVAILABLE_PREFIX}：创建持久终端失败，已改用普通终端`;

type ReadinessSession = Pick<SessionInfo, "id" | "kind">;

// The desktop keeps the default terminal mode on the device; the server applies the account preference itself and
// reports it as "disabled".
async function regularPreferred(): Promise<boolean> {
  if (WEB) return false;
  const loading = waitAccountPreferences();
  if (loading) await loading;
  return getTerminalPrefs().defaultMode === "regular";
}

// prepareDurableInBackground starts the durable helper deployment for an SSH session when a host workspace opens,
// without creating a terminal. The result is read again when the user opens a terminal, so failures surface there.
export function prepareDurableInBackground(session: ReadinessSession): void {
  if (session.kind !== "ssh") return;
  void (async () => {
    if (await regularPreferred()) return;
    await sessionApi.prepareDurable(session.id);
  })().catch(() => undefined);
}

// regularTerminalReason decides how an explicitly opened terminal starts. undefined keeps the default (durable when
// the helper is ready, or regular because the user chose regular terminals); a string opens a regular terminal now,
// without waiting, and is shown on the tab as the reason.
export async function regularTerminalReason(session: ReadinessSession): Promise<string | undefined> {
  if (session.kind !== "ssh" && session.kind !== "local") return undefined;
  if (await regularPreferred()) return undefined;
  if (session.kind === "local") return LOCAL_DURABLE_UNAVAILABLE_REASON;
  try {
    const readiness = await sessionApi.prepareDurable(session.id);
    switch (readiness.state) {
      case "ready":
      case "disabled":
        return undefined;
      case "preparing":
        return DURABLE_PREPARING_REASON;
      default:
        return `${DURABLE_UNAVAILABLE_PREFIX}：${readiness.reason || "原因未知"}`;
    }
  } catch (error) {
    return `${DURABLE_UNAVAILABLE_PREFIX}：${describeError(error)}`;
  }
}
