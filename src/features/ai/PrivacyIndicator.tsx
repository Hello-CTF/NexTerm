import { useEffect, useState } from "react";
import { onPrivacyStatus, privacyApi, privacyBroken, type PrivacyStatus } from "../../ipc/privacy";
import { useUi } from "../../app/store";
import { IconAlert, IconShieldCheck } from "../../ui/icons";
import { showSettingsSection } from "../settings/settingsNavigation";

export interface PrivacyGuardState { enabled: boolean; broken: boolean }
const OFF: PrivacyGuardState = { enabled: false, broken: false };
const toState = (status: PrivacyStatus): PrivacyGuardState => ({ enabled: status.config.enabled, broken: privacyBroken(status) });

/** Follows the privacy guard through the status IPC; unknown state counts as off. */
export function usePrivacyGuard(): PrivacyGuardState {
  const [state, setState] = useState<PrivacyGuardState>(OFF);
  useEffect(() => {
    let active = true;
    const unsubscribe = onPrivacyStatus((status) => { if (active) setState(toState(status)); });
    const load = () => { Promise.resolve().then(() => privacyApi.status()).catch(() => undefined); };
    load();
    window.addEventListener("focus", load);
    return () => { active = false; unsubscribe(); window.removeEventListener("focus", load); };
  }, []);
  return state;
}

// The guard shows as a lone shield next to the usage ring; its wording lives in the tooltip and the accessible name.
export function PrivacyIndicator({ guard }: { guard: PrivacyGuardState }) {
  if (!guard.enabled) return null;
  if (!guard.broken) {
    return (
      <span
        className="nx-cbar-icon nx-privacy-icon is-on"
        role="status"
        title="脱敏已开启：文本中的敏感片段在发送前替换为占位符；图片不会脱敏"
        aria-label="脱敏已开启"
      >
        <IconShieldCheck size={14} />
      </span>
    );
  }
  return (
    <button
      type="button"
      className="nx-cbar-icon nx-privacy-icon is-error"
      aria-label="脱敏不可用"
      title="脱敏不可用：本地脱敏模型不可用，AI 请求会被阻止。点击前往设置"
      onClick={() => {
        useUi.getState().addTab({ id: "settings", kind: "settings", title: "设置", closable: true });
        showSettingsSection("settings-ai-privacy");
      }}
    >
      <IconAlert size={14} />
    </button>
  );
}
