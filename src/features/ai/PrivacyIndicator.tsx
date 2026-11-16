import { useEffect, useState } from "react";
import { onPrivacyStatus, privacyApi, privacyBroken, type PrivacyStatus } from "../../ipc/privacy";
import { useUi } from "../../app/store";
import { modelApi, type ModelProfilesView } from "../../ipc/commands";
import type { ModelPrivacyOverride } from "../../ipc/types";
import { useChatModelChoice } from "./chatModelChoice";
import { setChatPrivacyChoice, useChatPrivacyChoice } from "./chatPrivacyChoice";
import { IconAlert, IconShield, IconShieldCheck } from "../../ui/icons";
import { showSettingsSection } from "../settings/settingsNavigation";
import { t } from "../../i18n";

export interface PrivacyGuardState { enabled: boolean; broken: boolean }
const OFF: PrivacyGuardState = { enabled: false, broken: false };

/** The guard a request really gets: an override (the chat's, else the profile's) wins over the global switch. */
export function privacyGuardFor(status: PrivacyStatus | null, override: ModelPrivacyOverride = ""): PrivacyGuardState {
  if (!status || override === "off") return OFF;
  if (override !== "on") return { enabled: status.config.enabled, broken: privacyBroken(status) };
  // Forced on while the global switch is off: a request is blocked unless a local model is chosen and usable.
  return { enabled: true, broken: !privacyUsable(status) };
}

/** Whether redaction can run at all: a supported runtime and a chosen local model that is available. */
export function privacyUsable(status: PrivacyStatus): boolean {
  return status.runtime.supported && Boolean(status.config.revision) && status.selection?.available !== false;
}

/** Follows the privacy status IPC; null until the first status arrives. */
export function usePrivacyStatus(): PrivacyStatus | null {
  const [status, setStatus] = useState<PrivacyStatus | null>(null);
  useEffect(() => {
    let active = true;
    const unsubscribe = onPrivacyStatus((next) => { if (active) setStatus(next); });
    const load = () => { Promise.resolve().then(() => privacyApi.status()).catch(() => undefined); };
    load();
    window.addEventListener("focus", load);
    return () => { active = false; unsubscribe(); window.removeEventListener("focus", load); };
  }, []);
  return status;
}

/** Follows the privacy guard through the status IPC; unknown state counts as off. */
export function usePrivacyGuard(override: ModelPrivacyOverride = ""): PrivacyGuardState {
  return privacyGuardFor(usePrivacyStatus(), override);
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

export interface ChatPrivacy {
  /** The guard the tab's next message gets. */
  guard: PrivacyGuardState;
  /** Whether the tab's own temporary choice decides the guard. */
  temporary: boolean;
  /** Whether redaction can run, so turning it on would not block every request. */
  usable: boolean;
  known: boolean;
  /** Turns redaction on or off for the tab's next messages; a choice equal to the profile and global one is cleared. */
  toggle: () => void;
}

/** The redaction an AI chat tab's messages get, and the temporary switch for it. */
export function useChatPrivacy(tabKey: string): ChatPrivacy {
  const status = usePrivacyStatus();
  const profile = useChatProfilePrivacy(tabKey);
  const choice = useChatPrivacyChoice(tabKey);
  const guard = privacyGuardFor(status, choice ?? profile);
  const usable = status ? privacyUsable(status) : false;
  const toggle = () => {
    if (!status) return;
    const next = !guard.enabled;
    if (next && !usable) {
      openPrivacySettings();
      return;
    }
    const inherited = privacyGuardFor(status, profile).enabled;
    setChatPrivacyChoice(tabKey, next === inherited ? null : next ? "on" : "off");
    useUi.getState().pushToast("info", next ? t("本对话已开启脱敏，从下一条消息起生效") : t("本对话已关闭脱敏，从下一条消息起生效"));
  };
  return { guard, temporary: choice !== undefined, usable, known: status !== null, toggle };
}

function openPrivacySettings(): void {
  useUi.getState().addTab({ id: "settings", kind: "settings", title: t("设置"), closable: true });
  showSettingsSection("settings-ai-privacy");
}

// The guard shows as a lone shield next to the usage ring; its wording lives in the tooltip and the accessible name.
// With a chat switch it is a toggle button for the tab's next messages; without one it only reports the guard.
export function PrivacyIndicator({ guard, chat }: { guard: PrivacyGuardState; chat?: ChatPrivacy }) {
  if (!chat) {
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
        onClick={openPrivacySettings}
      >
        <IconAlert size={14} />
      </button>
    );
  }
  if (!chat.known) return null;
  const source = chat.temporary ? t("（本对话临时设置）") : "";
  if (guard.enabled && guard.broken) {
    return (
      <button
        type="button"
        className="nx-cbar-icon nx-privacy-icon is-error"
        aria-label={t("脱敏不可用")}
        aria-pressed={true}
        title={`${t("脱敏不可用：本地脱敏模型不可用，AI 请求会被阻止。")}${source}\n${t("点击在本对话关闭脱敏；在设置中可修复本地模型")}`}
        onClick={chat.toggle}
      >
        <IconAlert size={14} />
      </button>
    );
  }
  if (guard.enabled) {
    return (
      <button
        type="button"
        className="nx-cbar-icon nx-privacy-icon is-on"
        aria-label={t("脱敏已开启")}
        aria-pressed={true}
        title={`${t("脱敏已开启：文本中的敏感片段在发送前替换为占位符；图片不会脱敏")}${source}\n${t("点击在本对话关闭脱敏")}`}
        onClick={chat.toggle}
      >
        <IconShieldCheck size={14} />
      </button>
    );
  }
  return (
    <button
      type="button"
      className="nx-cbar-icon nx-privacy-icon is-off"
      aria-label={t("脱敏已关闭")}
      aria-pressed={false}
      title={
        chat.usable
          ? `${t("脱敏已关闭：消息原样发送给 AI 服务")}${source}\n${t("点击在本对话开启脱敏")}`
          : t("脱敏未配置：点击前往设置下载本地脱敏模型")
      }
      onClick={chat.toggle}
    >
      <IconShield size={14} />
    </button>
  );
}
