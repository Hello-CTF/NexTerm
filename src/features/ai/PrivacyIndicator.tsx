import { useEffect, useState } from "react";
import { onPrivacyStatus, privacyApi, privacyBroken, type PrivacyStatus } from "../../ipc/privacy";
import { useUi } from "../../app/store";
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

export function PrivacyIndicator({ guard }: { guard: PrivacyGuardState }) {
  if (!guard.enabled) return null;
  if (!guard.broken) {
    return <span className="nx-chip text-[11px]" title="已开启 AI 信息脱敏，敏感片段在发送前替换为占位符">已脱敏</span>;
  }
  return (
    <button
      type="button"
      className="nx-chip text-[11px] text-red-400 hover:text-red-300"
      title="本地脱敏模型不可用，AI 请求会被阻止。点击前往设置"
      onClick={() => {
        useUi.getState().addTab({ id: "settings", kind: "settings", title: "设置", closable: true });
        showSettingsSection("settings-ai-privacy");
      }}
    >
      脱敏不可用
    </button>
  );
}
