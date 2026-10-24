import { useEffect, useId, useMemo, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { aiApi, biometricApi, desktopTrayApi, vaultApi, type AiPermissionConfig } from "../../ipc/commands";
import { useUi } from "../../app/store";
import {
  setTerminalCloseAction,
  setTerminalDefaultMode,
  useTerminalPrefs,
  type TerminalCloseAction,
  type TerminalDefaultMode,
} from "../../app/preferences";
import { ask, promptText } from "../../ui/dialogs";
import { PasswordChangeForm } from "../../ui/PasswordChangeForm";
import { onSettingsSection } from "./settingsNavigation";
import { isImeKeyEvent } from "../../ui/DialogHost";
import { isMobile, isWindows } from "../../app/platform";
import { ModelManager } from "../ai/ModelPanel";
import { AppearanceCard } from "./AppearanceCard";
import { KnownHostsCard } from "./KnownHostsCard";
import { MemoryCard } from "./MemoryCard";
import { ProductionSettingsPanel } from "../production/ProductionSettingsPanel";
import { ShortcutsCard } from "./ShortcutsCard";
import { SkillsCard } from "./SkillsCard";
import { productionSkillsApi } from "./skillsApi";
import { SyncCard } from "./SyncCard";
import { SyncBundleCard } from "./SyncBundleCard";
import { ShareCard } from "./ShareCard";
import { UpdateCard } from "./UpdateCard";
import { DiagnosticsCard } from "./DiagnosticsCard";
import { describeError } from "../../ui/errorText";
import { DESKTOP } from "../../ipc/env";
import {
  IconActivity,
  IconAlert,
  IconArchive,
  IconBot,
  IconCheckCircle,
  IconChevronDown,
  IconChevronLeft,
  IconChevronRight,
  IconClose,
  IconCommand,
  IconDownload,
  IconGlobe,
  IconLayers,
  IconLock,
  IconMonitor,
  IconPlus,
  IconRefresh,
  IconServer,
  IconSettings,
  IconShield,
  IconShieldCheck,
  IconSparkles,
  IconTerminal,
  IconXCircle,
  IconZap,
} from "../../ui/icons";

// Settings are split into a few groups shown one at a time; each group lists
// its sections in the navigation. Hidden groups stay mounted, so switching
// groups never drops an open form.
type SettingsGroupId = "general" | "ai" | "security" | "account";

const SETTINGS_GROUPS: readonly { id: SettingsGroupId; label: string; icon: typeof IconMonitor }[] = [
  { id: "general", label: "通用", icon: IconSettings },
  { id: "ai", label: "AI", icon: IconSparkles },
  { id: "security", label: "安全", icon: IconLock },
  { id: "account", label: "账号与同步", icon: IconServer },
];

const SETTINGS_SECTIONS = [
  { id: "settings-appearance", label: "外观", icon: IconMonitor, group: "general" },
  { id: "settings-terminal", label: "终端", icon: IconTerminal, group: "general" },
  { id: "settings-shortcuts", label: "快捷键", icon: IconCommand, group: "general" },
  { id: "settings-update", label: "软件更新", icon: IconDownload, group: "general" },
  { id: "settings-diagnostics", label: "诊断与调试", icon: IconActivity, group: "general" },
  { id: "settings-ai-model", label: "模型", icon: IconSparkles, group: "ai" },
  { id: "settings-ai-rules", label: "拦截规则", icon: IconShield, group: "ai" },
  { id: "settings-ai-skills", label: "技能", icon: IconLayers, group: "ai" },
  { id: "settings-memory", label: "记忆", icon: IconBot, group: "ai" },
  { id: "settings-vault", label: "凭据保护", icon: IconLock, group: "security" },
  { id: "settings-known-hosts", label: "已知主机", icon: IconShieldCheck, group: "security" },
  { id: "settings-production", label: "生产安全", icon: IconAlert, group: "security" },
  { id: "settings-account", label: "账号同步", icon: IconServer, group: "account" },
  { id: "settings-bundle", label: "资产包", icon: IconArchive, group: "account" },
  { id: "settings-share", label: "分享", icon: IconGlobe, group: "account" },
].filter((section) => {
  if (section.id === "settings-update") return DESKTOP;
  return true;
}) as readonly { id: string; label: string; icon: typeof IconMonitor; group: SettingsGroupId }[];

function groupOfSection(id: string): SettingsGroupId {
  return SETTINGS_SECTIONS.find((section) => section.id === id)?.group ?? "general";
}

const GROUP_KEY = "nexterm.settingsGroup.v1";

function loadGroup(): SettingsGroupId {
  try {
    const saved = localStorage.getItem(GROUP_KEY);
    return SETTINGS_GROUPS.some((group) => group.id === saved) ? (saved as SettingsGroupId) : "general";
  } catch {
    return "general";
  }
}

function saveGroup(group: SettingsGroupId): void {
  try {
    localStorage.setItem(GROUP_KEY, group);
  } catch {
    // The group choice is a convenience; failing to remember it is harmless.
  }
}

function jumpToSection(id: string): void {
  document.getElementById(id)?.scrollIntoView({ behavior: "smooth", block: "start" });
}

const NAV_OPEN_KEY = "nexterm.settingsNav.v1";

const CLOSE_ACTION_OPTIONS: { value: TerminalCloseAction; label: string }[] = [
  { value: "ask", label: "每次询问" },
  { value: "kill", label: "直接关闭" },
  { value: "detach", label: "转到后台" },
];

const DEFAULT_MODE_OPTIONS: { value: TerminalDefaultMode; label: string }[] = [
  { value: "durable", label: "持久终端（推荐）" },
  { value: "regular", label: "普通终端" },
];

function loadNavOpen(): boolean {
  try {
    return localStorage.getItem(NAV_OPEN_KEY) !== "collapsed";
  } catch {
    return true;
  }
}

function saveNavOpen(open: boolean): void {
  try {
    localStorage.setItem(NAV_OPEN_KEY, open ? "open" : "collapsed");
  } catch {
  }
}

export function SettingsView() {
  const { pushToast } = useUi();
  const terminalOsc52 = useUi((s) => s.terminalOsc52);
  const setTerminalOsc52 = useUi((s) => s.setTerminalOsc52);
  const terminalPrefs = useTerminalPrefs();
  const [terminalModeSaving, setTerminalModeSaving] = useState(false);
  const saveTerminalMode = async (value: TerminalDefaultMode) => {
    if (terminalModeSaving) return;
    setTerminalModeSaving(true);
    try {
      await setTerminalDefaultMode(value);
    } catch (error) {
      pushToast("error", `保存默认终端模式失败：${describeError(error)}`);
    } finally {
      setTerminalModeSaving(false);
    }
  };
  const showCloseToTray = DESKTOP && isWindows();
  const [closeToTray, setCloseToTray] = useState(false);
  const [closeToTrayLoading, setCloseToTrayLoading] = useState(showCloseToTray);
  const [closeToTraySaving, setCloseToTraySaving] = useState(false);
  useEffect(() => {
    if (!showCloseToTray) return;
    let cancelled = false;
    desktopTrayApi
      .get()
      .then((dto) => {
        if (!cancelled) setCloseToTray(dto.enabled);
      })
      .catch((error) => {
        if (!cancelled) pushToast("error", describeError(error));
      })
      .finally(() => {
        if (!cancelled) setCloseToTrayLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [showCloseToTray, pushToast]);
  const changeCloseToTray = (enabled: boolean) => {
    if (closeToTraySaving) return;
    const previous = closeToTray;
    setCloseToTray(enabled);
    setCloseToTraySaving(true);
    desktopTrayApi.set(enabled).catch((e) => {
      setCloseToTray(previous);
      pushToast("error", describeError(e));
    }).finally(() => setCloseToTraySaving(false));
  };

  const [testing, setTesting] = useState(false);
  const [testResult, setTestResult] = useState<{
    modelsOk: boolean;
    chatOk: boolean;
    modelsError?: string | null;
    chatError?: string | null;
    fatal?: string;
  } | null>(null);
  const connTestPanelId = useId();
  const [connTestOpen, setConnTestOpen] = useState(false);

  const [protPwd, setProtPwd] = useState("");
  const [pendingEnable, setPendingEnable] = useState(false);
  const [vaultPasswordOpen, setVaultPasswordOpen] = useState(false);
  const [autoLockDraft, setAutoLockDraft] = useState("");

  const qc = useQueryClient();
  // refetchOnWindowFocus off: refetches triggered by window focus would overwrite the auto-lock draft being edited with stale server values;
  // refreshes only follow explicit invalidations such as save/header buttons.
  const vault = useQuery({
    queryKey: ["vault-status"],
    queryFn: () => vaultApi.status(),
    refetchOnWindowFocus: false,
  }).data ?? null;
  const vaultPasswordProtected = vault?.mode === "master" && !vault.passwordless;
  const vaultPasswordless = vault?.mode === "master" && !!vault?.passwordless;
  const systemProtection = DESKTOP && !!vault?.systemProtection;
  const refreshVault = () => void qc.invalidateQueries({ queryKey: ["vault-status"] });
  useEffect(() => {
    setAutoLockDraft(vault ? String(vault.autoLockMinutes) : "");
  }, [vault]);

  const [navOpen, setNavOpenState] = useState(loadNavOpen);
  const setNavOpen = (open: boolean) => {
    setNavOpenState(open);
    saveNavOpen(open);
  };
  const [group, setGroupState] = useState<SettingsGroupId>(() =>
    !DESKTOP && window.location.hash === "#settings-account" ? "account" : loadGroup(),
  );
  const groupSections = SETTINGS_SECTIONS.filter((section) => section.group === group);
  const [activeSection, setActiveSection] = useState<string>(groupSections[0]?.id ?? SETTINGS_SECTIONS[0].id);
  const openGroup = (next: SettingsGroupId) => {
    setGroupState(next);
    saveGroup(next);
    setActiveSection(SETTINGS_SECTIONS.find((section) => section.group === next)?.id ?? SETTINGS_SECTIONS[0].id);
    contentRef.current?.scrollTo?.({ top: 0 });
  };
  const jump = (id: string) => {
    const target = groupOfSection(id);
    if (target !== group) {
      setGroupState(target);
      saveGroup(target);
      // The section only gets a layout once its group is shown.
      requestAnimationFrame(() => jumpToSection(id));
    } else {
      jumpToSection(id);
    }
    setActiveSection(id);
  };

  // Links from elsewhere in the app open the section's group, then scroll.
  const jumpRef = useRef(jump);
  jumpRef.current = jump;
  useEffect(() => onSettingsSection((id) => jumpRef.current(id)), []);

  useEffect(() => {
    if (DESKTOP || window.location.hash !== "#settings-account") return;
    // The initial group is already "account" for this link; only scroll.
    const frame = requestAnimationFrame(() => {
      setActiveSection("settings-account");
      jumpToSection("settings-account");
    });
    return () => cancelAnimationFrame(frame);
  }, []);

  // The TOC highlight follows the right-side content scroll: the current section is the last one whose top crossed the threshold inside the scroll container.
  // The optimistic highlight from click-to-jump and the scroll settle on the same section; jump and collapse behavior is unchanged.
  const contentRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const el = contentRef.current;
    if (!el) return;
    let frame: number | null = null;
    const update = () => {
      frame = null;
      const top = el.getBoundingClientRect().top;
      const visible = SETTINGS_SECTIONS.filter((section) => section.group === group);
      let current: string = visible[0]?.id ?? SETTINGS_SECTIONS[0].id;
      for (const s of visible) {
        const node = el.querySelector<HTMLElement>(`#${s.id}`);
        if (node && node.getBoundingClientRect().top - top <= 64) current = s.id;
      }
      setActiveSection((prev) => (prev === current ? prev : current));
    };
    const onScroll = () => {
      if (frame !== null) return;
      frame = requestAnimationFrame(update);
    };
    update();
    el.addEventListener("scroll", onScroll, { passive: true });
    return () => {
      el.removeEventListener("scroll", onScroll);
      if (frame !== null) cancelAnimationFrame(frame);
    };
  }, [group]);

  const saveAutoLock = () => {
    const trimmed = autoLockDraft.trim();
    const minutes = Number(trimmed);
    if (trimmed === "" || !Number.isInteger(minutes) || minutes < 0 || minutes > 1440) {
      pushToast("error", "自动锁定时长须为 0 至 1440 的整数分钟，0 表示禁用");
      return;
    }
    void vaultApi
      .setAutoLock(minutes)
      .then(() => {
        pushToast("success", minutes === 0 ? "已禁用闲置自动锁定" : `闲置 ${minutes} 分钟后自动锁定`);
        void qc.invalidateQueries({ queryKey: ["vault-status"] });
      })
      .catch((e) => pushToast("error", describeError(e)));
  };

  // changeVaultPassword runs from PasswordChangeForm: it verifies the current
  // password, then sets the new one. An empty new password removes password
  // protection; errors are thrown so the form shows them inline.
  const changeVaultPassword = async (old: string, next: string) => {
    await vaultApi.unlock(old);
    if (next === "" && systemProtection) {
      await vaultApi.initDpapi(old);
      pushToast("success", "已关闭密码保护 · 凭据改由系统级密钥保护");
    } else {
      await vaultApi.changePassword(old, next);
      // On iOS/Android with biometric material enrolled, silently re-store it with the new password after a change; otherwise biometric unlock would consistently fail
      if (next !== "" && isMobile()) {
        try {
          const bio = await biometricApi.status();
          if (bio.canEvaluate && bio.hasMaterial) await biometricApi.enable(next);
        } catch {
          // A failed re-store only means the password unlock is needed again next time
        }
      }
      pushToast(
        "success",
        next === ""
          ? "已关闭密码保护 · 凭据不受影响"
          : vaultPasswordless
            ? "已开启密码保护 · 凭据不受影响"
            : "密码已修改 · 凭据不受影响",
      );
    }
    setVaultPasswordOpen(false);
    refreshVault();
  };

  const disableVaultEncryption = async () => {
    const ok = await ask("关闭加密？\n关闭后凭据改由系统级密钥保护，无需密码即可使用。");
    if (!ok) return;
    try {
      await vaultApi.initDpapi("");
      pushToast("success", "已关闭加密 · 凭据改由系统级密钥保护");
      refreshVault();
    } catch (e) {
      pushToast("error", describeError(e));
    }
  };

  const [aiPerm, setAiPerm] = useState<AiPermissionConfig | null>(null);
  const [ruleDraft, setRuleDraft] = useState("");
  const [ruleDraftOpen, setRuleDraftOpen] = useState(false);
  const [ruleQuery, setRuleQuery] = useState("");
  const [rulePage, setRulePage] = useState(0);
  const rulePageSize = 8;
  const ruleQ = ruleQuery.trim().toLowerCase();
  const filteredRules = useMemo(
    () => (aiPerm?.dangerRules ?? []).filter((r) => !ruleQ || r.toLowerCase().includes(ruleQ)),
    [aiPerm?.dangerRules, ruleQ],
  );
  const rulePageCount = Math.max(1, Math.ceil(filteredRules.length / rulePageSize));
  const currentRulePage = Math.min(rulePage, rulePageCount - 1);
  const pageRules = filteredRules.slice(
    currentRulePage * rulePageSize,
    (currentRulePage + 1) * rulePageSize,
  );
  const rulePrefill = useUi((s) => s.aiRulePrefill);

  useEffect(() => {
    void aiApi
      .getPermission()
      .then(setAiPerm)
      .catch(() => undefined);
  }, []);

  useEffect(() => {
    if (rulePrefill === null) return;
    setRuleDraft(rulePrefill);
    setRuleDraftOpen(true);
    useUi.getState().setAiRulePrefill(null);
    jumpRef.current("settings-ai-rules");
  }, [rulePrefill]);

  const saveRules = async (rules: string[]) => {
    try {
      const latest = await aiApi.getPermission();
      const next = { ...latest, dangerRules: rules };
      setAiPerm(next);
      await aiApi.setPermission(next);
    } catch (e) {
      pushToast("error", `保存拦截规则失败：${describeError(e)}`);
      void aiApi
        .getPermission()
        .then(setAiPerm)
        .catch(() => undefined);
    }
  };

  const addRule = () => {
    const r = ruleDraft.trim();
    setRuleDraft("");
    setRuleDraftOpen(false);
    if (!r || !aiPerm) return;
    if (aiPerm.dangerRules.some((x) => x.toLowerCase() === r.toLowerCase())) return;
    void saveRules([...aiPerm.dangerRules, r]);
  };

  const editRule = (orig: string, next: string): boolean => {
    if (!aiPerm) return false;
    const r = next.trim();
    if (!r || r === orig) return false;
    if (aiPerm.dangerRules.some((x) => x !== orig && x.toLowerCase() === r.toLowerCase())) {
      return false;
    }
    void saveRules(aiPerm.dangerRules.map((x) => (x === orig ? r : x)));
    return true;
  };

  const test = async () => {
    setTesting(true);
    setTestResult(null);
    try {
      const r = await aiApi.testProvider();
      setTestResult({
        modelsOk: r.modelsOk,
        chatOk: r.chatOk,
        modelsError: r.modelsError,
        chatError: r.chatError,
      });
    } catch (e) {
      setTestResult({ modelsOk: false, chatOk: false, fatal: describeError(e) });
    } finally {
      setTesting(false);
    }
  };

  const testPassed = !!testResult && testResult.modelsOk && testResult.chatOk;
  const testDetail = testResult
    ? testResult.fatal
      ? `调用失败：${testResult.fatal}`
      : [
          `模型列表：${testResult.modelsOk ? "可用" : `失败（${testResult.modelsError ?? "原因未查明"}）`}`,
          `实际对话：${testResult.chatOk ? "可用" : `失败（${testResult.chatError ?? "原因未查明"}）`}`,
        ].join("\n")
    : "";

  return (
    <div className="nx-pane nx-settings-view h-full">
      <div className="nx-toolbar">
        <IconSettings size={14} className="text-neutral-500" />
        <span className="nx-toolbar-title">设置</span>
        <div className="nx-spacer" />
      </div>

      <div className="flex min-h-0 flex-1 flex-col md:flex-row">
        <nav
          aria-label="设置分区"
          className={`z-10 flex shrink-0 flex-col md:overflow-y-auto ${
            navOpen ? "md:w-52" : "md:w-14"
          }`}
        >
          <div
            className="flex gap-1.5 overflow-x-auto px-3 py-2 sm:px-5 md:hidden"
            data-testid="settings-nav-chips"
          >
            {SETTINGS_GROUPS.map((g) => (
              <button
                key={g.id}
                type="button"
                className={`nx-chip shrink-0 ${g.id === group ? "is-active" : ""}`}
                aria-pressed={g.id === group}
                onClick={() => openGroup(g.id)}
              >
                {g.label}
              </button>
            ))}
          </div>
          <div
            className="hidden md:flex md:flex-col md:gap-0.5 md:p-2"
            data-testid="settings-nav-toc"
          >
            <button
              type="button"
              className="nx-icon-btn nx-icon-btn-sm self-end"
              aria-label={navOpen ? "收起设置目录" : "展开设置目录"}
              aria-expanded={navOpen}
              title={navOpen ? "收起目录" : "展开目录"}
              onClick={() => setNavOpen(!navOpen)}
            >
              {navOpen ? <IconChevronLeft size={13} /> : <IconChevronRight size={13} />}
            </button>
            {navOpen
              ? SETTINGS_GROUPS.map((g) => (
                  <div key={g.id} className="flex flex-col">
                    <button
                      type="button"
                      className={`flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-[13px] font-medium whitespace-nowrap transition-colors ${
                        g.id === group ? "text-neutral-100" : "text-neutral-400 hover:bg-neutral-800/45 hover:text-neutral-200"
                      }`}
                      aria-expanded={g.id === group}
                      onClick={() => openGroup(g.id)}
                    >
                      <g.icon size={13} />
                      {g.label}
                    </button>
                    {g.id === group &&
                      SETTINGS_SECTIONS.filter((section) => section.group === g.id).map((section) => (
                        <button
                          key={section.id}
                          type="button"
                          className={`w-full rounded-md py-1 pl-7 pr-2 text-left text-[12.5px] whitespace-nowrap transition-colors ${
                            section.id === activeSection
                              ? "bg-neutral-800/70 text-neutral-100"
                              : "text-neutral-400 hover:bg-neutral-800/45 hover:text-neutral-200"
                          }`}
                          aria-current={section.id === activeSection ? "true" : undefined}
                          onClick={() => jump(section.id)}
                        >
                          {section.label}
                        </button>
                      ))}
                  </div>
                ))
              : SETTINGS_GROUPS.map((g) => (
                  <button
                    key={g.id}
                    type="button"
                    className={`nx-icon-btn self-center${g.id === group ? " is-active" : ""}`}
                    title={g.label}
                    aria-label={g.label}
                    aria-current={g.id === group ? "true" : undefined}
                    onClick={() => openGroup(g.id)}
                  >
                    <g.icon size={14} />
                  </button>
                ))}
          </div>
        </nav>

        <div className="min-w-0 flex-1 overflow-y-auto" ref={contentRef}>
          <div className="mx-auto flex w-full max-w-[720px] flex-col gap-3 p-3 sm:gap-4 sm:p-5">
        <div className="flex flex-col gap-4" hidden={group !== "general"} data-settings-group="general">
        <div id="settings-appearance" className="scroll-mt-12">
          <AppearanceCard />
        </div>

        <section id="settings-terminal" className="nx-card scroll-mt-12">
          <div className="mb-1 flex flex-wrap items-center gap-x-2 gap-y-1.5">
            <IconTerminal size={15} className="text-neutral-400" />
            <span className="nx-card-title">终端</span>
            <span className={`nx-badge ${terminalOsc52 ? "nx-badge-amber" : ""}`}>
              {terminalOsc52 ? "OSC 52 已开启" : "OSC 52 已关闭"}
            </span>
          </div>
          <div className="mb-3.5 mt-3.5">
            <div className="nx-setting-label mb-1.5">默认终端模式</div>
            <div className="nx-segment" role="group" aria-label="默认终端模式" aria-busy={terminalModeSaving}>
              {DEFAULT_MODE_OPTIONS.map((option) => (
                <button
                  key={option.value}
                  type="button"
                  className={`nx-segment-item ${terminalPrefs.defaultMode === option.value ? "is-active" : ""}`}
                  aria-pressed={terminalPrefs.defaultMode === option.value}
                  disabled={terminalModeSaving}
                  onClick={() => void saveTerminalMode(option.value)}
                >
                  {option.label}
                </button>
              ))}
            </div>
            <p className="nx-hint mt-1.5">
              {terminalPrefs.defaultMode === "durable"
                ? "持久终端断开后进程继续运行，可在「后台会话」接管。首次连接主机时会自动安装持久终端组件，安装期间先使用普通终端。"
                : "普通终端随连接断开而结束，不会在主机上安装持久终端组件。"}
              资产单独设置优先；只影响新开的终端。
            </p>
          </div>
          <label className="flex cursor-pointer items-start gap-3">
            <div className="min-w-0 flex-1">
              <div className="text-[12.5px] text-neutral-200">允许远程主机写入剪贴板（OSC 52）</div>
              <p className="nx-hint mt-0.5">
                {terminalOsc52
                  ? "开启后，远程主机可以通过转义序列直接改写你的系统剪贴板，不再逐次询问。只在可信的主机与网络环境下保持开启。"
                  : "默认关闭。远程主机每次请求写剪贴板时都会先询问你，可选择允许本次、允许本会话或拒绝。"}
              </p>
            </div>
            <input
              type="checkbox"
              className="mt-0.5 h-4 w-4 shrink-0"
              checked={terminalOsc52}
              onChange={(e) => setTerminalOsc52(e.target.checked)}
            />
          </label>
          <div className="mt-3.5">
            <div className="nx-setting-label mb-1.5">关闭运行中的终端标签时</div>
            <div className="nx-segment" role="group" aria-label="关闭运行中的终端标签时">
              {CLOSE_ACTION_OPTIONS.map((option) => (
                <button
                  key={option.value}
                  type="button"
                  className={`nx-segment-item ${terminalPrefs.closeAction === option.value ? "is-active" : ""}`}
                  aria-pressed={terminalPrefs.closeAction === option.value}
                  onClick={() => setTerminalCloseAction(option.value)}
                >
                  {option.label}
                </button>
              ))}
            </div>
            <p className="nx-hint mt-1.5">
              {terminalPrefs.closeAction === "ask" &&
                "每次关闭前询问：转到后台运行（可在「后台会话」接管）或直接关闭结束进程。弹窗里勾选「以后都按此选择」会记住你的决定。"}
              {terminalPrefs.closeAction === "kill" && "直接结束进程并关闭标签，不再询问。"}
              {terminalPrefs.closeAction === "detach" && "进程转入后台继续运行，可在「后台会话」中随时接管。"}
            </p>
            <p className="nx-hint mt-1.5">
              以上策略适用于支持后台的交互终端；本机后台终端仅在宿主应用运行期间保留，不是 durable SSH。
            </p>
          </div>
          {showCloseToTray && (
            <label className="mt-3.5 flex cursor-pointer items-start gap-3">
              <div className="min-w-0 flex-1">
                <div className="text-[12.5px] text-neutral-200">关闭时保留后台运行（最小化到托盘）</div>
                <p className="nx-hint mt-0.5">
                  {closeToTray
                    ? "关闭窗口后 NexTerm 保留在系统托盘中，终端会话保持连接；右键托盘图标可彻底退出。"
                    : "关闭窗口后直接退出 NexTerm，所有本机会话随之结束。"}
                </p>
              </div>
              <input
                type="checkbox"
                className="mt-0.5 h-4 w-4 shrink-0"
                checked={closeToTray}
                disabled={closeToTrayLoading || closeToTraySaving}
                onChange={(e) => changeCloseToTray(e.target.checked)}
              />
            </label>
          )}
        </section>

        <div id="settings-shortcuts" className="scroll-mt-12">
          <ShortcutsCard />
        </div>

        {DESKTOP && (
          <div id="settings-update" className="scroll-mt-12">
            <UpdateCard />
          </div>
        )}

        <div id="settings-diagnostics" className="scroll-mt-12">
          <DiagnosticsCard />
        </div>
        </div>
        <div className="flex flex-col gap-4" hidden={group !== "ai"} data-settings-group="ai">
        <section id="settings-ai-model" className="nx-card scroll-mt-12">
          <div className="mb-1 flex flex-wrap items-center gap-2">
            <IconSparkles size={15} className="text-blue-300" />
            <span className="nx-card-title">AI 模型</span>
            <span className="nx-badge">OpenAI 兼容协议 · 自带密钥</span>
          </div>
          <p className="nx-hint mb-3.5">
            可保存多份模型档案，并将其中一份设为当前。密钥加密后保存在当前实例的数据库中；启用「同步 AI 模型档案」并解锁凭据库后，密钥才会随档案端到端加密同步。支持本地 Ollama、LM Studio 和 vLLM。
          </p>

          <ModelManager />

          <div className="mt-4 border-t border-neutral-800/60 pt-3.5">
            <button
              type="button"
              className="flex items-center gap-1.5 text-[12px] text-neutral-400 transition-colors hover:text-neutral-100"
              aria-expanded={connTestOpen}
              aria-controls={connTestPanelId}
              onClick={() => setConnTestOpen((v) => !v)}
            >
              {connTestOpen ? (
                <IconChevronDown size={12} className="shrink-0" />
              ) : (
                <IconChevronRight size={12} className="shrink-0" />
              )}
              连通性测试
            </button>
            {connTestOpen && (
              <div id={connTestPanelId}>
                <div className="mt-2 flex flex-wrap items-center gap-2">
                  <button
                    className="nx-btn nx-btn-outline h-auto min-h-7 shrink py-1 text-left whitespace-normal"
                    disabled={testing}
                    onClick={() => void test()}
                  >
                    {testing ? (
                      <IconRefresh size={13} className="shrink-0 animate-spin" />
                    ) : (
                      <IconZap size={13} className="shrink-0" />
                    )}
                    {testing ? "测试中…" : "测试当前模型连通性"}
                  </button>
                  <span className="nx-hint">
                    只能测试已生效的当前模型；未保存的草稿请用上方的「刷新模型列表」验证。
                  </span>
                </div>

                {testResult && (
                  <div
                    className={`mt-3.5 nx-alert ${
                      testPassed
                        ? "nx-alert-success"
                        : "nx-alert-danger"
                    }`}
                  >
                    <div className="mb-1 flex items-center gap-1.5 font-semibold">
                      {testPassed ? <IconCheckCircle size={13} /> : <IconXCircle size={13} />}
                      测试结果
                    </div>
                    <pre className="whitespace-pre-wrap font-mono text-[11px]">{testDetail}</pre>
                  </div>
                )}
              </div>
            )}
          </div>
        </section>

        <section id="settings-ai-rules" className="nx-card scroll-mt-12">
          <div className="mb-1 flex flex-wrap items-center gap-x-2 gap-y-1.5">
            <IconShield size={15} className="text-neutral-400" />
            <span className="nx-card-title">AI 拦截规则</span>
            {aiPerm && (
              <span className={`nx-badge ${aiPerm.dangerRules.length ? "nx-badge-amber" : ""}`}>
                {aiPerm.dangerRules.length} 条
              </span>
            )}
          </div>
          <p className="nx-hint mb-3.5">
            命令、文件路径或 SQL 命中规则后，读写与自动执行模式下每次先向你确认，只读与无人值守模式下直接拒绝。按整词匹配、忽略大小写，例如 rm 会命中 rm -rf，但不会命中 format。
          </p>

          {aiPerm && aiPerm.dangerRules.length > 0 && (
            <input
              className="nx-input nx-input-sm mb-1.5 w-full font-mono"
              aria-label="搜索拦截规则"
              placeholder="搜索规则"
              value={ruleQuery}
              onChange={(e) => {
                setRuleQuery(e.target.value);
                setRulePage(0);
              }}
            />
          )}
          <div className="mb-1.5 flex flex-col gap-1">
            {pageRules.map((r) => (
              <div key={r} className="flex items-center gap-1">
                <input
                  className="nx-input nx-input-sm min-w-0 flex-1 font-mono"
                  defaultValue={r}
                  title={r}
                  onKeyDown={(e) => {
                    if (e.key === "Enter" && !isImeKeyEvent(e)) {
                      e.preventDefault();
                      e.currentTarget.blur();
                    }
                  }}
                  onBlur={(e) => {
                    if (!editRule(r, e.currentTarget.value)) e.currentTarget.value = r;
                  }}
                />
                <button
                  className="nx-icon-btn"
                  title="删除这条规则"
                  onClick={() =>
                    void saveRules((aiPerm?.dangerRules ?? []).filter((x) => x !== r))
                  }
                >
                  <IconClose size={11} />
                </button>
              </div>
            ))}
            {ruleDraftOpen && (
              <input
                autoFocus
                className="nx-input nx-input-sm w-full font-mono"
                placeholder="例如 kubectl delete，Enter 保存，Esc 取消"
                value={ruleDraft}
                onChange={(e) => setRuleDraft(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === "Enter" && !isImeKeyEvent(e)) {
                    e.preventDefault();
                    addRule();
                  }
                  if (e.key === "Escape") {
                    setRuleDraft("");
                    setRuleDraftOpen(false);
                  }
                }}
              />
            )}
            {aiPerm && aiPerm.dangerRules.length === 0 && !ruleDraftOpen && (
              <div className="nx-hint text-[11px]">还没有拦截规则</div>
            )}
          </div>
          {filteredRules.length > rulePageSize && (
            <div className="mb-1.5 flex items-center gap-2 text-[11px] text-neutral-500">
              <span>
                第 {currentRulePage + 1} / {rulePageCount} 页
              </span>
              <div className="nx-spacer" />
              <button
                className="nx-btn nx-btn-ghost nx-btn-xs"
                disabled={currentRulePage === 0}
                onClick={() => setRulePage((p) => Math.max(0, p - 1))}
              >
                上一页
              </button>
              <button
                className="nx-btn nx-btn-ghost nx-btn-xs"
                disabled={currentRulePage >= rulePageCount - 1}
                onClick={() => setRulePage((p) => Math.min(rulePageCount - 1, p + 1))}
              >
                下一页
              </button>
            </div>
          )}
          <button className="nx-btn nx-btn-outline nx-btn-sm" onClick={() => setRuleDraftOpen(true)}>
            <IconPlus size={11} />
            添加一条规则
          </button>

          <p className="nx-hint mt-3 border-t border-neutral-800/60 pt-2 text-[11px]">
            不可逆操作（格式化磁盘、清空系统目录、删除数据库）会直接拒绝。
          </p>
        </section>

        <div id="settings-ai-skills" className="scroll-mt-12">
          <SkillsCard api={productionSkillsApi} />
        </div>

        <div id="settings-memory" className="scroll-mt-12">
          <MemoryCard />
        </div>
        </div>
        <div className="flex flex-col gap-4" hidden={group !== "security"} data-settings-group="security">
        <section id="settings-vault" className="nx-card scroll-mt-12">
          <div className="mb-1 flex flex-wrap items-center gap-x-2 gap-y-1.5">
            <IconLock size={15} className="text-neutral-400" />
            <span className="nx-card-title">凭据保护</span>
            {vault &&
              (vaultPasswordProtected ? (
                <span className="nx-badge nx-badge-green">已开启</span>
              ) : vault.mode === "not_init" ? (
                <span className="nx-badge nx-badge-amber">未初始化</span>
              ) : vaultPasswordless ? (
                <span className="nx-badge nx-badge-amber">加密已启用 · 未设密码</span>
              ) : (
                <span className="nx-badge">未开启</span>
              ))}
          </div>

          <label className="flex cursor-pointer items-start gap-3">
            <div className="min-w-0 flex-1">
              <div className="text-[12.5px] text-neutral-200">用密码保护凭据</div>
              <p className="nx-hint mt-0.5">
                {vaultPasswordProtected
                  ? "开启：每次启动需输入密码后方可使用凭据。"
                  : vault?.mode === "not_init"
                    ? "尚未初始化：启用保护后才能保存密码或私钥。"
                    : vaultPasswordless
                      ? "已启用：凭据加密保存、无需密码即可使用，但任何拿到数据目录的人都能解密；设置密码后，每次启动需输入密码。"
                      : "关闭：无需密码，凭据由系统级密钥保护，启动后直接使用。"}
              </p>
            </div>
            <input
              type="checkbox"
              className="mt-0.5 h-4 w-4 shrink-0"
              checked={vaultPasswordProtected}
              onChange={() => {
                if (vaultPasswordProtected) {
                  const confirmText = systemProtection
                    ? "关闭密码保护？\n关闭后凭据改由系统级密钥保护，无需密码即可使用。"
                    : "关闭密码保护？\n关闭后凭据无需密码即可使用，但任何拿到数据目录的人都能解密。";
                  void ask(confirmText).then(async (ok) => {
                    if (!ok) return;
                    const old = await promptText("输入当前保护密码：", "", { secret: true });
                    if (old === null) return;
                    void (systemProtection ? vaultApi.initDpapi(old) : vaultApi.changePassword(old, ""))
                      .then(() => {
                        pushToast("success", "已关闭密码保护");
                        return refreshVault();
                      })
                      .catch((e) => pushToast("error", describeError(e)));
                  });
                } else if (vaultPasswordless) {
                  setVaultPasswordOpen(true);
                } else {
                  setProtPwd("");
                  setPendingEnable(true);
                }
              }}
            />
          </label>

          {pendingEnable && !vaultPasswordProtected && (
            <div className="mt-3 flex flex-wrap items-center gap-2">
              <input
                type="password"
                className="nx-input max-w-[240px]"
                placeholder="设置保护密码（留空则不设置）"
                value={protPwd}
                autoComplete="off"
                onChange={(e) => setProtPwd(e.target.value)}
              />
              <button
                className="nx-btn nx-btn-primary"
                disabled={protPwd.length > 0 && protPwd.length < 8}
                onClick={() =>
                  void vaultApi
                    .initMaster(protPwd)
                    .then(() => {
                      setPendingEnable(false);
                      pushToast(
                        "success",
                        protPwd === "" ? "已完成初始化，未设置密码" : "已开启密码保护",
                      );
                      return refreshVault();
                    })
                    .catch((e) => pushToast("error", describeError(e)))
                }
              >
                启用保护
              </button>
              <button className="nx-btn nx-btn-ghost" onClick={() => setPendingEnable(false)}>
                取消
              </button>
              <p className="nx-hint w-full text-[11px]">
                保护密码仅用于此处凭据库，不会随账号同步。留空则不设密码，任何拿到数据目录的人都能解密；忘记密码后，已保存的凭据将无法恢复。
              </p>
            </div>
          )}

          {vault?.mode === "master" && vaultPasswordOpen && (
            <div className="mt-3">
              <PasswordChangeForm
                requireCurrent={!vaultPasswordless}
                submitLabel={vaultPasswordless ? "设置密码" : "确认修改"}
                allowEmpty={!vaultPasswordless}
                emptyLabel="关闭密码保护"
                onSubmit={changeVaultPassword}
                onCancel={() => setVaultPasswordOpen(false)}
              />
              <p className="nx-hint mt-1.5 text-[11px]">保护密码只用于这台设备上的凭据库，忘记后已保存的凭据无法恢复。</p>
            </div>
          )}

          {vault?.mode === "master" && !vaultPasswordOpen && (
            <div className="mt-3 flex flex-wrap items-center gap-2">
              {!vaultPasswordless && <span className="nx-hint">锁定与解锁在「凭据」页</span>}
              <button
                className={`nx-btn nx-btn-sm ${vaultPasswordless ? "nx-btn-primary" : "nx-btn-outline"}`}
                onClick={() => setVaultPasswordOpen(true)}
              >
                {vaultPasswordless ? "设置密码" : "修改密码"}
              </button>
              {vaultPasswordless && systemProtection && (
                <button
                  className="nx-btn nx-btn-outline nx-btn-sm"
                  onClick={() => void disableVaultEncryption()}
                >
                  关闭加密
                </button>
              )}
            </div>
          )}

          {vaultPasswordProtected && (
            <div className="mt-3 border-t border-neutral-800/60 pt-3">
              <div className="flex flex-wrap items-center gap-2">
                <span className="text-[12.5px] text-neutral-200">闲置自动锁定</span>
                <input
                  type="number"
                  className="nx-input nx-input-sm w-[96px]"
                  min={0}
                  max={1440}
                  step={1}
                  value={autoLockDraft}
                  onChange={(e) => setAutoLockDraft(e.target.value)}
                />
                <span className="nx-hint">分钟，0 为禁用</span>
                <button className="nx-btn nx-btn-outline nx-btn-sm" onClick={() => void saveAutoLock()}>
                  保存
                </button>
              </div>
              <p className="nx-hint mt-1.5">
                闲置超过该时长后凭据库自动锁定，再次使用需输入保护密码。
              </p>
            </div>
          )}
        </section>

        <div id="settings-known-hosts" className="scroll-mt-12">
          <KnownHostsCard />
        </div>

        <ProductionSettingsPanel />
        </div>
        <div className="flex flex-col gap-4" hidden={group !== "account"} data-settings-group="account">
        <div id="settings-account" className="scroll-mt-12">
          <SyncCard />
        </div>

        <div id="settings-bundle" className="scroll-mt-12">
          <SyncBundleCard />
        </div>

        <div id="settings-share" className="scroll-mt-12">
          <ShareCard />
        </div>
        </div>
          </div>
        </div>
      </div>
    </div>
  );
}
