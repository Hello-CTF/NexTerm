import { useEffect, useState } from "react";
import { onPrivacyStatus, privacyApi, privacyBroken, type PrivacyStatus } from "../../ipc/privacy";
import { useUi } from "../../app/store";
import { modelApi, type ModelProfilesView } from "../../ipc/commands";
import type { ModelPrivacyOverride } from "../../ipc/types";
import { useChatModelChoice } from "./chatModelChoice";
import { IconAlert, IconShieldCheck } from "../../ui/icons";
import { showSettingsSection } from "../settings/settingsNavigation";
import { t } from "../../i18n";

export interface PrivacyGuardState { enabled: boolean; broken: boolean }
const OFF: PrivacyGuardState = { enabled: false, broken: false };

/** The guard a request really gets: a model profile's override wins over the global switch. */
export function privacyGuardFor(status: PrivacyStatus | null, override: ModelPrivacyOverride = ""): PrivacyGuardState {
  if (!status || override === "off") return OFF;
  if (override !== "on") return { enabled: status.config.enabled, broken: privacyBroken(status) };
  // Forced on while the global switch is off: a request is blocked unless a local model is chosen and usable.
  const broken = !status.runtime.supported || !status.config.revision || status.selection?.available === false;
  return { enabled: true, broken };
}

/** Follows the privacy guard through the status IPC; unknown state counts as off. */
export function usePrivacyGuard(override: ModelPrivacyOverride = ""): PrivacyGuardState {
  const [status, setStatus] = useState<PrivacyStatus | null>(null);
  useEffect(() => {
    let active = true;
    const unsubscribe = onPrivacyStatus((next) => { if (active) setStatus(next); });
    const load = () => { Promise.resolve().then(() => privacyApi.status()).catch(() => undefined); };
    load();
    window.addEventListener("focus", load);
    return () => { active = false; unsubscribe(); window.removeEventListener("focus", load); };
  }, []);
  return privacyGuardFor(status, override);
}

/** The redaction override of the profile an AI chat tab uses: its own choice, else the default profile. */
export function useChatProfilePrivacy(tabKey: string): ModelPrivacyOverride {
  const revision = useUi((s) => s.modelProfilesRevision);
  const choice = useChatModelChoice(tabKey);
  const [view, setView] = useState<ModelProfilesView | null>(null);
  useEffect(() => {
    let active = true;
    Promise.resolve()
      .then(() => modelApi.overview())
      .then((next) => { if (active) setView(next); }, () => undefined);
    return () => { active = false; };
  }, [revision]);
  const id = choice ?? view?.activeId ?? undefined;
  return view?.profiles.find((profile) => profile.id === id)?.privacy ?? "";
}

// The guard shows as a lone shield next to the usage ring; its wording lives in the tooltip and the accessible name.
export function PrivacyIndicator({ guard }: { guard: PrivacyGuardState }) {
  if (!guard.enabled) return null;
  if (!guard.broken) {
    return (
      <span
        className="nx-cbar-icon nx-privacy-icon is-on"
        role="status"
        title={t("脱敏已开启：文本中的敏感片段在发送前替换为占位符；图片不会脱敏")}
        aria-label={t("脱敏已开启")}
      >
        <IconShieldCheck size={14} />
      </span>
    );
  }
  return (
    <button
      type="button"
      className="nx-cbar-icon nx-privacy-icon is-error"
      aria-label={t("脱敏不可用")}
      title={t("脱敏不可用：本地脱敏模型不可用，AI 请求会被阻止。点击前往设置")}
      onClick={() => {
        useUi.getState().addTab({ id: "settings", kind: "settings", title: t("设置"), closable: true });
        showSettingsSection("settings-ai-privacy");
      }}
    >
      <IconAlert size={14} />
    </button>
  );
}
