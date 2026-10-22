import { useEffect } from "react";
import { useAuth } from "../auth/store";
import { useUi } from "../../app/store";
import { WEB } from "../../ipc/env";
import { describeError } from "../../ui/errorText";
import type { ForwardRecoverySettings, ForwardSettingsApi } from "./forwardSettingsApi";
import { createForwardSettingsIpcApi } from "./forwardSettingsIpc";

export type ForwardStartupRecoveryApi = Pick<ForwardSettingsApi, "getRecoverySettings" | "recoverNow">;

export interface ForwardStartupRecoveryRun {
  done: Promise<void>;
  cancel: () => void;
}

// The backend owns device selection, the four-connection cap, production
// skipping, owner checks, and host-key policy. This consumer only honors the
// persisted opt-in and keeps late completions from crossing a lifecycle
// boundary; it never opens or reconnects workspace tabs itself. A preference
// read failure fails closed without a dial; an explicitly enabled recovery
// failure remains visible to the current account.
export function startForwardStartupRecovery(
  api: ForwardStartupRecoveryApi,
  onError: (error: unknown) => void,
): ForwardStartupRecoveryRun {
  let cancelled = false;
  const done = (async () => {
    let settings: ForwardRecoverySettings;
    try {
      settings = await api.getRecoverySettings();
    } catch {
      return;
    }
    if (cancelled || settings?.enabled !== true) return;
    try {
      await api.recoverNow();
    } catch (error) {
      if (!cancelled) onError(error);
    }
  })();
  return {
    done,
    cancel: () => {
      cancelled = true;
    },
  };
}

const defaultApi = createForwardSettingsIpcApi();

export function ForwardStartupRecovery({ api = defaultApi }: { api?: ForwardStartupRecoveryApi }) {
  const accountEpoch = useUi((state) => state.accountEpoch);
  const pushToast = useUi((state) => state.pushToast);
  const authGate = useAuth((state) => state.gate);
  const ready = !WEB || authGate === "ready";

  useEffect(() => {
    if (!ready) return;
    const run = startForwardStartupRecovery(api, (error) => {
      pushToast("error", `启动恢复失败：${describeError(error)}`);
    });
    return run.cancel;
  }, [accountEpoch, api, pushToast, ready]);

  return null;
}
